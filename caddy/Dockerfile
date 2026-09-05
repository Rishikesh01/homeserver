# Caddy + one DNS-provider plugin, so Let's Encrypt can be used via the DNS-01 challenge —
# i.e. WITHOUT forwarding any port from the internet, which keeps the stack LAN-only.
#
# hsctl builds this only when you enable Let's Encrypt with the DNS challenge
# (`hsctl letsencrypt enable --dns <provider>` or the dashboard's HTTPS page); with the default
# private-CA setup, or the HTTP challenge, the stock caddy image in docker-compose.yml is used
# and this file is never touched. Any github.com/caddy-dns/<name> module works as the provider
# (cloudflare, duckdns, hetzner, desec, digitalocean, gandi, ...).
#
# Keep CADDY_VERSION in step with the `image:` tag in docker-compose.yml. Don't go below 2.10:
# current caddy-dns modules pull in newer dependencies than Caddy 2.8/2.9 compile against, so
# `xcaddy build` fails there (undefined zapslog.HandlerOptions) — that's why the stack is on 2.11.
ARG CADDY_VERSION=2.11
FROM caddy:${CADDY_VERSION}-builder-alpine AS builder
ARG CADDY_DNS_PROVIDER=cloudflare
RUN xcaddy build --with github.com/caddy-dns/${CADDY_DNS_PROVIDER}

FROM caddy:${CADDY_VERSION}-alpine
COPY --from=builder /usr/bin/caddy /usr/bin/caddy
