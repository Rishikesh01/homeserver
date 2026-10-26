// Package backup is the backup service: encrypted restic snapshots of the stack (a consistent
// Postgres dump, a consistent Vaultwarden SQLite fileset, every data volume and the config), the
// restore that puts one back, an off-site replica, and the self-test that proves it all works.
//
// Backups use restic: encrypted, deduplicated, snapshotted, many backends (local path, USB, SFTP to
// another host, S3-compatible storage). Volume files live under /var/lib/docker/volumes, so a
// backup needs root (run via sudo, the dashboard, or the root-owned systemd timer).
package backup

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/Rishikesh01/homeserver/hsctl/internal/envfile"
	"github.com/Rishikesh01/homeserver/hsctl/internal/fsutil"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
)

// Docker is what backups need from Docker.
type Docker interface {
	Command(dir string, args ...string) *exec.Cmd
	Run(dir string, args ...string) error
	Output(dir string, args ...string) (string, error)
	Combined(dir string, args ...string) (string, error)
	ContainerState(name string) string
	ContainerImage(name string) string
	VolumeMountpoint(name string) (string, error)
	Volumes() ([]string, error)
	FirstLocalImage() string
	EnsureNetwork(name string) error
	Require() error
}

// Restic runs restic against a repository.
type Restic interface {
	Env(repository string) []string
	Run(repository string, args ...string) error
	Output(repository string, args ...string) (string, error)
	Installed() bool
	Require() error
	Version() (string, error)
}

// Stack is stopped and started around a restore; its services own the volumes.
type Stack interface {
	Down(volumes, keepProxy bool) error
	Up() error
	StartProxy() error
}

// Asker confirms a destructive restore with the person at the terminal.
type Asker interface {
	CanAsk() bool
	YesNo(prompt string, def bool) bool
}

// Service backs up and restores one repo's stack.
type Service struct {
	Repo   paths.Repo
	Docker Docker
	Restic Restic
	Stack  Stack
	Ask    Asker
	HTTP   *http.Client // for the failure notification
	// TimerState reports the nightly timer's state; EnableTimer switches it on once there's a
	// repository to back up to.
	TimerState  func() Timer
	EnableTimer func()

	Out, Err io.Writer
}

// New returns the backup service for repo.
func New(repo paths.Repo, dk Docker, rs Restic, st Stack, ask Asker) *Service {
	return &Service{Repo: repo, Docker: dk, Restic: rs, Stack: st, Ask: ask,
		HTTP: &http.Client{Timeout: 15 * time.Second}, TimerState: func() Timer { return Timer{} }, EnableTimer: func() {},
		Out: os.Stdout, Err: os.Stderr}
}

// CredentialsEnv reads the cloud credentials in .backup-env as KEY=VALUE pairs for restic (for an
// S3 repo or replica). Local and SFTP destinations don't need any.
func CredentialsEnv(repo paths.Repo) func() []string {
	return func() []string {
		var env []string
		if kv, err := envfile.Read(repo.Path(paths.BackupEnv)); err == nil {
			for k, v := range kv {
				env = append(env, k+"="+v)
			}
		}
		return env
	}
}

// preflight is what every repository operation checks first: restic is installed, the backup
// settings are readable, and the backup disk (if guarded) is mounted.
func (s *Service) preflight() (Settings, error) {
	if err := s.Restic.Require(); err != nil {
		return Settings{}, err
	}
	if err := fsutil.RequireReadable(s.Repo.Root, ConfigFiles...); err != nil {
		return Settings{}, err
	}
	cfg := s.Settings()
	if err := s.RequireMount(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// RequireRoot refuses an operation that only works as root. Without this, a non-root
// `backup run` didn't fail cleanly: restic saved a snapshot missing every file it couldn't read,
// and that partial snapshot became "latest" — the default for a restore.
func RequireRoot(what, why string) error {
	if os.Geteuid() == 0 {
		return nil
	}
	return fmt.Errorf("%s needs root (%s) — run it with sudo:\n  sudo hsctl %s", what, why, what)
}
