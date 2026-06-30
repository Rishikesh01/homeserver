# Test sandbox

A throwaway, fully isolated copy of the homeserver you spin up to **try things before
they touch your live system** — a new service image, an `hsctl` change, a newer restic,
or a real restore (to actually *see* your data come back).

It runs its **own nested Docker daemon** (docker-in-docker) inside one `--rm` container.
Nothing it does — pulling images, starting containers, mounting disks, restoring volumes —
can reach the host's Docker, your live containers, or your live volumes. When you stop it,
it's gone.

> This is an **operator tool, driven by `make`** — it is deliberately *not* part of the
> `hsctl` binary your family uses.

## Use it

Run from the repo root:

```bash
make sandbox            # build your current hsctl + boot the isolated sandbox
                        #   admin UI -> http://localhost:18088/admin  (admin / test)
make sandbox-restore    # restore your real backup into it, then bring the stack up
make sandbox-shell      # a shell inside the sandbox
make sandbox-logs       # follow its logs
make sandbox-down       # stop it and sweep its loopback device off the host
```

After `make sandbox`, open the admin UI and use **Commands → Start all services** to pull
the manifest's images into the nested daemon and bring the stack up. Browsing from another
machine? Use the server's LAN IP instead of `localhost` (e.g. `http://192.168.0.150:18088`).

## Knobs

Override on the command line, e.g. `make sandbox PORT=19000`:

| Var | Default | Meaning |
|-----|---------|---------|
| `IMAGES`   | `sandbox/images.env` | image manifest to bring up (see below) |
| `PORT`     | `18088` | host port for the admin UI |
| `PASS`     | `test`  | admin password inside the sandbox |
| `REPO`     | `RESTIC_REPO` from `backup.conf` | **local** restic repo to restore from |
| `SNAPSHOT` | `latest` | snapshot id for `make sandbox-restore` |

## Testing image updates

`sandbox/images.env` is an **override** file. By default it's empty, so the sandbox uses the
tags pinned in each service's own `docker-compose.yml` (the single source of truth — nothing
to keep in sync). To try a new version, add/uncomment a line for that component; the sandbox
writes a `docker-compose.override.yml` into just that service dir so `hsctl up` runs **that**
image, leaving the real compose files untouched. Confirm it works, *then* bump the live tag.

```bash
# e.g. try a Nextcloud major bump
echo 'nextcloud-app=nextcloud:31-apache' >> sandbox/images.env
make sandbox        # boot, click "Start all services", log in, look around
```

## Testing a restic upgrade

restic comes from the sandbox base image's distro (see `restic version` in `make
sandbox-logs`). To test a newer restic against your real repo, bump the `FROM docker:..-dind`
tag in `sandbox/Dockerfile` (a newer tag tracks a newer Alpine → newer restic), rebuild with
`make sandbox`, then `make sandbox-restore` and confirm it still restores cleanly before you
upgrade restic on the live box.

## Testing a restore (see your data)

```bash
make sandbox REPO=/mnt/restic    # mount your real repo read-only
make sandbox-restore             # restore latest, bring the stack up on it
```

Then open your restored data in a browser. The sandbox forwards each app's port to the host
at **its live port + 10000** (so it never collides with your running stack):

| App | Sandbox URL |
|-----|-------------|
| Vaultwarden (passwords) | `http://<host>:18082` |
| Nextcloud (files)       | `http://<host>:18081` |
| Pi-hole                 | `http://<host>:18053/admin` |

Log in with your normal credentials and confirm everything's there. It's plain `http` (no
TLS in the sandbox), so Vaultwarden may show a "domain not configured" banner — your vault
still opens. `make sandbox-restore` auto-adds the host to Nextcloud's trusted domains so it
loads.

**Safety:** the real repo is mounted **read-only** and read with `restic restore --no-lock`,
so this never writes to (or even locks) your live backup repository. The restored data lands
only in the sandbox's nested volumes.

> This is the *human* counterpart to `hsctl backup verify`: `verify` is the automated,
> synthetic-data, pass/fail self-test; the sandbox restore lets you **see your real data**
> in the actual apps.

## Files

| File | Role |
|------|------|
| `Dockerfile`      | docker-in-docker base + tools + your hsctl + the repo |
| `entrypoint.sh`   | starts nested dockerd, applies the manifest, serves the UI |
| `apply-images.sh` | manifest → per-service `docker-compose.override.yml` |
| `restore.sh`      | read-only `--no-lock` restore into the nested volumes |
| `images.env`      | the image manifest |
| `_build/`         | host-built hsctl binary (gitignored) |

## Caveat

The fake disk on the Drives page is a loopback device, which is a host-global resource.
A graceful `make sandbox-down` (or `docker stop`) detaches it; a hard `docker kill`/`rm -f`
can leave it behind — `make sandbox-down` sweeps any stragglers.
