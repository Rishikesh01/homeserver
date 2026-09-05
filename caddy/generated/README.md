# caddy/generated — hsctl's Let's Encrypt config

In Let's Encrypt mode hsctl writes a complete `Caddyfile` here (`https://<app>.<domain>` per app
on `:443` with publicly trusted certificates, `http://` → `https://` redirects, an info page on
`:80`) and points Caddy at it with `CADDY_CONFIG=/etc/caddy/generated/Caddyfile` in `../.env`.
In private-CA mode the file is removed and Caddy loads [`../Caddyfile`](../Caddyfile) as usual.

It is rewritten on every `hsctl up`, `hsctl letsencrypt …` and dashboard apply, and git-ignored —
don't edit it; change `setup.conf` / the dashboard instead. Sites of your own belong in
[`../sites.d/`](../sites.d/README.md), which both configs import.
