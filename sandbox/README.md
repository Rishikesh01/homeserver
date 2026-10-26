# Test sandbox

A throwaway, fully isolated copy of the homeserver you spin up to **try things before
they touch your live system** — a new service image, an `hsctl` change, a newer restic,
or a real restore (to actually *see* your data come back).

It runs its **own nested Docker daemon** (docker-in-docker) inside one `--rm` container.
Nothing it does — pulling images, starting containers, mounting disks, restoring volumes —
can reach the host's Docker, your live containers, or your live volumes. When you stop it,
it's gone.

> This is an **operator tool, driven by `make`** — it is deliberately *not* part of the
> `hsctl` binary the household uses.

## Use it

Run from the repo root:

```bash
make sandbox            # build your current hsctl + boot the isolated sandbox
                        #   admin UI -> http://localhost:18088/admin  (admin / test)
make sandbox-restore    # restore your real backup into it (same as the dashboard's Restore)
make sandbox-shell      # a shell inside the sandbox
make sandbox-logs       # follow its logs
make sandbox-down       # stop it and sweep its loopback device off the host
make sandbox-purge      # sandbox-down, plus delete the cached nested-image volume
```

After `make sandbox`, open the admin UI and use **Commands → Start all services** to pull
the manifest's images into the nested daemon and bring the stack up. Browsing from another
machine? Use the server's LAN IP instead of `localhost` (e.g. `http://HOST:18088`, where
`HOST` is the server's LAN address).

## Knobs

Override on the command line, e.g. `make sandbox PORT=19000`:

| Var | Default | Meaning |
|-----|---------|---------|
| `IMAGES`   | `sandbox/images.env` | image manifest to bring up (see below) |
| `PORT`     | `18088` | host port for the admin UI |
| `PASS`     | `test`  | admin password inside the sandbox |
| `BACKUP_DISK` | — | the disk your backups are on (`LABEL=Backup`, `UUID=…` or a device), mounted **read-write at `/mnt` inside the sandbox**; it must not be mounted on the host |
| `REPO`     | `RESTIC_REPO` from `backup.conf` | the restic repo to restore from: a path under `/mnt` with `BACKUP_DISK`, else a local folder on the host (bind-mounted read-write) |
| `PASSFILE` | `.restic-password` | the repo's password file, if you have it here; otherwise enter the password on the sandbox's Backups page |
| `SNAPSHOT` | `latest` | snapshot id for `make sandbox-restore` |
| `ACCESS_HOST` | autodetected LAN IP | host the app URLs are built for — Vaultwarden's `VW_DOMAIN` and Nextcloud's trusted domain are set from it, so override it if you'll browse the sandbox from a different machine |

## Testing image updates

`sandbox/images.env` is an **override** file. By default it's empty, so the sandbox uses the
tags pinned in each service's own `docker-compose.yml` (the single source of truth — nothing
to keep in sync). To try a new version, add/uncomment a line for that component; the sandbox
writes a `docker-compose.override.yml` into just that service dir so `hsctl up` runs **that**
image, leaving the real compose files untouched. Confirm it works, *then* bump the live tag.

```bash
# e.g. try a Nextcloud major bump
echo 'nextcloud-app=nextcloud:36.0.0-apache' >> sandbox/images.env
make sandbox        # boot, click "Start all services", log in, look around
```

## Testing a restic upgrade

restic comes from the sandbox base image's distro (see `restic version` in `make
sandbox-logs`). To test a newer restic against your real repo, bump the `FROM docker:..-dind`
tag in `sandbox/Dockerfile` (a newer tag tracks a newer Alpine → newer restic), rebuild with
`make sandbox` (with your backups wired in, below), restore, and confirm it still restores cleanly
before you upgrade restic on the live box.

## Testing a restore (see your data)

```bash
make sandbox BACKUP_DISK=LABEL=Backup REPO=/mnt/restic_v2
```

The disk is mounted at `/mnt` inside the sandbox and its backups are set up there (with `/mnt`
as the disk guard). Then, in the sandbox's dashboard:

1. **Backups** — if you didn't hand in a `PASSFILE`, enter the repository's password under
   **Backup password**. It's checked against the repository before it's saved.
2. **Backups → Restore** — type `RESTORE`. The restore runs as a job: it extracts the snapshot,
   checks it, stops the stack, puts every volume back (Vaultwarden from its consistent copy,
   Nextcloud's database rebuilt from its dump) and starts the stack. `make sandbox-restore` does
   the same from the terminal.

Then open your restored data in a browser. The apps are served **over HTTPS by the restored
Caddy** — which carries your own CA, so the certificate is already trusted on any device
that's been onboarded. Each port is **its live port + 10000**, so it never collides with your
running stack:

| App | Sandbox URL |
|-----|-------------|
| Vaultwarden (passwords) | `https://<host>:18443` |
| Nextcloud (files)       | `https://<host>:18444` |
| Pi-hole                 | `https://<host>:18445/admin` |
| Stirling-PDF · IT-Tools · Image tool | `https://<host>:18446` · `:18447` · `:18448` |

The admin UI itself stays on plain http at `http://<host>:18088`.

Log in with your normal credentials and confirm everything's there. HTTPS isn't cosmetic
here: Vaultwarden refuses to unlock a vault outside a secure context, so the sandbox points
`VW_DOMAIN` at the forwarded HTTPS URL, and sets the stack up for `ACCESS_HOST` so Nextcloud
trusts the address you browse it at.

**Safety:** restic writes a lock into the repository even to restore, so the repo is mounted
**read-write**. Point the sandbox at a **copy** of your repository (like `restic_v2` above),
never the one your live server backs up to — and don't press "Back up now" in the sandbox unless
you want its snapshots in that copy. The Makefile refuses to start while the disk is mounted on
the host (two read-write mounts of one filesystem would corrupt it); the sandbox's mount goes
away when the sandbox stops. The restored data lands only in the sandbox's nested volumes —
and the extraction needs room: about the snapshot's size, twice over, on the host's Docker disk.

> This is the *human* counterpart to `hsctl backup verify`: `verify` is the automated,
> synthetic-data, pass/fail self-test; the sandbox restore lets you **see your real data**
> in the actual apps.

## Files

| File | Role |
|------|------|
| `Dockerfile`      | docker-in-docker base + tools + your hsctl + the repo |
| `entrypoint.sh`   | starts nested dockerd, mounts the backup disk, applies the manifest, serves the UI |
| `apply-images.sh` | manifest → per-service `docker-compose.override.yml` |
| `images.env`      | the image manifest |
| `_build/`         | host-built hsctl binary (gitignored) |

## Caveat

The fake disk on the Drives page is a loopback device, which is a host-global resource.
A graceful `make sandbox-down` (or `docker stop`) detaches it; a hard `docker kill`/`rm -f`
can leave it behind — `make sandbox-down` sweeps any stragglers.
