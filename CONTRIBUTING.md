# Contributing

Thanks for taking a look. This is a small project with a clear scope, so the most useful
contributions are focused ones: a bug fix, a service worth adding, a doc that was wrong.

## Scope

Homeserver is deliberately **LAN-only, single-machine, and boring**. Things that fit:

- fixes and hardening for the existing services,
- new self-hosted apps that follow the [existing pattern](docs/adding-an-app.md),
- making the dashboard clearer for non-technical users,
- anything that makes backups more trustworthy.

Things that don't: remote access / VPN / tunnels, multi-node orchestration, and cloud
control planes. If you want one of those, this probably isn't the base to build on.

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
make -C hsctl vet       # go vet ./...
make -C hsctl test      # go test ./...
```

The suite is plain Go unit tests — no Docker required. `replica_test.go` exercises a real
restic repo round-trip and skips itself if `restic` isn't installed, so install restic if you
touch backup code. CI runs both commands on every push and pull request.

If you changed anything that touches the stack itself — a compose file, the Caddyfile, the
backup path, `hsctl up`/`down` — **try it in the sandbox** rather than on a live box:

```bash
make sandbox            # an isolated copy of the whole stack in a nested Docker daemon
make sandbox-restore    # restore a real backup into it (repo mounted read-only)
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

That file is **read at runtime** by the dashboard (`hsctl/ui.go`) and served as a rendered
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
