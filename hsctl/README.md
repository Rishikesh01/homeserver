# hsctl — homeserver control tool

A single Go binary (no external Go deps) that configures, runs, and backs up the
stack, and serves a **web UI** so non-technical users never need the terminal.

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
hsctl backup config --repo /mnt/usb/restic   # set a destination (USB/sftp:/b2:/s3:)
sudo hsctl backup init  # create the encrypted restic repo (first time)
sudo hsctl backup run   # snapshot: DB dump + data volumes + config
hsctl backup list       # list snapshots
sudo hsctl backup restore latest --target /tmp/restore   # extract a snapshot (then see README)
```

Backups are encrypted by restic — keep `.restic-password` safe (lost = unrecoverable). The
full restore (disaster-recovery) walkthrough is in the main [README](../README.md#backup--restore).

`setup` autodetects the LAN IP/timezone, picks free host ports, reads any existing
`.env` so it stays consistent with a running stack, and saves answers to `setup.conf`
(re-run non-interactively with `--yes`, or pass `--server-ip`, `--email`, etc.).

## Web UI

```bash
hsctl ui              # binds :<UI port>; reach it at https://<server-ip> via Caddy,
                      # or http://<server-ip>:8088 directly
```

- **`/`** — the dashboard / home page: tiles for every app (from `services.json`, so it
  updates when you add/remove one) + one-click **certificate install**. No login.
- **`/admin`** — basic-auth (user `admin`, password in `.ui-password`): container
  status + Start/Stop/Restart, and **Backups** (set destination, run, view snapshots).

**Make it permanent (auto-start on boot):**

```bash
hsctl install        # installs + enables the dashboard systemd service (uses sudo)
```

The app **containers** already come back on reboot (`restart: unless-stopped`); `hsctl
install` does the same for the dashboard process. For the nightly backup timer too, use
`make install-services` (edit `systemd/*.service` first: set `__DIR__` to this repo).

## Files it creates (all gitignored)

`setup.conf` (your settings) · `WELCOME.txt` (handout) · `.secrets.txt` (logins) ·
`.ui-password` · `backup.conf` · `.restic-password` (back this up separately!).
