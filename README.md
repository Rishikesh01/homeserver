# Homeserver

Self-hosted stack. Each service lives in its own folder with its own `docker-compose.yml`
and `.env`, so you can start/stop them independently.

| Service | What | Folder | Direct URL | Via Caddy (HTTPS) |
|---------|------|--------|-----------|-------------------|
| Vaultwarden | FOSS password manager (Bitwarden-compatible) | `vaultwarden/` | http://HOST:8082 | https://vault.lan |
| Nextcloud | Encrypted cloud file storage | `nextcloud/` | http://HOST:8081 | https://cloud.lan |
| Pi-hole | Network-wide DNS ad-blocker | `pihole/` | http://HOST:8053/admin | https://pihole.lan |
| Caddy | Reverse proxy / HTTPS front door | `caddy/` | — | ports 80 + 443 |
| wg-easy | WireGuard VPN (remote access + DNS) | `wireguard/` | http://HOST:51821 | — |

Start order: the three services first, then `caddy/`, then `wireguard/`.

> **Full step-by-step setup is in [CONFIGURE.md](CONFIGURE.md)** — per-service `.env`, HTTPS/CA
> trust, DNS strategy, and the VPN (DDNS + port-forward + client setup). This README is the
> overview.

## First-time setup

**Fast path (reproducible):** generate every `.env` with fresh random secrets and
bring the stack up:

```bash
./bootstrap.sh     # writes all .env + pihole/custom.list; prints logins to .secrets.txt
./up.sh            # starts everything in order; ./down.sh stops it
./get-ca.sh        # extract Caddy's root CA to install on your devices
```

`bootstrap.sh` never overwrites an existing `.env`, so re-running is safe. Deploying on
a different machine? Override the host's IP: `SERVER_IP=192.168.1.50 ./bootstrap.sh`.
Onboarding a family member / new device → see **[ONBOARDING.md](ONBOARDING.md)**.

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

1. **Copy the folder** to the homeserver and do the per-service `.env` setup above.
2. **Pi-hole local DNS** — `cp pihole/custom.list.example pihole/custom.list` and set the
   IP to the homeserver's LAN IP for all three names.
3. **Port 53** — this host runs `systemd-resolved`, which holds :53 on its loopback stub,
   so Pi-hole is pinned to the LAN IP via `PIHOLE_DNS_BIND` (see below) instead of
   `0.0.0.0`. On a host where :53 is free, you can set `PIHOLE_DNS_BIND=0.0.0.0` to serve
   every interface.
4. **Give the server a static IP** (or a DHCP reservation). A DNS server whose address
   changes breaks everything pointing at it.
5. **Hand out DNS** — see "DNS strategy" below. With a single server, **do not** set
   Pi-hole as the router's DHCP DNS.

Everything is already LAN-listenable by design: Caddy (`:80`/`:443`) and every service
port bind `0.0.0.0`. There's no host firewall here; if your homeserver runs `ufw`, allow
the ports: `sudo ufw allow 80,443,53,8082,8081,8053/tcp && sudo ufw allow 53/udp`.

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

On this host `systemd-resolved` is **active** and holds port 53 on its loopback stub
(`127.0.0.53` / `127.0.0.54`). Binding Pi-hole to `0.0.0.0:53` would clash with it, so
`pihole/.env` pins `PIHOLE_DNS_BIND=192.168.0.150` (this server's LAN IP) — Pi-hole then
serves DNS on the LAN while the stub keeps handling the host's own lookups. If the LAN IP
changes, update `PIHOLE_DNS_BIND` in `pihole/.env`.

Alternatively, free :53 entirely (then you can bind `0.0.0.0`):

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
