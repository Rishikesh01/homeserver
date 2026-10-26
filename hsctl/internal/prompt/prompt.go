// Package prompt asks the person at the terminal: a value with a default, yes/no, or a word to
// type to confirm something destructive. Off a terminal (a dashboard job, the timer) it says so,
// so callers can refuse instead of hanging or guessing.
package prompt

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// Terminal asks questions on In/Out.
type Terminal struct {
	In  *bufio.Reader
	Out io.Writer
	// Interactive reports whether there's a person to ask.
	Interactive func() bool
}

// Stdio is a Terminal on the process's stdin/stdout.
func Stdio() *Terminal {
	return &Terminal{In: bufio.NewReader(os.Stdin), Out: os.Stdout, Interactive: func() bool { return IsTerminal(os.Stdin) }}
}

// IsTerminal reports whether f is a terminal. (A character-device check isn't enough: /dev/null,
// which jobs and the timer get, is one too.)
func IsTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	return err == nil
}

// CanAsk reports whether there's someone to ask.
func (t *Terminal) CanAsk() bool { return t.Interactive == nil || t.Interactive() }

// Ask prints prompt with its default and returns the answer, or def for an empty one.
func (t *Terminal) Ask(prompt, def string) string { return t.AskValid(prompt, def, nil) }

// AskValid is Ask with a check: an answer that fails validate is explained and asked again, so a
// typo never lands in a generated file. At end of input it settles for the default.
func (t *Terminal) AskValid(prompt, def string, validate func(string) error) string {
	for {
		fmt.Fprintf(t.Out, "  %s [%s]: ", prompt, def)
		line, err := t.In.ReadString('\n')
		if line = strings.TrimSpace(line); line == "" {
			return def
		}
		if validate == nil {
			return line
		}
		verr := validate(line)
		if verr == nil {
			return line
		}
		fmt.Fprintf(t.Out, "    %v\n", verr)
		if err != nil {
			return def
		}
	}
}

// YesNo asks a yes/no question; an empty answer is def.
func (t *Terminal) YesNo(prompt string, def bool) bool {
	d := "y/N"
	if def {
		d = "Y/n"
	}
	fmt.Fprintf(t.Out, "  %s [%s]: ", prompt, d)
	line, _ := t.In.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	if line == "" {
		return def
	}
	return strings.HasPrefix(line, "y")
}

// Typed asks for word to be typed exactly, n times. False on any mismatch.
func (t *Terminal) Typed(word string, n int) bool {
	for i := 1; i <= n; i++ {
		if n > 1 {
			fmt.Fprintf(t.Out, "Confirm %d/%d — type %s to continue: ", i, n, word)
		} else {
			fmt.Fprintf(t.Out, "  Type %s to continue: ", word)
		}
		line, _ := t.In.ReadString('\n')
		if strings.TrimSpace(line) != word {
			return false
		}
	}
	return true
}
