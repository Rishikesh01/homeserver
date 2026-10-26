//go:build integration

package priv_test

import (
	"os"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/priv"
)

// TestCommand: root (the dashboard service, sudo) runs the command directly — and it really runs.
func TestCommand(t *testing.T) {
	itest.NeedRoot(t)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"id", []string{"-u"}, "0"},
		{"echo", []string{"mount", "/dev/sdb1"}, "mount /dev/sdb1"},
	}
	for _, tc := range cases {
		cmd := priv.Root{}.Command(tc.name, tc.args...)
		out, err := cmd.Output()
		if err != nil || strings.TrimSpace(string(out)) != tc.want {
			t.Errorf("%s %v = %q %v, want %q", tc.name, tc.args, out, err, tc.want)
		}
	}
}

// TestCommandAsUser: a normal user's run goes through sudo.
func TestCommandAsUser(t *testing.T) {
	if !itest.Unprivileged(t) {
		return
	}
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"mount", []string{"/dev/sdb1", "/mnt/backup"}, []string{"sudo", "mount", "/dev/sdb1", "/mnt/backup"}},
		{"systemctl", []string{"restart", "hsctl-ui.service"}, []string{"sudo", "systemctl", "restart", "hsctl-ui.service"}},
	}
	for _, tc := range cases {
		cmd := priv.Command(tc.name, tc.args...)
		if strings.Join(cmd.Args, " ") != strings.Join(tc.want, " ") {
			t.Errorf("as uid %d: %v, want %v", os.Geteuid(), cmd.Args, tc.want)
		}
	}
}
