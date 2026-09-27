# letsencrypt/

The public Let's Encrypt certificate for your domain — used to reach chosen apps from
outside your home network — lives here once you set it up with `hsctl cert`; see
[docs/letsencrypt.md](../docs/letsencrypt.md). (At home, the apps keep using Caddy's own
local certificates.) Nothing here is committed: everything but this README is git-ignored.

| Path | What it is |
|------|-----------|
| `certificates/<domain>.crt` | the certificate **with its chain** (full chain) — covers `<domain>` and `*.<domain>` |
| `certificates/<domain>.key` | its private key (`0600`) |
| `certificates/<domain>.issuer.crt` | the issuer (intermediate) certificate alone |
| `accounts/` | your Let's Encrypt account key |

hsctl renews the certificate in place (the dashboard checks twice a day) and reloads
Caddy, which reads this folder read-only. To use the same certificate for another
service on this machine, point it at the `.crt` / `.key` above — for a container,
bind-mount this folder read-only — and restart that service now and then (weekly is
plenty) so it picks up renewals.

`hsctl uninstall` deletes `certificates/` and `accounts/`; `hsctl cert issue` gets a
fresh certificate at any time.
