# Public access with Let's Encrypt

The stack keeps **two kinds of certificate**, side by side:

| | Local (always on) | Public (optional) |
|--|--|--|
| For | using the apps **at home** | reaching chosen apps **from outside** your home network |
| Address | `https://SERVER_IP:<port>` — the server's LAN IP | `https://<your domain>:<same port>` |
| Certificate | Caddy's own CA (`tls internal`) — Let's Encrypt can't issue for a private IP | **Let's Encrypt**, for `<domain>` and `*.<domain>` |
| Devices | trust `root.crt` once | nothing to install — trusted everywhere |
| Which apps | all of them, plus the dashboard | the ones you pick — **never the dashboard** |

Turning public access on changes nothing at home: the local addresses and their certificate
stay exactly as they are. `hsctl cert` gets the Let's Encrypt certificate, keeps it renewed,
and serves the apps you chose on your domain as well. The certificate's files sit in a plain
folder, so you can **reuse the wildcard for your other services** too.

The main use is **Vaultwarden**: your passwords from anywhere, with the Bitwarden apps and
extensions connecting to a normal, publicly trusted address.

- [What you need](#what-you-need)
- [Set it up](#set-it-up)
- [What's public — and what never is](#whats-public--and-what-never-is)
- [At home and away](#at-home-and-away)
- [Renewal](#renewal)
- [Using the certificate for your other services](#using-the-certificate-for-your-other-services)
- [Turning it off](#turning-it-off)
- [How it works](#how-it-works)
- [Troubleshooting](#troubleshooting)

---

## What you need

1. **A domain (or subdomain) you control**, e.g. `home.example.com`. If your home's public IP
   changes now and then, use a dynamic-DNS name — a free `yourname.duckdns.org` from
   [DuckDNS](https://www.duckdns.org) works.
2. **A DNS record pointing it at your public IP** — the address your router has on the
   internet. You create it at your DNS provider (or let your router's dynamic-DNS client keep
   it current); hsctl never touches your DNS records.
3. **Port forwards on your router**: for each app you make public, forward its port (TCP) to
   the server's LAN IP — e.g. `8443 → SERVER_IP:8443` for Vaultwarden. Nothing else: not `443`
   (the dashboard) and not `80`.
4. **Your DNS provider's API**, and a token for it. hsctl proves you own the domain with the
   **DNS-01 challenge**: lego briefly adds a TXT record (`_acme-challenge.<domain>`) through
   the API. That needs no open port, and it's the only challenge that can issue the wildcard.
   [lego](https://go-acme.github.io/lego/dns/) supports ~180 providers — Cloudflare, DuckDNS,
   Porkbun, deSEC, DigitalOcean, Hetzner, Route 53, OVH, Namecheap, …

## Set it up

### From the dashboard

**Admin → 🌍 Public access**: fill in your domain, tick the apps to reach from outside
(Vaultwarden is pre-ticked), enter the DNS provider and its API credentials, press **Save
settings**, then **Get certificate** and watch it work (a minute or two). The page then lists
each public address and the port to forward for it.

### From a terminal

```bash
hsctl cert config --domain home.example.com --dns cloudflare --public vaultwarden
# on a terminal it asks for the API token (input hidden) — or edit .acme-env yourself
hsctl cert issue        # get the certificate and serve the public apps (safe to repeat)
hsctl cert status       # what's public, the ports to forward, when the certificate expires
```

`--public` takes a comma-separated list of apps (`vaultwarden,nextcloud`); it defaults to
`vaultwarden`.

> [!TIP]
> Let's Encrypt limits how many certificates you can get per week, so for a first dry run
> use its **staging** CA — same flow, but an untrusted test certificate:
> `hsctl cert config --server letsencrypt-staging`, then `hsctl cert issue`. When that
> works: `hsctl cert config --server "" && hsctl cert issue` fetches the real one.

### Your provider's credentials

They go in `.acme-env` in the repo root — one `KEY=value` per line, `chmod 600`,
git-ignored. `hsctl cert config` writes it with your provider's variables listed.

| Provider | `--dns` | What to put in `.acme-env` |
|----------|---------|----------------------------|
| Cloudflare | `cloudflare` | `CF_DNS_API_TOKEN=…` — *My Profile → API Tokens → Create Token → "Edit zone DNS"* template, limited to your zone |
| DuckDNS | `duckdns` | `DUCKDNS_TOKEN=…` — the token on your duckdns.org page; domain `yourname.duckdns.org` |
| Porkbun | `porkbun` | `PORKBUN_API_KEY=…` and `PORKBUN_SECRET_API_KEY=…` (enable API access for the domain) |
| deSEC | `desec` | `DESEC_TOKEN=…` |
| DigitalOcean | `digitalocean` | `DO_AUTH_TOKEN=…` |
| Hetzner | `hetzner` | `HETZNER_API_TOKEN=…` |
| anything else | lego's code | see [lego's provider list](https://go-acme.github.io/lego/dns/) — or the list in `.acme-env` |

Give the token the **least access that works** — DNS edit on this one zone.

## What's public — and what never is

Each app you pick gets a public site at `https://<domain>:<its usual port>`, served by the
same Caddy as its local one. Forward exactly those ports, and nothing else is reachable from
outside:

| App | Port to forward | Make it public? |
|-----|-----------------|-----------------|
| 🔑 Vaultwarden | `8443` | the point of all this — **after** switching off open signups (below) |
| ☁️ Nextcloud | `8444` | if you want your files away from home; turn on 2FA |
| 🛡️ Pi-hole admin | `8445` | not recommended — nobody needs it from outside |
| 📄 🧰 🖼️ Web tools | `8446`–`8448` | if you like; they hold no data |
| 🏠 Dashboard | `443` — **never forward** | **impossible** — it's a root shell on the server, so it has no public site |

Before you open a port to the internet:

- **Vaultwarden: switch off open signups** once your family has accounts, or anyone could
  create one — set `VW_SIGNUPS_ALLOWED=false` in `vaultwarden/.env` and in `setup.conf`, then
  `cd vaultwarden && docker compose up -d`. `hsctl cert issue` reminds you while they're on.
  Turn on two-step login for every account.
- **Nextcloud:** turn on two-factor authentication; its brute-force protection is on by
  default.
- The rest of [security.md](security.md) applies with more force: full-disk encryption, strong
  unique passwords, keeping the apps updated (`hsctl updates`).

## At home and away

- **Browsers at home** keep using the local addresses, `https://SERVER_IP:<port>`, exactly as
  before — the dashboard's tiles still point there, and show each public app's outside address
  as well.
- **Vaultwarden has one official address** — its `DOMAIN`, which passkeys/2FA security keys,
  links and browser security checks follow — so once it's public, its `DOMAIN` is the public
  address: point the Bitwarden apps and extensions at `https://<domain>:8443` (*Settings →
  Self-hosted → Server URL*). A phone then uses that one address wherever it is. At home that
  needs your router to support *NAT loopback* (most do); if yours doesn't, give the domain a
  local DNS entry pointing at the server's LAN IP. A security key or passkey registered for
  2FA against the IP address needs registering again. Make Vaultwarden local-only again and
  its `DOMAIN` returns to the IP.
- **Other apps** have no such single address: use the local one at home and the public one
  away.

## Renewal

Automatic. The dashboard service checks twice a day (the first check ~2 minutes after it
starts, so a server that was off for a while catches up). lego only asks Let's Encrypt for a
new certificate when a third of the current one's lifetime is left — or earlier, if Let's
Encrypt says so ([ARI](https://letsencrypt.org/2024/04/25/guide-to-integrating-ari-into-existing-acme-clients/)) —
and hsctl then reloads Caddy gracefully (no dropped connections). This keeps working as Let's
Encrypt moves to shorter-lived certificates.

If renewals keep failing, the Admin page shows a red warning well before the certificate
expires; `hsctl cert issue` shows why, and `journalctl -u hsctl-ui` has each check's log.
Running without the dashboard service? Schedule `hsctl cert issue` yourself (daily cron) —
it's a no-op until renewal is due.

## Using the certificate for your other services

The certificate covers `*.<domain>`, so any service reached as `something.<domain>` can use
it. The files, in the repo (e.g. `/opt/homeserver`):

| File | What |
|------|------|
| `letsencrypt/certificates/<domain>.crt` | the certificate **with its chain** (what servers call *fullchain*) |
| `letsencrypt/certificates/<domain>.key` | its private key |
| `letsencrypt/certificates/<domain>.issuer.crt` | the intermediate certificate alone |

They belong to the user who owns the repo (not root), key `0600`. For another container on
this machine, bind-mount the folder read-only:

```yaml
    volumes:
      - /opt/homeserver/letsencrypt/certificates:/certs:ro
    # ...and point the service at /certs/home.example.com.crt + /certs/home.example.com.key
```

Renewals replace the files in place, but most servers only read their certificate at start —
restart them now and then (weekly is plenty — certificates are renewed well before they
expire), e.g. a cron `docker restart myservice`. For another machine, copy the two files over
the same way.

## Turning it off

**Admin → Public access → Turn off**, or `hsctl cert off`: nothing is served publicly any
more, Vaultwarden's `DOMAIN` returns to the local address, and renewals stop. At home nothing
changes. Remove the port forwards from your router too. The certificate files are kept, so
switching back on (`hsctl cert config --domain …` + `hsctl cert issue`) is instant.

## How it works

- **lego** is the `lego` service in [`caddy/docker-compose.yml`](../caddy/docker-compose.yml),
  in the `cert` profile so `up` never starts it: `hsctl cert issue` (and the renewal check)
  run it one-shot with `docker compose run`, as the owner of `letsencrypt/`, reading
  `.acme-env`. `lego run` is idempotent — it gets the certificate the first time and after
  that renews only when due.
- **Caddy** keeps the local sites in the [`Caddyfile`](../caddy/Caddyfile) as they always
  were. [`caddy/letsencrypt.caddy`](../caddy/letsencrypt.caddy) defines a public site per app
  (`public_vaultwarden`, …): same port, same per-app snippet as the local site, but the Let's
  Encrypt certificate (mounted read-only at `/letsencrypt`). None is served until
  `caddy/domain.caddy` imports it — one line per public app — which hsctl writes **only once
  the certificate exists**: Caddy refuses to start at all if a certificate file it's told to
  load is missing, which would take the local sites and the dashboard down too. For the same
  reason `hsctl up` and every renewal check remove `domain.caddy` again if its certificate has
  gone missing.
- **Settings** live in `setup.conf` — `DOMAIN`, `PUBLIC_APPS`, `DNS_PROVIDER`, `ACME_SERVER`
  (empty = Let's Encrypt) and `ACME_EMAIL` (optional; the `you@example.com` placeholder is
  never sent). See [configuration.md](configuration.md).
- **Backups** include `letsencrypt/`, `.acme-env` and `caddy/domain.caddy`, alongside the
  other config; `hsctl uninstall` deletes them with the rest of the generated config.
- **The lego version** is pinned like every other image (`LEGO_IMAGE` — override it in
  `caddy/.env`); `hsctl images` checks it covers amd64 and arm64. `hsctl updates` only
  looks at running containers, so it doesn't report newer lego releases.

## Troubleshooting

- **"lego couldn't get the certificate"** — the lines above it are lego's own log. The usual
  suspects: a token without DNS-edit permission on that zone (a 403 from the provider), the
  wrong `--dns` code, or a domain that isn't in the provider account the token belongs to.
- **Rate limits** — Let's Encrypt allows 5 certificates per week for the same names, and 5
  failed validations per hour. Experiment against staging (see the tip above).
- **"… doesn't resolve yet" / "… resolves to a private address"** after `hsctl cert issue` —
  the certificate is fine, but from outside nothing leads to your server yet: point the DNS
  record at your **public** IP.
- **Works from outside, not at home** (or the reverse) — at home, use the local address; for
  the public one to work at home too, see NAT loopback under [At home and away](#at-home-and-away).
- **Reachable at home but not from outside** — check the router forwards that app's port to
  the server's LAN IP, and that your internet provider gives you a public IPv4 address (with
  CGNAT, incoming connections can't reach you at all).
- **"… is a private name"** — `.local`, `.lan`, `.internal`, `.home.arpa` and friends can't
  get a public certificate; use a real domain.
