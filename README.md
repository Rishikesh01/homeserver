# Homeserver

Self-hosted stack. Each service lives in its own folder with its own `docker-compose.yml`
and `.env`, so you can start/stop them independently.

| Service | What | Folder | Default UI |
|---------|------|--------|-----------|
| Vaultwarden | FOSS password manager (Bitwarden-compatible) | `vaultwarden/` | http://HOST:8080 |
| Nextcloud | Encrypted cloud file storage | `nextcloud/` | http://HOST:8081 |
| Pi-hole | Network-wide DNS ad-blocker | `pihole/` | http://HOST:8053/admin |

## First-time setup

For each service:

```bash
cd <service>
cp .env.example .env
# edit .env and replace every "change-me" value
docker compose up -d
docker compose logs -f      # watch it come up
```

Generate strong secrets with: `openssl rand -base64 32`

## ⚠️ Pi-hole + Ubuntu port 53

Ubuntu runs `systemd-resolved`, which already binds port 53 and will block Pi-hole.
Free it before starting Pi-hole:

```bash
sudo sed -i 's/^#\?DNSStubListener=.*/DNSStubListener=no/' /etc/systemd/resolved.conf
sudo ln -sf /run/systemd/resolve/resolv.conf /etc/resolv.conf
sudo systemctl restart systemd-resolved
```

Then point your router's DNS (or individual devices) at this server's IP to use Pi-hole.

## HTTPS (important for Vaultwarden & Nextcloud)

Both ship HTTP on a local port. The Bitwarden and Nextcloud mobile/desktop clients
**require HTTPS**. Put a reverse proxy in front and give each a hostname. The simplest
is Caddy (automatic Let's Encrypt):

```
vault.example.com  { reverse_proxy localhost:8080 }
cloud.example.com  { reverse_proxy localhost:8081 }
```

Then set `VW_DOMAIN=https://vault.example.com` and add `cloud.example.com` to
`NC_TRUSTED_DOMAINS`.

## Nextcloud encryption

Nextcloud's server-side encryption is **off by default**. After first login, enable it in
**Admin → Settings → Security**, or from the CLI:

```bash
docker compose -f nextcloud/docker-compose.yml exec -u www-data app \
  php occ app:enable encryption
docker compose -f nextcloud/docker-compose.yml exec -u www-data app \
  php occ encryption:enable
```

For true at-rest privacy, also encrypt the host disk/volume (LUKS) and/or use a client
that does end-to-end encryption. Server-side encryption mainly protects external storage
backends — the keys live on the server.

## Backups

Back up these named Docker volumes regularly:
- `vaultwarden_vw-data` — all your passwords
- `nextcloud_nc-data` — your files
- `nextcloud_db-data` — Nextcloud database

Example: `docker run --rm -v vaultwarden_vw-data:/d -v $PWD:/b alpine tar czf /b/vw-backup.tgz -C /d .`

## Optional: run all three at once

From this directory:

```bash
for s in vaultwarden nextcloud pihole; do (cd $s && docker compose up -d); done
```
