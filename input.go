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

	"golang.org/x/term"
)

// Reader implements a line string reader.
type Reader struct {
	h        []string
	unread   []byte
	offset   int
	row, col int // size of screen (refreshed each time readString is called.
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
	m := len(r.h)
	if m < n || n < 0 {
		return "", m
	}
	return r.h[m-1-n], m
}

// moves the cursor n columns up.
func (r *Reader) up(n int) {
	fmt.Printf("\033[%dA", n)
}

// moves the cursor n columns down.
func (r *Reader) down(n int) {
	fmt.Printf("\033[%dB", n)
}

// moves the cursor n columns right.
func (r *Reader) right(n int) {
	fmt.Printf("\033[%dC", n)
}

// moves the cursor n columns left.
func (r *Reader) left(n int) {
	if n > 0 {
		fmt.Printf("\033[%dD", n)
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

// ErrNoReader is the error return for a *Reader being nil.
var (
	ErrNoReader   = errors.New("no reader defined")
	ErrTerminated = errors.New("program terminated")
	ErrEOF        = errors.New("end of file")
)

func (r *Reader) readString(echo bool) (string, error) {
	var pick int
	var orig *term.State
	sc, err := os.Stdin.SyscallConn()
	if err == nil {
		sc.Control(func(fd uintptr) {
			if fdi := int(fd); term.IsTerminal(fdi) {
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

	// was holds the last iteration cursor position.
	was := 0
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

		// Only a partial line stored so display it again and
		// accept more input.  move left r.offset and display
		// r.unread. Then adjust the cursor position.

		rs := bytes.Runes(r.unread)
		n := len(rs)
		if echo {
			r.left(was)
			fmt.Print(string(r.unread))
			r.clearEOL()
			r.left(n - r.offset)
			was = r.offset
		}

		n, err := os.Stdin.Read(p[from:])
		if n == 0 && err != nil {
			log.Fatalf("TODO some sort of error has occurred: %v", err)
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

			// Consume exactly one character of input.
			b = append(b, r.unread[r.offset:]...)
			r.unread = append(r.unread[:r.offset], b...)
			r.offset++
			copy(p[:], p[1:])
			from--
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
