# Adding an app to the dashboard

The stack is deliberately simple to extend: a service is a folder with a compose file, a Caddy
block on a new HTTPS port, and a tile in `services.json`.

## 1. The service

Create `myapp/docker-compose.yml` with `container_name: myapp` and the shared edge network.
Copy the network stanza from an existing service — e.g. [`it-tools/docker-compose.yml`](../it-tools/docker-compose.yml),
which is the smallest one:

```yaml
services:
  myapp:
    image: vendor/myapp:1.2.3     # pin an exact version
    container_name: myapp
    restart: unless-stopped
    # No published port: Caddy reaches it over the shared "edge" network as myapp:80.
    networks:
      - edge

networks:
  edge:
    external: true
    name: homeserver-edge
```

Do **not** publish its HTTP port. Caddy reaches it by container name over `homeserver-edge`,
so it never touches the LAN — that's what keeps every app HTTPS-only.

## 2. The Caddy block

In [`caddy/Caddyfile`](../caddy/Caddyfile), add an HTTPS site on a new port, following the
pattern of the existing blocks:

```caddyfile
{$SERVER_IP}:{$MYAPP_HTTPS} {
	bind 0.0.0.0
	tls internal
	reverse_proxy {$MYAPP_UPSTREAM}
}
```

Then, in [`caddy/docker-compose.yml`](../caddy/docker-compose.yml):

- publish the port — `"${MYAPP_HTTPS:-8449}:${MYAPP_HTTPS:-8449}"`,
- add the upstream default — `MYAPP_UPSTREAM: ${MYAPP_UPSTREAM:-myapp:80}`,
- add the port variable — `MYAPP_HTTPS: ${MYAPP_HTTPS:-8449}`.

Finally add the new `MYAPP_HTTPS` line to `hsctl/env.go` so a fresh `hsctl setup` writes it
into `caddy/.env` too.

## 3. The tile

Add an entry to [`services.json`](../services.json) with that `https_port`:

```json
{ "key": "myapp", "name": "My app", "icon": "🧩", "desc": "What it does", "https_port": 8449 }
```

Fields: `key`, `name`, `icon`, `desc`, `https_port`, and an optional `path` (Pi-hole uses
`"path": "/admin"`). The dashboard reads this file on each load, so the tile appears without
a rebuild.

## 4. Start it

```bash
hsctl up
```

`hsctl up` brings up every service directory it knows about, then Caddy last. The start order
lives in [`hsctl/lifecycle.go`](../hsctl/lifecycle.go) — add your service there so it starts
before the proxy.

## Try it in the sandbox first

```bash
make sandbox     # an isolated copy of the whole stack, nested Docker, nothing shared with live
```

See [sandbox/README.md](../sandbox/README.md).

---

Related: [configuration.md](configuration.md) for what each config file controls, and
[CONTRIBUTING.md](../CONTRIBUTING.md) if you want to send the app upstream.
