package main

import (
	"time"

	"github.com/Rishikesh01/homeserver/hsctl/internal/backup"
	"github.com/Rishikesh01/homeserver/hsctl/internal/config"
	"github.com/Rishikesh01/homeserver/hsctl/internal/devices"
	"github.com/Rishikesh01/homeserver/hsctl/internal/docker"
	"github.com/Rishikesh01/homeserver/hsctl/internal/images"
	"github.com/Rishikesh01/homeserver/hsctl/internal/install"
	"github.com/Rishikesh01/homeserver/hsctl/internal/oplock"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/Rishikesh01/homeserver/hsctl/internal/priv"
	"github.com/Rishikesh01/homeserver/hsctl/internal/prompt"
	"github.com/Rishikesh01/homeserver/hsctl/internal/provision"
	"github.com/Rishikesh01/homeserver/hsctl/internal/registry"
	"github.com/Rishikesh01/homeserver/hsctl/internal/restic"
	"github.com/Rishikesh01/homeserver/hsctl/internal/secrets"
	"github.com/Rishikesh01/homeserver/hsctl/internal/setup"
	"github.com/Rishikesh01/homeserver/hsctl/internal/stack"
	"github.com/Rishikesh01/homeserver/hsctl/internal/sysinfo"
	"github.com/Rishikesh01/homeserver/hsctl/internal/updates"
	"github.com/spf13/cobra"
)

// app is hsctl's services, wired once for the homeserver folder it works on. The commands only
// parse their flags and call these.
type app struct {
	repo    paths.Repo // paths.Guess's folder; repo.Require says when it isn't a checkout
	docker  *docker.Client
	restic  *restic.Client
	term    *prompt.Terminal
	lock    *oplock.Locker
	store   *config.Store
	prov    *provision.Service
	stack   *stack.Service
	setup   *setup.Service
	backup  *backup.Service
	secrets *secrets.Service
	install *install.Service
	devices *devices.Service
	sysinfo *sysinfo.Service
	updates *updates.Checker
	images  *images.Checker
}

func newApp(repo paths.Repo) *app {
	root := priv.Root{}
	reg := registry.New()
	a := &app{repo: repo, docker: docker.New(repo.Root), term: prompt.Stdio(), lock: oplock.New(repo.Root),
		store: config.NewStore(repo, config.LocalHost{}), devices: devices.New(root),
		restic: restic.New(repo.Path(paths.ResticPassword), backup.CredentialsEnv(repo))}
	a.prov = provision.New(repo, a.docker)
	a.stack = stack.New(repo, a.docker, a.store, a.prov)
	a.stack.DashboardActive = install.DashboardActive
	a.setup = setup.New(repo, a.store, a.prov, a.term, a.docker, root)
	a.setup.DashboardActive = install.DashboardActive
	a.secrets = secrets.New(repo, a.docker, root)
	a.secrets.DashboardActive = install.DashboardActive
	a.install = install.New(repo, a.docker, a.store, a.stack, a.term, a.secrets.UIPassword, a.prov.Configured)
	a.backup = backup.New(repo, a.docker, a.restic, a.stack, a.term)
	a.backup.TimerState = backup.SystemdTimer
	a.backup.EnableTimer = a.install.EnableBackupTimer
	a.sysinfo = sysinfo.New(a.devices, root, a.backupDisk)
	a.updates = updates.New(repo, a.docker, reg, a.stack.ContainerNames, a.term)
	a.images = images.New(repo, reg)
	return a
}

// backupDisk is the backup disk's mountpoint (the REQUIRE_MOUNT path) and whether it's mounted,
// for the admin page's disk cards.
func (a *app) backupDisk() (string, bool) {
	cfg := a.backup.Settings()
	if cfg.RequireMount == "" {
		return "", false
	}
	return cfg.RequireMount, a.backup.RequireMount(cfg) == nil
}

// inRepo wraps a command that works on the stack so it refuses to run outside the homeserver repo
// (instead of quietly treating the current directory as the repo and writing a stray setup.conf,
// or installing a service that points at your home directory).
func (a *app) inRepo(run func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if err := a.repo.Require(); err != nil {
			return err
		}
		return run(cmd, args)
	}
}

// locked wraps a command's RunE so it holds the operation lock while it runs. Commands outside the
// repo run unlocked — they fail on their own "not in the homeserver repo" check.
func (a *app) locked(what string, wait time.Duration, run func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if !paths.IsRepo(a.repo.Root) {
			return run(cmd, args)
		}
		l, err := a.lock.Acquire(what, wait)
		if err != nil {
			return err
		}
		defer l.Release()
		return run(cmd, args)
	}
}
