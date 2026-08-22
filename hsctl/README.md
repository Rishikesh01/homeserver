# hsctl — homeserver control tool

A single Go binary that configures, runs, and backs up the stack, and serves a **web
UI** so non-technical users never need the terminal.

## Build & install

Needs the Go version in [`go.mod`](go.mod). The Makefile uses `go` from your `PATH` and falls
back to `~/sdk/go/bin/go` for a hand-unpacked toolchain — override with `make build GO=…`.

```bash
cd hsctl
make build              # -> ./hsctl   (version stamped from the git tag; see below)
make vet                # go vet ./...
make test               # go test ./...
make install            # -> /usr/local/bin/hsctl (uses sudo); restarts hsctl-ui if it's running
```

`make build`/`make install` bake the version into `hsctl --version` from the git tag via
`-ldflags` (e.g. `v1.1.0`, or `v1.1.0-3-gabc123` past a tag). A bare `go build .` (no ldflags)
reports `dev` — fine for local hacking; use `make` for anything you install.

So the UI can control Docker without password prompts, add yourself to the docker
group once: `sudo usermod -aG docker $USER` then log out/in.

## CLI

```bash
hsctl setup             # interactive config -> writes every .env + setup.conf
hsctl up                # start the stack (apps + tools -> caddy)
hsctl status            # container status
hsctl down              # stop (down --volumes also deletes data)
hsctl updates           # check whether newer app images are available (read-only)
hsctl install           # dashboard as a systemd service; on a fresh box also starts Caddy + opens the setup wizard
hsctl get-ca            # write caddy-root-ca.crt for installing on devices
hsctl secrets show      # print the generated logins (read from the .env files)
hsctl apps              # list apps; `apps enable|disable NAME` switches one on/off (data kept)
hsctl secrets rotate-vw-admin   # new Vaultwarden /admin token (stored Argon2-hashed)
```

`hsctl updates` compares each installed image against its registry digest and reports what's
behind. It downloads nothing and restarts nothing, but it needs `docker buildx`
(`sudo apt-get install -y docker-buildx-plugin`).

Every command above is also a card in the dashboard's Command Center (`/admin/commands`).

`setup` autodetects the LAN IP/timezone, picks a free dashboard port (the apps aren't
published on the LAN — Caddy reaches them over an internal network — so there are no per-app
host ports), reads any existing `.env` so it stays consistent with a running stack, and saves
answers to `setup.conf` (re-run non-interactively with `--yes`, or pass `--server-ip`, `--tz`, etc.).

## Backups & restore

```bash
hsctl backup config --repo <dest>     # set destination (also: --retention, --password)
sudo hsctl backup init                # create the encrypted repo (first time)
sudo hsctl backup run                 # snapshot: Postgres dump + Vaultwarden DB + data volumes + config
hsctl backup list                     # list snapshots
sudo hsctl backup restore [snap] --target <dir>   # extract a snapshot (default: latest)
sudo hsctl backup restore latest --into-volumes   # one-command DR: stop -> restore all volumes -> up
sudo hsctl backup forget              # apply the retention policy + prune
sudo hsctl backup replicate           # copy every snapshot to the off-site replica
```

Three config files (gitignored, in the repo root):

- **`backup.conf`** — `RESTIC_REPO` (the destination), `RETENTION` (restic forget policy),
  and optionally `REQUIRE_MOUNT`, `RESTIC_VERSION` and `REPLICA_REPO`. Written by
  `backup config`; see [`backup.conf.example`](../backup.conf.example). Destinations: local
  path / USB (`/mnt/restic`), another host (`sftp:user@host:/path`), or S3 (`s3:…`).
- **`.restic-password`** — the repo encryption password. Set your own with
  `hsctl backup config --password '…'` (before `init`), or leave it to auto-generate on
  first `init`/`run`. **Back this up separately** — without it the backups are unrecoverable.
  Full details (changing it later, restoring with plain restic) →
  [docs/configuration.md](../docs/configuration.md#backups).
- **`.backup-env`** — cloud credentials (S3) for a remote repo or replica, one `KEY=VALUE`
  per line. Not needed for local paths or `sftp:`.

`init`/`run`/`restore`/`forget` need **restic installed** and **root** (to read the Docker
volume files). The full disaster-recovery walkthrough (putting the volumes + DB dump back)
is in [docs/backup-restore.md](../docs/backup-restore.md).

`backup verify` is the automated self-test (synthetic data, pass/fail). To instead **see a
real backup restore** into the actual apps without risking the live stack, use the `make`
sandbox: `make sandbox-restore` → [sandbox/README.md](../sandbox/README.md).

## Shell completion

hsctl uses Cobra, so `hsctl <Tab>` completes commands and flags. Enable it for your shell:

```bash
source <(hsctl completion bash)        # this session (use zsh / fish as needed)
# persistent (bash):
hsctl completion bash | sudo tee /etc/bash_completion.d/hsctl >/dev/null
```

## Web UI

```bash
hsctl ui              # reach it at https://<server-ip> via Caddy. With no --addr it binds
                      # loopback + the docker bridge gateway only (so Caddy can reach it),
                      # never the LAN — the dashboard is a root shell, so it stays off the LAN.
```

- **`/`** — the dashboard / home page: tiles for every app (from `services.json`, so it
  updates when you add/remove one) + one-click **certificate install**. No login.
- **`/help`** — the setup guide: renders the repo's `ONBOARDING.md` with the server IP filled
  in, so you can send a new user one link instead of a file. No login.
- **`/root.crt`** — the CA certificate download. No login.
- **`/admin`** — sign in at `/login` (user `admin`, password in `.ui-password`; a form,
  not Basic Auth, so Bitwarden/Vaultwarden can autofill it — login sets a session cookie).
  The page shows **system health** — CPU, memory, root and backup disk space, per-disk SMART
  status (via `smartctl`, if installed) and how long ago the last backup ran, flagged when it
  goes stale — then the container table with Start all / Restart / Stop all and **Shut down
  server** (a graceful power-off; the apps auto-start again on next boot). Plus four tools:
  - **🧰 Commands** (`/admin/commands`) — every `hsctl` command as an explained card with a
    Run button; output streams live. Each maps to a fixed argument list (the browser only
    sends a slug), so there's no command-injection surface. Destructive ones are flagged red
    and confirm first.
  - **💽 Drives** (`/admin/devices`) — lists the attached disks (`lsblk`) and mounts one with
    a click; the mount directory is pre-filled with a suggestion under `/mnt` and you can edit
    it. Plus Eject. Mounts are one-shot — no `/etc/fstab` changes, so they clear on reboot.
  - **💾 Backups** (`/admin/backup`) — destination, retention and off-site replica, live status
    (restic version, `REQUIRE_MOUNT` guard, repo size), and streamed Initialize / Back up /
    Prune / Self-test / Copy off-site, plus the destructive **Restore**.
  - **⌨️ Terminal** (`/admin/terminal`) — a real shell on the server (xterm.js over a
    WebSocket; admin-session gated + Origin-checked). Powerful — it's a root shell on the LAN.

**Make it permanent (auto-start on boot):**

```bash
hsctl install        # installs + enables the dashboard systemd service (uses sudo); first run also starts Caddy so the setup wizard is reachable
```

The app **containers** already come back on reboot (`restart: unless-stopped`); `hsctl
install` does the same for the dashboard process. For the nightly backup timer too, use
`make install-services` (it fills `__DIR__` in the unit templates with this repo's path).

## Files it creates (all gitignored)

`setup.conf` (your settings, `0600`) · `WELCOME.txt` (the logins handout — `0644`, so delete
it once you've saved them) · `.ui-password` (dashboard admin, `0600`) · `backup.conf` (backup
destination, `0600`) · `.restic-password` (`0600` — back this up separately!) · `.backup-env`
(cloud credentials, `0600`) · `caddy-root-ca.crt` (the CA cert, from `get-ca`).
