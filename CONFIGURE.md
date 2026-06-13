# Configuration guide

Step-by-step setup for the whole stack. For the high-level overview see [README.md](README.md).

Services and how they're reached:

| Service | Folder | Direct (LAN) | Friendly name (needs DNS) |
|---------|--------|--------------|---------------------------|
| Vaultwarden (passwords) | `vaultwarden/` | `http://SERVER_IP:8080` | `https://vault.lan` |
| Nextcloud (files) | `nextcloud/` | `http://SERVER_IP:8081` | `https://cloud.lan` |
| Pi-hole (DNS/ad-block) | `pihole/` | `http://SERVER_IP:8053/admin` | `https://pihole.lan` |
| Caddy (HTTPS proxy) | `caddy/` | ports 80 / 443 | — |

Replace `SERVER_IP` with your homeserver's LAN IP everywhere below.

---

## 0. Prerequisites

- Docker Engine + Compose v2 (`docker --version`, `docker compose version`).
- A **static LAN IP / DHCP reservation** for the homeserver. Everything points at it; if it
  moves, things break.
- The repo copied to the homeserver.

---

## 1. Bring up the core services

Each service folder has a `.env.example`. For every one:

```bash
cd <service>
cp .env.example .env
# edit .env — replace every change-me / placeholder value
docker compose up -d
docker compose logs -f      # Ctrl-C once it's settled
```

Generate strong secrets with `openssl rand -base64 32`.

**Order:** `vaultwarden`, `nextcloud`, `pihole` first, then `caddy`.
Caddy must come up after the services it proxies.

Per-service notes:

- **vaultwarden/.env** — set `VW_DOMAIN=https://vault.lan`. Leave `VW_SIGNUPS_ALLOWED=true`
  until you've created your account, then set it `false` and `docker compose up -d` again.
- **nextcloud/.env** — set strong DB/Redis/admin passwords. `NC_TRUSTED_DOMAINS` should list
  every name/IP you'll use, space-separated: `SERVER_IP cloud.lan`.
- **pihole/.env** — set `PIHOLE_PASSWORD`. `PIHOLE_DNS_BIND` defaults to `0.0.0.0`; pin it to
  `SERVER_IP` only if another resolver already holds port 53 (see §4).

---

## 2. Port 53 conflict (Pi-hole)

Pi-hole needs UDP/TCP port 53. If the host already runs a resolver, free it first.

- **systemd-resolved** (most Ubuntu):
  ```bash
  sudo sed -i 's/^#\?DNSStubListener=.*/DNSStubListener=no/' /etc/systemd/resolved.conf
  sudo ln -sf /run/systemd/resolve/resolv.conf /etc/resolv.conf
  sudo systemctl restart systemd-resolved
  ```
- **Something else on :53** (libvirt/LXC dnsmasq, etc.): either stop it, or set
  `PIHOLE_DNS_BIND=SERVER_IP` in `pihole/.env` so Pi-hole binds only the LAN IP.

Check what holds it: `sudo ss -tulpn | grep ':53 '`.

---

## 3. HTTPS via Caddy + trusting the certificate

```bash
cd caddy
cp .env.example .env
docker compose up -d
```

`caddy/.env` defaults to `TLS_DIRECTIVE=tls internal` — Caddy runs its own local CA and
issues certs for `vault.lan` / `cloud.lan` / `pihole.lan`. Because `.lan` isn't a public
domain, you can't use Let's Encrypt, so browsers and **the Bitwarden/Nextcloud mobile apps
will reject the cert until you install Caddy's root CA** on each device.

Extract the root CA:

```bash
docker exec caddy cat /data/caddy/pki/authorities/local/root.crt > caddy-root-ca.crt
```

Install `caddy-root-ca.crt` as a trusted root:
- **Windows:** double-click → Install Certificate → Local Machine → "Trusted Root
  Certification Authorities".
- **macOS:** Keychain Access → System → drag it in → set to "Always Trust".
- **iOS:** AirDrop/email it → Settings → Profile Downloaded → Install → then Settings →
  General → About → Certificate Trust Settings → enable it.
- **Android:** Settings → Security → Encryption & credentials → Install a certificate → CA
  certificate.
- **Linux:** `sudo cp caddy-root-ca.crt /usr/local/share/ca-certificates/ && sudo update-ca-certificates`

Adding a name to Nextcloud after first install (the env var only applies on install):

```bash
docker compose -f nextcloud/docker-compose.yml exec -u www-data app \
  php occ config:system:get trusted_domains          # see current + next free index
docker compose -f nextcloud/docker-compose.yml exec -u www-data app \
  php occ config:system:set trusted_domains 3 --value=cloud.lan
```

---

## 4. DNS strategy — resolving `*.lan` on the LAN

A device resolves `vault.lan` only if its DNS queries reach Pi-hole. By default devices ask
the router, which doesn't know `.lan`. Pick one:

- **Recommended — one router setting (every device, no per-device work):** in the router's
  DHCP settings set **Primary DNS = `SERVER_IP`** and leave **Secondary blank**. Every device
  then resolves `*.lan` and gets ad-blocking automatically. Requires a **DHCP reservation** so
  `SERVER_IP` never changes.
  - **Tradeoff (SPOF):** Pi-hole becomes the LAN's only resolver, so if the box is down the
    LAN loses DNS until you clear that field (~30-second revert). Don't add a public
    "secondary" — OSes query both in parallel, so blocking leaks and failover is unreliable.
- **Alternative — per device:** set DNS to `SERVER_IP` only on chosen devices. No SPOF, but
  repeat per device (`/etc/hosts` works on computers, not phones).

The name→IP records live in `pihole/custom.list` (bind-mounted), one `IP hostname` per line:

```
SERVER_IP vault.lan
SERVER_IP cloud.lan
SERVER_IP pihole.lan
```

After editing: `docker exec pihole pihole reloaddns`.

> **No VPN.** Remote access via WireGuard was removed — this box isn't guaranteed to be up,
> so a VPN into it isn't worth maintaining. Use the services on the home network.

---

## 6. Verify

```bash
# containers
docker ps --format 'table {{.Names}}\t{{.Status}}'

# services direct
curl -s -o /dev/null -w "vaultwarden %{http_code}\n" http://SERVER_IP:8080/
curl -s http://SERVER_IP:8081/status.php; echo            # Nextcloud -> JSON
curl -s -o /dev/null -w "pihole %{http_code}\n" http://SERVER_IP:8053/admin/

# DNS
dig +short @SERVER_IP vault.lan                            # -> SERVER_IP

# via Caddy (skip -k once the CA is trusted)
curl -sk --resolve vault.lan:443:SERVER_IP https://vault.lan/
```

---

## 7. Backups

Back up these named volumes regularly:
- `vaultwarden_vw-data` — passwords (critical)
- `nextcloud_nc-data` — files
- `nextcloud_db-data` — Nextcloud database
- `caddy_caddy-data` — certs + local CA root

Snapshot example:
```bash
docker run --rm -v vaultwarden_vw-data:/d -v "$PWD":/b alpine \
  tar czf /b/vw-backup.tgz -C /d .
```

---

## 8. Troubleshooting

| Symptom | Likely cause / fix |
|---------|--------------------|
| Pi-hole container won't start | Port 53 already held — see §2. |
| Browser cert warning on `*.lan` | Caddy CA not installed on the device — §3. |
| Nextcloud "Trusted domain error" | Add the hostname to trusted_domains — §3. |
| `*.lan` doesn't resolve | Device isn't using Pi-hole — point the router's DHCP DNS at `SERVER_IP`, or set the device's DNS manually — §4. |
