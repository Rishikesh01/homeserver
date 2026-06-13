# Homeserver

A self-hosted stack for your home network — a password manager, cloud file storage, an
ad-blocker, and a few web tools — managed by **`hsctl`**, a small command-line tool with a
web dashboard. Everything runs in Docker on one always-on Linux machine and is reached over
HTTPS at that machine's IP address. `HOST` below = your server's LAN IP (e.g. `192.168.0.150`).

| Tile | What it is | Folder | URL |
|------|-----------|--------|-----|
| 🏠 **Dashboard** | Home page — links to everything | `hsctl/` | **https://HOST** |
| 🔑 Vaultwarden | Password manager (works with the Bitwarden apps) | `vaultwarden/` | https://HOST:8443 |
| ☁️ Nextcloud | Cloud file storage / photos / calendar | `nextcloud/` | https://HOST:8444 |
| 🛡️ Pi-hole | Network ad-blocker (admin) | `pihole/` | https://HOST:8445/admin |
| 📄 Stirling-PDF | Compress / merge / split / convert PDFs | `stirling/` | https://HOST:8446 |
| 🧰 IT-Tools | Converters, QR codes, hashes, image↔base64 | `it-tools/` | https://HOST:8447 |
| 🖼️ Image tool | Resize / compress-to-KB / convert (in your browser) | `imagetools/` | https://HOST:8448 |
| Caddy | The HTTPS front door — one cert per app | `caddy/` | — |

The dashboard at **https://HOST** is the home page; it's built from `services.json`, so
adding or removing an app updates it automatically.

---

## Prerequisites

You need, on the machine that will be the server:

1. **A Linux machine that stays on** (a spare PC, mini-PC, or NUC). These steps assume
   Ubuntu/Debian; adjust package commands for other distros.
2. **A user account with `sudo`.**
3. **Docker Engine + Docker Compose v2:**
   ```bash
   curl -fsSL https://get.docker.com | sh
   docker --version && docker compose version    # both should print a version
   ```
4. **Go** (to build `hsctl` once). Either `sudo apt install golang-go`, or download from
   <https://go.dev/dl/> and unpack into `~/sdk/go` (then use `~/sdk/go/bin/go`).
5. **A fixed LAN IP for the server.** In your router, give the server a **DHCP reservation**
   (a.k.a. static lease) so its IP never changes. Note that IP — it's `HOST` everywhere below.

---

## Setup — step by step

```bash
# 1. Get the code onto the server, then enter the folder
git clone <this-repo> homeserver && cd homeserver

# 2. Build hsctl and install it so you can run it from anywhere
cd hsctl && go build -o hsctl . && sudo install -m755 hsctl /usr/local/bin/hsctl && cd ..

# 3. Let your user run Docker without sudo (log out + back in afterwards)
sudo usermod -aG docker $USER

# 4. Configure (press Enter to accept each suggested default)
hsctl setup

# 5. Start everything
hsctl up

# 6. Keep the dashboard running + auto-start it on every boot
hsctl install
```

**What `hsctl setup` asks:** your server's LAN IP, timezone, an admin email, and the host
ports for each app — all pre-filled with sensible autodetected values, so you can usually
just press Enter through it. It writes the configuration to `setup.conf` and generates each
service's secrets.

**Your generated logins** are printed once at the end of `setup`. You can see them again
anytime — `hsctl secrets show` reads them straight from the `.env` files:

```bash
hsctl secrets show     # admin token + Vaultwarden/Pi-hole/dashboard passwords
# ... save those into Vaultwarden ...
```

> **These secrets are plaintext on disk** (in each service's `.env` — the stack needs them
> there). There's no extra copy to clean up, and **full-disk encryption is what protects
> them at rest** — see [Security](#security).

After `hsctl up`, check everything is running with `hsctl status`.

---

## Accessing the apps — install the certificate (once per device)

The server makes its own HTTPS certificate (there's no public domain). Each phone/laptop
must **trust that certificate once**, or browsers show a warning and the Bitwarden/Nextcloud
apps refuse to connect.

1. On the device, open **http://HOST/** in a browser and download **`root.crt`**.
2. Install it as a *trusted certificate authority*. Step-by-step per OS (Android, iPhone,
   Windows, Mac), plus the whole "add a family member" flow, is in
   **[ONBOARDING.md](ONBOARDING.md)**.

Then open **https://HOST** — that's your dashboard, linking to every app.

---

## Security

This stack stores your passwords and files, so treat the server like a safe.

- **Encrypt the server's disk (most important).** The apps store data *unencrypted on disk*
  — anyone who takes the drive can read your passwords and files. Use **full-disk encryption
  (LUKS)**: the easiest way is to tick **"Encrypt the new installation"** when installing
  Ubuntu. Without it, none of the rest matters if the machine is stolen.
- **Keep it on your LAN only — do not expose it to the internet.** Don't port-forward any of
  these ports on your router. The stack is designed for home-network access; there's no
  remote access by design (a home box you can't guarantee is online shouldn't be a VPN
  endpoint).
- **Firewall (optional but nice).** If you run `ufw`, allow only what's needed:
  ```bash
  sudo ufw allow 80,443,8443,8444,8445,8446,8447,8448,53/tcp && sudo ufw allow 53/udp
  ```
- **Protect the secret files.** `.env`, `.secrets.txt`, `.ui-password`, `.restic-password`
  are all `chmod 600` and git-ignored — never commit them, and back up `.restic-password`
  separately (see below). The **certificate authority's private key** lives in the
  `caddy-data` volume; anyone with it could impersonate your sites, so it's backed up and
  shouldn't leak.
- **Use strong, unique passwords and turn on 2FA.** Especially the Vaultwarden master
  password (nobody can reset it for you) and the Nextcloud/Pi-hole admin logins.
- **Keep things updated.** All images are pinned to specific versions (reproducible). To
  update one, bump the tag in that service's `docker-compose.yml`, then `cd <service> &&
  docker compose pull && docker compose up -d`. Keep the OS patched too (`sudo apt update
  && sudo apt upgrade`).
- **Encrypt Nextcloud (optional).** Server-side encryption is off by default; enable it in
  *Nextcloud → Admin → Settings → Security* if you want it on top of disk encryption.

---

## Changing & rotating passwords

After first setup it's good practice to set your real passwords *inside each app*. For the
human logins, the value in `.env` is only used to bootstrap the account — once you change
it in the app, the `.env` value is stale (and for Nextcloud you can blank it).

- **Vaultwarden** — your **master password** isn't in `.env` at all; you set it when you
  create your account and change it in the web vault (*Account settings → Master password*).
  The only `.env` value is the `/admin` panel token (`VW_ADMIN_TOKEN`): change it by editing
  `vaultwarden/.env` then `cd vaultwarden && docker compose up -d --force-recreate`. (You can
  store it as an irreversible Argon2 hash with `docker run --rm vaultwarden/server /vaultwarden hash`.)
- **Nextcloud** — `NC_ADMIN_PASSWORD` is used **only on first install** to create the admin
  user. Change the password in the web UI (*Personal → Security*) or:
  ```bash
  docker compose -f nextcloud/docker-compose.yml exec -u www-data app php occ user:resetpassword admin
  ```
  After that the `.env` value does nothing — you can blank it.
- **Pi-hole** — the admin password is re-applied from the env on each start, so edit
  `PIHOLE_PASSWORD` in `pihole/.env` then `cd pihole && docker compose up -d --force-recreate`.
- **Postgres / Redis** (the machine passwords) — these are never typed by a human, so leave
  them as the long random values `hsctl` generated. Rotating them is a **coordinated** change
  (and note `POSTGRES_PASSWORD` only sets the password on the *first* DB init — changing it in
  `.env` later does nothing). To actually rotate the DB password:
  ```bash
  docker exec -it nextcloud-db psql -U nextcloud -c "ALTER USER nextcloud PASSWORD 'NEW';"
  # then set the same value in nextcloud/.env (POSTGRES_PASSWORD) and recreate the app:
  cd nextcloud && docker compose up -d --force-recreate
  ```

See your current generated logins anytime with `hsctl secrets show`.

---

## Backup & restore

Backups are **encrypted** (restic: AES-256, client-side — the destination only ever sees
ciphertext) and cover a **Postgres dump + every data volume + your config**.

### Choose a destination — off the server

A backup on the same disk only protects against mistakes, not disk failure or theft. Point
it somewhere **off the box**:

| Destination | `--repo` value |
|-------------|----------------|
| External USB drive | `/mnt/usb/restic` |
| Another machine (NAS, Pi) over SSH | `sftp:user@nas:/backups` |
| Cloud (Backblaze B2) | `b2:your-bucket:homeserver` |
| Cloud (S3-compatible) | `s3:s3.region.amazonaws.com/your-bucket` |

### Set it up and run

```bash
sudo apt install -y restic                       # one-time
hsctl backup config --repo /mnt/usb/restic       # set the destination
hsctl backup config --password 'StrongPassword'  # OPTIONAL: set your own repo password
                                                 #   (omit and one is auto-generated)
sudo hsctl backup init                           # create the encrypted repo (first time only)
sudo hsctl backup run                             # take a snapshot
hsctl backup list                                 # see snapshots
```

The repo password lives in **`.restic-password`** (set via `--password`, or auto-generated
on first init). Full details — including changing it later and restoring with plain restic
(no hsctl) — are in **[CONFIG.md → Backups](CONFIG.md#backups)**.

> **Back up `.restic-password` somewhere else** (e.g. write it down, or store it in
> Vaultwarden). It encrypts your backups — **without it, the backups are unrecoverable.**

`backup run` needs `sudo` because it reads the Docker volume files (owned by root).

### Schedule it nightly

```bash
make -C hsctl install-services    # installs a systemd timer (edit systemd/*.service: set __DIR__ first)
```

### Restore (disaster recovery)

```bash
# 1. Extract a snapshot to a folder (latest, or a specific id from `backup list`)
sudo hsctl backup restore latest --target /tmp/restore

# 2. Stop the stack
hsctl down

# 3. Put each volume's files back (the restored tree mirrors the original paths)
for v in vaultwarden_vw-data nextcloud_nc-data nextcloud_db-data caddy_caddy-data; do
  sudo cp -a /tmp/restore/var/lib/docker/volumes/$v/_data/. /var/lib/docker/volumes/$v/_data/
done

# 4. Bring the DB back up and import the SQL dump
( cd nextcloud && docker compose up -d db )
docker exec -i nextcloud-db psql -U nextcloud -d nextcloud \
  < /tmp/restore/$(pwd)/backups/staging/nextcloud-db.sql

# 5. Start everything
hsctl up
```

On a brand-new machine, restore the config files too (the restored `<repo>/*/.env` and
`<repo>/setup.conf`) before `hsctl up`. The restic repo password must be the same one you
saved.

---

## Pi-hole / ad-blocking

Pi-hole is a network-wide ad-blocker. To ad-block **every** device automatically, point your
router's DHCP **Primary DNS** at the server's IP (leave Secondary blank) and keep the server's
DHCP reservation.

> **Tradeoff:** Pi-hole then becomes the only DNS on the LAN — if the server is down, the LAN
> loses DNS until you clear that field (~30-second revert). Don't add a public "secondary"
> (devices query both at random, so blocking leaks). Or set it per device, or skip it.

**Port 53:** if another resolver already holds it (`systemd-resolved`'s loopback stub, or
libvirt/LXC dnsmasq), `hsctl setup` binds Pi-hole to the LAN IP so they don't clash. Check
with `sudo ss -tulpn | grep ':53 '`. If the server is on **WiFi**, disable "client/AP
isolation" on the router or other devices can't reach it.

---

## Day-to-day

```bash
hsctl up | down | status        # start / stop / show the stack
hsctl ui                        # run the dashboard in the foreground (hsctl install runs it as a service)
hsctl get-ca                    # save caddy-root-ca.crt to hand to a new device
hsctl backup run | list | restore
```

The app containers restart automatically on reboot (`restart: unless-stopped`); `hsctl
install` does the same for the dashboard.

## Adding an app to the dashboard

1. Create `myapp/docker-compose.yml` (publish its HTTP port, e.g. `8093:80`).
2. In `caddy/Caddyfile` add an HTTPS block on a new port; add the upstream + port to
   `caddy/docker-compose.yml`, `caddy/.env`, and `hsctl/env.go` (so a fresh `setup` includes them).
3. Add a tile to **`services.json`** with that `https_port`.
4. `hsctl up`. The tile appears on the dashboard.

## More docs

- **[CONFIG.md](CONFIG.md)** — every configurable value, in one place (incl. the restic password).
- **[hsctl/README.md](hsctl/README.md)** — the `hsctl` command reference.
- **[ONBOARDING.md](ONBOARDING.md)** — per-device setup for family members.
