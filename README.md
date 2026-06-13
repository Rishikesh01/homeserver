# Homeserver

Self-hosted stack. Each service lives in its own folder with its own `docker-compose.yml`
and `.env`, so you can start/stop them independently.

Reached over **HTTPS at the server IP + a port** (no hostnames/TLD). `HOST` = the
server's LAN IP. Caddy terminates TLS with its own CA, so install that CA once per device.

| Service | What | Folder | URL |
|---------|------|--------|-----|
| **Dashboard** (hsctl UI) | Home page linking to everything | `hsctl/` | **https://HOST** |
| Vaultwarden | Password manager (Bitwarden-compatible) | `vaultwarden/` | https://HOST:8443 |
| Nextcloud | Cloud file storage | `nextcloud/` | https://HOST:8444 |
| Pi-hole | Network ad-blocker | `pihole/` | https://HOST:8445/admin |
| Caddy | HTTPS front door (one cert per app) | `caddy/` | serves the above + CA on http://HOST/ |

Start order: the three services first, then `caddy/`. The whole stack is driven by
**`hsctl`** (a Go tool with a web UI) — see [First-time setup](#first-time-setup). The
dashboard at **https://HOST** is the home page; it renders from `services.json`, so
adding/removing an app updates it.

> **No VPN, no `.lan` names.** Remote VPN was removed (the box's uptime isn't guaranteed),
> and apps are reached by IP:port over HTTPS rather than friendly names. Pi-hole is just a
> network ad-blocker now.

## First-time setup

Everything is driven by **`hsctl`** — a small Go tool (zero external deps) with a **web
UI**, so non-technical users never need a terminal. Build it once, then run setup:

```bash
cd hsctl && ~/sdk/go/bin/go build -o hsctl . && sudo install -m755 hsctl /usr/local/bin/hsctl && cd ..
hsctl setup        # asks: LAN IP, timezone, admin email, ports
hsctl up           # start the stack (services -> caddy)
hsctl get-ca       # write caddy-root-ca.crt to install on devices
hsctl ui           # serve the dashboard -> https://HOST  (or http://HOST:8088 direct)
```

`setup` autodetects sensible defaults (IP / timezone / free ports), reads any existing
`.env` so it stays consistent with a running stack, generates fresh random secrets, and
saves your answers to `setup.conf`. Re-running never overwrites an existing `.env`
(`--force` regenerates); `--yes` runs unattended. Backups (encrypted, via restic):
`hsctl backup`. Full reference: **[hsctl/README.md](hsctl/README.md)**.

So `hsctl`/the UI can control Docker without sudo prompts, add yourself to the docker
group once: `sudo usermod -aG docker $USER` (then log out/in). Onboarding a family member
/ new device → **[ONBOARDING.md](ONBOARDING.md)**.

> The shell scripts (`bootstrap.sh` / `up.sh` / `down.sh` / `get-ca.sh`) still work and do
> the same generation, but `hsctl` supersedes them and adds the web UI + backups.

**Manual path (per service):**

```bash
cd <service>
cp .env.example .env
# edit .env and replace every "change-me" value
docker compose up -d
docker compose logs -f      # watch it come up
```

Generate strong secrets with: `openssl rand -base64 32`

## Deploying to your homeserver + LAN access

Nothing is hardcoded to a particular machine. To stand this up on the real homeserver
and make it reachable across the whole LAN:

1. **Copy the folder** to the homeserver and run `./bootstrap.sh` (it writes
   `pihole/custom.list` and binds DNS appropriately for you).
2. **Port 53** — if another resolver already holds it (`systemd-resolved`'s loopback stub,
   or libvirt/LXC dnsmasq), bootstrap sets `PIHOLE_DNS_BIND` to your LAN IP so the binds
   don't clash; where :53 is free it uses `0.0.0.0` to serve every interface. Check with
   `sudo ss -tulpn | grep ':53 '`.
3. **Give the server a static IP** (or a DHCP reservation). A DNS server whose address
   changes breaks everything pointing at it.
4. **Hand out DNS** — see "DNS strategy" below.

Caddy (`:80`/`:443`) and the app HTTPS ports listen on the LAN. There's no host firewall
here; if your homeserver runs `ufw`, allow:
`sudo ufw allow 80,443,8443,8444,8445,53/tcp && sudo ufw allow 53/udp`.

### Pi-hole ad-blocking (optional)

Apps are reached by IP, so Pi-hole serves **no local names** — it's purely a network
ad-blocker now. To ad-block every device automatically, point the router's DHCP **Primary
DNS** at the server's IP (leave Secondary blank) and give the server a DHCP reservation.

- **Tradeoff (SPOF):** Pi-hole becomes the LAN's only resolver — if the box is down the
  LAN loses DNS until you clear that field (~30-second revert). Don't set a public
  "secondary" (OSes query both in parallel, so blocking leaks).
- Or set it per device, or skip it — the apps work either way (they're reached by IP).

> **WiFi note:** if the server is on WiFi, some access points enable "client/AP isolation"
> which blocks device-to-device traffic and will make the server unreachable from other
> LAN devices. Disable it on the router, or use a wired connection.

## ⚠️ Pi-hole + port 53

Pi-hole needs port 53. Many hosts already have something on it — `systemd-resolved`'s
loopback stub (`127.0.0.53`), or libvirt/LXC `dnsmasq`. Binding Pi-hole to `0.0.0.0:53`
would then clash. `bootstrap.sh` detects this and sets `PIHOLE_DNS_BIND` to your LAN IP
so Pi-hole serves DNS on the LAN while the stub keeps handling the host's own lookups; on
a host where :53 is free it uses `0.0.0.0`. If your LAN IP changes, update
`PIHOLE_DNS_BIND` in `pihole/.env`.

Alternatively, free :53 entirely (then you can bind `0.0.0.0`) — e.g. for systemd-resolved:

```bash
sudo sed -i 's/^#\?DNSStubListener=.*/DNSStubListener=no/' /etc/systemd/resolved.conf
sudo ln -sf /run/systemd/resolve/resolv.conf /etc/resolv.conf
sudo systemctl restart systemd-resolved
```

Then point your router's DNS (or individual devices) at this server's IP to use Pi-hole.

## HTTPS via Caddy (`caddy/`)

Caddy terminates TLS and reverse-proxies each app at **`https://<server-ip>:<port>`** —
`:8443` Vaultwarden, `:8444` Nextcloud, `:8445` Pi-hole, **`:443` (no port) the
dashboard**. It uses its own local CA (`tls internal`) with the **server IP in the cert
SAN**, and `default_sni` so browsers that don't send SNI for a bare IP still get the cert.
`caddy/.env` holds `SERVER_IP`, the upstream ports, and the HTTPS ports.

**Install the CA once per device:** browse **`http://<server-ip>/`** → download `root.crt`
→ trust it (or `hsctl get-ca` writes it to a file). The Bitwarden/Nextcloud apps require a
trusted cert, so this step is mandatory for them.

The dashboard's tiles come from **`services.json`**; the HTTPS ports there must match the
`*_HTTPS` vars in `caddy/.env` and the Caddy blocks. Want public, no-install HTTPS? That
needs a real domain + Let's Encrypt — out of scope for this IP-based setup.

### Adding a hostname to Nextcloud's trusted domains

The `NEXTCLOUD_TRUSTED_DOMAINS` env var only applies on first install. To add one to a
running instance:

```bash
docker compose -f nextcloud/docker-compose.yml exec -u www-data app \
  php occ config:system:set trusted_domains 3 --value=cloud.example.com
```

(use the next free index — check current ones with `... occ config:system:get trusted_domains`)

## Nextcloud encryption

Nextcloud's server-side encryption is **off by default**. After first login, enable it in
**Admin → Settings → Security**, or from the CLI:

```bash
docker compose -f nextcloud/docker-compose.yml exec -u www-data app \
  php occ app:enable encryption
docker compose -f nextcloud/docker-compose.yml exec -u www-data app \
  php occ encryption:enable
```

For true at-rest privacy, also encrypt the host disk/volume (LUKS) and/or use a client
that does end-to-end encryption. Server-side encryption mainly protects external storage
backends — the keys live on the server.

## Backups

Encrypted backups (restic) are built into `hsctl` — `hsctl backup config` then
`hsctl backup run` (see [hsctl/README.md](hsctl/README.md)). It snapshots a Postgres dump
plus these named Docker volumes:
- `vaultwarden_vw-data` — all your passwords
- `nextcloud_nc-data` — your files
- `nextcloud_db-data` — Nextcloud database
- `caddy_caddy-data` — issued certs + local CA root

## Run / stop everything

```bash
./up.sh                 # start all, in dependency order (auto-uses sudo if needed)
./down.sh               # stop all (reverse order); ./down.sh --volumes also wipes data
```

Or by hand:

```bash
for s in vaultwarden nextcloud pihole caddy; do (cd $s && docker compose up -d); done
for s in caddy pihole nextcloud vaultwarden; do (cd $s && docker compose down); done
```
