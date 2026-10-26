//go:build integration

package backup_test

import (
	"bytes"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/backup"
	"github.com/Rishikesh01/homeserver/hsctl/internal/docker"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/Rishikesh01/homeserver/hsctl/internal/prompt"
	"github.com/Rishikesh01/homeserver/hsctl/internal/restic"
)

// newService is the backup service for repo with the real Docker and restic clients (and no
// stack: enough for everything but a restore into the volumes). Its output collects in the buffer.
func newService(t *testing.T, repo paths.Repo) (*backup.Service, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	dk := docker.New(repo.Root)
	dk.Stdout, dk.Stderr = &out, &out
	rs := restic.New(repo.Path(paths.ResticPassword), backup.CredentialsEnv(repo))
	rs.Stdout, rs.Stderr = &out, &out
	s := backup.New(repo, dk, rs, nil, &prompt.Terminal{Out: &out, Interactive: func() bool { return false }})
	s.Out, s.Err = &out, &out
	return s, &out
}
