# Your own domain + Let's Encrypt

Out of the box every app is served at `https://SERVER_IP:<port>` with a certificate from
Caddy's own private CA, which every phone and laptop has to trust by hand (`root.crt`). If
you own a domain, `hsctl cert` gets a real **Let's Encrypt** certificate for it instead:

- every app is then **also** served at `https://<your domain>:<same port>` — trusted by every
  browser, phone and app out of the box, with nothing to install on any device;
- the certificate covers `<domain>` **and** `*.<domain>`, and its files sit in a plain folder
  you can **reuse for your other services**;
- it renews itself; the IP addresses keep working exactly as before.

The main win is **Vaultwarden**: the Bitwarden apps and browser extensions connect without
the per-device certificate dance.

- [What you need](#what-you-need)
- [Set it up](#set-it-up)
- [What changes when it's on](#what-changes-when-its-on)
- [Renewal](#renewal)
- [Using the certificate for your other services](#using-the-certificate-for-your-other-services)
- [Turning it off](#turning-it-off)
- [How it works](#how-it-works)
- [Troubleshooting](#troubleshooting)

---

## What you need

1. **A domain (or subdomain) you control**, e.g. `home.example.com`. A free
   `yourname.duckdns.org` from [DuckDNS](https://www.duckdns.org) works too.
2. **Its DNS hosted somewhere with an API**, and an API token for it. hsctl uses
   [lego](https://go-acme.github.io/lego/dns/), which supports ~180 providers — Cloudflare,
   DuckDNS, Porkbun, deSEC, DigitalOcean, Hetzner, Route 53, OVH, Namecheap, …
3. **A DNS record pointing the domain at this server** — usually an `A` record to its LAN IP
   (e.g. `home.example.com → 192.168.1.10`), or to your public address if you forward the
   ports. You create it at your DNS provider; hsctl never touches your DNS records.

No port forwarding is needed to get the certificate: it's issued via the **DNS-01
challenge** — lego proves you control the domain by briefly adding a TXT record
(`_acme-challenge.<domain>`) through your provider's API. Nothing on the internet has to
reach this machine, and it's the only challenge that can issue the wildcard.

## Set it up

### From the dashboard

**Admin → 🔒 Domain & HTTPS**: fill in your domain, the DNS provider and its API
credentials, press **Save settings**, then **Get certificate** and watch it work (a minute
or two). That's it.

### From a terminal

```bash
hsctl cert config --domain home.example.com --dns cloudflare
# on a terminal it asks for the API token (input hidden) — or edit .acme-env yourself
hsctl cert issue        # get the certificate and serve the domain (safe to repeat)
hsctl cert status       # what it covers and when it expires
```

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

Give the token the **least access that works** — DNS edit on this one zone. It's the only
secret in the stack that reaches outside your LAN.

## What changes when it's on

| | IP only (default) | With your domain |
|--|--|--|
| Addresses | `https://SERVER_IP:<port>` | `https://<domain>:<port>` **and** `https://SERVER_IP:<port>` |
| Devices need `root.crt` | yes | only for the IP addresses |
| Dashboard tiles, setup guide | link to the IP | link to the domain |
| Vaultwarden `DOMAIN` | `https://SERVER_IP:8443` | `https://<domain>:8443` |
| Nextcloud trusted domains | the IP | the IP + the domain |

**Vaultwarden has one official address** — its `DOMAIN`, which passkeys/2FA security keys,
links and browser security checks all follow — so it moves to the domain. Point every
Bitwarden app and extension at `https://<domain>:8443` (*Settings → Self-hosted → Server
URL*); the IP address may stop working for Vaultwarden. If you'd registered a **security key
or passkey for 2FA** against the IP address, register it again on the domain.

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
restart them now and then (weekly is plenty — certificates are renewed well before they expire), e.g.
a cron `docker restart myservice`. For another machine, copy the two files over the same way.

## Turning it off

**Admin → Domain & HTTPS → Turn off**, or `hsctl cert off`: Caddy goes back to the IP
addresses only, Vaultwarden's `DOMAIN` returns to the IP, and renewals stop. The certificate
files are kept, so switching back on (`hsctl cert config --domain …` + `hsctl cert issue`)
is instant.

## How it works

- **lego** is the `lego` service in [`caddy/docker-compose.yml`](../caddy/docker-compose.yml),
  in the `cert` profile so `up` never starts it: `hsctl cert issue` (and the renewal check)
  run it one-shot with `docker compose run`, as the owner of `letsencrypt/`, reading
  `.acme-env`. `lego run` is idempotent — it gets the certificate the first time and after
  that renews only when due.
- **Caddy** serves the domain from [`caddy/letsencrypt.caddy`](../caddy/letsencrypt.caddy): a
  twin of every IP site, on the same port, importing the same per-app snippet but loading
  the Let's Encrypt certificate (mounted read-only at `/letsencrypt`). The Caddyfile imports
  it through `caddy/domain.caddy`, which hsctl writes **only once the certificate exists**:
  Caddy refuses to start at all if a certificate file it's told to load is missing, which
  would take the IP sites and the dashboard down too. For the same reason `hsctl up` and every
  renewal check remove `domain.caddy` again if its certificate has gone missing.
- **Settings** live in `setup.conf` — `DOMAIN`, `DNS_PROVIDER`, `ACME_SERVER` (empty = Let's
  Encrypt) and `ACME_EMAIL` (optional; the `you@example.com` placeholder is never sent).
  See [configuration.md](configuration.md).
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
- **"… doesn't resolve yet"** after `hsctl cert issue` — the certificate is fine, but
  nothing points the name at the server yet: add the DNS record. Some routers' *DNS rebinding
  protection* drops public DNS answers that point at LAN addresses; if the name resolves
  elsewhere but not at home, allow it in the router.
- **The browser still warns** — you're on an IP address (`https://192.168.…`); use the domain.
- **"… is a private name"** — `.local`, `.lan`, `.internal`, `.home.arpa` and friends can't
  get a public certificate; use a real domain.
