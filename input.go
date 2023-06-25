// Package lined provides an interactive text line reading interface for
// compatible terminals.
//
// The code uses escape sequences mostly from this list:
// https://en.wikipedia.org/wiki/ANSI_escape_code. However, in
// addition, it also uses some shortcuts found in Bash and Emacs.
package lined

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"sync"

	"golang.org/x/term"
)

// Reader implements a line string reader.
type Reader struct {
	mu       sync.Mutex
	h        []string
	unread   []byte
	offset   int
	row, col int // size of screen (refreshed each time readString is called.
	atR, atC int // last displayed cursor position.
}

// NewReader returns a new Reader that sources its data from
// os.Stdin allowing editing visible via os.Stdout.
func NewReader() *Reader {
	return &Reader{}
}

// reset resets the terminal settings to their original state.
func reset(orig *term.State) {
	if orig == nil {
		return
	}
	sc, err := os.Stdin.SyscallConn()
	if err == nil {
		sc.Control(func(fd uintptr) {
			fdi := int(fd)
			// Reenable input echo.
			term.Restore(fdi, orig)
		})
	}
}

// match matches one of a number of strings to the start of an array
// of bytes.
func match(p []byte, values ...string) (n int, ok bool) {
	for _, value := range values {
		l := len(value)
		if bytes.Compare(p[:l], []byte(value)) == 0 {
			return len(value), true
		}
	}
	return 0, false
}

// History returns a recent line of input read from the Reader.
// This function returns the nth most recent line content and the
// total number of lines of history so far recorded.
func (r *Reader) History(n int) (string, int) {
	if r == nil {
		return "", 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r == nil {
		return "", 0
	}
	m := len(r.h)
	if m < n || n < 0 {
		return "", m
	}
	return r.h[m-1-n], m
}

// moves the cursor n columns up.
func (r *Reader) up(n int) {
	if n > 0 {
		fmt.Printf("\033[%dA", n)
	} else if n < 0 {
		fmt.Printf("\033[%dB", -n)
	}
}

// moves the cursor n columns down.
func (r *Reader) down(n int) {
	r.up(-n)
}

// moves the cursor n columns right.
func (r *Reader) right(n int) {
	r.left(-n)
}

// moves the cursor n columns left.
func (r *Reader) left(n int) {
	if n > 0 {
		fmt.Printf("\033[%dD", n)
	} else if n < 0 {
		fmt.Printf("\033[%dC", -n)
	}
}

// start moves the cursor to the start of the line.
func (r *Reader) start() {
	fmt.Print("\033[G")
}

// clearEOL clears from (and including) the cursor location to the end of the line.
func (r *Reader) clearEOL() {
	fmt.Print("\033[0K")
}

// clearLine clears all of the line the cursor is on
func (r *Reader) clearLine() {
	fmt.Print("\033[2K")
}

// ErrNoReader is the error return for a *Reader being nil.
var (
	ErrNoReader   = errors.New("no reader defined")
	ErrTerminated = errors.New("program terminated")
	ErrEOF        = errors.New("end of file")
)

// cursorAt is used to locate the cursor when the text input is being
// first read.
var cursorAt = regexp.MustCompile(`^(\d+);(\d+)R`)

func (r *Reader) readString(echo bool) (string, error) {
	if r == nil {
		return "", ErrNoReader
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	var pick int
	var orig *term.State
	sc, err := os.Stdin.SyscallConn()
	if err == nil {
		sc.Control(func(fd uintptr) {
			if fdi := int(fd); term.IsTerminal(fdi) {
				if echo {
					fmt.Print("\033[6n")
				}
				r.col, r.row, _ = term.GetSize(fdi)
				// Disable input echo.
				orig, _ = term.MakeRaw(fdi)
			}
		})
	}
	defer reset(orig)

	p := make([]byte, 20)
	from := 0
	newline := bytes.IndexByte(r.unread, byte('\n'))

	specials := []struct {
		codes []string
		fn    func(s string) ([]byte, error)
	}{
		{ // Delete next.
			codes: []string{"\004", "\033[3~"},
			fn: func(s string) ([]byte, error) {
				if r.offset == len(r.unread) {
					return r.unread, ErrEOF
				} else {
					r.unread = append(r.unread[:r.offset], r.unread[r.offset+1:]...)
				}
				return nil, nil
			},
		},
		{ // Delete previous
			codes: []string{"\177", "\010"},
			fn: func(s string) ([]byte, error) {
				if r.offset > 0 {
					r.offset--
					r.unread = append(r.unread[:r.offset], r.unread[r.offset+1:]...)
				}
				return nil, nil
			},
		},
		{ // Start of line
			codes: []string{"\001", "\033[H"},
			fn: func(s string) ([]byte, error) {
				r.offset = 0
				return nil, nil
			},
		},
		{ // End of line
			codes: []string{"\005", "\033[4~", "\033[F", "\033$"},
			fn: func(s string) ([]byte, error) {
				r.offset = len(r.unread)
				return nil, nil
			},
		},
		{ // Transpose characters
			codes: []string{"\024"},
			fn: func(s string) ([]byte, error) {
				c := r.offset
				if c == len(r.unread) {
					c--
				}
				if c > 0 {
					d := c - 1
					r.unread[c], r.unread[d] = r.unread[d], r.unread[c]
					if c == r.offset {
						r.offset++
					}
				}
				return nil, nil
			},
		},
		{ // up arrow (replicate history)
			codes: []string{"\033[A"},
			fn: func(s string) ([]byte, error) {
				if echo && pick < len(r.h) {
					old := r.h[len(r.h)-pick-1]
					d := []byte(old[:len(old)-1])
					r.offset = len(d)
					r.unread = d
					pick++
				}
				return nil, nil
			},
		},
		{ // down arrow (replicate history, or clear line)
			codes: []string{"\033[B"},
			fn: func(s string) ([]byte, error) {
				if !echo {
					return nil, nil
				}
				dPick := pick - 2
				if dPick >= 0 {
					pick--
					old := r.h[len(r.h)-dPick-1]
					d := []byte(old[:len(old)-1])
					r.offset = len(d)
					r.unread = d
				} else {
					r.offset = 0
					r.unread = nil
					pick = 0
				}
				return nil, nil
			},
		},
		{ // right arrow
			codes: []string{"\033[C"},
			fn: func(s string) ([]byte, error) {
				r.offset++
				if r.offset > len(r.unread) {
					r.offset = len(r.unread)
				}
				return nil, nil
			},
		},
		{ // left arrow
			codes: []string{"\033[D"},
			fn: func(s string) ([]byte, error) {
				r.offset--
				if r.offset < 0 {
					r.offset = 0
				}
				return nil, nil
			},
		},
		{ // Carriage return.
			codes: []string{"\r", "\n"},
			fn: func(s string) ([]byte, error) {
				r.offset = len(r.unread)
				newline = r.offset
				fmt.Println("\r") // even when echo == false.
				return []byte("\n"), nil
			},
		},
	}

	// was holds the last iteration offset position.
	was := 0
	// wasLines holds the last iteration number of lines
	wasLines := 0
	for {
		if newline != -1 {
			line := string(r.unread[:newline+1])
			if echo {
				r.h = append(r.h, line)
			}
			r.unread = append(r.unread[newline+1:], p[:from]...)
			r.offset = 0
			return line, nil
		}

		n, err := os.Stdin.Read(p[from:])
		if n == 0 && err != nil {
			return "", err
		}
		from += n

		for from > 0 {
			if _, ok := match(p, "\003"); ok {
				fmt.Print("^C")
				return "", ErrTerminated
			}
			b := []byte{p[0]}
			partial := false
			found := false
			keep := false
			for _, sp := range specials {
				if found {
					break
				}
				for _, m := range sp.codes {
					code := []byte(m)
					if from < len(m) {
						partial = partial || bytes.HasPrefix(code, p[:from])
						continue
					}
					if !bytes.HasPrefix(p, code) {
						continue
					}
					x, err := sp.fn(m)
					if err != nil {
						return string(x), err
					}
					if x != nil {
						b = x
						keep = true
					} else {
						c := len(m)
						copy(p[:], p[c:])
						from -= c
					}
					found = true
					break
				}
			}

			if found && !keep {
				continue
			} else if partial {
				// still a chance we'll find a match.
				break
			}

			// Check for the initial cursor location. This
			// is requested when we start servicing a
			// prompt, but there is a race condition
			// reading it, so we read it when we can.
			if c, ok := match(p, "\033["); ok {
				parts := cursorAt.FindSubmatch(p[c:])
				if len(parts) == 3 {
					row, err := strconv.Atoi(string(parts[1]))
					if err != nil {
						log.Fatalf("regexp returned invalid number: %q", parts[1])
					}
					col, err := strconv.Atoi(string(parts[2]))
					if err != nil {
						log.Fatalf("regexp returned invalid number: %q", parts[2])
					}
					c += len(parts[0])
					copy(p[:], p[c:])
					from -= c
					r.atR = row
					r.atC = col
					continue
				}
			}

			// Consume exactly one character of input.
			b = append(b, r.unread[r.offset:]...)
			r.unread = append(r.unread[:r.offset], b...)
			r.offset++
			copy(p[:], p[1:])
			from--
		}

		if newline == -1 && echo {
			w := r.col - 1

			// zero base the coordinate of the prompt.
			cOffset := r.atC - 1

			// return to line start
			cD := (was - 1 + cOffset) % w
			cAt := cD + 1 - cOffset
			cUp := (was - 1 + cOffset) / w

			r.up(cUp)
			r.left(cAt)

			rs := bytes.Runes(r.unread)
			n := len(rs) // runes to print (for now assume runes are 1 column wide)

			// display full line content
			cAt = ((n - 1 + cOffset) % w) - ((r.offset - 1 + cOffset) % w)
			cUp = (n-1+cOffset)/w - (r.offset-1+cOffset)/w
			was = r.offset
			lines := 0
			for i, u := range rs {
				ch := string(u)
				if at := (i + cOffset) % w; at == 0 {
					fmt.Print("\\\r\n")
					lines++
				}
				fmt.Print(ch)
			}
			r.clearEOL()
			if wasLines > lines {
				for i := lines; i < wasLines; i++ {
					r.down(1)
					r.clearLine()
				}
				r.up(cUp + wasLines - lines)
			}
			r.up(cUp)
			r.left(cAt)
			wasLines = lines
		}
	}
}

// ReadString reads a whole line of input in the form of a string.
// Unlike the bufio.Reader implementation, no delim is required since
// lines are expected to end "\n". This function allows visual editing
// of the line using supported escape sequences.
func (r *Reader) ReadString() (string, error) {
	return r.readString(true)
}

// ReadPassword reads a whole line of input, but does not echo the
// input on os.Stdout and neither adds the input to the history nor
// supports access to history while entering the text of the line.
func (r *Reader) ReadPassword() (string, error) {
	return r.readString(false)
}
