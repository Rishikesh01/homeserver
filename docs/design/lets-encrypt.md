# Let's Encrypt certificates — design

**Status: proposed.** Nothing here is implemented yet. This records the decisions made so far,
the architecture they lead to, the risks, and the questions still open. Where a choice is still
open, the architecture below shows the recommended option and the question is listed under
[Open questions](#open-questions).

- [Summary](#summary)
- [Goals and non-goals](#goals-and-non-goals)
- [Decisions](#decisions)
- [Architecture](#architecture)
- [Components](#components)
  - [Certificates: hsctl + lego](#certificates-hsctl--lego)
  - [Caddy: local sites and domain sites](#caddy-local-sites-and-domain-sites)
  - [Name resolution: Pi-hole, VPN clients only](#name-resolution-pi-hole-vpn-clients-only)
  - [The VPN](#the-vpn)
  - [Apps that need to know the domain](#apps-that-need-to-know-the-domain)
  - [Local certificates](#local-certificates)
- [Configuration and secrets](#configuration-and-secrets)
- [CLI](#cli)
- [Dashboard](#dashboard)
- [Apply pipeline](#apply-pipeline)
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

Add publicly trusted certificates from Let's Encrypt for a domain the user owns, **alongside**
today's local certificates, not instead of them.

- Every service gets a subdomain — `vault.lab.example.com`, `cloud.lab.example.com`, … — with
  one wildcard certificate. Those names **resolve and connect only over a VPN**.
- The IP addresses — `https://192.168.1.10:8443` — keep working **on the home network without a
  VPN**, with the local certificate authority, exactly as today.
- Certificates come from the **DNS challenge**, run by hsctl with the official **lego**
  container. Caddy stays the **official image** and only loads the certificate files.
- **Pi-hole** answers the service names for VPN clients only; everyone else gets "no such name".
- Works on a home server and on an internet-facing cloud or bare-metal server.

## Goals and non-goals

**Goals**

- Trusted HTTPS on every device without installing a certificate authority — over the domain.
- Configure it from the `hsctl` CLI and from the dashboard, during first setup and any time after.
- Keep the local certificates and the IP addresses unless the user explicitly opts out.
- Domain access only over the VPN; bots on the internet can't reach the apps.
- Support home servers and internet-facing servers (port-forwarding is allowed).
- DNS providers: Cloudflare, DuckDNS, deSEC.
- No custom Docker image for this repo to build or host.

**Non-goals**

- Public, non-VPN access to the apps from the internet.
- Certificates for IP addresses. Public authorities never certify private addresses.
- The HTTP-01 and TLS-ALPN-01 challenges. Both need publicly resolvable names and an open port.
- More DNS providers in the first version. lego supports many more; adding one is a registry
  entry plus a test, so this can grow later.

## Decisions

| # | Decision | Why |
|---|----------|-----|
| 1 | Let's Encrypt for a user-owned domain. | Trusted certificates without installing a CA on each device. |
| 2 | Local certificates and IP access are **kept**, unless the user explicitly opts out. | Existing devices and app configs keep working; the LAN works with no VPN. |
| 3 | The VPN is needed **only for the domain**. The IP addresses work on the LAN without it. | Daily home use stays as simple as today. |
| 4 | The domain names **resolve only for VPN clients**. | Keep bots away from Vaultwarden and the other apps. |
| 5 | **One subdomain per service**, one wildcard certificate `*.<base>`. The dashboard gets a subdomain too. | One certificate for everything. The public certificate logs show `*.<base>`, not the service names. DuckDNS can hold only one verification record at a time, so a single name is required. |
| 6 | **DNS-01 challenge only.** | The only challenge that works for names that don't resolve publicly, and the only one that allows wildcards. |
| 7 | Providers: **Cloudflare, DuckDNS, deSEC**. | Covers a paid domain and two free options. |
| 8 | Port-forwarding is allowed; **internet-facing servers are supported**. | Needed for the VPN, and for hosting on cloud or bare metal. |
| 9 | Caddy stays the **official image**. Something other than Caddy does the DNS challenge. | Stock Caddy can't talk to DNS providers (see [why](#why-caddy-doesnt-do-the-dns-challenge)); no custom image to host. |
| 10 | **Pi-hole resolves the service names for VPN clients only**; everyone else gets NXDOMAIN. Pi-hole can't be switched off while domain access is on. | No extra DNS server to run; VPN clients also get ad-blocking. |
| 11 | Vaultwarden must work over **both** the IP and the domain, as far as possible. | See [Vaultwarden](#vaultwarden). |

## Architecture

### Home server

```
  LAN device, no VPN                               VPN device (home or away)
  ──────────────────                               ─────────────────────────
  https://192.168.1.10:8443                        https://vault.lab.example.com
          │                                                │
          │                                                │ 1. DNS query to Pi-hole at 10.8.0.1
          │                                                │    vpn group  → 10.8.0.1
          │                                                │    any other  → NXDOMAIN
          │                                                │ 2. HTTPS through the WireGuard tunnel
          ▼                                                ▼
  ┌──────────────────────────── Caddy (official image) ───────────────────────────────┐
  │  IP sites  <ip>:<port>                    │  domain sites  *.lab.example.com       │
  │    tls internal  (local CA — kept)        │    tls <files written by lego>         │
  │    allow: LAN + VPN subnets               │    allow: VPN subnet only              │
  └───────────────────────┬────────────────────────────────────────┬───────────────────┘
                          └──────────── homeserver-edge network ────┘
           vaultwarden · nextcloud · pihole · stirling · it-tools · imagetools
           dashboard (hsctl ui, on the host, via host.docker.internal)

  Router: forward the VPN's UDP port only (e.g. 51820). Nothing else is forwarded.
```

### Getting and renewing the certificate

```
  hsctl-certs.timer (daily)
          │
          ▼
  hsctl certs renew
          │ 1. docker compose run --rm lego …        (official goacme/lego image)
          │       │ 2. write _acme-challenge TXT ──► DNS provider (Cloudflare / DuckDNS / deSEC)
          │       │ 3. order *.lab.example.com   ──► Let's Encrypt
          │       │                                   checks the TXT over public DNS, issues
          │       └─ writes the files to the `acme` volume
          │
          │ 4. certificate changed?
          ▼
  docker exec caddy caddy reload      Caddy reads the files. It never talks to the DNS provider
                                      and never holds the DNS token.
```

### Internet-facing server (cloud or bare metal)

```
  internet ── only UDP 51820 (WireGuard) is reachable ──►  server, public IP 203.0.113.7
                                                            │
                                                            ├─ wg0 10.8.0.1
                                                            ├─ Caddy publishes on 10.8.0.1 only
                                                            │    domain sites (Let's Encrypt)
                                                            │    IP sites https://10.8.0.1:8443 (local CA)
                                                            └─ Pi-hole DNS on 10.8.0.1 only
```

There is no LAN on such a server, so "IP access without a VPN" would mean the public IP. That
would expose the apps to the internet, so the IP sites move to the VPN address (or the
provider's private network). This is [open question 3](#open-questions).

### Who can reach what

| | Home server | Internet-facing server |
|---|---|---|
| IP sites (local CA) | LAN without VPN, and VPN | VPN or the provider's private network only, never the public IP |
| Domain sites (Let's Encrypt) | VPN only | VPN only |
| Pi-hole DNS | LAN IP and VPN address | VPN address only |
| Reachable from the internet | The VPN port (router forward) | The VPN port |

---

## Components

### Certificates: hsctl + lego

**What runs.** The official `goacme/lego` image (the ACME client Traefik is built on). It already
supports Cloudflare, DuckDNS and deSEC. It's pinned like every other image here
(`image: ${LEGO_IMAGE:-goacme/lego:<version>}`). It's defined as a service in
`caddy/docker-compose.yml` under a compose profile, so `docker compose up` never starts it. It
only runs on demand:

```bash
# first issue
docker compose --profile certs run --rm lego \
  --path /data --accept-tos --dns cloudflare \
  --dns.resolvers 1.1.1.1:53 --dns.resolvers 9.9.9.9:53 \
  --domains '*.lab.example.com' [--email me@mydomain.com] [--server <staging URL>] run

# renewal — lego only renews when fewer than 30 days remain
docker compose --profile certs run --rm lego … renew
```

- **`--dns.resolvers` is required, not optional.** Pi-hole answers NXDOMAIN for the service
  names to anything that isn't a VPN client. If lego's propagation check went through the host's
  resolver, it could be answered by our own rule. lego is pointed at public resolvers instead.
- **The token goes only to the lego run.** hsctl passes it as an environment variable
  (`CF_DNS_API_TOKEN`, `DUCKDNS_TOKEN` or `DESEC_TOKEN`) through the child process environment,
  never on a command line. It never enters Caddy's container.
- **Where the files go.** lego writes the account and the certificate to a named volume, `acme`,
  in the `caddy` compose project. Volume discovery puts it in every backup, so a restore doesn't
  request a new certificate. A wildcard certificate is saved as `_.lab.example.com.crt` / `.key`.

**Renewal.** A `hsctl-certs.timer` (daily, `RandomizedDelaySec`, `Persistent=true`), modelled on
the existing `hsctl-backup.timer`, runs `hsctl certs renew` as a oneshot service. lego decides
whether a renewal is due; hsctl hashes the certificate before and after the run. If it changed,
hsctl runs `docker exec caddy caddy reload --config /etc/caddy/Caddyfile --force`, which picks up
the new files without dropping connections. lego's own `--renew-hook` runs inside the lego
container, so it can't reload Caddy; hsctl does it instead. lego adds a random delay of up to
8 minutes to non-interactive renewals. The timer tolerates that.

**How Caddy uses the files.** The domain site loads the files with `tls <cert> <key>`. Caddy's
documentation says manually loaded certificates go into its shared cache, and Caddy won't try to
manage certificates for the names they cover. So Caddy never attempts its own ACME for the
domain. The domain site block is only written **after** the first certificate exists. A site
that points at a missing file is a config error, and a config error stops Caddy entirely —
local sites and the dashboard included.

#### Why Caddy doesn't do the DNS challenge

Caddy has Let's Encrypt support built in, but in the official image it can only prove ownership
by having Let's Encrypt connect to it at a public name, on port 80 or 443. The DNS challenge
needs code that writes a record through the DNS provider's API. In Caddy that code is a plugin
per provider, and plugins have to be compiled into the binary. The official image ships none of
them. Letting Caddy do the challenge would therefore mean a custom Caddy build in a custom
image, which someone has to build, sign, host and keep current with every Caddy release. Moving
the DNS step to lego avoids all of that, and Caddy security fixes keep coming straight from
upstream through `hsctl updates`.

### Caddy: local sites and domain sites

The Caddyfile is split so hsctl can write the site blocks while each app's routing stays in one
tracked place:

```
caddy/Caddyfile            tracked: global options, the :80 CA-download page,
                           one snippet per app, `import sites/*.caddy`, `import sites/custom/*.caddy`
caddy/sites/local.caddy    written by hsctl: the IP:port sites (today's blocks), tls internal
caddy/sites/public.caddy   written by hsctl when Let's Encrypt is on: *.<base>, routed by hostname
caddy/sites/custom/        user-owned and gitignored, for hand-added sites
```

`caddy/sites/` is mounted into the container **as a directory**. hsctl writes files atomically
by renaming them, and a renamed file is visible through a directory mount but not through a
single-file mount.

```caddyfile
# caddy/Caddyfile (tracked) — one snippet per app
(vault) {
	reverse_proxy {$VAULT_UPSTREAM} {
		header_up X-Real-IP {remote_host}
	}
}
```

```caddyfile
# caddy/sites/local.caddy — one block per app, as today, plus the allowlist
{$SERVER_IP}:{$VAULT_HTTPS} {
	bind 0.0.0.0
	tls internal
	@outside not remote_ip 192.168.1.0/24 10.8.0.0/24   # LAN + VPN, written by hsctl
	handle @outside {
		abort
	}
	import app_offline
	import vault
}
```

```caddyfile
# caddy/sites/public.caddy — only once the certificate exists
*.lab.example.com {
	tls /acme/certificates/_.lab.example.com.crt /acme/certificates/_.lab.example.com.key
	@outside not remote_ip 10.8.0.0/24                   # VPN only
	handle @outside {
		abort
	}
	import app_offline
	@home host home.lab.example.com
	handle @home {
		import home
	}
	@vault host vault.lab.example.com
	handle @vault {
		import vault
	}
	# … one pair per app, generated from services.json
	handle {
		abort                                            # any other name: drop the connection
	}
}
```

Rules for the allowlist:

- **List the VPN and LAN subnets explicitly.** Never use Caddy's `private_ranges` shortcut. It
  includes `172.16.0.0/12`, where Docker's own addresses live (see [Security](#security)).
- **Publish Caddy's ports on explicit IPv4 addresses** (`"0.0.0.0:443:443"`, or the VPN address on
  an internet-facing server), never the bare `"443:443"`. The bare form also listens on IPv6, and
  Docker relays IPv6 connections so they arrive from a Docker address.
- **Requests from the server itself** can arrive from a Docker address and be refused at the HTTP
  layer. hsctl's own checks only need the TLS handshake, which still completes.

### Name resolution: Pi-hole, VPN clients only

hsctl configures Pi-hole through its API, using the admin password it already generates:

1. A group `homeserver-vpn` containing the VPN subnet as a client (`10.8.0.0/24`). That client is
   **removed from the `Default` group**. Otherwise it would get both rules below, and which one
   wins would be undefined.
2. A regex denylist entry in `homeserver-vpn` that answers the service names with the server's
   VPN address:
   ```
   ^(home|vault|cloud|pihole|pdf|tools|image)\.lab\.example\.com$;reply=10.8.0.1
   ```
3. The same regex in `Default` with `;reply=nxdomain`.
4. Pi-hole's adlists also assigned to `homeserver-vpn`. Adlists belong to `Default` unless told
   otherwise, so without this, taking the VPN client out of `Default` would also take away its
   ad-blocking. Adlists the admin adds later need the same assignment, so `hsctl certs` re-syncs
   it and flags any it had to fix.

The name list is generated from `services.json`. Only the service names are covered, so a base
domain that also holds real public records keeps working for everyone.

This **fails closed**. Pi-hole puts any client it doesn't recognise in `Default` — new LAN
devices, IPv6 queries, Docker relay addresses — so they get NXDOMAIN. Only the VPN subnet gets
an answer. Pi-hole's regex extensions (`;reply=<IP>`, `;reply=nxdomain`) and the `Default` group
behaviour are documented upstream.

Requirements and consequences:

- Pi-hole must see each VPN client's real address. WireGuard on the host preserves it, and
  Docker's port forwarding preserves it over IPv4. A VPN that rewrites client addresses to its
  own would make every VPN client look like one address.
- The existing `FTLCONF_dns_listeningMode: ALL` is needed, because queries arrive from a subnet
  that isn't the container's own.
- VPN clients use the server's VPN address as their DNS server. With step 4 that also gives them
  ad-blocking.
- **Pi-hole becomes required while domain access is on.** `hsctl apps disable pihole` and the
  Admin → Apps switch refuse, with an explanation.
- On an internet-facing server, Pi-hole's DNS listens on the VPN address only. Listening on a
  public address makes it an open resolver, which gets abused for attacks on other people.
- **DNS is not what keeps the LAN out; the Caddy allowlist is.** If the Pi-hole rules are edited
  by hand and LAN devices start resolving the names, they still can't connect without the VPN.
- DuckDNS names resolve publicly whatever Pi-hole does. With DuckDNS, point the record at the
  VPN address so the public answer leads nowhere.

### The VPN

Which VPN is [open question 2](#open-questions). The certificate work only needs:

- the server's VPN address (`VPN_ADDR`, e.g. `10.8.0.1`) and the VPN subnet (`VPN_SUBNET`);
- clients configured to use `VPN_ADDR` as their DNS server;
- client addresses not rewritten (no masquerading);
- one UDP port forwarded on the router (home) or open in the provider firewall (internet-facing).

Peers, QR codes and the rest of VPN management are a separate feature.

### Apps that need to know the domain

| App / piece | Change |
|---|---|
| `services.json` | New `"sub"` field per tile (`"vault"`, `"cloud"`, …). The dashboard uses `"home"`. |
| Dashboard tiles | Links follow the host the visitor used: domain visitors get domain links, IP visitors get IP links. Today `Service.URL` always builds IP links. |
| `/help` page | The certificate-install section of `ONBOARDING.md` is shown to IP visitors only. Domain visitors are told nothing needs installing. |
| Terminal WebSocket origin check (`wsOriginOK`) | Explicitly allow `https://home.<base>`. |
| Nextcloud | `NC_TRUSTED_DOMAINS` is only applied on first install, so hsctl adds `cloud.<base>` with `occ config:system:set trusted_domains N` and keeps the IP entry. |
| Caddy's "app isn't running" page (`app_offline`) | Links back to `https://<ip>/` today; on domain sites it links to `https://home.<base>/`. |
| Vaultwarden | See below. |
| `WELCOME.txt`, `hsctl install` / `hsctl ui` output | Show the domain URLs too. |

#### Vaultwarden

Vaultwarden has one `DOMAIN` setting. From the source of the pinned version (1.37.1):

- With `DOMAIN` set, attachment and file-Send download links are built from `DOMAIN`, not from
  the address the client used (`src/auth.rs`, the `Host` request guard;
  `src/db/models/attachment.rs`, `get_url`).
- Passkey and security-key 2FA (WebAuthn) use `DOMAIN` as the relying-party ID
  (`src/api/core/two_factor/webauthn.rs`). Browsers never allow WebAuthn on a bare IP.
- Login **and refresh** tokens are checked against an issuer built from `DOMAIN` (`src/auth.rs`,
  `JWT_LOGIN_ISSUER`, `decode_refresh`). **Changing `DOMAIN` logs every device out.** So do
  "remember this device" 2FA tokens and pending invite and emergency-access links.
- `/api/config` advertises `DOMAIN` URLs to the apps (`src/api/core/mod.rs`).

| `DOMAIN` | IP route (LAN, no VPN) | Domain route (VPN) | Devices logged out? |
|---|---|---|---|
| The IP URL (today) | Everything works | Attachment links point at the IP, which needs the local CA trusted | No |
| The domain URL | Attachment and file-Send downloads fail (the link points at a name that only resolves on the VPN) | Everything, plus passkey / security-key 2FA | Once |
| Unset | Links follow whichever address was used | Same | Once |

"Unset" is the only option where attachments work on both routes, and it loses nothing compared
with today. It's still unproven:

- Vaultwarden falls back to `http://localhost` in what it advertises to the apps and in its
  WebAuthn settings.
- Browser requests may produce a malformed attachment link, because the `Referer` header is used
  as the base URL.

It's tested in the sandbox before anything is chosen ([open question 4](#open-questions)).

### Local certificates

Kept by construction:

- `local.caddy` is written whenever `TLS_LOCAL=on`, which is the default.
- No Let's Encrypt operation touches `local.caddy`, the `:80` CA-download page,
  `caddy-root-ca.crt`, or the CA in the `caddy-data` volume. That includes enable, disable,
  renew, `setup --force`, `down` and `uninstall` without `--data`.

Opting out (proposed, [open question 6](#open-questions)):

- `hsctl certs local disable` stops serving the IP sites. It's allowed only while the Let's
  Encrypt certificate is valid, so nobody locks themselves out. The CA stays on disk, so turning
  local back on restores the same CA, and devices that already trust it keep working.
- `hsctl certs local purge` is the only thing that deletes the CA, behind a typed confirmation.
- The wizard never offers the opt-out: the admin is connected over the IP and the local
  certificate at that point.

The local root CA lasts for years and isn't restricted to our names: a device that trusts it
would accept a certificate it signs for any site. Once every device uses the domain, opting out
— and removing the CA from the devices — is the safer end state. The docs should say so.

---

## Configuration and secrets

New keys in `setup.conf` (settings, not secrets):

| Key | Meaning | Default |
|---|---|---|
| `DEPLOYMENT` | `home` or `internet` | `home` |
| `TLS_LOCAL` | Serve the IP sites with the local CA | `on` |
| `ACME_BASE_DOMAIN` | e.g. `lab.example.com`; empty means Let's Encrypt is off | empty |
| `ACME_DNS_PROVIDER` | `cloudflare`, `duckdns` or `desec` | — |
| `ACME_EMAIL` | Optional contact. Must not be at `example.com`, which Let's Encrypt rejects | blank (was `you@example.com`) |
| `ACME_STAGING` | Use Let's Encrypt's staging server | `off` |
| `VPN_ADDR` / `VPN_SUBNET` | The server's VPN address and the VPN subnet | — |
| `LAN_SUBNET` | Allowed on the IP sites (home servers) | autodetected |
| `DASHBOARD_SUB` | Dashboard subdomain | `home` |

**The DNS token** is stored as `ACME_DNS_TOKEN` in `caddy/.env`. That file is already `0600`,
gitignored, included in backups and removed by `uninstall`. Compose uses `.env` only for
substitution, so the token reaches a container only where the compose file names it: the lego
service, never Caddy. The token is masked in `hsctl secrets show`, the dashboard and the logs.
`hsctl setup --force` rewrites `caddy/.env` from scratch today, so it has to carry the token
(and the other ACME keys) over. Otherwise a secrets rotation would silently break the next
renewal.

A provider registry in Go (key, display name, lego `--dns` name, environment variable, help
link) drives the CLI prompts, the wizard and the admin form.

## CLI

**First setup**

```bash
hsctl setup --base-domain lab.example.com --dns-provider cloudflare \
            --dns-token-file ~/cf.token --vpn-addr 10.8.0.1 --vpn-subnet 10.8.0.0/24 \
            [--acme-email me@mydomain.com] [--acme-staging] [--deployment internet]
hsctl up
```

- The token comes from a file, stdin or `HSCTL_DNS_TOKEN`, never a flag value, which would show
  up in `ps` output and shell history.
- Interactive `hsctl setup` asks one extra question: "Use your own domain with Let's Encrypt?
  [y/N]". The token is read without echo.
- `sudo hsctl install` still brings up local HTTPS only. On an internet-facing server it opens
  nothing to the internet; the admin reaches the wizard through an SSH tunnel
  (`ssh -L 8443:127.0.0.1:443 you@host`).

**After setup** — a new `hsctl certs` group:

```
hsctl certs                         status: local CA, public certificate, Pi-hole rules, exposure
hsctl certs public enable  …        same flags as setup
hsctl certs public disable          remove the domain sites and Pi-hole rules; local untouched
hsctl certs renew [--force]         what the timer runs; --force renews now (after fixing DNS or the token)
hsctl certs local disable|enable    see Local certificates
hsctl certs local purge             the only command that deletes the local CA
hsctl get-ca                        unchanged
```

## Dashboard

**First-run wizard**

- Step 1 gets a collapsed "Use your own domain (optional)" section: base domain, provider, token
  (write-only), optional email, staging, and the VPN address and subnet if they can't be
  detected.
- Step 3 shows "Requesting certificate…" after "Start everything", then the result.
- Step 4 adapts. When the certificate was issued, it shows the domain URLs ("nothing to install
  on VPN devices"). The `root.crt` download stays available for the IP route.
- **The wizard never blocks on Let's Encrypt.** A failure is reported and fixed later from
  Admin → Certificates.

**After setup**

- **Admin → Certificates** (`/admin/certs`) with two cards:
  - *This network (local)*: status, CA fingerprint, `root.crt`, and a danger zone (turn off,
    delete the CA).
  - *Public (Let's Encrypt)*: the form, status (issuer, expiry, last renewal, last error), Renew
    now, Turn off.
- **Command Center** cards: "Certificate status" (read-only) and "Renew certificate". The enable
  form holds a secret, so it lives on its own page, like other interactive actions.
- **Admin → Apps** refuses to switch Pi-hole off while domain access is on.
- **A banner on the admin page** for: certificate expiring within 14 days, last renewal failed,
  or an exposure problem.
- Applying changes reloads or recreates Caddy, which fronts the dashboard. The page uses the
  existing background-job + status-polling pattern (setup, restore) so it survives the drop.

## Apply pipeline

Shared by the CLI and the dashboard. Enabling Let's Encrypt:

1. **Validate the input.** A real hostname (not an IP, not a wildcard); a known provider; a
   token present; a valid email (if given) that isn't at `example.com`; a VPN address and subnet.
2. **Write** `setup.conf` and the token in `caddy/.env`.
3. **Run lego** (`run`). If it fails, stop and report. Caddy hasn't been touched, so nothing else
   is affected.
4. **Write** `public.caddy` (and `local.caddy`) to temporary names. Run `caddy validate` in a
   throwaway container of the same Caddy image. If it fails, keep the old files and stop.
5. **Swap** the files in and `caddy reload`.
6. **Follow-ups.** Pi-hole groups and rules, the Nextcloud trusted domain, Vaultwarden per the
   chosen `DOMAIN` option.
7. **Probe.** A TLS handshake to the VPN address with `ServerName=home.<base>`, verified against
   the system roots, confirms Caddy serves the new certificate.

Disabling reverses steps 5–6 and never touches the local sites.

## Status and monitoring

`hsctl certs` and the Certificates page show:

- **The certificate**: issuer and expiry from the file, plus the TLS probe confirming Caddy
  serves it.
- **The last lego run**: when, exit status, and the tail of its output.
- **DNS**: the Pi-hole rules are present (read back through the API), and public DNS returns
  NXDOMAIN for the service names. DuckDNS is the exception; there the public answer is expected.
- **Exposure self-check**: every listening socket on a non-private address, IPv4 and IPv6. Only
  the VPN port is allowed.

## Security

| Risk | Mitigation |
|---|---|
| **The DNS token can rewrite the whole zone**, not just the homeserver's names. It sits in `caddy/.env` and every backup, including the off-site copy. | Use a domain dedicated to the homeserver, or point `_acme-challenge.<base>` at a throwaway zone with a CNAME (lego follows CNAMEs by default). Scope tokens: Cloudflare `DNS:Edit` + `Zone:Read` on one zone; deSEC token policies. Mask the token everywhere. `uninstall` reminds the user to revoke it at the provider, because deleting the file doesn't. |
| **IPv6 bypasses the allowlist.** With Docker's defaults, a bare `"443:443"` also listens on IPv6. If the container has no IPv6, Docker relays the connection, and Caddy sees a `172.x` source. | Publish on explicit IPv4 addresses. Allowlist only the VPN and LAN subnets, never `private_ranges` or Docker's ranges. The exposure self-check covers IPv6. |
| **Docker-published ports bypass ufw.** | Don't rely on ufw. Use the port bindings, the allowlist and the self-check. |
| **What sits behind a leak.** The dashboard is a root shell behind one password with no login throttling (`handleLogin` in `hsctl/ui.go`). Its admin forms have no CSRF token and rely on `SameSite=Lax`, which treats sibling subdomains (and today, sibling ports on the same IP) as the same site. | Add login throttling. Check `Origin` on every admin POST, as `wsOriginOK` already does for the terminal. |
| **The VPN becomes the perimeter.** A lost phone with a WireGuard config is "inside". | One VPN identity per device, with one-click revoke. Vaultwarden's own 2FA still applies. |
| **The local CA is unrestricted and long-lived.** | The documented opt-out path ([Local certificates](#local-certificates)). |
| **Hiding the names is partial.** The certificate logs show `*.<base>`; DuckDNS names resolve publicly; an off-VPN device that tries a name leaks the lookup. | Low risk: the allowlist is the real control. |
| **The lego image joins the chain of trust.** | It's the upstream project's official image, pinned, and checked by `hsctl images` like every other image. |
| **The sandbox restores the real config**, token included. | The sandbox forces `ACME_BASE_DOMAIN` off (or staging) and drops the token. |

One improvement comes for free: today the dashboard's login cookie is sent to every app on the
same IP, because cookies ignore ports. A cookie for `home.<base>` isn't sent to `vault.<base>`.

## Operations

- **Renewals can fail silently.** Causes: an expired or rotated token, a provider outage (DuckDNS
  is free and has occasional outages), a Let's Encrypt outage. Let's Encrypt stopped sending
  expiry emails in 2025, and lifetimes are shrinking toward 45 days by 2028, so the warning
  window drops from about 30 days to about 15. When the certificate expires, every domain URL
  breaks at once; the IP route keeps working. → Expiry tracking and the admin banner now; a
  notification channel (ntfy, email) is the real fix, and the stack has none yet.
- **Renewal depends on the timer, Docker and hsctl.** If hsctl is broken or removed,
  certificates stop renewing. `hsctl certs` shows the last run, so this is visible.
- **More things have to work for a domain request**: VPN up → Pi-hole answering → Caddy serving
  → a valid certificate. Every failure looks the same to the user ("can't connect"), so
  `hsctl certs` checks each step and names the one that failed.
- **Home connectivity prerequisites.** Port-forwarding is impossible behind carrier-grade NAT.
  A changing home IP means the VPN endpoint needs dynamic DNS; with DuckDNS that's a second name,
  since the first points at the VPN address. → Setup checks for a public IPv4 address and warns.
- **Boot order.** On an internet-facing server, Caddy and Pi-hole publish on the VPN address. If
  the VPN interface isn't up when Docker starts them, they don't start. → A systemd ordering rule
  for Docker after the VPN, or `net.ipv4.ip_nonlocal_bind=1`.
- **Pi-hole is required for domain access**, and its restarts and updates briefly interrupt DNS
  for VPN clients, as they already do for the LAN.
- **A Caddy config error takes everything down**, the dashboard included, because one Caddy
  serves all sites. → Validate before every apply, keep the last working config, and document
  the SSH escape hatch: `hsctl certs public disable`.
- **Two addresses per service can confuse people.** The Bitwarden and Nextcloud apps store one
  server address, and switching means logging in again. → The onboarding guide says: the domain
  on the VPN, the IP address as the fallback.
- **Rate limits.** Let's Encrypt allows 5 identical certificates per week. Reinstalls, a
  misconfigured sandbox or a retry loop can hit that. → The staging toggle, the sandbox guard,
  and restores that keep the `acme` volume.
- **Backups** gain the `acme` volume (automatic) and, if the VPN is bundled, WireGuard keys.
  Restoring onto new hardware or after an ISP change means updating the VPN endpoint's DNS.

## Upgrades and migration

- **hsctl and the repo checkout must move together.** The tracked Caddyfile imports files that
  hsctl writes, so a mismatch takes HTTPS down:
  - new checkout + old hsctl → `sites/` is never written; after Caddy's next restart only
    port 80 answers;
  - new hsctl + old checkout → hsctl writes IP sites the old Caddyfile already defines; Caddy
    refuses to start because of the duplicate sites.

  `install.sh` is one-time and there's no upgrade command today. → A layout version marker in the
  Caddyfile that hsctl checks before writing anything, and an `hsctl upgrade` that moves the
  binary and the checkout to the same release, rewrites the site files and recreates Caddy. The
  same command handles rollback.
- **The Caddyfile is a single-file mount.** Git replaces the file on checkout, but the running
  container keeps the old one; even `caddy reload` reads the old content. → Upgrades recreate the
  Caddy container. The generated files live in the `sites/` directory mount, which doesn't have
  this problem.
- **Hand-edited Caddyfiles.** `docs/adding-an-app.md` tells people to edit the tracked
  Caddyfile, and moving the IP blocks out of it would conflict. → The upgrade stops if the
  Caddyfile has local edits and explains why. Custom sites go in `caddy/sites/custom/` from then
  on, and `adding-an-app.md` changes to "add a snippet and a `services.json` entry".
- **The first upgrade must change nothing visible.** → `caddy adapt` the old and the new config
  to JSON and require them to be equal (apart from the allowlist, see below).
- **The allowlist could lock out existing users.** Anyone reaching the server from outside the
  expected subnets would be cut off — Tailscale's `100.64.0.0/10`, or a port-forward someone
  already made. → The first release logs instead of blocking. hsctl detects Tailscale and
  includes its range.
- **Explicit IPv4 publishing** drops IPv6 access to the IP sites. That's rarely used on a LAN,
  but it's called out in the release notes.
- **Changing Vaultwarden's `DOMAIN` logs every device out** (see [Vaultwarden](#vaultwarden)).
  → Announce it, do it once, choose the base name carefully.
- **`hsctl updates` only looks at existing containers** (`updateTargets`), and the lego container
  is removed after each run. → `updates` also checks images that only run on a schedule.
  `hsctl images` already reads pins from the compose files, so it covers lego automatically.

## Testing

- **Unit tests** (no Docker): config round-trip, the provider registry, golden files for each
  rendering mode (local only, local + public, public only), form validation, Pi-hole rule
  generation, tile URLs by request host, the WebSocket origin check, token masking.
- **CI**: `caddy validate` on every rendered mode with the official Caddy image and a throwaway
  self-signed certificate in place of the Let's Encrypt one.
- **Sandbox** (forced to local certificates by default). Things to verify before building on them:
  - Pi-hole 2026.07 applies the per-group `;reply=<IP>` and `;reply=nxdomain` rules as described,
    and AAAA queries for the names get an empty answer rather than an address;
  - Vaultwarden with `DOMAIN` unset: attachments over both routes, and the apps not switching to
    the `/api/config` URLs;
  - `caddy reload --force` picks up a renewed certificate file;
  - the allowlist doesn't block the sandbox's own forwarded ports;
  - nothing listens on IPv6 after the publishing change.
- **End to end without the internet**: Pebble (Let's Encrypt's test CA) and lego's `exec` DNS
  provider pointed at Pebble's test DNS server. **With the internet**: a staging run against a
  real test domain.

## Phases

| Phase | What ships | Visible change |
|---|---|---|
| 0. Groundwork | Caddyfile snippets + generated `local.caddy` (JSON-equal to today), the `sites/` mount, the layout marker, `hsctl upgrade`, explicit IPv4 publishing, the exposure self-check, login throttling, the `Origin` check on admin POSTs | None |
| 1. Access control | `DEPLOYMENT` profiles, the allowlist (log-only at first), Pi-hole binding on internet-facing servers, a safe `install` on internet-facing servers | Internet-facing servers supported |
| 2. Let's Encrypt (CLI) | Config keys, the provider registry, the lego service + `hsctl-certs.timer`, `public.caddy`, the Pi-hole VPN-only rules, the Nextcloud / Vaultwarden / dashboard domain changes, `hsctl setup` flags, `hsctl certs`, the sandbox guard, docs | Domain access from the CLI |
| 3. Dashboard | Wizard section, Admin → Certificates, Command Center cards, the Apps guard, banners | Domain access from the UI |
| 4. VPN and opt-out | Bundled VPN (if chosen), the local-certificate opt-out | — |

## Open questions

| # | Question | Recommendation |
|---|---|---|
| 1 | How hsctl runs ACME: the official lego **container**, or lego compiled into hsctl as a **library**? | The container: less code, and lego updates arrive through `hsctl updates` instead of waiting for an hsctl release. |
| 2 | Which VPN: WireGuard bundled and managed by hsctl (peers, QR codes in the dashboard), or bring-your-own (Tailscale, NetBird, the router's VPN)? | Bundled WireGuard: self-hosted, needs one forwarded port, and doesn't answer unauthenticated packets, so scanners can't see it. The certificate work doesn't depend on the answer. |
| 3 | On internet-facing servers, where do the IP sites listen? | The VPN address or the provider's private network only, never the public IP. |
| 4 | Vaultwarden `DOMAIN`: the IP URL, the domain URL, or unset? | Test "unset" in the sandbox first. If it works, attachments work over both routes. |
| 5 | The base domain (e.g. `lab.example.com`) and the dashboard's subdomain (e.g. `home`). | A dedicated subdomain as the base, so the token and the Pi-hole rules never touch other records. |
| 6 | The local-certificate opt-out: "turning off doesn't delete; purge is separate, with a typed confirmation". | As proposed. |

## Alternatives considered

| Alternative | Why not |
|---|---|
| Let's Encrypt certificates for the IP addresses | Public authorities never certify private addresses. |
| Stock Caddy with the HTTP-01 / TLS-ALPN-01 challenge | Needs publicly resolvable names and port 80 or 443 open to the internet. No wildcards, so every service name lands in the public certificate logs. Conflicts with decisions 4 and 6. |
| A custom Caddy image with DNS plugins, published by this repo | Has to be built for amd64 and arm64, signed, hosted and kept current with every Caddy release. A lagging DNS plugin can block a Caddy security fix. Decision 9. |
| Building Caddy on each server | Slow and memory-hungry on a Raspberry Pi, needs to download Go modules at build time, and every box ends up with its own build. |
| A community Caddy image with plugins | A stranger's build in front of the password manager, and most bundle only Cloudflare. |
| `caddy add-package` | Depends on caddyserver.com's build service, and the downloaded binary is lost whenever the container is recreated. |
| Switching to Traefik (DNS providers built in) | Traefik has no built-in local certificate authority, which decision 2 requires, and the whole proxy layer would need rewriting. |
| A separate CoreDNS container for the VPN names | Superseded by decision 10: Pi-hole's per-group rules do the same with one less container. |
| Public DNS records pointing at the VPN address | Simple, but the names would resolve for everyone. Conflicts with decision 4. |
