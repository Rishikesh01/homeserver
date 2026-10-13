# Let's Encrypt certificates — design

**Status: proposed.** Nothing here is implemented yet. This records the decisions made so far,
the architecture they lead to, the risks, and the questions still open. Where a choice is still
open, the architecture below shows the recommended option and the question is listed under
[Open questions](#open-questions).

- [Summary](#summary)
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
certificates. Every service gets a subdomain (`vault.lab.example.com`, …), reachable only over
a VPN. The IP addresses keep working on the home network without a VPN, with the local CA, as
today. hsctl gets the certificate through the DNS challenge using the official lego container.
Caddy stays the official image and only loads the files.

**Non-goals**

- Public, non-VPN access to the apps from the internet.
- Certificates for IP addresses. Public authorities never certify private addresses.
- The HTTP-01 and TLS-ALPN-01 challenges. Both need publicly resolvable names and an open port.
- DNS providers beyond the three below in the first version. lego supports many more; adding one
  is a registry entry plus a test.

## Decisions

| # | Decision | Why |
|---|----------|-----|
| 1 | Let's Encrypt for a user-owned domain, configured from the CLI and the dashboard, at first setup and later. | Trusted certificates without installing a CA on each device. |
| 2 | Local certificates and IP access are **kept** unless the user explicitly opts out. | Existing devices and app configs keep working. |
| 3 | The VPN is needed **only for the domain**. The IP addresses work on the LAN without it. | Daily home use stays as simple as today. |
| 4 | The domain names **resolve only for VPN clients**. | Keep bots away from Vaultwarden and the other apps. |
| 5 | **One subdomain per service**, one wildcard certificate `*.<base>`. The dashboard gets a subdomain too. | One certificate for everything. The public certificate logs show `*.<base>`, not the service names. DuckDNS can hold only one verification record at a time, so a single name is required. |
| 6 | **DNS-01 challenge only.** | The only challenge that works for names that don't resolve publicly, and the only one that allows wildcards. |
| 7 | Providers: **Cloudflare, DuckDNS, deSEC**. | Covers a paid domain and two free options. |
| 8 | Port-forwarding is allowed; **internet-facing (cloud, bare-metal) servers are supported**. | Needed for the VPN, and for hosting outside the home. |
| 9 | Caddy stays the **official image**. Something other than Caddy does the DNS challenge. | Stock Caddy can't talk to DNS providers ([why](#why-caddy-doesnt-do-the-dns-challenge)); no custom image to host. |
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
  │    allow: VPN + ALLOW_SUBNETS (the LAN)   │    allow: VPN subnet only              │
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
          │ 1. docker compose run --rm lego … renew   (official goacme/lego image)
          │       │ 2. write _acme-challenge TXT ──► DNS provider (Cloudflare / DuckDNS / deSEC)
          │       │ 3. order *.lab.example.com   ──► Let's Encrypt
          │       │                                   checks the TXT over public DNS, issues
          │       └─ writes the files to the `acme` volume
          ▼
  4. docker exec caddy caddy reload    Caddy reads the files. It never talks to the DNS
                                       provider and never holds the DNS token.
```

### Internet-facing server (cloud or bare metal)

```
  internet ── only UDP 51820 (WireGuard) is reachable ──►  server, public IP 203.0.113.7
                                                            │
                                                            ├─ wg0 10.8.0.1
                                                            ├─ Caddy published on 10.8.0.1 only
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
| IP sites (local CA) | LAN without VPN, and VPN | VPN, or the provider's private network if added to `ALLOW_SUBNETS` |
| Domain sites (Let's Encrypt) | VPN only | VPN only |
| Pi-hole DNS | LAN IP and VPN address | VPN address only |
| Reachable from the internet | The VPN port (router forward) | The VPN port |

---

## Components

### Certificates: hsctl + lego

**What runs.** The official `goacme/lego` image (the ACME client Traefik is built on). It already
supports Cloudflare, DuckDNS and deSEC. It's pinned like every other image here
(`image: ${LEGO_IMAGE:-goacme/lego:<version>}`). It's defined as a service in
`caddy/docker-compose.yml` under a compose profile, so `docker compose up` never starts it:

```bash
docker compose --profile certs run --rm lego \
  --path /data --accept-tos --dns cloudflare \
  --dns.resolvers 1.1.1.1:53 --dns.resolvers 9.9.9.9:53 \
  --domains '*.lab.example.com' [--email me@mydomain.com] [--server <staging URL>] \
  run                         # first issue
  # or: renew --no-random-sleep   (renews only when due; also follows Let's Encrypt's renewal hints)
```

- **`--dns.resolvers` is required.** Pi-hole answers NXDOMAIN for the service names to anything
  that isn't a VPN client. lego's propagation check must use public resolvers, or our own rule
  could answer it.
- **The token is only loaded by the lego service**, from its own credentials file (see
  [Configuration and secrets](#configuration-and-secrets)). It never enters Caddy's container.
- **Where the files go.** lego writes the account and the certificate to a named volume, `acme`,
  in the `caddy` compose project. Volume discovery puts it in every backup, so a restore doesn't
  request a new certificate. A wildcard certificate is saved as `_.lab.example.com.crt` / `.key`.

**Renewal.** A `hsctl-certs.timer` (daily, `RandomizedDelaySec`, `Persistent=true`) runs
`hsctl certs renew` as a oneshot service:

1. `lego … renew --no-random-sleep`. lego's own random delay of up to 8 minutes isn't needed,
   because systemd already spreads the start time. Without it, "Renew now" in the dashboard
   doesn't hang for minutes.
2. Then `caddy reload`. It's graceful, so reloading on every successful run is simpler than
   working out whether the files changed.
3. Then [reconcile](#reconcile-one-writer-for-derived-state).

`hsctl install` writes the timer and its service the same way it writes the dashboard unit
(`cmdInstall`), and `uninstall` removes them through `uninstallUnits`. That way a `curl | sh`
install gets renewals too; the backup timer, by contrast, only arrives with
`make install-services`.

**How Caddy uses the files.** The domain site loads them with `tls <cert> <key>`. Caddy's
documentation says manually loaded certificates go into its shared cache, and Caddy won't manage
certificates for the names they cover itself. So Caddy never attempts its own ACME for the domain.
The domain site is only written once the certificate exists: a site that points at a missing
file is a config error, and a config error stops all of Caddy.

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

- **A directory mount (`./conf:/etc/caddy:ro`) rather than today's single-file mount.** Git and
  hsctl both replace files by renaming them. A single-file mount keeps showing the container the
  old file; a directory mount shows the new one.
- **A subdirectory, not all of `caddy/`.** `caddy/.env` and the credentials file stay out of the
  container.

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
	@outside not remote_ip 10.8.0.0/24 192.168.1.0/24   # VPN + ALLOW_SUBNETS
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
	# … one pair per services.json entry, using its `key` as the subdomain
	handle {
		abort                                            # any other name: drop the connection
	}
}
```

Rules for the allowlist and the ports:

- **List subnets explicitly.** Never use Caddy's `private_ranges` shortcut. It includes
  `172.16.0.0/12`, where Docker's own addresses live.
- **Publish Caddy's ports on an explicit IPv4 address**: `"${CADDY_BIND}:443:443"`, following the
  existing `PIHOLE_DNS_BIND` convention. Never use the bare `"443:443"`. The bare form also listens
  on IPv6. With Docker's defaults, if the container has no IPv6, Docker relays IPv6 connections
  and they arrive from a `172.x` address, which slips past an allowlist built on private ranges.
- **Requests from the server itself** can arrive from a Docker address and be refused at the HTTP
  layer. hsctl's own checks only need the TLS handshake, which still completes.

### Name resolution: Pi-hole, VPN clients only

[Reconcile](#reconcile-one-writer-for-derived-state) configures Pi-hole through its API, using the
admin password hsctl already generates:

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
   otherwise, so without this, VPN clients would lose ad-blocking. Because reconcile runs daily
   and on every change, adlists the admin adds later are picked up too.

The names come from the `services.json` keys plus `home`. Only the service names are covered, so a
base domain that also holds real public records keeps working for everyone.

This **fails closed**. Pi-hole puts any client it doesn't recognise in `Default` — new LAN
devices, IPv6 queries, Docker relay addresses — so they get NXDOMAIN. Only the VPN subnet gets an
answer. Pi-hole's regex extensions (`;reply=<IP>`, `;reply=nxdomain`) and the `Default` group
behaviour are documented upstream.

Requirements and consequences:

- **Pi-hole must see each VPN client's real address.** WireGuard on the host preserves it, and
  Docker's port forwarding preserves it over IPv4. A VPN that rewrites client addresses to its
  own would make every VPN client look like one address.
- **The existing `FTLCONF_dns_listeningMode: ALL` is needed,** because queries arrive from a
  subnet that isn't the container's own.
- **VPN clients use the server's VPN address as their DNS server.** With step 4, that also gives
  them ad-blocking.
- **Pi-hole is required while domain access is on.** This is a rule in `Config.Validate()` (see
  [Reconcile](#reconcile-one-writer-for-derived-state)), so it holds for every path that changes
  `DISABLED_APPS`: the Apps page, `hsctl apps`, the wizard and `setup --disable-apps`. Pi-hole
  restarts and updates briefly interrupt DNS for VPN clients, as they already do for the LAN.
- **DNS is not what keeps the LAN out; the Caddy allowlist is.** If the Pi-hole rules are edited by
  hand and LAN devices start resolving the names, they still can't connect without the VPN.
- **DuckDNS names resolve publicly** whatever Pi-hole does. Point the DuckDNS record at the VPN
  address so the public answer leads nowhere.

### The VPN

Which VPN is [open question 2](#open-questions). The certificate work only needs:

- the server's VPN address with its prefix (`VPN_ADDR=10.8.0.1/24`);
- clients configured to use that address as their DNS server;
- client addresses not rewritten (no masquerading);
- one UDP port forwarded on the router (home) or open in the provider's firewall (internet-facing).

Peers, QR codes and the rest of VPN management are a separate feature.

### Apps that need to know the domain

| App / piece | Change |
|---|---|
| `services.json` | No new field: each entry's existing `key` (`vault`, `cloud`, `pihole`, `pdf`, `tools`, `image`) is its subdomain. `home` is reserved for the dashboard. |
| Dashboard URLs and origins | One helper works out the request's base URL and the allowed origins (`https://<ip>`, `https://home.<base>`) from the config. Everything uses it: the tiles (`Service.URL` takes a base URL instead of an IP, so domain visitors get domain links), `/help`, the terminal WebSocket check (`wsOriginOK`) and the new `Origin` check on admin POSTs. |
| `/help` page | The certificate-install section of `ONBOARDING.md` is shown to IP visitors only. Domain visitors are told nothing needs installing. |
| Caddy's "app isn't running" page (`app_offline`) | Links back to `https://<ip>/` today; on domain sites it links to `https://home.<base>/`. |
| Nextcloud | `NC_TRUSTED_DOMAINS` is only applied on first install, so reconcile adds `cloud.<base>` with `occ config:system:set trusted_domains N` and keeps the IP entry. |
| Vaultwarden | See below. |
| `WELCOME.txt`, `hsctl install` / `hsctl ui` output | Show the domain URLs too. |

#### Vaultwarden

Vaultwarden has one `DOMAIN` setting. From the source of the pinned version (1.37.1):

- **Download links come from `DOMAIN`.** With `DOMAIN` set, attachment and file-Send links are built
  from it, not from the address the client used (`src/auth.rs`, the `Host` request guard;
  `src/db/models/attachment.rs`, `get_url`).
- **Passkey and security-key 2FA (WebAuthn) use `DOMAIN` as the relying-party ID**
  (`src/api/core/two_factor/webauthn.rs`). Browsers never allow WebAuthn on a bare IP.
- **Login and refresh tokens are checked against an issuer built from `DOMAIN`** (`src/auth.rs`,
  `JWT_LOGIN_ISSUER`, `decode_refresh`). **Changing `DOMAIN` logs every device out.** So do
  "remember this device" 2FA tokens and pending invite and emergency-access links.
- **`/api/config` advertises `DOMAIN` URLs to the apps** (`src/api/core/mod.rs`).

| `DOMAIN` | IP route (LAN, no VPN) | Domain route (VPN) | Devices logged out? |
|---|---|---|---|
| The IP URL (today) | Everything works | Attachment links point at the IP, which needs the local CA trusted | No |
| The domain URL | Attachment and file-Send downloads fail (the link points at a name that only resolves on the VPN) | Everything, plus passkey / security-key 2FA | Once |
| Unset | Links follow whichever address was used | Same | Once |

"Unset" is the only option where attachments work on both routes, and it loses nothing compared
with today. It's still unproven, for two reasons:

- Vaultwarden falls back to `http://localhost` in what it advertises to the apps and in its
  WebAuthn settings.
- Browser requests may produce a malformed attachment link, because the `Referer` header is used
  as the base URL.

It's tested in the sandbox before anything is chosen ([open question 4](#open-questions)).

### Local certificates

Kept by construction:

- `local.caddy` is written whenever `TLS_LOCAL=on`, which is the default.
- No Let's Encrypt operation touches `local.caddy`, the `:80` CA-download page,
  `caddy-root-ca.crt`, or the CA in the `caddy-data` volume. Neither do `setup --force`, `down`,
  or `uninstall` without `--data`.

Opting out (proposed, [open question 6](#open-questions)):

- **`hsctl certs local disable` stops serving the IP sites.** It's allowed only while the Let's
  Encrypt certificate is valid, so nobody locks themselves out. The CA stays on disk, so turning
  local back on restores the same CA, and devices that already trust it keep working.
- **`hsctl certs local purge` is the only thing that deletes the CA,** behind a typed confirmation.
- **The wizard never offers the opt-out.** The admin is connected over the IP and the local
  certificate at that point.

The local root CA lasts for years and isn't restricted to our names: a device that trusts it
would accept a certificate it signs for any site. Once every device uses the domain, opting out
— and removing the CA from the devices — is the safer end state. The docs should say so.

---

## Configuration and secrets

`setup.conf` (settings, not secrets):

| Key | Meaning | Default |
|---|---|---|
| `DEPLOYMENT` | `home` or `internet` | `home` |
| `TLS_LOCAL` | Serve the IP sites with the local CA | `on` |
| `ACME_BASE_DOMAIN` | e.g. `lab.example.com`; empty means Let's Encrypt is off | empty |
| `ACME_DNS_PROVIDER` | `cloudflare`, `duckdns` or `desec` | — |
| `ACME_EMAIL` | *Existing key.* Optional contact. Must not be at `example.com`, which Let's Encrypt rejects. A migration blanks a saved `you@example.com` | blank (was `you@example.com`) |
| `ACME_STAGING` | Use Let's Encrypt's staging server | `off` |
| `VPN_ADDR` | The server's VPN address with prefix, e.g. `10.8.0.1/24` (the form WireGuard's `Address` uses); the VPN subnet follows from it | — |
| `ALLOW_SUBNETS` | Subnets besides the VPN allowed on the IP sites: the LAN, a provider private network, Tailscale's `100.64.0.0/10`, … | the LAN subnet (home), empty (internet) |
| `PIHOLE_DNS_BIND` | *Existing key.* `defaultDNSBind` returns the VPN address on internet-facing servers | today's rule (home) |
| `CADDY_BIND` | IPv4 address Caddy's ports are published on | `0.0.0.0` (home), the VPN address (internet) |

**The DNS token** lives in its own credentials file, `caddy/.acme-env`, like backup's
`.backup-env`:

- **Format:** provider variable names as lego expects them (`CF_DNS_API_TOKEN=…`,
  `DUCKDNS_TOKEN=…` or `DESEC_TOKEN=…`).
- **Who loads it:** only the lego service, via `env_file` with `required: false`. Caddy never does.
- **Permissions:** `0600` and gitignored; hsctl writes it with `writeFile0600`.
- **Not touched by `Generate`,** so `setup --force` can't wipe it.
- **Lifecycle:** added to the config files a backup includes and to the files `uninstall` removes.
  The sandbox deletes it.
- **Masked** in `hsctl secrets show`, the dashboard and the logs.

A provider registry in Go (key, display name, lego `--dns` name, credential variable, help link)
drives the CLI prompts, the wizard and the admin form.

## Reconcile: one writer for derived state

Everything derived from `setup.conf` and `services.json` is written by one idempotent function,
`reconcile`, and by nothing else:

- the site files;
- the Pi-hole group, rules and adlist assignments;
- Nextcloud's trusted domain;
- Vaultwarden's `DOMAIN` (per [open question 4](#open-questions));
- the dashboard's allowed origins.

Callers:

- `hsctl up`, `hsctl setup` and the wizard;
- the Apps toggle;
- `hsctl certs public enable|disable`;
- `hsctl upgrade`, restore, and the daily timer.

"Enable Let's Encrypt" is just "save the config, then reconcile", and so is "disable". So generated
state can't drift from its inputs, whichever path changed them.

1. **`Config.Validate()`**, also run by `Config.Save`. Its cross-field rules:
   - the base domain is a real hostname (not an IP, not a wildcard);
   - the provider is known and its credentials file is present;
   - the email, if given, is valid and isn't at `example.com`;
   - `VPN_ADDR` is set;
   - Pi-hole isn't in `DISABLED_APPS` while domain access is on.
2. **Certificate.** If `ACME_BASE_DOMAIN` is set and the `acme` volume has no valid certificate for
   `*.<base>`, run `lego … run`. Otherwise do nothing; renewal is the timer's job. That way a
   re-apply (new token, wizard retry) doesn't order a duplicate certificate. If issuance fails,
   stop and report; Caddy hasn't been touched.
3. **Render the site files**, writing only those whose content changed (atomically, with
   `writeFileAtomic`).
4. **If a file changed, run `caddy reload`.** A failed reload leaves Caddy on its old config. So on
   failure reconcile puts the previous files back; otherwise the bad files would stop Caddy at its
   next restart.
5. **Pi-hole, Nextcloud and Vaultwarden** are independent: they run concurrently, and each only
   acts when its state differs.
6. **Record the results** in the state file ([Status and monitoring](#status-and-monitoring)).

## CLI

**First setup**

```bash
hsctl setup --base-domain lab.example.com --dns-provider cloudflare \
            --dns-token-file ~/cf.token --vpn-addr 10.8.0.1/24 \
            [--acme-email me@mydomain.com] [--acme-staging] [--deployment internet]
hsctl up
```

- **The token comes from a file, stdin or `HSCTL_DNS_TOKEN`,** never a flag value, which would
  show up in `ps` output and shell history.
- **Interactive `hsctl setup` asks one extra question:** "Use your own domain with Let's Encrypt?
  [y/N]". The token is read without echo.
- **`sudo hsctl install` still brings up local HTTPS only.** On an internet-facing server it opens
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

- **Step 1** gets a collapsed "Use your own domain (optional)" section: base domain, provider,
  token (write-only), optional email, staging, and the VPN address if it can't be detected.
- **Step 3** starts the lego run alongside "Start everything", because lego doesn't depend on the
  apps. Only the site write and reload wait for both. The step then shows the certificate result.
- **Step 4 adapts.** When the certificate was issued, it shows the domain URLs ("nothing to
  install on VPN devices"). The `root.crt` download stays available for the IP route.
- **The wizard never blocks on Let's Encrypt.** A failure is reported and fixed later from
  Admin → Certificates.

**After setup**

- **Admin → Certificates** (`/admin/certs`) with two cards:
  - *This network (local)*: status, CA fingerprint, `root.crt`, and a danger zone (turn off,
    delete the CA).
  - *Public (Let's Encrypt)*: the form, status (issuer, expiry, last renewal, last error), Renew
    now, Turn off.
- **Command Center cards**, following its rule that every CLI command has a card:
  - "Certificate status" (read-only);
  - "Renew certificate".

  The enable form holds a secret, so it lives on its own page, like other interactive actions.
- **Admin → Apps** explains why Pi-hole can't be switched off while domain access is on (the rule
  itself is in `Config.Validate()`).
- **A banner on the admin page** for: certificate expiring within 14 days, last renewal failed, or
  an exposure problem.
- **Long-running work uses one job runner** built on `runHsctl`. It replaces the two hand-written
  copies today (`setupRunStatus`, `restoreStatus`), and the certificate step joins the existing
  setup job. Applying changes reloads Caddy, which fronts the dashboard, so the page polls the job
  and survives the drop.

## Status and monitoring

- **A small state file, `caddy/.certs-state.json`,** written by reconcile, `certs renew` and the
  status checks. It holds:
  - the certificate's issuer and expiry;
  - the last lego run (time, exit status, error tail);
  - the last check results.
- **The admin banner only reads that file,** the way the backup freshness tile reads
  `backups/.last-backup` (`backupFreshnessFor`). No Docker, network or TLS work on page loads.
- **`hsctl certs` and the Certificates page show the last result.** "Check again" runs the live
  checks concurrently under one deadline, with a single Pi-hole API session, then caches the
  result, as the Updates page does. The checks:
  - **the TLS probe:** a handshake to the VPN address with `ServerName=home.<base>`, verified
    against the system roots;
  - **the Pi-hole rules:** present, and the adlist assignment as reconcile left it;
  - **public DNS:** NXDOMAIN for the service names (DuckDNS excepted);
  - **the exposure self-check:** every listening socket on a non-private address, IPv4 and IPv6;
    only the VPN port is allowed.

## Security

| Risk | Mitigation |
|---|---|
| **The DNS token can rewrite the whole zone**, not just the homeserver's names, and it's in every backup, including the off-site copy. | Use a domain dedicated to the homeserver, or point `_acme-challenge.<base>` at a throwaway zone with a CNAME (lego follows CNAMEs by default). Scope tokens: Cloudflare `DNS:Edit` + `Zone:Read` on one zone; deSEC token policies. `uninstall` reminds the user to revoke the token at the provider; deleting the file doesn't revoke it. |
| **Exposure is one misconfiguration away**: IPv6 relay addresses slipping past the allowlist, and Docker-published ports bypassing ufw. | The publishing and allowlist rules in [Caddy](#caddy-local-sites-and-domain-sites), plus the exposure self-check. Don't rely on ufw. |
| **What sits behind a leak.** The dashboard is a root shell behind one password with no login throttling (`handleLogin` in `hsctl/ui.go`). Its admin forms have no CSRF token and rely on `SameSite=Lax`, which treats sibling subdomains (and today, sibling ports on the same IP) as the same site. | Add login throttling. Check `Origin` on every admin POST using the shared origins helper ([Apps](#apps-that-need-to-know-the-domain)). |
| **The VPN becomes the perimeter.** A lost phone with a WireGuard config is "inside". | One VPN identity per device, with one-click revoke. Vaultwarden's own 2FA still applies. |
| **The local CA is unrestricted and long-lived.** | The documented opt-out path ([Local certificates](#local-certificates)). |
| **Hiding the names is only partial**: the certificate logs show `*.<base>`, DuckDNS names resolve publicly, and an off-VPN device that tries a name leaks the lookup. | Low risk; the allowlist is the real control. |
| **The lego image joins the chain of trust.** | It's the upstream project's official image, pinned, and checked by `hsctl images` like every other image. |
| **The sandbox restores the real config.** | The sandbox deletes `caddy/.acme-env` and clears `ACME_BASE_DOMAIN`, so it can't use the real token or order certificates. |

One improvement comes for free: today the dashboard's login cookie is sent to every app on the
same IP, because cookies ignore ports. A cookie for `home.<base>` isn't sent to `vault.<base>`.

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
- **More things have to work for a domain request**: VPN up → Pi-hole answering → Caddy serving →
  a valid certificate. Every failure looks the same to the user ("can't connect"), so
  `hsctl certs` checks each step and names the one that failed.
- **Home connectivity prerequisites.** Port-forwarding is impossible behind carrier-grade NAT. A
  changing home IP means the VPN endpoint needs dynamic DNS; with DuckDNS that takes a second name,
  since the first points at the VPN address. Setup checks for a public IPv4 address and warns.
- **Boot order.** On an internet-facing server, Caddy and Pi-hole publish on the VPN address. If
  the VPN interface isn't up when Docker starts them, they don't start. Fix it with a systemd
  ordering rule for Docker after the VPN, or `net.ipv4.ip_nonlocal_bind=1`.
- **A Caddy config error takes everything down**, the dashboard included. Reconcile's reload
  fallback keeps bad files off disk. The SSH escape hatch is `hsctl certs public disable`.
- **Two addresses per service can confuse people.** The Bitwarden and Nextcloud apps store one
  server address, and switching means logging in again. The onboarding guide says: the domain on
  the VPN, the IP address as the fallback.
- **Rate limits.** Let's Encrypt allows 5 identical certificates per week. The protections:
  - the staging toggle;
  - reconcile only ordering when no valid certificate exists;
  - the sandbox guard;
  - restores that keep the `acme` volume.
- **Backups** gain the `acme` volume (automatic), the credentials file and, if the VPN is bundled,
  WireGuard keys. Restoring onto new hardware or after an ISP change means updating the VPN
  endpoint's DNS.

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
    - moving the Caddyfile into `caddy/conf/` (recreating Caddy once, for the new mount);
    - splitting out `local.caddy`;
    - blanking a saved `ACME_EMAIL=you@example.com`.
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
  Announce it, do it once, and choose the base name carefully.
- **`hsctl updates` builds its target list from the compose pins** (`composePins`, as
  `hsctl images` does) instead of from existing containers (`updateTargets`). That covers lego,
  whose container only exists while it runs. It also covers disabled apps, which today's
  container-based list already misses.

## Testing

- **Unit tests** (no Docker): config round-trip and `Config.Validate()` rules, the provider
  registry, golden files for each rendering mode (local only, local + public, public only), form
  validation, Pi-hole rule generation, the origins helper, token masking.
- **CI**: `caddy validate` on every rendered mode with the official Caddy image, and a throwaway
  self-signed certificate in place of the Let's Encrypt one.
- **Sandbox** (forced to local certificates by default). Things to verify before building on them:
  - **Pi-hole 2026.07:** it applies the per-group `;reply=<IP>` and `;reply=nxdomain` rules as
    described, and AAAA queries for the names get an empty answer rather than an address.
  - **A Pi-hole setup without adlist syncing:**
    - keep the VPN client in `Default`;
    - put the names in `custom.list`;
    - add a group-scoped *allowlist* entry for `homeserver-vpn` that overrides the `Default`
      NXDOMAIN rule.

    If Pi-hole evaluates the rules before its local records, this works with nothing to re-sync.
  - **Vaultwarden with `DOMAIN` unset:** attachments work over both routes, and the apps don't
    switch to the `/api/config` URLs.
  - **Caddy reload:** `caddy reload` picks up a renewed certificate file, and a failed reload leaves
    the old config running.
  - **Sandbox ports:** the allowlist doesn't block the sandbox's own forwarded ports.
  - **IPv6:** nothing listens on IPv6 after the publishing change.
- **End to end without the internet:** Pebble (Let's Encrypt's test CA) and lego's `exec` DNS
  provider pointed at Pebble's test DNS server. **With the internet:** a staging run against a real
  test domain.

## Phases

| Phase | What ships | Visible change |
|---|---|---|
| 0. Groundwork | The `caddy/conf/` layout with snippets and a generated `local.caddy` (JSON-equal to today), the layout version and ordered migrations, `hsctl upgrade`, reconcile (local sites only at first), explicit IPv4 publishing, the exposure self-check, login throttling, the shared origins helper and admin-POST `Origin` check, the shared job runner | None |
| 1. Access control | `DEPLOYMENT` profiles, `ALLOW_SUBNETS` and the allowlist (log-only at first), the bind defaults for internet-facing servers, a safe `install` on internet-facing servers, `updates` targets from compose pins | Internet-facing servers supported |
| 2. Let's Encrypt (CLI) | Config keys and `Config.Validate()`, the provider registry and credentials file, the lego service + `hsctl-certs.timer`, `public.caddy`, the Pi-hole VPN-only rules, the Nextcloud / Vaultwarden domain changes, `hsctl setup` flags, `hsctl certs`, the state file, the sandbox guard, docs | Domain access from the CLI |
| 3. Dashboard | Wizard section, Admin → Certificates, Command Center cards, banners | Domain access from the UI |
| 4. VPN and opt-out | Bundled VPN (if chosen), the local-certificate opt-out | — |

## Open questions

| # | Question | Recommendation |
|---|---|---|
| 1 | How hsctl runs ACME: the official lego **container**, or lego compiled into hsctl as a **library**? | The container: less code, and lego updates arrive through `hsctl updates` instead of waiting for an hsctl release. |
| 2 | Which VPN: WireGuard bundled and managed by hsctl (peers, QR codes in the dashboard), or bring-your-own (Tailscale, NetBird, the router's VPN)? | Bundled WireGuard: self-hosted, needs one forwarded port, and doesn't answer unauthenticated packets, so scanners can't see it. The certificate work doesn't depend on the answer. |
| 3 | On internet-facing servers, where do the IP sites listen? | The VPN address, or the provider's private network via `ALLOW_SUBNETS`; never the public IP. |
| 4 | Vaultwarden `DOMAIN`: the IP URL, the domain URL, or unset? | Test "unset" in the sandbox first. If it works, attachments work over both routes. |
| 5 | The base domain (e.g. `lab.example.com`), and whether `home` is the right name for the dashboard. | A dedicated subdomain as the base, so the token and the Pi-hole rules never touch other records. |
| 6 | The local-certificate opt-out: "turning off doesn't delete; purge is separate, with a typed confirmation". | As proposed. |

## Alternatives considered

| Alternative | Why not |
|---|---|
| Let's Encrypt certificates for the IP addresses | Public authorities never certify private addresses. |
| Stock Caddy with the HTTP-01 / TLS-ALPN-01 challenge | Needs publicly resolvable names and port 80 or 443 open to the internet. No wildcards, so every service name lands in the public certificate logs. Conflicts with decisions 4 and 6. |
| A custom Caddy image with DNS plugins, published by this repo | See [why Caddy doesn't do the DNS challenge](#why-caddy-doesnt-do-the-dns-challenge). A lagging DNS plugin can also block a Caddy security fix. |
| Building Caddy on each server | Slow and memory-hungry on a Raspberry Pi, needs to download Go modules at build time, and every box ends up with its own build. |
| A community Caddy image with plugins | A stranger's build in front of the password manager, and most bundle only Cloudflare. |
| `caddy add-package` | Depends on caddyserver.com's build service, and the downloaded binary is lost whenever the container is recreated. |
| Switching to Traefik (DNS providers built in) | Traefik has no built-in local certificate authority, which decision 2 requires, and the whole proxy layer would need rewriting. |
| A separate CoreDNS container for the VPN names | Superseded by decision 10: Pi-hole's per-group rules do the same with one less container. |
| Public DNS records pointing at the VPN address | Simple, but the names would resolve for everyone. Conflicts with decision 4. |
| One static Pi-hole record for everyone, with the Caddy allowlist as the only control | Removes the per-group rules, but the names would resolve on the LAN too. Conflicts with decision 4. |
