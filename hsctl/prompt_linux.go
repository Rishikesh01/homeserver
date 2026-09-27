package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// askSecret prompts like ask, but with terminal echo off — for API tokens, which shouldn't
// end up on screen (or in a screenshot of it). Falls back to a plain read when stdin isn't
// a terminal.
func askSecret(prompt string) string {
	fmt.Printf("  %s: ", prompt)
	fd := int(os.Stdin.Fd())
	if old, err := unix.IoctlGetTermios(fd, unix.TCGETS); err == nil {
		quiet := *old
		quiet.Lflag &^= unix.ECHO
		if unix.IoctlSetTermios(fd, unix.TCSETS, &quiet) == nil {
			defer func() {
				_ = unix.IoctlSetTermios(fd, unix.TCSETS, old)
				fmt.Println() // the user's Enter wasn't echoed either
			}()
		}
	}
	line, _ := stdinReader.ReadString('\n')
	return strings.TrimSpace(line)
}
