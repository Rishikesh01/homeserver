# Setup

Getting from a bare Linux machine to a running homeserver, and the day-to-day commands
afterwards. `HOST` below = your server's LAN IP.

- [Prerequisites](#prerequisites)
- [Install](#install)
- [Install the certificate (once per device)](#install-the-certificate-once-per-device)
- [Optional: a public domain with Let's Encrypt](#optional-a-public-domain-with-lets-encrypt)
- [Day-to-day](#day-to-day)
- [Keeping it updated](#keeping-it-updated)
- [Pi-hole / network-wide ad-blocking](#pi-hole--network-wide-ad-blocking)

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
4. **Go** (to build `hsctl` once) — `sudo apt install golang-go`, or download from
   <https://go.dev/dl/> and unpack it somewhere like `~/sdk/go`. See the version required in
   [`hsctl/go.mod`](../hsctl/go.mod).
5. **A fixed LAN IP for the server.** In your router, give the server a **DHCP reservation**
   (a.k.a. static lease) so its IP never changes. Note that IP — it's `HOST` everywhere below.

Optional, but recommended:

- **`restic`** — required for backups (`sudo apt install -y restic`). See
  [Backup & restore](backup-restore.md).
- **`docker-buildx-plugin`** — required by `hsctl updates`
  (`sudo apt-get install -y docker-buildx-plugin`).
- **`smartmontools`** — lets the admin dashboard report SMART disk health.

---

## Install

```bash
# 1. Get the code onto the server, then enter the folder
git clone https://github.com/Rishikesh01/homeserver.git && cd homeserver

# 2. Build hsctl (version stamped from the git tag) and install it system-wide
make -C hsctl install

# 3. Let your user run Docker without sudo (log out + back in afterwards)
sudo usermod -aG docker $USER

# 4. Configure (press Enter to accept each suggested default)
hsctl setup

# 5. Start everything
hsctl up

# 6. Keep the dashboard running + auto-start it on every boot
hsctl install
```

**What `hsctl setup` asks:** your server's LAN IP, timezone, an admin email, the dashboard
port, the Pi-hole DNS bind address, whether Vaultwarden allows open signups — all
pre-filled with sensible autodetected values, so you can usually just press Enter through it —
and finally whether you want the **optional** [public domain with Let's Encrypt](#optional-a-public-domain-with-lets-encrypt)
(default: no). (The apps themselves aren't published on the LAN — Caddy reaches them over an
internal network — so there are no per-app ports to set.) It writes the configuration to
`setup.conf` and generates each service's secrets. Run it non-interactively with `--yes` plus
flags like `--server-ip` / `--tz` / `--email` (and `--domain --letsencrypt …` for the domain).

**Your generated logins** are printed once at the end of `setup` (and saved to `WELCOME.txt`).
You can see them again anytime — `hsctl secrets show` reads them straight from the `.env` files:

```bash
hsctl secrets show     # Nextcloud / Pi-hole / dashboard passwords
# ... save those into Vaultwarden ...
# (The Vaultwarden /admin token is stored Argon2-hashed, so it can't be shown here —
#  rotate it with `hsctl secrets rotate-vw-admin` if you forget it.)
```

> **These secrets are plaintext on disk** (in each service's `.env` — the stack needs them
> there). **Full-disk encryption is what protects them at rest** — see
> [Security](security.md).

After `hsctl up`, check everything is running with `hsctl status`.

Every value written by setup is documented in the
[configuration reference](configuration.md).

---

## Install the certificate (once per device)

The server makes its own HTTPS certificate (there's no public domain). Each phone/laptop
must **trust that certificate once**, or browsers show a warning and the Bitwarden/Nextcloud
apps refuse to connect.

1. On the device, open **http://HOST/** in a browser and download **`root.crt`**.
2. Install it as a *trusted certificate authority*. Step-by-step per OS (Android, iPhone,
   Windows, macOS, Linux), plus the whole "add a family member" flow, is in
   **[ONBOARDING.md](../ONBOARDING.md)** — which the dashboard also serves as a rendered page
   at **https://HOST/help**, so you can just send someone that link.

Then open **https://HOST** — that's your dashboard, linking to every app.

On the server itself, `hsctl get-ca` writes `caddy-root-ca.crt` for you to copy around.

Don't want to install anything on devices? Use a real domain with Let's Encrypt instead —
next section.

---

## Optional: a public domain with Let's Encrypt

By default everything is `https://HOST:port` with the private certificate above. If you **own
a domain**, hsctl can instead serve every app at a real name with a certificate from
[Let's Encrypt](https://letsencrypt.org) that every device already trusts — **replacing the
private CA**, so there is nothing to install on any device:

| App | Address |
|-----|---------|
| 🏠 Dashboard | `https://home.example.com` |
| 🔑 Vaultwarden | `https://vault.home.example.com` |
| ☁️ Nextcloud | `https://cloud.home.example.com` |
| 🛡️ Pi-hole | `https://pihole.home.example.com/admin` |
| 📄 🧰 🖼️ tools | `https://pdf.` / `tools.` / `image.home.example.com` |

The `https://HOST:port` addresses stop working when you switch (and come back when you switch
back). Vaultwarden's `DOMAIN` and Nextcloud's trusted domains are updated for you; the Bitwarden
and Nextcloud apps need the new server address.

**The server stays LAN-only.** Let's Encrypt has to see that you control the domain, and the
default **DNS challenge** does that through your DNS provider's API: Caddy creates a temporary
`_acme-challenge` TXT record, nothing is port-forwarded, and the names can point at a private
`192.168.x.x` address. It needs:

1. a domain at a provider with an API that Caddy has a plugin for — the
   [`caddy-dns`](https://github.com/caddy-dns) modules that take a single token: Cloudflare,
   DuckDNS, Hetzner, deSEC, DigitalOcean, Gandi, GoDaddy, Linode, Vultr, Netlify, Vercel,
   DNSimple, IONOS, Infomaniak, Njalla, … (a domain elsewhere? point its nameservers at one of
   those, or delegate just `home.example.com` to it);
2. an **API token** from that provider (Cloudflare: *My Profile → API Tokens → Edit zone DNS*,
   scoped to the one zone).

**Make the names resolve first.** Once switched, the server is *only* reachable by name, so
devices have to find `vault.home.example.com` → `HOST` before you flip it — otherwise you lose
the dashboard until they do (from the server itself, `hsctl letsencrypt disable` always brings
the IP addresses back). Three ways:

- **Pi-hole is your DNS** → nothing to do; hsctl writes the names into Pi-hole's local records
  (`pihole/custom.list`) as part of the switch.
- **Your router's local-DNS page** → add `home.example.com` and each subdomain (or a wildcard,
  if it supports one) → `HOST`.
- **Public A records** → `home.example.com` and `*.home.example.com` → `HOST`. Pointing a
  public name at a private IP is allowed, though a few routers block such answers ("DNS
  rebinding protection"; whitelist the domain there).

Then either answer *yes* to the domain question in `hsctl setup`, or switch any time later:

```bash
hsctl letsencrypt enable --domain home.example.com --email you@yourdomain.net \
    --dns-provider cloudflare            # prompts for the token (or --dns-token / $ACME_DNS_TOKEN)
hsctl letsencrypt status                 # what each name is serving; the names to point here
hsctl letsencrypt logs                   # Caddy's log, if a certificate doesn't arrive
hsctl letsencrypt disable                # back to the private CA (settings are kept)
```

…or do the same from the dashboard: **Admin → 🌐 Domain & HTTPS**. Either way hsctl:

- builds Caddy with your provider's plugin the first time (`caddy/Dockerfile`, a few minutes),
- generates a complete Caddy config into `caddy/generated/Caddyfile`, checks it parses, and
  points Caddy at it (the committed `caddy/Caddyfile` stays untouched as the private-CA
  fallback); the certificates arrive within a minute or so,
- adds the names to Pi-hole's local DNS records.

**Test with staging first.** Let's Encrypt limits failed attempts (5 per hour per name) and
issuances (50 per week per domain). Tick *staging* (or `--staging`) the first time: you get
untrusted test certificates that prove the DNS token and names work, then untick and apply
again for the real ones.

**The HTTP challenge instead.** If your provider has no plugin, `--challenge http` lets Let's
Encrypt validate by connecting to `http://home.example.com/` — which means the names must
resolve to your **public** IP from the internet and **port 80 must be forwarded** to this box.
That exposes the server, which the rest of this project deliberately avoids; read
[Security](security.md#a-public-domain-and-lets-encrypt) before choosing it.

Every setting is in the [configuration reference](configuration.md#public-domain--lets-encrypt).

---

## Day-to-day

```bash
hsctl up | down | status        # start / stop / show the stack
hsctl updates                   # check whether newer app images are available (read-only)
hsctl ui                        # run the dashboard in the foreground (hsctl install runs it as a service)
hsctl get-ca                    # save caddy-root-ca.crt to hand to a new device
hsctl letsencrypt status        # (if you use a domain) certificates per name
hsctl secrets show              # print the generated logins
hsctl backup run | list         # see docs/backup-restore.md
```

Almost all of this is also available without a terminal, from the dashboard's
**Command Center** at `/admin/commands`.

The app containers restart automatically on reboot (`restart: unless-stopped`); `hsctl
install` does the same for the dashboard.

`hsctl` uses Cobra, so tab-completion works:

```bash
source <(hsctl completion bash)        # this session (or zsh / fish)
hsctl completion bash | sudo tee /etc/bash_completion.d/hsctl >/dev/null   # persistent
```

---

## Keeping it updated

```bash
hsctl updates          # read-only: compares each installed image against its registry
```

This needs `docker buildx` (`sudo apt-get install -y docker-buildx-plugin`). It downloads
nothing and restarts nothing — it just reports which services have a newer image available.

To take an update, bump the tag in that service's `docker-compose.yml`, then:

```bash
cd <service> && docker compose pull && docker compose up -d
```

Try it in the [sandbox](../sandbox/README.md) first if it's a major version — `make sandbox`
runs a complete copy of the stack inside its own nested Docker daemon, so nothing it does can
reach your live containers or volumes.

Keep the OS patched too (`sudo apt update && sudo apt upgrade`). Note that `restic` is
deliberately pinned — see [Backup & restore](backup-restore.md#pin-restic).

---

## Pi-hole / network-wide ad-blocking

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
