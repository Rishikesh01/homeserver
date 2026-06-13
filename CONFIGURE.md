# Configuration guide

Step-by-step setup for the whole stack. For the high-level overview see [README.md](README.md).

Services and how they're reached:

| Service | Folder | Direct (LAN) | Friendly name (needs DNS) |
|---------|--------|--------------|---------------------------|
| Vaultwarden (passwords) | `vaultwarden/` | `http://SERVER_IP:8080` | `https://vault.lan` |
| Nextcloud (files) | `nextcloud/` | `http://SERVER_IP:8081` | `https://cloud.lan` |
| Pi-hole (DNS/ad-block) | `pihole/` | `http://SERVER_IP:8053/admin` | `https://pihole.lan` |
| Caddy (HTTPS proxy) | `caddy/` | ports 80 / 443 | — |
| wg-easy (VPN) | `wireguard/` | `http://SERVER_IP:51821` | — |

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

**Order:** `vaultwarden`, `nextcloud`, `pihole` first, then `caddy`, then `wireguard`.
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

## 4. DNS strategy — why it's opt-in (single box, no SPOF)

With **one** always-on box, do **not** set Pi-hole as the router's DHCP DNS. If you did and
the box went down (reboot, update, crash), the whole LAN would lose DNS and therefore the
internet. So:

- **Leave the router's DHCP DNS at its default.** The LAN resolves the internet without ever
  depending on the homeserver.
- Resolve the `*.lan` names **only where you want them**, via one of:
  - **The VPN (recommended)** — see §5. The tunnel pushes Pi-hole as DNS *while connected*,
    so names + ad-blocking work, scoped to the tunnel. No permanent dependency.
  - **Per-device DNS** — manually set a device's DNS to `SERVER_IP`. That device gets names
    + ad-blocking; if the box is down, switch it back to automatic.
  - **`/etc/hosts`** on a computer (won't work on phones).
- **Don't** use "primary = Pi-hole, secondary = 1.1.1.1" in DHCP — clients query both in
  parallel, so blocking leaks and failover is unreliable.

The name→IP records live in `pihole/custom.list` (bind-mounted), one `IP hostname` per line:

```
SERVER_IP vault.lan
SERVER_IP cloud.lan
SERVER_IP pihole.lan
```

After editing: `docker exec pihole pihole reloaddns`.

---

## 5. VPN (wg-easy) — remote access + name resolution

The VPN is how you reach the services from outside home **and** how `*.lan` names resolve
without touching the router. While connected, the tunnel hands the client Pi-hole as DNS and
routes only the LAN subnet (split tunnel) — so internet stays direct and a box outage never
breaks the client's general connectivity.

### 5.1 Configure

```bash
cd wireguard
cp .env.example .env
```

Edit `wireguard/.env`:
- `WG_HOST` — your **public** address. Home IPs change, so use DDNS (see §5.2), e.g.
  `yourname.tplinkdns.com`.
- `PASSWORD_HASH` — generate it:
  ```bash
  docker run --rm ghcr.io/wg-easy/wg-easy:14 wgpw 'YourStrongPassword'
  ```
  Paste the hash **without** the surrounding quotes it prints.
- `WG_DEFAULT_DNS=SERVER_IP` — pushes Pi-hole to clients.
- `WG_ALLOWED_IPS=192.168.0.0/24` — set to your LAN subnet (split tunnel). Use `0.0.0.0/0,
  ::/0` only if you want to route ALL client traffic through home (not recommended here).

Start it:

```bash
docker compose up -d
```

### 5.2 Router: DDNS + one port-forward (TP-Link)

TP-Link stock firmware can't do local DNS, but it does DDNS and port-forwarding, which is all
the VPN needs:

1. **DDNS** — TP-Link web UI → look for **DDNS** (often under *Network* or its own menu) →
   enable **TP-Link DDNS** → register a free hostname (e.g. `yourname.tplinkdns.com`). Put
   that in `WG_HOST`.
2. **Port-forward** — *NAT Forwarding → Virtual Servers* (a.k.a. Port Forwarding) → add:
   - External port: `51820`, Internal port: `51820`, Protocol: **UDP**,
     Internal IP: `SERVER_IP`.
   - **Only forward 51820/udp.** Never forward the web UI (51821) or the service ports.

### 5.3 Add a device (client)

1. Open the web UI at `http://SERVER_IP:51821`, log in with the password you set.
2. Click **New Client**, name it (e.g. "phone").
3. **Phone:** install the official **WireGuard** app → scan the QR code shown.
   **Desktop:** download the `.conf`, import it into the WireGuard app.
4. Toggle the tunnel on. Test: browse to `https://vault.lan` and open a site with ads.

### 5.4 Make the fallback graceful

- **Don't enable a VPN "kill switch" / "block connections without VPN."** That's the one
  setting that would break a client's internet when the box is down. Leave it off and the
  client simply falls back to its normal connection.
- For the cleanest behavior (only `*.lan` uses Pi-hole even while the tunnel is up), edit the
  client config's DNS line to add the search domain — change `DNS = SERVER_IP` to
  `DNS = SERVER_IP, lan`. Supported by the official WireGuard clients and systemd-resolved.
  Otherwise, if the box dies while you're connected, just toggle the tunnel off.

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

# VPN
curl -s -o /dev/null -w "wg-easy UI %{http_code}\n" http://SERVER_IP:51821/
```

---

## 7. Backups

Back up these named volumes regularly:
- `vaultwarden_vw-data` — passwords (critical)
- `nextcloud_nc-data` — files
- `nextcloud_db-data` — Nextcloud database
- `caddy_caddy-data` — certs + local CA root
- `wireguard_wg-data` — VPN keys + client configs

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
| `*.lan` doesn't resolve | Device isn't using Pi-hole. Connect the VPN, or set the device DNS to `SERVER_IP`. |
| VPN connects but no internet | Likely full-tunnel + a routing issue, or a kill switch is on. Use split tunnel (`WG_ALLOWED_IPS`=LAN) and disable the kill switch — §5.4. |
| VPN won't connect from outside | DDNS not updating, or 51820/udp not forwarded to `SERVER_IP` — §5.2. |
| `PASSWORD_HASH` rejected | Don't wrap it in quotes in `.env`; `env_file` passes it literally. Regenerate with `wgpw`. |
