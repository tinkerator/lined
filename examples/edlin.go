package main

import (
	"flag"
	"fmt"
	"os"

	"zappem.net/pub/io/lined"
)

var (
	prompt = flag.String("prompt", "prompt> ", "input prompt")
)

func main() {
	flag.Parse()

	r := lined.NewReader()
	pass := false
	thisPrompt := *prompt
	for {
		fmt.Print(thisPrompt)

		var line string
		var err error
		if pass {
			line, err = r.ReadPassword()
			pass = false
			thisPrompt = *prompt
		} else {
			line, err = r.ReadString()
		}

		if err != nil {
			fmt.Println()
			if err == lined.ErrEOF {
				if len(line) != 0 {
					fmt.Println("no auto-fill suggestions")
					continue
				}
				os.Exit(0)
			} else {
				fmt.Printf("failed to read line: %v", err)
				os.Exit(1)
			}
		}

		switch line[:len(line)-1] {
		case "exit":
			fmt.Println("exiting")
			os.Exit(0)
		case "password":
			pass = true
			thisPrompt = "password> "
		default:
			fmt.Printf("read [%q]\n", line)
		}
	}
}
