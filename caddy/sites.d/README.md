# caddy/sites.d — your extra Caddy sites

Every `*.caddy` file in this folder is imported by [`../Caddyfile`](../Caddyfile) (private-CA
mode) **and** by the Let's Encrypt config hsctl generates (`../generated/Caddyfile`), so a site
you add here survives switching modes. Nothing is generated into this folder.

Use it for an app you added yourself — see [docs/adding-an-app.md](../../docs/adding-an-app.md)
for the block to write in each mode. Reload Caddy afterwards: `hsctl letsencrypt apply` (works in
either mode), or `docker exec caddy caddy reload --config <the active config>`.
