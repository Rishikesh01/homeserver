# Contributing

Thanks for taking a look. This is a small project with a clear scope, so the most useful
contributions are focused ones: a bug fix, a service worth adding, a doc that was wrong.

## Scope

Homeserver is deliberately **LAN-only, single-machine, and boring**. Things that fit:

- fixes and hardening for the existing services,
- new self-hosted apps that follow the [existing pattern](docs/adding-an-app.md),
- making the dashboard clearer for non-technical users,
- anything that makes backups more trustworthy.

Things that don't: multi-node orchestration and cloud control planes. If you want one of
those, this probably isn't the base to build on.

## Getting set up

You need **Go** (the version in [`hsctl/go.mod`](hsctl/go.mod)) and **Docker Engine + Compose
v2**. `restic` is optional — one test skips without it.

```bash
git clone https://github.com/Rishikesh01/homeserver.git && cd homeserver
make -C hsctl build     # -> hsctl/hsctl
```

If `go` isn't on your `PATH`, point the Makefile at it: `make -C hsctl build GO=~/sdk/go/bin/go`.

## Before you open a PR

```bash
make -C hsctl vet       # go vet, the integration tests included
make -C hsctl test      # the integration tests, in a throwaway Docker-in-Docker container
```

### The tests

Every test is an **integration test**, behind the `integration` build tag, and **table-driven**:
each one drives a package through its public API (the tests live in `package x_test`) against real
dependencies — the filesystem, restic, a Docker daemon running the stack's own compose files and
images, a TLS container registry, a loop-device disk, a real PTY and real HTTP for the dashboard,
and root (the ownership rules, mounts, Docker's volume files). Where a test needs a normal user it
re-runs itself as one (`itest.Unprivileged`); where it needs an installed binary it re-runs from a
root-owned copy (`itest.Installed`). The one stand-in is `systemctl`, since the test container runs
no systemd (`itest.FakeSystemctl`).

`make -C hsctl test` (also `make -C hsctl test-integration`) compiles each package's tests and the
hsctl binary, and runs them as root inside a throwaway privileged Docker-in-Docker container
(`hsctl/test/run-integration.sh`), so nothing touches your Docker, containers or volumes. The
images the tests pull are cached in the `hsctl-itest-docker` volume between runs
(`docker volume rm hsctl-itest-docker` drops them). Narrow a run while you work:

```bash
PKG=./internal/backup make -C hsctl test                    # one package (or several, space-separated)
TESTRUN='TestBackupAndRestore' TESTV=1 make -C hsctl test   # matching tests, verbose
```

A plain `go test -tags integration ./...` also works on your machine, but skips what it lacks
(Docker, restic, root) — so it isn't the real check. CI runs `make -C hsctl test-integration` on
every push and pull request.

### Where the code lives

`hsctl/main.go` and the `cli_*.go` files only wire the services together (`app.go`) and define the
commands. The work is in `hsctl/internal/`, one package per service, each a struct built with
`New(…)` from what it depends on, taken as interfaces:

| Package | What it does |
|---|---|
| `paths`, `fsutil`, `envfile`, `secret` | the checkout's layout; atomic writes that keep the admin's ownership; .env files; generated secrets |
| `docker`, `restic`, `registry`, `prompt`, `priv` | the Docker, restic and registry clients; asking at the terminal; running things as root |
| `config`, `provision`, `setup` | `setup.conf`, the generated .env files, and `hsctl setup` |
| `layout`, `catalog`, `stack` | the stack's shape; the home page's tiles; starting, stopping and switching apps |
| `images`, `updates` | platform checks of the pins; checking for and applying newer images |
| `oplock`, `jobs` | the operation lock every state-changing command takes; the dashboard's background jobs |
| `backup` | backups, restores, the off-site replica and the self-test |
| `devices`, `sysinfo` | the Drives page; system health |
| `install`, `secrets` | the systemd units and uninstall; the logins |
| `web` | the dashboard |
| `itest` | test helpers |

If you changed anything that touches the stack itself — a compose file, the Caddyfile, the
backup path, `hsctl up`/`down` — **try it in the sandbox** rather than on a live box:

```bash
make sandbox            # an isolated copy of the whole stack in a nested Docker daemon
make sandbox-restore    # restore a real backup into it (see sandbox/README.md)
make sandbox-down       # stop and clean up
```

Nothing the sandbox does can reach your real Docker, containers, or volumes. See
[sandbox/README.md](sandbox/README.md).

## Conventions

- **Comments explain *why*.** The existing code is heavy on rationale — when a line exists
  because of a specific failure mode (the Vaultwarden WAL handling, the `REQUIRE_MOUNT`
  device-id check), the comment says so. Match that.
- **Commit messages** are `type: summary` — `feat:`, `fix:`, `docs:`, `chore:`, `refactor:`.
- **Pin image versions** in compose files; don't use `:latest`.
- **Never commit secrets.** `.env`, `setup.conf`, `.restic-password`, `.backup-env` and
  friends are git-ignored — only the `*.example` templates are tracked.
- **Docs live with the change.** If you add a flag or a config key, update
  [docs/configuration.md](docs/configuration.md) in the same PR.

### A note on `ONBOARDING.md`

That file is **read at runtime** by the dashboard (`hsctl/internal/web`) and served as a rendered
page at `/help`. Two consequences:

- it must stay at the repo root,
- the literal token `SERVER_IP` in it is a placeholder that gets substituted with the real IP —
  don't "fix" it to an actual address.

## Releases

The version in `hsctl --version` comes from `git describe --tags`, injected via `-ldflags` by
[`hsctl/Makefile`](hsctl/Makefile). A plain `go build .` reports `dev`, which is fine for
local work — use `make` for anything you install. Tags are `vMAJOR.MINOR.PATCH`.

## Reporting things

- **Bugs and features** — open an issue; the templates ask for the few details that matter.

By contributing, you agree that your contributions are licensed under the
[MIT License](LICENSE).
