# Setup

Getting from a bare Linux machine to a running homeserver, and the day-to-day commands
afterwards. `HOST` below = your server's LAN IP.

- [Prerequisites](#prerequisites)
- [Install](#install)
- [Install the certificate (once per device)](#install-the-certificate-once-per-device)
- [Day-to-day](#day-to-day)
- [Keeping it updated](#keeping-it-updated)
- [Pi-hole / network-wide ad-blocking](#pi-hole--network-wide-ad-blocking)

---

## Prerequisites

You need, on the machine that will be the server:

1. **A 64-bit Linux machine that stays on** (a spare PC, mini-PC, NUC, or a Raspberry Pi
   4/5). Both **x86-64 and arm64** are supported — every image in the stack is published
   for both. 32-bit ARM is not: IT-Tools and Stirling-PDF are arm64-only, so on a Pi use
   the **64-bit** Raspberry Pi OS. These steps assume Ubuntu/Debian; adjust package
   commands for other distros.
2. **A user account with `sudo`.**
3. **Docker Engine + Docker Compose v2:**
   ```bash
   curl -fsSL https://get.docker.com | sh
   docker --version && docker compose version    # both should print a version
   ```
4. **A fixed LAN IP for the server.** In your router, give the server a **DHCP reservation**
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
# 1. Install hsctl and the stack it manages
curl -fsSL https://raw.githubusercontent.com/Rishikesh01/homeserver/main/install.sh | sh

# 2. Start the dashboard as a service (auto-starts on every boot)
cd /opt/homeserver && sudo hsctl install
```

Step 1 downloads the `hsctl` binary built for your architecture, verifies it against the
release's `checksums.txt`, installs it to `/usr/local/bin/hsctl`, and clones this repo at
the matching tag into `/opt/homeserver` — `hsctl` reads the compose files, Caddy config and
dashboard assets from that checkout at runtime. It starts nothing and writes no config, so
it is safe to re-run; on an existing checkout it moves both binary and repo to the newer
release, and refuses rather than discarding local edits (`hsctl updates` rewrites compose
pins in place). Override with `VERSION=v1.8.0`, `HOMESERVER_DIR=/srv/homeserver` or
`PREFIX=/usr`.

Prefer to build it yourself? You'll need Go (version in [`hsctl/go.mod`](../hsctl/go.mod)):

```bash
git clone https://github.com/Rishikesh01/homeserver.git && cd homeserver
make -C hsctl install
```

`hsctl install` is the last thing you type. It:

- saves `setup.conf` with autodetected values (LAN IP, timezone, a free dashboard port),
- creates the dashboard admin password (`.ui-password`) and prints it,
- starts **Caddy** on its own, so `https://HOST/` already answers,
- installs and starts the `hsctl-ui` systemd service.

Then open **`https://HOST/`** from any phone or laptop on the network. Your browser warns
about the certificate the first time (the server made its own CA) — click through once.
Log in as `admin` with the printed password and the dashboard opens the **setup wizard**:

1. **Settings** — the LAN IP, timezone, Pi-hole DNS bind, which apps to run
   (untick any you don't want) and whether Vaultwarden allows open signups, all pre-filled.
   Usually just press *Continue*.
2. **Your logins** — it writes `setup.conf` and every service's `.env` and shows the
   generated admin logins **once**. Save them (the Vaultwarden admin token is stored
   hashed and can't be shown again; the rest are in *Command Center → Show logins*).
3. **Start** — one button runs `hsctl up` and streams the progress live. The first start
   downloads the app images, so give it a few minutes.
4. **Certificate** — download `root.crt` and follow the per-device guide.

The wizard only appears while the apps are unconfigured; afterwards `https://HOST/` is the
normal dashboard. (If you'd rather not use a browser, the equivalent is `hsctl setup` then
`hsctl up` — see below.)

### The terminal route

```bash
sudo usermod -aG docker $USER   # run Docker without sudo (log out + back in afterwards)
hsctl setup                     # configure (press Enter to accept each suggested default)
hsctl up                        # start everything
```

**What `hsctl setup` asks:** your server's LAN IP, timezone, the dashboard
port, the Pi-hole DNS bind address, and whether Vaultwarden allows open signups — all
pre-filled with sensible autodetected values, so you can usually just press Enter through it.
(The apps themselves aren't published on the LAN — Caddy reaches them over an internal
network — so there are no per-app ports to set.) It writes the configuration to `setup.conf`
and generates each service's secrets. Run it non-interactively with `--yes` plus flags like
`--server-ip` / `--tz`.

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

---

## Switching apps on and off

Don't need the PDF tools, or want to pause Nextcloud? **Admin → Apps** has a switch per app;
the same from a shell is `hsctl apps disable stirling` / `hsctl apps enable stirling`. Off
means the app's containers are stopped, `hsctl up` skips it and its tile leaves the home page
— its data volumes and `.env` are kept, so switching it back on is lossless. Anyone opening
the app's address meanwhile gets a "this app isn't running" page linking back to the
dashboard (served by Caddy, which — like the dashboard — can't be switched off). The choice
is saved as
`DISABLED_APPS` in `setup.conf`, and you can make it at first setup too (wizard checkboxes,
or `hsctl setup --disable-apps stirling,it-tools`).

## Day-to-day

```bash
hsctl up | down | status        # start / stop / show the stack
hsctl updates                   # check whether newer app images are available (read-only)
hsctl ui                        # run the dashboard in the foreground (hsctl install runs it as a service)
hsctl get-ca                    # save caddy-root-ca.crt to hand to a new device
hsctl secrets show              # print the generated logins
hsctl apps                      # list apps; hsctl apps disable stirling switches one off (data kept)
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
