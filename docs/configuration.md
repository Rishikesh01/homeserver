# Configuration reference

Every configurable value, where it lives, and how to set it. All files are in the repo
root unless noted; everything with secrets is `chmod 600` and git-ignored (only the
`*.example` templates are committed).

| File | What | Set with |
|------|------|----------|
| `setup.conf` | Main settings (IP, ports, timezone…) | `hsctl setup` |
| `<service>/.env` | Each service's generated secrets + ports | `hsctl setup` (generated) |
| `caddy/.env` | Caddy: server IP, upstreams, HTTPS ports | `hsctl setup` (generated) |
| `services.json` | Dashboard tiles | edit by hand |
| `backup.conf` | Backup destination, retention, replica, guards | `hsctl backup config` |
| `.restic-password` | Backup repo password | see [Backups](#backups) |
| `.backup-env` | Cloud credentials for the off-site replica | edit by hand |
| `.ui-password` | Dashboard admin password | auto / `hsctl secrets set-ui-password` |
| `WELCOME.txt` | Printed handout of the addresses (no secrets, `0644`) | `hsctl setup` (generated) |

Files hsctl writes stay **owned by the repo's owner** (you), even when root writes them — `sudo
hsctl …`, the dashboard (a root service) or the nightly backup timer. So you can always read and
edit your own config, and `docker compose` keeps working as you. If an older hsctl left some of
them root-owned, any `sudo hsctl …` command (or the dashboard starting) gives them back; until
then hsctl says which files it can't read and the `chown` that fixes them, instead of treating
them as missing.

---

## Main settings — `setup.conf`

Written by `hsctl setup` (interactive — press Enter to accept each autodetected default; a
value that doesn't look right is explained and asked again), by the dashboard's setup wizard, or
non-interactively with flags (`hsctl setup --yes --server-ip … --tz …`, which refuses invalid
values). Pre-seed an unattended setup by copying `setup.conf.example` → `setup.conf`; keep
comments on their own lines (an inline `# …` after a value is ignored, as in a compose `.env`).

| Key | Meaning | Flag |
|-----|---------|------|
| `SERVER_IP` | Server LAN IP (cert SAN, DNS, upstreams) | `--server-ip` |
| `TZ_VAL` | Timezone (e.g. `Asia/Kolkata`) | `--tz` |
| `ACME_EMAIL` | Certificate contact email (only used if you move to a real domain; edit `setup.conf` manually) | — |
| `UI_PORT` | Dashboard (hsctl ui) port — the one app still on a host port, since it runs on the host | — |
| `PIHOLE_DNS_BIND` | Pi-hole `:53` bind IP (`0.0.0.0` or the LAN IP) | `--pihole-dns-bind` |
| `VW_SIGNUPS_ALLOWED` | Open Vaultwarden signups (`true`/`false`) | `--vw-signups` |
| `DISABLED_APPS` | Comma-separated apps switched off (compose dir names, e.g. `stirling,it-tools`). `hsctl up` skips them, their tiles are hidden; data volumes are kept. Toggle from Admin → Apps or `hsctl apps enable|disable` | `--disable-apps` |

**Changing a value:** re-run `hsctl setup` (or edit `setup.conf` and run `hsctl setup --yes`),
then `hsctl up`. Setup writes the changed settings into the existing `.env` files and lists each
change — secrets are never touched — so the stack picks them up on the next `hsctl up`. On the
dashboard: edit `setup.conf`, then Command Center → *Apply settings*, then *Start all services*.

**Moving to a new IP** (a new box, a new router): `hsctl setup --server-ip <new IP>` updates
Caddy's certificate address, Vaultwarden's `VW_DOMAIN` and Nextcloud's trusted domains (in its
`.env`, and in Nextcloud's own config via `occ` once it's running), and re-derives a Pi-hole DNS
bind that followed the old IP. Then `hsctl up`, and install the certificate again on each device
if Caddy issued a new one.

**`hsctl setup --force`** generates new secrets, in place: the Vaultwarden admin token and the
Pi-hole and Redis passwords. Nextcloud's database and admin passwords are **kept** while Nextcloud
has data (Postgres and Nextcloud only read them when they first initialize, so rotating them would
lock Nextcloud out of its own database, or print a login that doesn't work). It asks you to type
`REGENERATE` first (`--yes` skips that).

## Service / HTTPS ports — `caddy/.env`

The apps are **not** published on the LAN. They share an internal docker network
(`homeserver-edge`, created by hsctl) and Caddy reaches each by container name — so the only
way in is Caddy's HTTPS, and no app is exposed as plaintext HTTP on the LAN. The one exception
is the hsctl dashboard, which runs on the host: Caddy reaches it via the docker host gateway
(`HOME_UPSTREAM=host.docker.internal:<UI_PORT>`), and hsctl binds it to that gateway + loopback
only. Pi-hole's DNS (`:53`) is still published — that's the point of Pi-hole.

- **Upstreams** — container names, set as defaults in `caddy/docker-compose.yml`
  (`VAULT_UPSTREAM=vaultwarden:80`, `CLOUD_UPSTREAM=nextcloud-app:80`, …). Override in
  `caddy/.env` only if you rename a service.
- **HTTPS ports** — what you actually browse: `https://SERVER_IP:<port>`. Set in `caddy/.env`:
  `VAULT_HTTPS=8443`, `CLOUD_HTTPS=8444`, `PIHOLE_HTTPS=8445`, `HOME_HTTPS=443` (the
  dashboard), and the tools `8446/8447/8448`.

To change a service's HTTPS port you edit it in **three** places so they agree: `caddy/.env`,
the matching block in `caddy/Caddyfile`, and its tile in `services.json`. Then
`cd caddy && docker compose up -d --force-recreate`.

The **dashboard's own port** is different: it's a setting (`UI_PORT`). Change it with
`hsctl setup` (it follows into `caddy/.env`'s `HOME_UPSTREAM`), then restart both:
`hsctl up` and `sudo systemctl restart hsctl-ui`. The dashboard always listens on the port Caddy
forwards to, so a `UI_PORT` edited by hand but not yet applied can't leave Caddy pointing at
nothing.

## Dashboard tiles — `services.json`

A JSON array; each entry is a tile the dashboard renders, so adding/removing one updates the
home page. Fields: `key`, `name`, `icon`, `desc`, `https_port`, optional `path`. Example:

```json
{ "key": "vault", "name": "Passwords", "icon": "🔑", "desc": "Vaultwarden", "https_port": 8443 }
```

## Per-service `.env`

Generated by `hsctl setup`, these hold each app's secrets (DB/Redis passwords, admin
password/token). See `hsctl secrets show` to read the logins, and
[Changing & rotating passwords](security.md#changing--rotating-passwords) for how to
change them post-install.

## Dashboard admin password — `.ui-password`

Generated (random) by `sudo hsctl install`, which prints it. Used to sign in at `/admin` (the
`/login` form, with user `admin`). View it with `hsctl secrets show`. To set your own (or a new
random one):

```bash
hsctl secrets set-ui-password 'your-new-password'   # or no argument for a random one
```

That writes `.ui-password` and restarts the dashboard, which reads it only at startup.
(`HSCTL_UI_PASSWORD` overrides the file for a dashboard you run by hand with `hsctl ui`; the
systemd service doesn't see your shell's environment.) Wrong passwords are throttled: after a few
in a row, each further one locks that client out for longer, up to 15 minutes.

---

## Backups

Two pieces: the **destination/policy** (`backup.conf`) and the **repo password**
(`.restic-password`). hsctl wraps restic — see [`backup.conf.example`](../backup.conf.example)
and the [Backup & restore guide](backup-restore.md).

### Destination & retention — `backup.conf`

Written by `hsctl backup config`; every key is optional except `RESTIC_REPO`.

```bash
hsctl backup config --repo /mnt/restic \
  --retention "--keep-daily 7 --keep-weekly 4 --keep-monthly 6"
```

| Key | Meaning | Flag |
|-----|---------|------|
| `RESTIC_REPO` | Where backups go: local path / `sftp:user@host:/path` / `s3:…` (off-box!) | `--repo` |
| `RETENTION` | restic `--keep-*` options applied by `backup run` / `backup forget` | `--retention` |
| `REQUIRE_MOUNT` | A path that must be on a mounted backup disk before any backup/restore runs | `--require-mount` |
| `RESTIC_VERSION` | Pinned restic version; `backup verify` **fails** if the installed one differs | `--pin-restic` |
| `REPLICA_REPO` | Optional second repo every backup copies itself to | `--replica` |
| `NOTIFY_URL` | Optional URL that gets a plain-text POST when a backup run fails (e.g. an ntfy topic) | `--notify-url` |
| `BACKUP_IMAGES` | `yes` saves the app images themselves into every backup, not just their versions | `--images` |

- **`RETENTION`** only accepts restic's `--keep-*` options and `--group-by`; anything else is
  refused (restic would take a stray word for a snapshot ID to delete). Snapshots are grouped by
  tag unless you add `--group-by`.
- **`REQUIRE_MOUNT`** stops a backup silently landing on the system disk when an external disk
  isn't mounted — hsctl refuses unless the path is on a different disk from `/`, the homeserver
  folder and Docker's data. A `RESTIC_REPO` under `/mnt/…` or `/media/…` sets it automatically
  (`/mnt/backup/restic` → `/mnt/backup`); set it yourself with
  `hsctl backup config --require-mount /mnt/backup` (empty string disables).
- **`RESTIC_VERSION`** is recorded by `hsctl backup config --pin-restic`. It catches a system
  upgrade that swaps restic out from under you, instead of silently changing your backup tool.
- **`BACKUP_IMAGES`** is off by default: every backup records which image each app ran either
  way, and a restore pins those versions again. With it on, the images come from the backup too,
  so a restore doesn't depend on a registry still serving them — a few GB the first time,
  deduplicated after that. An image that can't be saved stops the backup (no snapshot, the run
  fails). `hsctl backup run --images` (or `--images=false`) overrides it for one run. Details: [Backup & restore → App versions and images](backup-restore.md#app-versions-and-images).
- **`REPLICA_REPO`** is the off-site copy. Every backup copies itself there (the first copy
  creates it), incrementally, with the **same** encryption password, and applies `RETENTION` to
  it as well. Details: [Backup & restore → Off-site replica](backup-restore.md#off-site-replica).

### Cloud credentials — `.backup-env`

Only needed when the repo or replica is a cloud backend; local paths and `sftp:` don't use it
(SFTP uses **root's** SSH key, since backups run as root — see
[Off-site replica](backup-restore.md#off-site-replica)). One `KEY=VALUE` per line, `chmod 600`, git-ignored, lives next to
`.restic-password`:

```bash
# S3-compatible storage:
AWS_ACCESS_KEY_ID=…
AWS_SECRET_ACCESS_KEY=…
```

### The repo password — `.restic-password`

This is the one people miss. Ways to set it:

1. **Auto** — leave it; `hsctl backup init` generates a random 32-char password into
   `.restic-password` when it creates the repository. Nothing else ever generates one: with the
   file missing, every other backup command stops and asks for your saved password.
2. **The dashboard** — Backups page → *Backup password*.
3. **The CLI** — `hsctl backup config --password -` (reads it from stdin, so it stays out of your
   shell history and `ps`; `--password 'YourStrongPassword'` works too).

Either way, hsctl does what keeps the backups readable:

- **No repository yet:** it's saved, and Initialize creates the repository with it (12+ characters).
- **It opens the existing repository** (a new machine, a reinstall): you're putting your saved
  password back — it's saved.
- **The saved password opens the repository:** it's a **change** — the repository's own password
  is changed with `restic key passwd`, on the off-site replica too (they share one), and only then
  is the file updated. Every existing snapshot stays readable with the new password; the old one
  stops working.
- **Neither opens it:** refused, nothing changes.

See it any time: `hsctl secrets show` (or Command Center → Show logins).

**Two musts:**
- **Keep a copy of the password OFF the server** (write it down / another vault). It lives in
  `.restic-password` on the box; if the box dies and you don't have it elsewhere, the backups
  are mathematically unrecoverable.
- **You don't need hsctl to restore** — it's a standard restic repo. See below.

### Manual disaster recovery — plain restic, no hsctl

hsctl just wraps `restic`; the repo is a vanilla restic repository. If the server is gone
and all you have is the **backup destination** + the **repo password**, you can decrypt and
extract everything from any machine with the `restic` binary — no Go, no Docker, no this repo.

You need exactly two things:

1. **The repo location** — wherever you pointed `RESTIC_REPO` (the USB drive, or the
   `sftp:` / `s3:` URL).
2. **The password** — the contents of `.restic-password` (this is why you keep a copy off the box).

```bash
sudo apt install -y restic          # any machine; the repo format is portable across OSes.
                                    # Use restic >= 0.14 (the repo is v2 format); ideally match the
                                    # RESTIC_VERSION in backup.conf — a too-old restic can't open it.

export RESTIC_REPOSITORY=/mnt/restic               # your RESTIC_REPO (or sftp:user@nas:/backups, s3:…)
export RESTIC_PASSWORD_FILE=/path/to/.restic-password   # the password FILE — keeps the secret out of shell
                                                        # history. No file handy? `read -rs RESTIC_PASSWORD;
                                                        # export RESTIC_PASSWORD` types it in without echoing.

restic snapshots                                   # decrypts the repo + lists every backup
restic restore latest --target ~/restore           # ~/restore, not /tmp (often RAM-backed — a big restore
                                                   # can fill it); or restore a specific id from the list
```

For a **remote** repo, also export the backend's credentials before running restic — e.g.
`AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` for S3. (An `sftp:` repo just uses your SSH key.)

**What's inside `~/restore`** (the snapshot mirrors the original absolute paths):

| Path under the target | What it is |
|-----------------------|------------|
| `var/lib/docker/volumes/<name>/_data/` | every data volume — Nextcloud files, the Postgres DB volume, Pi-hole config, Caddy certs, Vaultwarden attachments/keys |
| `<repo>/backups/staging/vaultwarden/db.sqlite3*` | Vaultwarden's **consistent DB fileset** (`db.sqlite3` + `-wal` + `-shm`) — excluded from the volume, lives only here |
| `<repo>/backups/staging/nextcloud-db.sql` | consistent Postgres dump (the DB volume was copied live; restore the database from this) |
| `<repo>/backups/staging/images.json` | the image every app ran when the backup was taken (pin these in each service's `.env` before starting it) |
| `<repo>/backups/staging/images/*.tar` | the app images themselves, when the backup included them — `docker load -i <file>` |
| `<repo>/*/.env`, `<repo>/setup.conf`, `<repo>/backup.conf`, … | per-service config, the main server config, and the rest of hsctl's config |

`<repo>` is the absolute path the server used, e.g. `/home/you/homeserver`.

**Just need your passwords back?** Vaultwarden is plain SQLite — you don't even have to boot
the stack. Point a fresh Vaultwarden at the restored fileset, or read it directly. Dump the
**whole fileset** (main + `-wal` + `-shm`), not just `db.sqlite3`: the `-wal` can hold recent
entries not yet folded into the main file, and SQLite replays it only if it's named alongside:

```bash
base=/home/you/homeserver/backups/staging/vaultwarden/db.sqlite3
for suf in "" -wal -shm; do                          # -wal/-shm may be absent (already checkpointed) — fine
  restic dump latest "$base$suf" > "vault.sqlite3$suf" 2>/dev/null || true
done
sqlite3 vault.sqlite3 'select name, email from users;'   # WAL replayed on open; cipher data stays encrypted
```

**Browse without extracting** — inspect a snapshot, or pull a single file:

```bash
restic ls latest                       # list every path in the snapshot
restic mount /mnt/snap                 # FUSE-mount all snapshots read-only (needs fuse3); browse, then Ctrl-C
restic dump latest <path-from-ls>      # stream one file to stdout (no FUSE needed)
```

To actually put the extracted tree back into a running stack, follow the **Manual restore**
steps in [Backup & restore → Restore](backup-restore.md#restore-disaster-recovery) (stop the
stack, empty and refill each `_data`, overlay the Vaultwarden fileset, rebuild Nextcloud's
database from the dump, start up).
