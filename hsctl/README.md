# hsctl — homeserver control tool

A single Go binary that configures, runs, and backs up the stack, and serves a **web
UI** so non-technical users never need the terminal.

## Build & install

Go is at `~/sdk/go` on this box (no sudo needed to build).

```bash
cd hsctl
make build              # -> ./hsctl   (or: ~/sdk/go/bin/go build -o hsctl .)
make install            # -> /usr/local/bin/hsctl   (uses sudo)
```

So the UI can control Docker without password prompts, add yourself to the docker
group once: `sudo usermod -aG docker $USER` then log out/in.

## CLI

```bash
hsctl setup             # interactive config -> writes every .env + setup.conf
hsctl up                # start the stack (apps + tools -> caddy)
hsctl status            # container status
hsctl down              # stop (down --volumes also deletes data)
hsctl install           # run the dashboard as a systemd service (auto-start on boot)
hsctl get-ca            # write caddy-root-ca.crt for installing on devices
hsctl secrets show      # print the generated logins (read from the .env files)
```

`setup` autodetects the LAN IP/timezone, picks free host ports, reads any existing
`.env` so it stays consistent with a running stack, and saves answers to `setup.conf`
(re-run non-interactively with `--yes`, or pass `--server-ip`, `--email`, etc.).

## Backups & restore

```bash
hsctl backup config --repo <dest>     # set destination (also: --retention, --password)
sudo hsctl backup init                # create the encrypted repo (first time)
sudo hsctl backup run                 # snapshot: Postgres dump + data volumes + config
hsctl backup list                     # list snapshots
sudo hsctl backup restore [snap] --target <dir>   # default snapshot: latest
sudo hsctl backup forget              # apply the retention policy + prune
```

Two config files (gitignored, in the repo root):

- **`backup.conf`** — `RESTIC_REPO` (the destination) and `RETENTION` (restic forget
  policy). Written by `backup config`; see [`backup.conf.example`](../backup.conf.example).
  Destinations: local path / USB (`/mnt/usb/restic`), another host (`sftp:user@host:/path`),
  Backblaze (`b2:bucket:path`), or S3 (`s3:…`).
- **`.restic-password`** — the repo encryption password. Set your own with
  `hsctl backup config --password '…'` (before `init`), or leave it to auto-generate on
  first `init`/`run`. **Back this up separately** — without it the backups are unrecoverable.
  Full details (changing it later, restoring with plain restic) → [CONFIG.md](../CONFIG.md#backups).

`init`/`run`/`restore`/`forget` need **restic installed** and **root** (to read the Docker
volume files). The full disaster-recovery walkthrough (putting the volumes + DB dump back)
is in the main [README](../README.md#backup--restore).

## Shell completion

hsctl uses Cobra, so `hsctl <Tab>` completes commands and flags. Enable it for your shell:

```bash
source <(hsctl completion bash)        # this session (use zsh / fish as needed)
# persistent (bash):
hsctl completion bash | sudo tee /etc/bash_completion.d/hsctl >/dev/null
```

## Web UI

```bash
hsctl ui              # binds :<UI port>; reach it at https://<server-ip> via Caddy,
                      # or http://<server-ip>:8088 directly
```

- **`/`** — the dashboard / home page: tiles for every app (from `services.json`, so it
  updates when you add/remove one) + one-click **certificate install**. No login.
- **`/admin`** — sign in at `/login` (user `admin`, password in `.ui-password`; a form,
  not Basic Auth, so Bitwarden/Vaultwarden can autofill it — login sets a session cookie):
  container status + Start/Stop/Restart, **Shut down server** (graceful power-off; the
  apps auto-start again on next boot), and **Backups** (set destination, run, view
  snapshots).

**Make it permanent (auto-start on boot):**

```bash
hsctl install        # installs + enables the dashboard systemd service (uses sudo)
```

The app **containers** already come back on reboot (`restart: unless-stopped`); `hsctl
install` does the same for the dashboard process. For the nightly backup timer too, use
`make install-services` (edit `systemd/*.service` first: set `__DIR__` to this repo).

## Files it creates (all gitignored)

`setup.conf` (your settings) · `WELCOME.txt` (handout) · `.ui-password` (dashboard admin) ·
`backup.conf` (backup destination) · `.restic-password` (back this up separately!).
