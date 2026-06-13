# Homeserver

A self-hosted stack, managed by **`hsctl`** (a small Go tool with a web dashboard).
Each service lives in its own folder with its own `docker-compose.yml`. Apps are reached
over **HTTPS at the server's LAN IP + a port**. `HOST` below = the server's LAN IP.

| Tile | What | Folder | URL |
|------|------|--------|-----|
| 🏠 **Dashboard** | Home page — links to everything (from `services.json`) | `hsctl/` | **https://HOST** |
| 🔑 Vaultwarden | Password manager (Bitwarden-compatible) | `vaultwarden/` | https://HOST:8443 |
| ☁️ Nextcloud | Cloud file storage | `nextcloud/` | https://HOST:8444 |
| 🛡️ Pi-hole | Network ad-blocker | `pihole/` | https://HOST:8445/admin |
| 📄 Stirling-PDF | Compress / merge / split / convert PDFs | `stirling/` | https://HOST:8446 |
| 🧰 IT-Tools | Converters, QR, hashes, image↔base64 | `it-tools/` | https://HOST:8447 |
| 🖼️ Image tool | Resize / compress-to-KB / convert (in-browser) | `imagetools/` | https://HOST:8448 |
| Caddy | HTTPS front door — one cert per app, serves the CA on http://HOST/ | `caddy/` | — |

The dashboard at **https://HOST** is the home page; it renders from `services.json`, so
adding or removing an app updates it automatically.

## Quick start

`hsctl` is a single Go binary (no external deps). Build it once, then:

```bash
cd hsctl && ~/sdk/go/bin/go build -o hsctl . && sudo install -m755 hsctl /usr/local/bin/hsctl && cd ..
hsctl setup        # configure: LAN IP, timezone, admin email, ports (Enter = autodetected default)
hsctl up           # start everything (apps + tools, then caddy)
hsctl install      # run the dashboard as a service (auto-starts on boot)
hsctl get-ca       # extract caddy-root-ca.crt to install on your devices
```

- Add yourself to the docker group once so `hsctl` needs no sudo: `sudo usermod -aG docker $USER` (then re-login).
- `setup` autodetects sensible defaults, reads existing `.env` to stay consistent, and saves
  answers to `setup.conf`. It never overwrites an existing `.env` (`--force` regenerates);
  `--yes` runs unattended. Generated logins are printed and saved to `.secrets.txt`.
- Full CLI reference: **[hsctl/README.md](hsctl/README.md)**.

## Accessing the apps — install the certificate first

Caddy serves HTTPS using its own local CA (the cert carries the server IP). Each device
must **trust that CA once**, or browsers warn and the Bitwarden/Nextcloud apps refuse to
connect:

1. On the device, open **http://HOST/** and download `root.crt`.
2. Install it as a trusted CA (per-OS steps + the rest of the family flow are in
   **[ONBOARDING.md](ONBOARDING.md)**).

After that, every `https://HOST[:port]` URL is trusted.

## Operations

```bash
hsctl up            # start the stack (apps + tools -> caddy)
hsctl down          # stop it (down --volumes also deletes data)
hsctl status        # container status
hsctl backup config --repo <dest>   # set a destination (USB / sftp: / b2: / s3:)
hsctl backup run    # encrypted snapshot (restic): Postgres dump + data volumes + config
```

The app **containers** auto-start on boot (`restart: unless-stopped`); `hsctl install` does
the same for the dashboard. Backups are encrypted client-side by restic — keep
`.restic-password` somewhere safe (without it, backups are unrecoverable). Backed-up
volumes: `vaultwarden_vw-data`, `nextcloud_nc-data`, `nextcloud_db-data`, `caddy_caddy-data`.

## Pi-hole / ad-blocking

Pi-hole is a network-wide ad-blocker. To ad-block every device automatically, point the
router's DHCP **Primary DNS** at the server's IP (leave Secondary blank) and give the
server a **DHCP reservation**.

> **Tradeoff:** Pi-hole then becomes the LAN's only resolver — if the box is down, the LAN
> loses DNS until you clear that field (~30-second revert). Don't add a public "secondary"
> (OSes query both in parallel, so blocking leaks). Or set it per device, or skip it — the
> apps work regardless.

**Port 53:** if another resolver already holds it (`systemd-resolved`'s loopback stub, or
libvirt/LXC dnsmasq), `hsctl setup` binds Pi-hole to the LAN IP so they don't clash; where
:53 is free it uses `0.0.0.0`. Check with `sudo ss -tulpn | grep ':53 '`.

If the server is on **WiFi**, disable "client/AP isolation" on the router or it'll be
unreachable from other LAN devices.

## Adding an app to the dashboard

1. Create `myapp/docker-compose.yml` (publish its HTTP port, e.g. `8093:80`).
2. In `caddy/Caddyfile` add an HTTPS block on a new port; add the upstream + port to
   `caddy/docker-compose.yml`, `caddy/.env`, and (so a fresh `setup` includes them)
   `hsctl/env.go`.
3. Add a tile to **`services.json`** with that `https_port`.
4. `hsctl up` (and recreate caddy). The tile appears on the dashboard automatically.

## Notes

- **Nextcloud server-side encryption** is off by default — enable it in *Admin → Settings →
  Security* (or `occ app:enable encryption && occ encryption:enable`). The host disk is
  already LUKS-encrypted, which covers at-rest.
- **Firewall:** if you run `ufw`, allow `sudo ufw allow 80,443,8443,8444,8445,8446,8447,8448,53/tcp && sudo ufw allow 53/udp`.
- **More docs:** [hsctl/README.md](hsctl/README.md) (the tool reference) and
  [ONBOARDING.md](ONBOARDING.md) (per-device setup for family).
