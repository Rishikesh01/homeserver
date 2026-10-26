//go:build integration

package prompt_test

import (
	"bufio"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/prompt"
)

func terminal(input string) (*prompt.Terminal, *strings.Builder) {
	out := &strings.Builder{}
	return &prompt.Terminal{In: bufio.NewReader(strings.NewReader(input)), Out: out}, out
}

// TestAsk: an empty answer takes the default; a validated question explains a bad answer and asks
// again; at the end of input it settles for the default instead of looping forever.
func TestAsk(t *testing.T) {
	isIP := func(s string) error {
		if strings.Count(s, ".") != 3 {
			return errors.New("not an IPv4 address")
		}
		return nil
	}
	cases := []struct {
		name, input string
		validate    func(string) error
		want        string
		wantOut     []string
	}{
		{"answer", "Europe/Berlin\n", nil, "Europe/Berlin", []string{"  Timezone [Etc/UTC]: "}},
		{"empty takes the default", "\n", nil, "Etc/UTC", nil},
		{"end of input takes the default", "", nil, "Etc/UTC", nil},
		{"answer without a newline", "Asia/Kolkata", nil, "Asia/Kolkata", nil},
		{"valid answer", "10.0.0.5\n", isIP, "10.0.0.5", nil},
		{"bad answer asked again", "10.0.0\n10.0.0.9\n", isIP, "10.0.0.9", []string{"not an IPv4 address"}},
		{"bad answer at end of input", "nope", isIP, "Etc/UTC", []string{"not an IPv4 address"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			term, out := terminal(tc.input)
			if got := term.AskValid("Timezone", "Etc/UTC", tc.validate); got != tc.want {
				t.Errorf("answer = %q, want %q", got, tc.want)
			}
			for _, w := range tc.wantOut {
				if !strings.Contains(out.String(), w) {
					t.Errorf("output %q lacks %q", out.String(), w)
				}
			}
		})
	}
}

// TestYesNo: y/yes (any case) is yes, anything else no, empty the default.
func TestYesNo(t *testing.T) {
	cases := []struct {
		input   string
		def     bool
		want    bool
		wantOut string
	}{
		{"y\n", false, true, "[y/N]"},
		{"YES\n", false, true, "[y/N]"},
		{"n\n", true, false, "[Y/n]"},
		{"maybe\n", true, false, "[Y/n]"},
		{"\n", true, true, "[Y/n]"},
		{"", false, false, "[y/N]"},
	}
	for _, tc := range cases {
		term, out := terminal(tc.input)
		if got := term.YesNo("Restore now?", tc.def); got != tc.want {
			t.Errorf("YesNo(%q, %v) = %v, want %v", tc.input, tc.def, got, tc.want)
		}
		if !strings.Contains(out.String(), tc.wantOut) {
			t.Errorf("prompt %q lacks %q", out.String(), tc.wantOut)
		}
	}
}

// TestTyped: a destructive step needs the word typed exactly, as many times as asked.
func TestTyped(t *testing.T) {
	cases := []struct {
		input string
		n     int
		want  bool
	}{
		{"DESTROY\n", 1, true},
		{" DESTROY \n", 1, true},
		{"destroy\n", 1, false},
		{"DESTROY\nDESTROY\n", 2, true},
		{"DESTROY\nnope\n", 2, false},
		{"DESTROY\n", 2, false},
		{"", 1, false},
	}
	for _, tc := range cases {
		term, out := terminal(tc.input)
		if got := term.Typed("DESTROY", tc.n); got != tc.want {
			t.Errorf("Typed(%q, %d) = %v, want %v", tc.input, tc.n, got, tc.want)
		}
		if tc.n > 1 && !strings.Contains(out.String(), "Confirm 1/2") {
			t.Errorf("a repeated confirmation should count: %q", out.String())
		}
	}
}

// TestCanAsk: there's someone to ask only on a terminal — never on a pipe or /dev/null, which a
// dashboard job and the backup timer get.
func TestCanAsk(t *testing.T) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	pty := itest.PTY(t)
	cases := []struct {
		name        string
		interactive func() bool
		want        bool
	}{
		{"no check: assume a person", nil, true},
		{"/dev/null", func() bool { return prompt.IsTerminal(devnull) }, false},
		{"a pipe", func() bool { return prompt.IsTerminal(r) }, false},
		{"a terminal", func() bool { return prompt.IsTerminal(pty) }, true},
		{"told yes", func() bool { return true }, true},
	}
	for _, tc := range cases {
		term := &prompt.Terminal{Interactive: tc.interactive}
		if got := term.CanAsk(); got != tc.want {
			t.Errorf("%s: CanAsk = %v, want %v", tc.name, got, tc.want)
		}
	}
}
