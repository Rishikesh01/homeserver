# Backup & restore

Backups are **encrypted** (restic: AES-256, client-side — the destination only ever sees
ciphertext) and cover a **consistent Postgres dump + a consistent Vaultwarden DB snapshot +
every data volume + your config**.

- [What gets backed up, and how](#what-gets-backed-up-and-how)
- [Choose a destination](#choose-a-destination)
- [Set it up and run](#set-it-up-and-run)
- [Check it actually works](#check-it-actually-works)
- [Schedule it nightly](#schedule-it-nightly)
- [Off-site replica](#off-site-replica)
- [Restore (disaster recovery)](#restore-disaster-recovery)
- [See a real restore, safely](#see-a-real-restore-safely)

All of this is also available without a terminal, from the dashboard's **Backups** page at
`/admin/backup`.

![The Backups page](screenshots/backups.png)

---

## What gets backed up, and how

Vaultwarden uses SQLite (WAL mode), so its DB can't be copied safely while live — `backup run`
briefly stops Vaultwarden (~1s) to copy its **SQLite fileset (`db.sqlite3` + `-wal` + `-shm`)**
into `backups/staging/vaultwarden/`, then restarts it; its attachments and keys are backed up
**live** with the volume. So the stop stays ~1s no matter how large attachments grow. (The
`-wal` is essential — `docker stop` may not checkpoint it, and dropping it would lose recent
writes.) Nextcloud is never stopped; its DB is dumped online with `pg_dump`.

`backup run` needs `sudo` because it reads the Docker volume files, which are owned by root.

---

## Choose a destination

A backup on the same disk only protects against mistakes, not disk failure or theft. Point
it somewhere **off the box**:

| Destination | `--repo` value |
|-------------|----------------|
| External USB drive | `/mnt/restic` |
| Another machine (NAS, Pi) over SSH | `sftp:user@nas:/backups` |
| Cloud (Backblaze B2) | `b2:your-bucket:homeserver` |
| Cloud (S3-compatible) | `s3:s3.region.amazonaws.com/your-bucket` |

If the destination is an external disk you mount by hand, also set the **mount guard** so a
backup can never silently land on the root filesystem when the disk isn't attached:

```bash
sudo hsctl backup config --require-mount /mnt/restic
```

With that set, every backup and restore operation refuses to run unless `/mnt/restic` is a
real mount (hsctl compares its device id against `/`'s). The dashboard's **Drives** page
(`/admin/devices`) can mount the disk for you with one click.

---

## Set it up and run

```bash
sudo apt install -y restic                       # restic >= 0.14 (the repo is format v2)
sudo apt-mark hold restic                        # pin it — apt upgrade won't change it
                                                 #   (undo later with: sudo apt-mark unhold restic)
hsctl backup config --repo /mnt/restic           # set the destination
hsctl backup config --password 'StrongPassword'  # OPTIONAL: set your own repo password
                                                 #   (omit and one is auto-generated)
sudo hsctl backup init                           # create the encrypted repo (first time only)
sudo hsctl backup run                            # take a snapshot
sudo hsctl backup list                           # see snapshots (repo is root-owned, so sudo)
sudo hsctl backup forget                         # apply the retention policy and prune
```

The repo password lives in **`.restic-password`** (set via `--password`, or auto-generated
on first init). Full details — including changing it later and restoring with plain restic
(no hsctl) — are in **[configuration.md → Backups](configuration.md#backups)**.

> **Back up `.restic-password` somewhere else** (e.g. write it down, or store it in
> Vaultwarden). It encrypts your backups — **without it, the backups are unrecoverable.**

Default retention is `--keep-daily 7 --keep-weekly 4 --keep-monthly 6`; change it with
`hsctl backup config --retention '…'`.

---

## Check it actually works

<a id="pin-restic"></a>

```bash
sudo hsctl backup config --pin-restic   # once: record the known-good restic version
sudo hsctl backup verify                # aliases: selftest, test
```

A self-contained drill that **never touches your live stack or repo** — it spins up throwaway
Docker volumes/containers and an isolated temp repo. It runs five checks:

1. **restic round-trip** — token into a volume → backup → wipe → restore → read it back.
2. **`restore --into-volumes` put-back** — seeds a throwaway volume with stale data, runs the
   real put-back primitive, and confirms the stale data is **gone** and the snapshot's data is
   in place (so the destructive one-command restore can't silently leave old data behind).
3. **Vaultwarden (passwords)** — boots the real Vaultwarden image so it writes its SQLite DB,
   seeds a fake attachment, then runs the real path: stages the DB **fileset**, backs the volume up
   **live excluding the DB**, restores + overlays the fileset, confirms the DB is **byte-identical**,
   the **attachment survived** the live-volume path, and a fresh Vaultwarden **boots** and stays up.
4. **Vaultwarden WAL** — builds a SQLite DB with a row left **uncheckpointed in `-wal`** and proves
   the main file alone loses it but the `db.sqlite3` + `-wal` + `-shm` fileset preserves it
   (regression test for a real data-loss bug).
5. **Nextcloud database** — seeds a row in a throwaway Postgres, dumps it with the same
   `pg_dump` the backup uses, pushes it through restic, then imports into a **brand-new**
   Postgres and checks the row is back.

It also enforces the **restic version pin**: `verify` fails if the installed restic differs
from the one recorded by `--pin-restic` (stored as `RESTIC_VERSION` in `backup.conf`), so a
system upgrade that swaps restic out is caught instead of silently changing your backup tool.
Safe to run anytime.

---

## Schedule it nightly

```bash
make -C hsctl install-services    # installs the UI service + nightly backup timer
```

This installs `hsctl-ui.service`, `hsctl-backup.service` and `hsctl-backup.timer` into
`/etc/systemd/system/` (filling in `__DIR__` with this repo's path for you) and enables them.
The timer runs at **03:30 daily**, with `Persistent=true` so a missed run catches up after a
reboot, and `RequiresMountsFor` on the backup path.

The dashboard shows how long ago the last backup completed, and flags it when it goes stale.

---

## Off-site replica

A second copy, somewhere the house isn't — protection against fire and theft, not just disk
failure.

```bash
hsctl backup config --replica b2:your-bucket:homeserver   # or sftp:user@host:/path, s3:…
sudo hsctl backup replicate                               # copy every snapshot to the replica
```

- The first `replicate` **creates** the replica repo; later runs are incremental and
  deduplicated (`restic copy`), so only new data moves.
- The replica uses the **same encryption password** as the primary, so `.restic-password`
  restores from either.
- Retention is applied to the replica too, using the same `RETENTION` policy.
- The primary repo must be reachable — `replicate` copies *from* it.

**Cloud credentials** go in **`.backup-env`** next to `.restic-password`, one `KEY=VALUE` per
line, mode `0600` and git-ignored:

```bash
# .backup-env — Backblaze B2
B2_ACCOUNT_ID=…
B2_ACCOUNT_KEY=…
# …or S3:
# AWS_ACCESS_KEY_ID=…
# AWS_SECRET_ACCESS_KEY=…
```

Local paths and `sftp:` destinations need no credentials file (SFTP uses your SSH key).

There's a **Copy backups off-site** card on the dashboard's Command Center that runs the same
thing.

---

## Restore (disaster recovery)

**One command** restores the whole stack: it stops everything, repopulates every volume
from the snapshot — including Vaultwarden, whose data comes from its consistent staged copy
(not a volume dir) — and brings the stack back up.

```bash
sudo hsctl backup restore latest --into-volumes   # add --yes to skip the confirm prompt
```

It's destructive (it **wipes** each volume before restoring), so it confirms first, and it
refuses to run unless the backup disk is mounted (the `REQUIRE_MOUNT` guard). When it
finishes, check `hsctl status` and log into each app to confirm your data is back.

There's a **Restore from a backup** flow on the dashboard too. Note that the restore restarts
Caddy, so the browser page will drop part-way through — that's expected; the restore keeps
running on the server and the page can be reloaded once it's back. Running it on the server
itself is the more reliable route.

<details>
<summary>Manual restore — if you'd rather inspect the files first or restore selectively</summary>

```bash
# 1. Extract a snapshot to a folder (latest, or a specific id from `backup list`)
sudo hsctl backup restore latest --target /tmp/restore

# 2. Stop the stack
hsctl down

# 3. Put each volume's files back (the restored tree mirrors the original paths).
for d in /tmp/restore/var/lib/docker/volumes/*/; do
  v=$(basename "$d")
  sudo cp -a "$d/_data/." "/var/lib/docker/volumes/$v/_data/"
done

# 3b. IMPORTANT: step 3 restored Vaultwarden's volume (attachments/keys) but NOT its db.sqlite3
#     (it's excluded from the volume backup). Overlay the consistent DB from staging, or you
#     lose your passwords:
sudo cp -a /tmp/restore/<repo>/backups/staging/vaultwarden/db.sqlite3* \
           /var/lib/docker/volumes/vaultwarden_vw-data/_data/   # <repo> = abs path, e.g. /home/you/homeserver

# 4. Start everything
hsctl up
```
</details>

> The snapshot also holds a consistent SQL dump at `backups/staging/nextcloud-db.sql`. You
> only need it as a **fallback**: if the restored DB volume won't start (e.g. it was captured
> mid-write), wipe `nextcloud_db-data`, bring up a fresh `nextcloud-db`, and import the dump
> with `docker exec -i nextcloud-db psql -U nextcloud -d nextcloud < …/nextcloud-db.sql`.
> Don't do both — importing onto an already-restored DB volume just errors on existing tables.

On a brand-new machine, restore the config files too (the restored `<repo>/*/.env` and
`<repo>/setup.conf`) before `hsctl up`. The restic repo password must be the same one you
saved.

**You don't need hsctl at all to recover.** The repo is a vanilla restic repository — with
just the destination and the password you can decrypt and extract everything from any machine
that has the `restic` binary. Step-by-step:
[configuration.md → Manual disaster recovery](configuration.md#manual-disaster-recovery--plain-restic-no-hsctl).

---

## See a real restore, safely

`hsctl backup verify` is the automated pass/fail self-test on synthetic data. To instead
**see your real data come back** — restore an actual snapshot and log into the apps — without
risking the live stack, use the sandbox:

```bash
make sandbox REPO=/mnt/restic    # boot an isolated copy of the stack, real repo mounted read-only
make sandbox-restore             # restore latest into it, bring the stack up
```

The real repo is mounted **read-only** and read with `restic restore --no-lock`, so your live
backup is never written or even locked. Full details: **[sandbox/README.md](../sandbox/README.md)**.
