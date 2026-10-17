# Let's Encrypt certificates — design

**Status: proposed.** Nothing here is implemented yet. This records the decisions made so far,
the architecture they lead to, what has been verified, the risks, and the questions still open.
Where a choice is still open, the architecture below shows the recommended option and the
question is listed under [Open questions](#open-questions).

- [Summary](#summary)
- [Decisions](#decisions)
- [Architecture](#architecture)
- [Components](#components)
  - [Certificates: hsctl + lego](#certificates-hsctl--lego)
  - [Caddy: local sites and domain sites](#caddy-local-sites-and-domain-sites)
  - [Name resolution: Pi-hole, VPN clients only](#name-resolution-pi-hole-vpn-clients-only)
  - [The VPN: NetBird](#the-vpn-netbird)
  - [Apps that need to know the domain](#apps-that-need-to-know-the-domain)
  - [Local certificates](#local-certificates)
- [Configuration and secrets](#configuration-and-secrets)
- [Reconcile: one writer for derived state](#reconcile-one-writer-for-derived-state)
- [CLI](#cli)
- [Dashboard](#dashboard)
- [Status and monitoring](#status-and-monitoring)
- [Security](#security)
- [Operations](#operations)
- [Upgrades and migration](#upgrades-and-migration)
- [Testing](#testing)
- [Phases](#phases)
- [Open questions](#open-questions)
- [Alternatives considered](#alternatives-considered)

---

## Summary

Add Let's Encrypt certificates for a domain the user owns, alongside today's local
certificates. Every service gets a subdomain (`vault.lab.example.com`, …) that resolves and
connects only over **NetBird**. The IP addresses keep working on the home network without a VPN,
with the local CA, as today. hsctl gets the certificate through the DNS challenge by running the
official lego container. Caddy stays the official image and only loads the files.

**Non-goals**

- Public, non-VPN access to the apps from the internet.
- Certificates for IP addresses. Public authorities never certify private addresses.
- The HTTP-01 and TLS-ALPN-01 challenges. Both need publicly resolvable names and an open port.
- DNS providers beyond the three below in the first version. lego supports many more; adding one
  is a registry entry plus a test.

## Decisions

| # | Decision | Why |
|---|----------|-----|
| 1 | Let's Encrypt for a user-owned domain. The base domain and the dashboard's subdomain are set **in the first-run wizard or later from the admin pages** (and the CLI). | Trusted certificates without installing a CA on each device. |
| 2 | **Local certificates always stay, alongside the public one.** There is no opt-out. | Existing devices and app configs keep working; the LAN never depends on the domain. |
| 3 | The VPN is needed **only for the domain**. The IP addresses work on the LAN without it. | Daily home use stays as simple as today. |
| 4 | The domain names **resolve only for VPN clients**. | Keep bots away from Vaultwarden and the other apps. |
| 5 | **One subdomain per service**, one wildcard certificate `*.<base>`. The dashboard gets a subdomain too. | One certificate for everything. The public certificate logs show `*.<base>`, not the service names. DuckDNS can hold only one verification record at a time, so a single name is required. |
| 6 | **DNS-01 challenge only.** | The only challenge that works for names that don't resolve publicly, and the only one that allows wildcards. |
| 7 | Providers: **Cloudflare, DuckDNS, deSEC**. | Covers a paid domain and two free options. |
| 8 | **Internet-facing (cloud, bare-metal) servers are supported**, as well as home servers. | Hosting outside the home. |
| 9 | Caddy stays the **official image**. **lego runs as a container, driven by hsctl.** | Stock Caddy can't talk to DNS providers ([why](#why-caddy-doesnt-do-the-dns-challenge)); no custom image to host; lego updates arrive through `hsctl updates`. |
| 10 | **Pi-hole resolves the service names for VPN clients only**; everyone else gets NXDOMAIN. Pi-hole can't be switched off while domain access is on. | No extra DNS server to run; VPN clients also get ad-blocking. [Verified](#verified-so-far). |
| 11 | The VPN is **NetBird**. | WireGuard-based, gets through NAT on its own (no port-forward), and has per-device access control. |
| 12 | Vaultwarden must work over **both** the IP and the domain, as far as possible. | See [Vaultwarden](#vaultwarden). |

## Architecture

### Home server

```
  LAN device, no VPN                               NetBird device (home or away)
  ──────────────────                               ─────────────────────────────
  https://192.168.1.10:8443                        https://vault.lab.example.com
          │                                                │
          │                                                │ 1. DNS: NetBird sends it to Pi-hole
          │                                                │    at the server's NetBird IP (100.121.5.10)
          │                                                │    NetBird client → 100.121.5.10
          │                                                │    any other      → NXDOMAIN
          │                                                │ 2. HTTPS through the NetBird tunnel
          ▼                                                ▼
  ┌──────────────────────────── Caddy (official image) ───────────────────────────────┐
  │  IP sites  <ip>:<port>                    │  domain sites  *.lab.example.com       │
  │    tls internal  (local CA — always on)   │    tls <files written by lego>         │
  │    allow: VPN + ALLOW_SUBNETS (the LAN)   │    allow: VPN subnet only              │
  └───────────────────────┬────────────────────────────────────────┬───────────────────┘
                          └──────────── homeserver-edge network ────┘
           vaultwarden · nextcloud · pihole · stirling · it-tools · imagetools
           dashboard (hsctl ui, on the host, via host.docker.internal)

  Router: nothing forwarded. NetBird gets through NAT itself (direct, or via its relays).
```

### Getting and renewing the certificate

```
  hsctl-certs.timer (daily)
          │
          ▼
  hsctl certs renew   (holds the reconcile lock)
          │ 1. docker run … goacme/lego run …   (gets or renews; does nothing when not due)
          │       │ 2. write _acme-challenge TXT ──► DNS provider (Cloudflare / DuckDNS / deSEC)
          │       │ 3. order *.lab.example.com   ──► Let's Encrypt
          │       │                                   checks the TXT over public DNS, issues
          │       └─ writes certificates/production.{crt,key} to the `acme` volume
          ▼
  4. docker exec -w /etc/caddy caddy caddy reload --force
                                       Caddy reads the files. It never talks to the DNS
                                       provider and never holds the DNS token.
```

### Internet-facing server (cloud or bare metal)

```
  internet ── nothing of ours is reachable ──►  server, public IP 203.0.113.7
                                                 │   (only NetBird's own outbound traffic)
                                                 ├─ wt0 100.121.5.10  (NetBird)
                                                 ├─ Caddy published on 100.121.5.10 only
                                                 │    domain sites (Let's Encrypt)
                                                 │    IP sites https://100.121.5.10:8443 (local CA)
                                                 └─ Pi-hole DNS on 100.121.5.10 only
```

**Which address the IP sites use on such a server ([open question 3](#open-questions)).** At
home, "the IP address" is obvious: the server's LAN IP, which only devices in the house can reach.
A cloud server has no house around it. Its two addresses are:

- its **public IP**, which the whole internet can reach, bots included;
- its **NetBird IP**, which only your NetBird devices can reach.

If the IP sites used the public IP, Vaultwarden would be open to the internet. That's exactly
what the VPN is meant to prevent. So on an internet-facing server, `SERVER_IP` (the address the
IP sites, their certificate and `default_sni` are built on) defaults to the **NetBird IP**.
"IP access" there means `https://100.121.5.10:8443` with the local certificate, over NetBird.

### Who can reach what

| | Home server | Internet-facing server |
|---|---|---|
| IP sites (local CA) | LAN without VPN, and NetBird | NetBird only (`SERVER_IP` = the NetBird IP) |
| Domain sites (Let's Encrypt) | NetBird only | NetBird only |
| Pi-hole DNS | LAN IP and NetBird IP | NetBird IP only |
| Reachable from the internet | Nothing of ours | Nothing of ours |

---

## Components

### Certificates: hsctl + lego

**What runs.** The official `goacme/lego` image (lego v5; verified with 5.5.2), pinned like every
other image here (`image: ${LEGO_IMAGE:-goacme/lego:<version>}`). It's a service in
`caddy/docker-compose.yml` under a compose profile, so `docker compose up` never starts it. hsctl
runs it on demand:

```bash
docker compose --profile certs run --rm lego run \
  --path /data --accept-tos --env-file /acme.env \
  --server letsencrypt                 # or letsencrypt-staging \
  --cert.name production               # or staging: one stored certificate per CA \
  --dns cloudflare --dns.resolvers 1.1.1.1:53 --dns.resolvers 9.9.9.9:53 \
  --domains '*.lab.example.com' --force-cert-domains --no-random-sleep \
  [--email me@mydomain.com] [--renew-force]
```

- **`lego run` both gets and renews.** It does nothing when the certificate isn't due (lego
  works out when from the certificate's lifetime and Let's Encrypt's renewal hints), so hsctl can
  call it every time, without deciding first. `--renew-force` is "Renew now".
- **`--cert.name` per CA.** lego stores certificates by name, not by CA. Switching from staging
  to production under the same name keeps the staging certificate until it's due. Separate names
  (`staging`, `production`) keep both; Caddy loads the one for the configured CA.
- **`--force-cert-domains`** gets a new certificate when the base domain changes, under the same
  name.
- **`--dns.resolvers` must be public.** Pi-hole answers NXDOMAIN for anything under the base
  domain that isn't a service name, `_acme-challenge` included. lego's checks must not go through
  it.
- **The email is optional.** Without one, lego uses `noemail@example.com` only as the local
  account folder name.
- **The token** is read from the credentials file through `--env-file` (see
  [Configuration and secrets](#configuration-and-secrets)). Only the lego container mounts it;
  it never enters Caddy's container.
- **Where the files go.** The account and the certificates live in a named volume, `acme`, in the
  `caddy` compose project, so they're in every backup and a restore doesn't request a new
  certificate. Caddy mounts the volume read-only at `/acme` and loads
  `/acme/certificates/production.crt` and `.key` (or `staging.*`). `lego certificates list` shows
  each certificate's domains, issuer and expiry.

**Renewal.** A `hsctl-certs.timer` (daily, `RandomizedDelaySec`, `Persistent=true`) runs
`hsctl certs renew` as a oneshot service, holding the
[reconcile lock](#reconcile-one-writer-for-derived-state):

1. `lego run …` as above. `--no-random-sleep` drops lego's own delay of up to 8 minutes, because
   systemd already spreads the start time; "Renew now" in the dashboard doesn't hang.
2. `docker exec -w /etc/caddy caddy caddy reload --force`. Both flags are required:
   - **without `--force`**, Caddy sees an unchanged config, skips the reload, and keeps serving
     the old certificate;
   - **without `-w /etc/caddy`**, the reload fails with "no config file to load", because the
     image's working directory is `/srv`.
3. [Reconcile](#reconcile-one-writer-for-derived-state).

`hsctl install` writes the timer and its service the same way it writes the dashboard unit
(`cmdInstall`), and `uninstall` removes them through `uninstallUnits`. A `curl | sh` install
therefore gets renewals too. The backup timer, by contrast, only arrives with
`make install-services`.

**How Caddy uses the files.** The domain site loads them with `tls <cert> <key>`. Caddy's
documentation says manually loaded certificates go into its shared cache, and Caddy won't try to
manage certificates for those names itself. The domain site is only written once the
certificate exists: a site that points at a missing file is a config error, and a config error
stops all of Caddy.

#### Why Caddy doesn't do the DNS challenge

Caddy has Let's Encrypt support built in, but in the official image it can only prove ownership
by having Let's Encrypt connect to it at a public name, on port 80 or 443. The DNS challenge
needs code that writes a record through the DNS provider's API. In Caddy that code is a plugin
per provider, compiled into the binary, and the official image ships none. Letting Caddy do the
challenge would mean a custom Caddy image that someone has to build, sign, host and keep current
with every Caddy release. With lego doing the DNS step, Caddy security fixes keep coming straight
from upstream through `hsctl updates`.

### Caddy: local sites and domain sites

The Caddy config moves into its own directory, mounted as a directory:

```
caddy/conf/Caddyfile             tracked: global options, the :80 CA-download page,
                                 one snippet per app, `import sites/*.caddy`, `import sites/custom/*.caddy`
caddy/conf/sites/local.caddy     written by hsctl: the IP:port sites (today's blocks), tls internal
caddy/conf/sites/public.caddy    written by hsctl when Let's Encrypt is on: *.<base>, routed by hostname
caddy/conf/sites/custom/         user-owned and gitignored, for hand-added sites
```

- **A directory mount (`./conf:/etc/caddy:ro`)** rather than today's single-file mount. Git and
  hsctl both replace files by renaming them. A single-file mount keeps showing the container the
  old file; a directory mount shows the new one.
- **A subdirectory, not all of `caddy/`.** `caddy/.env` and the credentials file stay out of the
  container.
- **Caddy no longer does ACME.** The Caddyfile's global `email` option and the `ACME_EMAIL`
  variable in Caddy's compose file go away.

```caddyfile
# caddy/conf/Caddyfile (tracked) — one snippet per app
(vault) {
	reverse_proxy {$VAULT_UPSTREAM} {
		header_up X-Real-IP {remote_host}
	}
}
```

```caddyfile
# caddy/conf/sites/local.caddy — one block per app, as today, plus the allowlist
{$SERVER_IP}:{$VAULT_HTTPS} {
	bind 0.0.0.0
	tls internal
	@outside not remote_ip 100.121.0.0/16 192.168.1.0/24   # VPN_SUBNET + ALLOW_SUBNETS
	handle @outside {
		abort
	}
	import app_offline
	import vault
}
```

```caddyfile
# caddy/conf/sites/public.caddy — only once the certificate exists
*.lab.example.com {
	bind 0.0.0.0                                         # same bind as every other site
	tls /acme/certificates/production.crt /acme/certificates/production.key
	@outside not remote_ip 100.121.0.0/16                # VPN_SUBNET only
	handle @outside {
		abort
	}
	import app_offline
	@home host home.lab.example.com                      # DASHBOARD_SUB
	handle @home {
		import home
	}
	@vault host vault.lab.example.com
	handle @vault {
		import vault
	}
	# … one pair per services.json entry, using its `key` as the subdomain
	handle {
		abort                                            # any other name: drop the connection
	}
}
```

Rules for the sites, the allowlist and the published ports:

- **Every site uses the same `bind`.** In testing, leaving `bind` off the domain site while the
  IP sites kept it made the IP site on port 443 (which carries the dashboard) stop answering.
- **The allowlist drops the request, not the handshake.** In testing, a request from outside the
  allowed subnets got its connection dropped, while the TLS handshake still completed.
- **List subnets explicitly.** Never use Caddy's `private_ranges` shortcut. It includes
  `172.16.0.0/12`, where Docker's own addresses live.
- **Publish on explicit IPv4 addresses, never the bare `"443:443"`.**
  - The bare form also listens on IPv6. With Docker's defaults, if the container has no IPv6,
    Docker relays IPv6 connections, and they arrive from a `172.x` address.
  - Compose can't loop over a list of addresses, so the published ports for Caddy and for
    Pi-hole's DNS move out of the tracked compose files.
  - Reconcile writes them, one entry per address, into a generated, gitignored
    `docker-compose.override.yml` next to each app's compose file, which Compose loads
    automatically. It recreates the container when the set changes.
- **Requests from the server itself** arrive from a Docker address and are refused at the HTTP
  layer. hsctl's own checks only need the TLS handshake, which still completes.

### Name resolution: Pi-hole, VPN clients only

**How hsctl reaches Pi-hole.** Reconcile talks to Pi-hole's API at the container's address on the
`homeserver-edge` network (the host can reach Docker bridge addresses directly), using the admin
password hsctl already generates. The API stays unpublished, and Caddy's allowlist isn't
involved.

**The setup** ([verified](#verified-so-far) with Pi-hole 2026.07.2, FTL v6.7):

1. **`pihole/custom.list`** (already mounted, already written by hsctl) gets one record per name:
   `100.121.5.10 vault.lab.example.com`, … (`VPN_ADDR`).
2. **`local=/lab.example.com/`** in Pi-hole's `misc.dnsmasq_lines`, set through the API. Pi-hole
   then answers everything under the base domain itself and never forwards it:
   - AAAA queries get an empty answer, not a leaked upstream lookup;
   - names that aren't services get NXDOMAIN.
3. **A `Default`-group regex deny entry** answers the names with NXDOMAIN:
   ```
   ^(home|vault|cloud|pihole|pdf|tools|image)\.lab\.example\.com$;reply=nxdomain
   ```
4. **A group `homeserver-vpn`** contains the VPN subnet as a client, which **stays in `Default`
   too**, and the same regex as an **allow** entry. Allow entries beat deny entries, so VPN
   clients fall through to the local record and everyone else gets NXDOMAIN.

What testing showed:

| Query | VPN client | LAN client |
|---|---|---|
| `vault.<base>` A | the server's VPN address | NXDOMAIN |
| `vault.<base>` AAAA | empty answer | NXDOMAIN |
| another name under the base | NXDOMAIN (answered locally) | NXDOMAIN |

Pi-hole's log shows rules are applied before local records ("regex denied … is NXDOMAIN" for the
LAN client, although `custom.list` has the record). Because VPN clients stay in `Default`, every
adlist and every allow or deny entry the admin has, or adds later, applies to them unchanged.
There's nothing to keep in sync.

Requirements and consequences:

- **The base domain must be used only for the homeserver.** With `local=/<base>/`, Pi-hole
  answers everything under it, so any other public records there would vanish for Pi-hole's
  clients.
- **The names come from the `services.json` keys plus `DASHBOARD_SUB`.**
- **It fails closed.** Any client Pi-hole doesn't recognise is in `Default`, so it gets NXDOMAIN:
  new LAN devices, IPv6 queries, Docker relay addresses.
- **Pi-hole must see each VPN client's real address.** NetBird traffic arrives on the host from
  the peer's NetBird IP, and Docker's port forwarding preserves it over IPv4. This is to confirm
  on a real NetBird setup.
- **The existing `FTLCONF_dns_listeningMode: ALL` is needed,** because queries arrive from a
  subnet that isn't the container's own.
- **Pi-hole must listen on `VPN_ADDR`.** The published DNS addresses include it whenever domain
  access is on (see [Configuration](#configuration-and-secrets)). Today's rule alone isn't
  enough: when something already holds port 53 on the host, it binds the LAN IP only.
- **Pi-hole is required while domain access is on.** This is a rule in `Config.Validate()` (see
  [Reconcile](#reconcile-one-writer-for-derived-state)), so it holds for every path that changes
  `DISABLED_APPS`: the Apps page, `hsctl apps`, the wizard and `setup --disable-apps`. Pi-hole
  restarts and updates briefly interrupt DNS for VPN clients, as they already do for the LAN.
- **DNS is not what keeps the LAN out; the Caddy allowlist is.** If the Pi-hole rules go wrong
  and LAN devices start resolving the names, they still can't connect without the VPN.
- **DuckDNS names resolve publicly** whatever Pi-hole does. Point the DuckDNS record at
  `VPN_ADDR` so the public answer leads nowhere.

### The VPN: NetBird

NetBird is a WireGuard-based mesh VPN. Each device runs the NetBird agent and gets a NetBird IP
from one `/16` block per account inside `100.64.0.0/10` (for example `100.121.0.0/16`). Devices
connect directly through NAT where they can, and through NetBird's relays where they can't, so
**no port-forward is needed** on a home router.

**On the server.** The NetBird agent runs on the host and is joined to the user's NetBird
account with a setup key. hsctl reads the NetBird interface (`wt0`):

- **`VPN_ADDR`** is its address, e.g. `100.121.5.10`. Pi-hole answers the service names with it,
  and Pi-hole's DNS and, on internet-facing servers, Caddy listen on it.
- **`VPN_SUBNET`** is its network, e.g. `100.121.0.0/16`. The allowlist and the Pi-hole group use
  it.

Both can be overridden for other setups.

**In the NetBird dashboard** (shown as steps in the wizard; hsctl doesn't hold a NetBird API
token):

1. **DNS → Nameservers:** add the server's NetBird IP (port 53) for the group of devices that use
   the homeserver.
   - Set it as the **primary** nameserver. NetBird only supports domain-specific nameservers
     ("match domains") on macOS, Windows and Linux with systemd-resolved, not on Android or iOS.
   - As primary, all of those devices' DNS goes through Pi-hole while NetBird is connected, which
     also gives them ad-blocking.
   - Desktop-only setups can use a match domain for the base domain instead.
2. **Access control:** a policy that lets that device group reach the homeserver on TCP 443 and
   8443–8448 (HTTPS) and port 53 (DNS). NetBird's per-device access control sits in front of
   Caddy's allowlist.

NetBird also has "Custom Zones": private DNS records sent only to chosen device groups. They could
replace the Pi-hole rules, but hsctl would then need a NetBird API token
([alternatives](#alternatives-considered)).

Which NetBird control plane to use — NetBird's hosted service or a self-hosted one — is
[open question 2](#open-questions).

### Apps that need to know the domain

| App / piece | Change |
|---|---|
| `services.json` | No new field: each entry's existing `key` (`vault`, `cloud`, `pihole`, `pdf`, `tools`, `image`) is its subdomain. The dashboard's subdomain is `DASHBOARD_SUB` (default `home`), which can't equal an app key. |
| Dashboard URLs and origins | One helper works out the request's base URL and the allowed origins (`https://<ip>`, `https://<DASHBOARD_SUB>.<base>`) from the config. Everything uses it: the tiles (`Service.URL` takes a base URL instead of an IP, so domain visitors get domain links), `/help`, the terminal WebSocket check (`wsOriginOK`) and the new `Origin` check on admin POSTs. |
| `/help` page | The certificate-install section of `ONBOARDING.md` is shown to IP visitors only. Domain visitors are told nothing needs installing. |
| Caddy's "app isn't running" page (`app_offline`) | Links back to `https://<ip>/` today; on domain sites it links to the dashboard's domain URL. |
| Nextcloud | `NC_TRUSTED_DOMAINS` is only applied on first install, so reconcile adds `cloud.<base>` with `occ config:system:set trusted_domains N` and keeps the IP entry. |
| Vaultwarden | See below. |
| `WELCOME.txt`, `hsctl install` / `hsctl ui` output | Show the domain URLs too. |

#### Vaultwarden

Vaultwarden has one setting, `DOMAIN`, for "my own web address". It can hold only one address,
but this design gives Vaultwarden two: `https://192.168.1.10:8443` and
`https://vault.lab.example.com`. From the source of the pinned version (1.37.1), `DOMAIN` is used
for four things:

1. **Links Vaultwarden generates.** Attachment downloads and file-Sends get a link built from
   `DOMAIN`, not from the address the app used (`src/auth.rs`, the `Host` request guard;
   `src/db/models/attachment.rs`, `get_url`). If `DOMAIN` is the domain, a phone at home on WiFi
   without NetBird gets a link to `vault.lab.example.com`, which doesn't resolve there, so the
   download fails. Logins, passwords, sync and autofill don't use these links.
2. **Passkey and security-key 2FA (WebAuthn).** These are tied to exactly one address, `DOMAIN`
   (`src/api/core/two_factor/webauthn.rs`). Browsers never allow them on a bare IP, so today they
   can't work at all; with `DOMAIN` set to the domain, they work over the domain.
3. **The address Vaultwarden tells the apps** in `/api/config` (`src/api/core/mod.rs`).
4. **Login tokens.** These are signed with an issuer built from `DOMAIN`, and refresh tokens are
   checked against it (`src/auth.rs`, `JWT_LOGIN_ISSUER`, `decode_refresh`). **Changing `DOMAIN`
   logs every device out once.** "Remember this device" 2FA tokens and pending invite and
   emergency-access links stop working too.

| `DOMAIN` | IP route (LAN, no VPN) | Domain route (NetBird) | Devices logged out? |
|---|---|---|---|
| The IP URL (today) | Everything works | Attachment links point at the IP, which needs the local CA trusted | No |
| The domain URL | Attachment and file-Send downloads fail; everything else works | Everything, plus passkey / security-key 2FA | Once |
| Unset | Links follow whichever address was used | Same | Once |

"Unset" is the only option where attachments work on both routes, and it loses nothing compared
with today. It's still unproven, for two reasons:

- Vaultwarden falls back to `http://localhost` in what it tells the apps and in its passkey
  settings.
- Browser requests may produce a malformed attachment link, because the `Referer` header is used
  as the base URL.

It needs a test with real Bitwarden apps ([open question 4](#open-questions)).

### Local certificates

**Always on, alongside the public certificate** (decision 2):

- **`local.caddy` is always written.** No operation touches it, the `:80` CA-download page,
  `caddy-root-ca.crt`, or the CA in the `caddy-data` volume — not Let's Encrypt enable, disable or
  renew, not `setup --force`, `down`, or `uninstall` without `--data`.
- **There are no commands or settings to turn local certificates off.**

The local root CA lasts for years and isn't restricted to our names: a device that trusts it
would accept a certificate it signs for any site. That risk is accepted; the [Security](#security)
table records it.

---

## Configuration and secrets

`setup.conf` (settings, not secrets):

| Key | Meaning | Default |
|---|---|---|
| `DEPLOYMENT` | `home` or `internet` | `home`; `install` asks when the detected address is public |
| `SERVER_IP` | *Existing key.* The address the IP sites, their certificate and `default_sni` use | the detected LAN IP (home), `VPN_ADDR` (internet) |
| `ACME_BASE_DOMAIN` | e.g. `lab.example.com`; must be used only for the homeserver. Empty means Let's Encrypt is off | empty |
| `DASHBOARD_SUB` | The dashboard's subdomain | `home` |
| `ACME_DNS_PROVIDER` | `cloudflare`, `duckdns` or `desec` | — |
| `ACME_EMAIL` | *Existing key*, now only passed to lego. Optional; must not be at `example.com`, which Let's Encrypt rejects | blank — see below |
| `ACME_STAGING` | Use Let's Encrypt's staging server | `off` |
| `VPN_ADDR` | The server's NetBird IP ([The VPN](#the-vpn-netbird)) | read from `wt0` |
| `VPN_SUBNET` | The NetBird network | read from `wt0` |
| `ALLOW_SUBNETS` | Subnets besides `VPN_SUBNET` allowed on the IP sites: the LAN, a provider private network, … | the LAN subnet (home), empty (internet) |
| `CADDY_BIND` | *Override only.* IPv4 addresses Caddy is published on | derived: `0.0.0.0` (home); `SERVER_IP` and `VPN_ADDR` (internet) |
| `PIHOLE_DNS_BIND` | *Existing key, becomes override only.* IPv4 addresses Pi-hole's DNS is published on | derived — see below |

**The bind settings are derived at reconcile time** from `DEPLOYMENT`, `SERVER_IP`, `VPN_ADDR`
and the port-53 check:

- **Home:** today's rule (`0.0.0.0`, or the LAN IP when port 53 is taken), plus `VPN_ADDR` when
  domain access is on and the LAN IP was chosen.
- **Internet:** `VPN_ADDR` only.

`setup.conf` only stores a value when the admin overrides it. That way, switching an existing
install to `internet` or turning on domain access actually changes where things listen. A stored
copy of an old default would otherwise keep Pi-hole open on the public IP.

**`ACME_EMAIL` moves out of Caddy entirely.** Caddy no longer does ACME, so it leaves
`caddyEnv()`, the Caddyfile and Caddy's compose file. Its default in `Defaults()` becomes blank, so
`you@example.com` can't come back from any source, and a migration blanks a saved
`you@example.com`.

**The DNS token** lives in its own credentials file, `caddy/.acme-env`, like backup's
`.backup-env`:

- **Format:** provider variable names as lego expects them (`CF_DNS_API_TOKEN=…`,
  `DUCKDNS_TOKEN=…` or `DESEC_TOKEN=…`).
- **Who reads it:** lego, via `--env-file`; only the lego container mounts it. Caddy never does.
- **Permissions:** `0600`; hsctl writes it with `writeFile0600`.
- **Not touched by `Generate`,** so `setup --force` can't wipe it.
- **Listed in `.gitignore` and `.dockerignore`.** `make sandbox` copies the repo into an image
  (`COPY . /repo`), and today's `.dockerignore` only excludes `.env` files.
- **Lifecycle:** added to the config files a backup includes, and to the files `uninstall`
  removes.
- **Masked** in `hsctl secrets show`, the dashboard and the logs.

A provider registry in Go (key, display name, lego `--dns` name, credential variable, help link)
drives the CLI prompts, the wizard and the admin form.

## Reconcile: one writer for derived state

Everything derived from `setup.conf` and `services.json` is written by one idempotent function,
`reconcile`, and by nothing else:

- the site files;
- the published-ports overrides;
- the Pi-hole records, settings and rules;
- Nextcloud's trusted domain;
- Vaultwarden's `DOMAIN` (per [open question 4](#open-questions));
- the dashboard's allowed origins.

Callers:

- `hsctl up`, `hsctl setup` and the wizard;
- the Apps toggle;
- `hsctl certs public enable|disable` and the admin domain form;
- `hsctl upgrade`, restore, and the daily timer.

"Set or change the domain" is just "save the config, then reconcile", and so is "remove it". So
generated state can't drift from its inputs, whichever path changed them.

**Reconcile and `certs renew` hold one lock file**, so the CLI, the dashboard and the timer never
run them at the same time. Two concurrent lego runs would order duplicate certificates, and with
DuckDNS they'd overwrite each other's single verification record.

1. **`Config.Validate()`**, also run by `Config.Save`.
   - **When `ACME_BASE_DOMAIN` is set** (otherwise these rules don't apply):
     - the base domain is a real hostname (not an IP, not a wildcard);
     - `DASHBOARD_SUB` doesn't collide with an app key;
     - the provider is known and its credentials file is present;
     - the email, if given, is valid and isn't at `example.com`;
     - `VPN_ADDR` and `VPN_SUBNET` are set;
     - Pi-hole isn't in `DISABLED_APPS`.
   - Installs without Let's Encrypt validate exactly as today.
2. **Certificate.** This step only runs for callers that can wait: CLI commands, the wizard's job,
   the admin domain form's job and the timer. The Apps toggle skips it, and the timer catches up.
   - When `ACME_BASE_DOMAIN` is set, it runs `lego run` (see
     [Certificates](#certificates-hsctl--lego)). lego decides whether anything needs doing:
     nothing, a renewal, or a new certificate after a domain or CA change.
   - If the certificate changed, `caddy reload --force` follows.
   - A failure is recorded, and the remaining steps still run. Only `public.caddy` is held back
     until a certificate exists.
3. **Render the site files.** If any changed:
   - validate the new set in a throwaway container of the Caddy image (`docker run --rm …
     caddy validate`). This is rare, so the container start is cheap. If they're invalid, keep the
     old files and report.
   - Otherwise swap them in atomically (`writeFileAtomic`). If Caddy is running, `reload --force`;
     if it isn't, they load at its next start.
   - Validating before writing, rather than rolling back after a failed reload, keeps "Caddy
     isn't running" apart from "the config is bad".
4. **Render the published-ports overrides.** If an app's set of addresses changed, recreate that
   container.
5. **Pi-hole, Nextcloud and Vaultwarden** are independent: they run concurrently, and each only
   acts when its state differs.
6. **Record the results** in the state file ([Status and monitoring](#status-and-monitoring)).

## CLI

**First setup**

```bash
hsctl setup --base-domain lab.example.com [--dashboard-sub home] --dns-provider cloudflare \
            --dns-token-file ~/cf.token [--acme-email me@mydomain.com] [--acme-staging] \
            [--deployment internet] [--vpn-addr 100.121.5.10 --vpn-subnet 100.121.0.0/16]
hsctl up
```

- **The VPN settings default to what NetBird's `wt0` shows,** so the flags are only needed to
  override them.
- **The token comes from a file, stdin or `HSCTL_DNS_TOKEN`,** never a flag value, which would
  show up in `ps` output and shell history.
- **Interactive `hsctl setup` asks one extra question:** "Use your own domain with Let's Encrypt?
  [y/N]". The token is read without echo.

**Install on an internet-facing server**

1. Install NetBird on the server and join it to the account.
2. Run `sudo hsctl install --deployment internet`. Without the flag, `install` stops and asks for
   it when the detected address is public, rather than defaulting to `home`.
3. Nothing is published on the public IP. Before setup, the admin reaches the wizard either over
   NetBird, or through an SSH tunnel straight to the dashboard, which already listens on loopback:
   ```bash
   ssh -L 8088:127.0.0.1:<UI_PORT> you@host
   ```
   then opens `http://localhost:8088`. The SSH tunnel provides the encryption, and neither Caddy
   nor its allowlist is involved.

**After setup** — a new `hsctl certs` group:

```
hsctl certs                         status: local CA, public certificate, Pi-hole rules, NetBird, exposure
hsctl certs public enable  …        same flags as setup (also changes the domain later)
hsctl certs public disable          remove the domain sites and Pi-hole rules; local untouched
hsctl certs renew [--force]         what the timer runs; --force renews now (after fixing DNS or the token)
hsctl get-ca                        unchanged
```

## Dashboard

**First-run wizard**

- **Step 1** gets a collapsed "Use your own domain (optional)" section: base domain, dashboard
  subdomain, provider, token (write-only), optional email and staging. It also shows the NetBird
  address it detected, plus the steps to do in NetBird's dashboard.
- **Step 3** is "Start everything", which runs `hsctl up`. Its reconcile requests the certificate
  under the lock, in the same job that starts the apps. The step then shows the certificate
  result.
- **Step 4 adapts.** When the certificate was issued, it shows the domain URLs ("nothing to
  install on NetBird devices"). The `root.crt` download stays available for the IP route.
- **The wizard never blocks on Let's Encrypt.** A failure is reported and fixed later from
  Admin → Domain & certificates.

**After setup**

- **Admin → Domain & certificates** (`/admin/certs`) with two cards:
  - *This network (local)*: status, CA fingerprint, `root.crt`.
  - *Your domain (Let's Encrypt)*: the form (base domain, dashboard subdomain, provider, token,
    email, staging); status (issuer, expiry, last renewal, last error); Renew now; Remove.
    Changing the base domain shows the consequences first: a new certificate, and a one-time
    Vaultwarden logout if `DOMAIN` follows the domain.
- **Command Center cards**, following its rule that every CLI command has a card:
  - "Certificate status" (read-only);
  - "Renew certificate".

  The domain form holds a secret, so it lives on its own page, like other interactive actions.
- **Admin → Apps** explains why Pi-hole can't be switched off while domain access is on. The
  rule itself is in `Config.Validate()`.
- **A banner on the admin page** for: certificate expiring within 14 days, last renewal failed, or
  an exposure problem.
- **Long-running work uses one job runner** built on `runHsctl`. It replaces the two hand-written
  copies today (`setupRunStatus`, `restoreStatus`). Applying changes reloads Caddy, which fronts
  the dashboard, so the page polls the job and survives the drop.

## Status and monitoring

- **A small state file, `caddy/.certs-state.json`,** written by reconcile, `certs renew` and the
  status checks. It holds:
  - the certificate's issuer and expiry;
  - the last lego run (time, exit status, error tail);
  - the last check results.
- **The admin banner only reads that file,** the way the backup freshness tile reads
  `backups/.last-backup` (`backupFreshnessFor`). No Docker, network or TLS work on page loads.
- **`hsctl certs` and the certificates page show the last result.** "Check again" runs the live
  checks concurrently under one deadline, with a single Pi-hole API session, then caches the
  result, as the Updates page does. The checks:
  - **the TLS probe:** a handshake to `VPN_ADDR` with `ServerName=<DASHBOARD_SUB>.<base>`,
    verified against the system roots;
  - **the stored certificates:** `lego certificates list`;
  - **the Pi-hole records, settings and rules:** present, as reconcile left them;
  - **NetBird:** `wt0` is up and its address still matches `VPN_ADDR`;
  - **public DNS:** NXDOMAIN for the service names (DuckDNS excepted);
  - **the exposure self-check:** every listening socket on a non-private address, IPv4 and IPv6.
    Nothing of ours is allowed.

## Security

| Risk | Mitigation |
|---|---|
| **The DNS token can rewrite the whole zone**, not just the homeserver's names, and it's in every backup, including the off-site copy. | Use a domain dedicated to the homeserver, or point `_acme-challenge.<base>` at a throwaway zone with a CNAME (lego follows CNAMEs by default). Scope tokens: Cloudflare `DNS:Edit` + `Zone:Read` on one zone; deSEC token policies. `uninstall` reminds the user to revoke the token at the provider; deleting the file doesn't revoke it. |
| **Exposure is one misconfiguration away**: IPv6 relay addresses slipping past the allowlist, Docker-published ports bypassing ufw, a stale bind value in `setup.conf`. | The publishing and allowlist rules in [Caddy](#caddy-local-sites-and-domain-sites), the derived bind settings, and the exposure self-check. Don't rely on ufw. |
| **What sits behind a leak.** The dashboard is a root shell behind one password with no login throttling (`handleLogin` in `hsctl/ui.go`). Its admin forms have no CSRF token and rely on `SameSite=Lax`, which treats sibling subdomains (and today, sibling ports on the same IP) as the same site. | Add login throttling. Check `Origin` on every admin POST using the shared origins helper ([Apps](#apps-that-need-to-know-the-domain)). |
| **The VPN becomes the perimeter.** A lost phone with NetBird on it is "inside". | NetBird's per-device access policies and device removal in its dashboard; Vaultwarden's own 2FA still applies. |
| **NetBird's control plane** decides which devices join the network. | Protect the NetBird account with 2FA. The control-plane choice is [open question 2](#open-questions). |
| **The local CA is unrestricted and long-lived**, and always on (decision 2). | Accepted. Its key is in the `caddy-data` volume and backups, which are already treated as sensitive. |
| **Hiding the names is only partial**: the certificate logs show `*.<base>`, DuckDNS names resolve publicly, and an off-VPN device that tries a name leaks the lookup. | Low risk; the allowlist is the real control. |
| **The lego image joins the chain of trust.** | It's the upstream project's official image, pinned, and checked by `hsctl images` like every other image. |
| **The sandbox starts from the real repo and config.** | `.dockerignore` keeps the credentials file out of the sandbox image, and the sandbox clears `ACME_BASE_DOMAIN`, so it can't use the real token or order certificates. |

One improvement comes for free: today the dashboard's login cookie is sent to every app on the
same IP, because cookies ignore ports. A cookie for the dashboard's subdomain isn't sent to
`vault.<base>`.

## Operations

- **Renewals can fail silently.** Causes: an expired or rotated token, a provider outage (DuckDNS
  is free and has occasional outages), a Let's Encrypt outage. Let's Encrypt stopped sending expiry
  emails in 2025, and lifetimes are shrinking toward 45 days by 2028, so the warning window drops
  from about 30 days to about 15. On expiry every domain URL breaks at once; the IP route keeps
  working.
  - **Now:** the admin banner.
  - **The real fix:** a notification channel (ntfy, email), which the stack doesn't have yet.
- **Renewal depends on the timer, Docker and hsctl.** If hsctl is broken or removed, certificates
  stop renewing. The state file makes this visible.
- **More things have to work for a domain request**: NetBird connected → Pi-hole answering →
  Caddy serving → a valid certificate. Every failure looks the same to the user ("can't connect"),
  so `hsctl certs` checks each step and names the one that failed.
- **NetBird is now a dependency for domain access.** If the NetBird control plane or relays are
  down, new connections may fail; existing direct connections usually keep working. The IP route
  on the LAN never depends on NetBird.
- **Boot order.** Where Caddy or Pi-hole publish on the NetBird IP, `wt0` must be up before Docker
  starts them, or they don't start. Fix it with a systemd ordering rule (Docker after
  `netbird.service`), or `net.ipv4.ip_nonlocal_bind=1`.
- **A Caddy config error takes everything down**, the dashboard included. Reconcile validates
  before writing, so bad files never reach Caddy. The SSH escape hatch is
  `hsctl certs public disable`.
- **Two addresses per service can confuse people.** The Bitwarden and Nextcloud apps store one
  server address, and switching means logging in again. The onboarding guide says: the domain on
  NetBird, the IP address as the fallback at home.
- **Rate limits.** Let's Encrypt allows 5 identical certificates per week. The protections:
  - the staging toggle;
  - the reconcile lock;
  - `lego run` only ordering when something is due or changed;
  - the sandbox guard;
  - restores that keep the `acme` volume.
- **Backups** gain the `acme` volume (automatic) and the credentials file. NetBird's own
  enrolment lives with NetBird; a restored server is re-joined with a setup key.

## Upgrades and migration

- **hsctl and the repo checkout must move together.** The tracked Caddyfile imports files that
  hsctl writes, so a mismatch takes HTTPS down:
  - **new checkout + old hsctl:** `sites/` is never written, so after Caddy's next restart only
    port 80 answers;
  - **new hsctl + old checkout:** hsctl writes IP sites the old Caddyfile already defines, and
    Caddy refuses to start because of the duplicates.

  `install.sh` is one-time and there's no upgrade command today. The fix:
  - **A layout version** in the Caddyfile that hsctl checks before writing anything.
  - **An ordered list of migrations keyed by that version.** The existing
    `migrateSharedNetworkEnv` becomes the first. Later ones:
    - moving the Caddyfile into `caddy/conf/` and the published ports into the generated
      overrides (recreating Caddy and Pi-hole once);
    - splitting out `local.caddy`;
    - removing `ACME_EMAIL` from Caddy's files and blanking a saved `you@example.com`;
    - dropping saved `PIHOLE_DNS_BIND` values that equal the old default, so the bind settings
      become derived.
  - **An `hsctl upgrade` command** that moves the binary and the checkout to the same release,
    runs the pending migrations and reconciles. It handles rollback too.
- **Hand-edited Caddyfiles.** `docs/adding-an-app.md` tells people to edit the tracked Caddyfile,
  and moving the IP blocks out of it would conflict.
  - The upgrade stops if the Caddyfile has local edits, and explains why.
  - From then on, custom sites go in `caddy/conf/sites/custom/`.
  - `adding-an-app.md` becomes "add a snippet and a `services.json` entry".
- **The first upgrade must change nothing visible.** Run `caddy adapt` on the old and the new
  config and require the JSON to be equal, apart from the allowlist.
- **The allowlist could lock out existing users.** Anyone reaching the server from outside the
  expected subnets would be cut off, e.g. Tailscale's `100.64.0.0/10` or a port-forward someone
  already made.
  - The first release only logs what it would block, and reports those sources.
  - The admin adds them to `ALLOW_SUBNETS`; there's no per-product detection.
- **Explicit IPv4 publishing** drops IPv6 access to the IP sites. That's rarely used on a LAN, but
  it goes in the release notes.
- **Changing Vaultwarden's `DOMAIN` logs every device out** ([Vaultwarden](#vaultwarden)).
  Changing the base domain later does the same when `DOMAIN` follows the domain. The admin form
  warns first.
- **`hsctl updates` builds its target list from the compose pins** (`composePins`, as
  `hsctl images` does) instead of from existing containers (`updateTargets`). That covers lego,
  whose container only exists while it runs. It also covers disabled apps, which today's
  container-based list already misses.
- **Pin lego to a v5 release.** v5 changed the command line from v4 (`run` now also renews;
  `renew` and `list` are gone; options moved onto `run`). The commands here are written for v5.

## Testing

### Verified so far

Run on 2026-10-17 in a scratch Docker environment, with the official images:

- **lego 5.5.2**, against Pebble (Let's Encrypt's test CA, two instances standing in for staging
  and production) and Pebble's test DNS server. The `exec` DNS provider played the part of
  Cloudflare/DuckDNS/deSEC, and the credentials came from a file through `--env-file`.
  - A wildcard certificate is issued with no `--email`, and stored by `--cert.name`.
  - A second `lego run` skips renewal when the certificate isn't due; `--renew-force` renews.
  - Under the same name, switching CA keeps the old certificate. That's why there's one name per
    CA.
  - `--force-cert-domains` gets a new certificate when the domain changes.
  - `lego certificates list` shows domains, issuer and expiry.
- **Caddy 2.11** with a domain site and an IP site on port 443:
  - both answer when every site uses `bind 0.0.0.0`; without it on the domain site, the IP site
    on 443 stopped answering;
  - after a renewal, only `docker exec -w /etc/caddy caddy caddy reload --force` serves the new
    certificate. A plain reload keeps the old one, and without `-w` the reload fails;
  - the allowlist drops non-VPN requests while the TLS handshake completes; requests from the
    host arrive from the network gateway address.
- **Pi-hole 2026.07.2 (FTL v6.7)**, configured through its API from the host at the container's
  bridge address. The setup in
  [Name resolution](#name-resolution-pi-hole-vpn-clients-only) gives the results in its table:
  rules apply before local records, and `local=/<base>/` keeps everything under the base domain
  local.

### Still to test

- **On a real server, with NetBird, a real domain and real phones:**
  - a Let's Encrypt **staging** run through each provider in use (Cloudflare, DuckDNS, deSEC),
    then production;
  - Pi-hole sees each NetBird client's own address, and NetBird's primary-nameserver setting
    reaches Pi-hole from Android and iOS;
  - Vaultwarden with `DOMAIN` unset: attachments over both routes, and the Bitwarden apps not
    switching to the `/api/config` URLs;
  - the exposure self-check on an internet-facing server shows nothing of ours on the public IP;
  - boot order: a reboot brings `wt0` up before Caddy and Pi-hole publish on it.
- **Unit tests** (no Docker):
  - config round-trip, and the `Config.Validate()` rules with Let's Encrypt on and off;
  - the derived bind settings for each `DEPLOYMENT`;
  - the provider registry;
  - golden files for each rendering mode (local only, local + public), including the
    published-ports overrides;
  - form validation;
  - Pi-hole record, setting and rule generation;
  - the origins helper;
  - token masking.
- **CI:**
  - `caddy validate` on every rendered mode with the official Caddy image;
  - an end-to-end run of the lego + Pebble setup above.
- **Sandbox:**
  - the allowlist doesn't block the sandbox's own forwarded ports;
  - nothing listens on IPv6 after the publishing change.

## Phases

| Phase | What ships | Visible change |
|---|---|---|
| 0. Groundwork | The `caddy/conf/` layout with snippets and a generated `local.caddy` (JSON-equal to today), the layout version and ordered migrations, `hsctl upgrade`, reconcile and its lock (local sites only at first), the published-ports overrides (explicit IPv4), the exposure self-check, login throttling, the shared origins helper and admin-POST `Origin` check, the shared job runner | None |
| 1. Access control | `DEPLOYMENT` profiles with derived bind settings, NetBird detection (`VPN_ADDR`, `VPN_SUBNET`), `ALLOW_SUBNETS` and the allowlist (log-only at first), the internet-facing `install` path, `updates` targets from compose pins | Internet-facing servers supported |
| 2. Let's Encrypt (CLI) | Config keys and `Config.Validate()`, the provider registry and credentials file, the lego service + `hsctl-certs.timer`, `public.caddy`, the Pi-hole VPN-only setup, the Nextcloud / Vaultwarden domain changes, `hsctl setup` flags, `hsctl certs`, the state file, the sandbox guard, docs | Domain access from the CLI |
| 3. Dashboard | Wizard section (with the NetBird steps), Admin → Domain & certificates, Command Center cards, banners | Domain access from the UI |

## Open questions

| # | Question | Recommendation |
|---|---|---|
| 2 | NetBird's control plane: NetBird's hosted service, or a self-hosted NetBird management server? | The hosted service to start. A self-hosted one needs its own public server, and putting it on the homeserver itself would mean opening ports again. |
| 3 | On internet-facing servers, which address do the IP sites use? (Explained under [Internet-facing server](#internet-facing-server-cloud-or-bare-metal).) | The NetBird IP; never the public IP. |
| 4 | Vaultwarden `DOMAIN`: the IP URL, the domain URL, or unset? (Explained under [Vaultwarden](#vaultwarden).) | Test "unset" on the real server first. If it works, attachments work over both routes. |

Settled since the first draft:
- **Q1:** lego runs as a container, driven by hsctl.
- **Q5:** the names are user-set in the wizard or admin pages, and the base must be dedicated to
  the homeserver.
- **Q6:** local certificates always stay; there's no opt-out.

## Alternatives considered

| Alternative | Why not |
|---|---|
| Let's Encrypt certificates for the IP addresses | Public authorities never certify private addresses. |
| Stock Caddy with the HTTP-01 / TLS-ALPN-01 challenge | Needs publicly resolvable names and port 80 or 443 open to the internet. No wildcards, so every service name lands in the public certificate logs. Conflicts with decisions 4 and 6. |
| A custom Caddy image with DNS plugins, published by this repo | See [why Caddy doesn't do the DNS challenge](#why-caddy-doesnt-do-the-dns-challenge). A lagging DNS plugin can also block a Caddy security fix. |
| lego compiled into hsctl as a library | Certificate-client fixes would wait for an hsctl release, and hsctl has no upgrade path yet. The container updates through `hsctl updates` like every other image. |
| Building Caddy on each server | Slow and memory-hungry on a Raspberry Pi, needs to download Go modules at build time, and every box ends up with its own build. |
| A community Caddy image with plugins | A stranger's build in front of the password manager, and most bundle only Cloudflare. |
| `caddy add-package` | Depends on caddyserver.com's build service, and the downloaded binary is lost whenever the container is recreated. |
| Switching to Traefik (DNS providers built in) | Traefik has no built-in local certificate authority, which decision 2 requires, and the whole proxy layer would need rewriting. |
| A separate CoreDNS container for the VPN names | Superseded by decision 10: Pi-hole does the same with one less container. |
| NetBird Custom Zones for the names | Clean, because NetBird only sends the records to chosen devices. But hsctl would need a NetBird API token to manage them, and VPN clients would lose Pi-hole's ad-blocking unless Pi-hole is also their nameserver. |
| Pi-hole rules that take VPN clients out of `Default` | Works, but VPN clients then lose the admin's adlists and allow/deny entries unless reconcile keeps copying them. Testing showed the `Default`-plus-allow-entry setup works, so this isn't needed. |
| Public DNS records pointing at the VPN address | Simple, but the names would resolve for everyone. Conflicts with decision 4. |
| One static Pi-hole record for everyone, with the Caddy allowlist as the only control | No per-client rules, but the names would resolve on the LAN too. Conflicts with decision 4. |
