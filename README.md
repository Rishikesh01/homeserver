# Homeserver

Self-hosted stack. Each service lives in its own folder with its own `docker-compose.yml`
and `.env`, so you can start/stop them independently.

| Service | What | Folder | Direct URL | Via Caddy (HTTPS) |
|---------|------|--------|-----------|-------------------|
| Vaultwarden | FOSS password manager (Bitwarden-compatible) | `vaultwarden/` | http://HOST:8080 | https://vault.lan |
| Nextcloud | Encrypted cloud file storage | `nextcloud/` | http://HOST:8081 | https://cloud.lan |
| Pi-hole | Network-wide DNS ad-blocker | `pihole/` | http://HOST:8053/admin | https://pihole.lan |
| Caddy | Reverse proxy / HTTPS front door | `caddy/` | — | ports 80 + 443 |
| wg-easy | WireGuard VPN (remote access + DNS) | `wireguard/` | http://HOST:51821 | — |
| hsctl UI | Web dashboard + family portal | `hsctl/` | http://HOST:8088 | https://home.lan |

Start order: the three services first, then `caddy/`, then `wireguard/`. The whole stack
is driven by **`hsctl`** (a Go tool with a web UI) — see [First-time setup](#first-time-setup).

> **Full step-by-step setup is in [CONFIGURE.md](CONFIGURE.md)** — per-service `.env`, HTTPS/CA
> trust, DNS strategy, and the VPN (DDNS + port-forward + client setup). This README is the
> overview.

## First-time setup

Everything is driven by **`hsctl`** — a small Go tool (zero external deps) with a **web
UI**, so non-technical users never need a terminal. Build it once, then run setup:

```bash
cd hsctl && ~/sdk/go/bin/go build -o hsctl . && sudo install -m755 hsctl /usr/local/bin/hsctl && cd ..
hsctl setup        # asks: LAN IP, timezone, admin email, hostnames, ports, TLS mode
hsctl up           # start the stack (services -> caddy -> wireguard)
hsctl get-ca       # write caddy-root-ca.crt to install on devices
hsctl ui           # serve the dashboard -> https://home.lan  (or http://HOST:8088)
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
4. **Hand out DNS** — see "DNS strategy" below. With a single server, **do not** set
   Pi-hole as the router's DHCP DNS.

Caddy (`:80`/`:443`) and the service ports listen on the LAN. There's no host firewall
here; if your homeserver runs `ufw`, allow the ports (use whatever ports bootstrap chose):
`sudo ufw allow 80,443,53,8080,8081,8053/tcp && sudo ufw allow 53/udp`.

### DNS strategy (single server = no SPOF)

With only one always-on box, **don't make Pi-hole the LAN's mandatory resolver.** If the
router hands out Pi-hole as everyone's DNS and the box goes down (reboot, update, crash),
the whole LAN loses internet. So:

- **Leave the router's DHCP DNS at its default** (router/ISP/public). The network resolves
  DNS without ever depending on the homeserver — outages don't break internet.
- **Opt in per device:** on the machines you want ad-blocked (PC, phone, a TV), manually
  set their DNS to the homeserver's IP. Those devices also get the `*.lan` names. If the
  box is down, switch them back to automatic.
- **Avoid "primary = Pi-hole, secondary = 1.1.1.1" in DHCP.** OSes query both in
  parallel/round-robin, so blocking leaks *and* failover is unreliable — worst of both.
- **Want always-on, network-wide blocking?** That needs a second always-on device (a
  cheap Pi is the usual answer) running a 2nd Pi-hole, with both handed out via DHCP and
  lists kept in sync. Not possible to do safely with a single box.

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

Caddy terminates TLS and reverse-proxies to the three services on their published
host ports. Hostnames, TLS mode, and upstreams are set in `caddy/.env`.

**Default = LAN mode (`TLS_DIRECTIVE=tls internal`).** Caddy runs its own local CA and
issues certs for `vault.lan` / `cloud.lan` / `pihole.lan`. Works fully offline. Browsers
will warn until you install Caddy's root CA on your devices — extract it with:

```bash
docker exec caddy cat /data/caddy/pki/authorities/local/root.crt > caddy-root-ca.crt
```

Import `caddy-root-ca.crt` as a trusted root on each device. **The Bitwarden and Nextcloud
mobile apps require a trusted cert** — either install this CA or switch to Let's Encrypt.

**Real domain = Let's Encrypt.** Point real public DNS names at this host, open ports
80+443, then in `caddy/.env` set the `*_HOST` vars to those names and `TLS_DIRECTIVE=`
(empty). Caddy auto-issues trusted certs. Also update `VW_DOMAIN` and add the name to
Nextcloud's trusted domains (see below).

### Name resolution

Devices reach `*.lan` because Pi-hole resolves them to the server. Records live in
`pihole/custom.list` (bind-mounted into the container), one `IP hostname` per line.
Edit that file, then `docker exec pihole pihole reloaddns`.

Only devices that use Pi-hole as their DNS resolve `*.lan` (see "DNS strategy" above —
with one server, that's opt-in per device, not router-wide). Devices not using Pi-hole
can reach the services by IP, or get a matching `/etc/hosts` entry.

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

Back up these named Docker volumes regularly:
- `vaultwarden_vw-data` — all your passwords
- `nextcloud_nc-data` — your files
- `nextcloud_db-data` — Nextcloud database
- `caddy_caddy-data` — issued certs + local CA root
- `wireguard_wg-data` — VPN keys + client configs

Example: `docker run --rm -v vaultwarden_vw-data:/d -v $PWD:/b alpine tar czf /b/vw-backup.tgz -C /d .`

## Run / stop everything

From this directory (services first, Caddy + VPN last):

```bash
./up.sh                 # start all, in dependency order (auto-uses sudo if needed)
./down.sh               # stop all (reverse order); ./down.sh --volumes also wipes data
```

Or by hand:

```bash
for s in vaultwarden nextcloud pihole caddy wireguard; do (cd $s && docker compose up -d); done
for s in wireguard caddy pihole nextcloud vaultwarden; do (cd $s && docker compose down); done
```
