package docker

import (
	"errors"
	"strings"
	"testing"
)

// TestClassifyDockerError: an unusable Docker is diagnosed by what actually went wrong, not
// always as "needs sudo".
func TestClassifyDockerError(t *testing.T) {
	cases := []struct{ out, want string }{
		{"Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?", "isn't running"},
		{"permission denied while trying to connect to the Docker daemon socket", "refused this user"},
		{"sudo: a password is required", "no terminal"},
		{"some other failure\nmore", "Docker isn't usable: some other failure"},
		{"", "exit status 1"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := Classify(tc.out, errors.New("exit status 1")); !strings.Contains(got, tc.want) {
				t.Errorf("= %q, want it to mention %q", got, tc.want)
			}
		})
	}
}
