# Configuration reference

Every configurable value, where it lives, and how to set it. All files are in the repo
root unless noted; everything with secrets is `chmod 600` and git-ignored (only the
`*.example` templates are committed).

| File | What | Set with |
|------|------|----------|
| `setup.conf` | Main settings (IP, ports, timezone…) | `hsctl setup` |
| `<service>/.env` | Each service's generated secrets + ports | `hsctl setup` (generated) |
| `caddy/.env` | Caddy: server IP, upstreams, HTTPS ports, Let's Encrypt settings + DNS token | `hsctl setup` / `hsctl letsencrypt` (generated) |
| `caddy/generated/Caddyfile` | The complete Caddy config for Let's Encrypt mode (absent otherwise) | `hsctl up` / `hsctl letsencrypt` (generated) |
| `services.json` | Dashboard tiles | edit by hand |
| `backup.conf` | Backup destination, retention, replica, guards | `hsctl backup config` |
| `.restic-password` | Backup repo password | see [Backups](#backups) |
| `.backup-env` | Cloud credentials for the off-site replica | edit by hand |
| `.ui-password` | Dashboard admin password | auto / env / edit |
| `WELCOME.txt` | Printed handout of the generated logins (`0644`) | `hsctl setup` (generated) |

---

## Main settings — `setup.conf`

Written by `hsctl setup` (interactive — press Enter to accept each autodetected default),
or non-interactively with flags (`hsctl setup --yes --server-ip … --email …`). Pre-seed by
copying `setup.conf.example` → `setup.conf`.

| Key | Meaning | Flag |
|-----|---------|------|
| `SERVER_IP` | Server LAN IP (cert SAN, DNS, upstreams) | `--server-ip` |
| `TZ_VAL` | Timezone (e.g. `Asia/Kolkata`) | `--tz` |
| `ACME_EMAIL` | Admin email — the Let's Encrypt account contact | `--email` |
| `UI_PORT` | Dashboard (hsctl ui) port — the one app still on a host port, since it runs on the host | — |
| `PIHOLE_DNS_BIND` | Pi-hole `:53` bind IP (`0.0.0.0` or the LAN IP) | `--pihole-dns-bind` |
| `VW_SIGNUPS_ALLOWED` | Open Vaultwarden signups (`true`/`false`) | `--vw-signups` |
| `DOMAIN` | Public domain for Let's Encrypt (see below); empty = none | `--domain` |
| `LETSENCRYPT` | `true` = `https://<app>.DOMAIN` with Let's Encrypt certificates instead of the private CA | `--letsencrypt` |
| `ACME_CHALLENGE` | `dns` (LAN-only, needs a DNS API token) or `http` (port 80 forwarded) | `--acme-challenge` |
| `ACME_DNS_PROVIDER` | `caddy-dns` plugin for the dns challenge (`cloudflare`, `duckdns`, …) | `--dns-provider` |
| `ACME_STAGING` | `true` = Let's Encrypt's staging CA (untrusted test certificates) | `--acme-staging` |

Changing a value: edit `setup.conf` (or re-run `hsctl setup`), then `hsctl up`. Note that
`hsctl setup` never overwrites an existing service `.env` (use `--force` to regenerate, which
also rotates secrets). The Let's Encrypt keys are the exception: they're settings, so `hsctl up`
/ `hsctl letsencrypt apply` re-render them into `caddy/.env` and the generated sites file.

## Service / HTTPS ports — `caddy/.env`

`caddy/Caddyfile` is the private-CA config (`https://SERVER_IP:<port>` per app) and is complete on
its own — nothing is generated for the default mode. In Let's Encrypt mode hsctl generates a
separate, complete config into `caddy/generated/Caddyfile` (from the table in `hsctl/tls.go`,
using the same env) and sets `CADDY_CONFIG` in `caddy/.env` so `caddy run` loads that one
instead. Both configs import `caddy/sites.d/*.caddy`, where sites of your own go.

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

## Public domain + Let's Encrypt

Optional, off by default; when on it **replaces** the private CA (the `https://SERVER_IP:<port>`
addresses stop being served). Turn it on with `hsctl setup` (the last question), `hsctl letsencrypt
enable …`, or the dashboard's **Domain & HTTPS** page — the walkthrough is in
[Setup → Optional: a public domain](setup.md#optional-a-public-domain-with-lets-encrypt). What it
writes:

| Where | What |
|-------|------|
| `setup.conf` | `DOMAIN`, `LETSENCRYPT`, `ACME_CHALLENGE`, `ACME_DNS_PROVIDER`, `ACME_STAGING` (table above) |
| `caddy/.env` | the same keys, plus **`ACME_DNS_TOKEN`** (the provider's API token — the only place it's stored; `$` is doubled for compose), `CADDY_CONFIG=/etc/caddy/generated/Caddyfile` (which config `caddy run` loads), and `CADDY_IMAGE=homeserver-caddy:<provider>` for the dns challenge. All three are removed when off |
| `caddy/generated/Caddyfile` | a complete config: one `https://<sub>.DOMAIN` site per app on `:443` (so `HOME_HTTPS` must stay `443`), `http://` → `https://` redirects, an info page on `:80` for the bare IP, and the `sites.d` import. Rewritten on every apply, removed when off (Caddy then loads `caddy/Caddyfile` again) |
| `pihole/custom.list` | a marked block of `SERVER_IP <name>` lines, so Pi-hole resolves the names locally; your own lines outside the markers are kept |
| `vaultwarden/.env` | `VW_DOMAIN` = `https://vault.DOMAIN` (back to `https://SERVER_IP:8443` when off) |
| `nextcloud/.env` | `NC_TRUSTED_DOMAINS` gains `cloud.DOMAIN`; a running Nextcloud gets it via `occ` too |

The subdomain of each app is its `key` in `services.json` (`vault`, `cloud`, `pihole`, `pdf`,
`tools`, `image`); add `"host": "…"` to a tile to rename it. The dashboard is the bare domain.
The **dns** challenge needs a Caddy build with the provider's plugin: hsctl builds
`caddy/Dockerfile` (`--build-arg CADDY_DNS_PROVIDER=<name>`) into `homeserver-caddy:<name>` the
first time, and `hsctl letsencrypt apply --rebuild` rebuilds it (after a Caddy or plugin update).
Only plugins configured by a single token work with the generated `dns <provider>
{env.ACME_DNS_TOKEN}` line.

`hsctl letsencrypt status` shows what each name is currently serving (issuer, expiry, trusted or
not); `hsctl letsencrypt logs` shows Caddy's log, where a failed issuance explains itself.

## Dashboard tiles — `services.json`

A JSON array; each entry is a tile the dashboard renders, so adding/removing one updates the
home page. Fields: `key`, `name`, `icon`, `desc`, `https_port`, optional `path`, and optional
`host` (the subdomain under the Let's Encrypt domain; defaults to `key`). Example:

```json
{ "key": "vault", "name": "Passwords", "icon": "🔑", "desc": "Vaultwarden", "https_port": 8443 }
```

## Per-service `.env`

Generated by `hsctl setup`, these hold each app's secrets (DB/Redis passwords, admin
password/token). See `hsctl secrets show` to read the logins, and
[Changing & rotating passwords](security.md#changing--rotating-passwords) for how to
change them post-install.

## Dashboard admin password — `.ui-password`

Auto-generated (random) on the first `hsctl ui`. Used to sign in at `/admin` (the `/login`
form, with user `admin`). To set your own: `export HSCTL_UI_PASSWORD='…'` before running,
or write the file: `printf '%s' 'yourpass' > .ui-password && chmod 600 .ui-password`. View
it with `hsctl secrets show`.

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
| `RETENTION` | restic forget policy applied by `backup run` / `backup forget` | `--retention` |
| `REQUIRE_MOUNT` | A path that must be a real mount before any backup/restore runs | `--require-mount` |
| `RESTIC_VERSION` | Pinned restic version; `backup verify` **fails** if the installed one differs | `--pin-restic` |
| `REPLICA_REPO` | Optional second repo that `backup replicate` copies every snapshot to | `--replica` |

- **`REQUIRE_MOUNT`** stops a backup silently landing on the root disk when an external HDD
  isn't mounted — hsctl compares the path's device id against `/`'s. Set it to the repo's
  mountpoint: `hsctl backup config --require-mount /mnt/restic` (empty string disables).
- **`RESTIC_VERSION`** is recorded by `hsctl backup config --pin-restic`. It catches a system
  upgrade that swaps restic out from under you, instead of silently changing your backup tool.
- **`REPLICA_REPO`** is the off-site copy. `hsctl backup replicate` creates it on first run,
  then copies incrementally with the **same** encryption password, and applies `RETENTION` to
  it as well. Details: [Backup & restore → Off-site replica](backup-restore.md#off-site-replica).

### Cloud credentials — `.backup-env`

Only needed when the repo or replica is a cloud backend; local paths and `sftp:` don't use it
(SFTP uses your SSH key). One `KEY=VALUE` per line, `chmod 600`, git-ignored, lives next to
`.restic-password`:

```bash
# S3-compatible storage:
AWS_ACCESS_KEY_ID=…
AWS_SECRET_ACCESS_KEY=…
```

### The repo password — `.restic-password`

This is the one people miss. Three ways to set it:

1. **Auto** — leave it; `hsctl backup init` / `run` generates a random 32-char password into
   `.restic-password` the first time.
2. **Your own, via hsctl** — `hsctl backup config --password 'YourStrongPassword'` (writes it
   to `.restic-password`). Do this **before** `backup init`.
3. **By hand** — `printf '%s' 'YourStrongPassword' > .restic-password && chmod 600 .restic-password`.

See it any time: `cat .restic-password`.

**Changing it after the repo already exists:** the repo is encrypted with the original
password, so just editing the file to a new value will fail to open it. Rotate it through
restic instead:

```bash
RESTIC_PASSWORD_FILE=.restic-password restic -r "$RESTIC_REPO" key passwd   # prompts for a new one
# then put the new password into .restic-password
```

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
| `<repo>/backups/staging/nextcloud-db.sql` | consistent Postgres dump (fallback if the DB volume won't start) |
| `<repo>/*/.env`, `<repo>/setup.conf` | per-service config and the main server config |

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
stack, `cp -a` each `_data` back, overlay the Vaultwarden fileset, start up).
