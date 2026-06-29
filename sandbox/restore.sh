#!/bin/bash
# Restore a REAL backup snapshot into the sandbox and bring the stack up on it, so you
# can open the apps and SEE your data come back. Runs entirely inside the sandbox's
# nested Docker daemon.
#
# Safety: the real restic repo is bind-mounted READ-ONLY at /backup-repo and read with
# `--no-lock`, so this NEVER writes to (or locks) your live backup repository. The data
# lands only in the sandbox's nested Docker volumes — your live volumes are untouched.
set -eu

SNAP="${1:-latest}"
export RESTIC_REPOSITORY=/backup-repo
export RESTIC_PASSWORD_FILE=/backup-pass
export RESTIC_CACHE_DIR=/tmp/restic-cache

if [ ! -d /backup-repo ]; then
  echo "[restore] No /backup-repo mounted."
  echo "          Start the sandbox with a LOCAL repo, e.g.:"
  echo "            make sandbox REPO=/mnt/backup/restic"
  echo "          (remote sftp:/b2: repos aren't wired into the sandbox yet.)"
  exit 1
fi
if [ ! -f /backup-pass ]; then
  echo "[restore] No /backup-pass (restic password) mounted — cannot decrypt."
  exit 1
fi

echo "[restore] restoring snapshot '$SNAP' (read-only repo, --no-lock)..."
rm -rf /restore && mkdir -p /restore
restic restore "$SNAP" --no-lock --target /restore

echo "[restore] ensuring sandbox service config exists..."
hsctl setup --yes >/dev/null 2>&1 || true

VROOT=/restore/var/lib/docker/volumes
if [ ! -d "$VROOT" ]; then
  echo "[restore] snapshot has no volume data at $VROOT — nothing to load."
  exit 1
fi

echo "[restore] loading restored data into the sandbox's Docker volumes..."
for d in "$VROOT"/*/; do
  name="$(basename "$d")"
  src="${d}_data"
  [ -d "$src" ] || continue
  docker volume create "$name" >/dev/null
  mp="$(docker volume inspect -f '{{.Mountpoint}}' "$name")"
  [ -n "$mp" ] && rm -rf "${mp:?}/"* 2>/dev/null || true
  cp -a "$src/." "$mp/"
  echo "  loaded volume $name"
done

# Vaultwarden DB overlay: the snapshot stores its repo's absolute path, so staging lands
# somewhere under /restore — find it. Overlay the consistent SQLite fileset onto the
# restored volume (mirrors what hsctl's restoreIntoVolumes does for the real put-back).
VWS="$(find /restore -type d -path '*backups/staging/vaultwarden' 2>/dev/null | head -1 || true)"
if [ -n "${VWS:-}" ] && [ -f "$VWS/db.sqlite3" ]; then
  mp="$(docker volume inspect -f '{{.Mountpoint}}' vaultwarden_vw-data 2>/dev/null || true)"
  if [ -n "$mp" ]; then
    for s in '' '-wal' '-shm'; do
      [ -f "$VWS/db.sqlite3$s" ] && cp -a "$VWS/db.sqlite3$s" "$mp/db.sqlite3$s"
    done
    echo "  overlaid Vaultwarden SQLite fileset (db.sqlite3 + WAL)"
  fi
fi

echo "[restore] bringing the stack up on the restored data..."
hsctl up

# Make the restored Nextcloud reachable on the forwarded host:port. Out of the box it
# rejects any host not in its trusted_domains (a white error page), so add ours and point
# its generated URLs at plain http on the forwarded port. Best-effort + a short wait for it
# to finish initialising.
H="${ACCESS_HOST:-localhost}"
echo "[restore] making the restored Nextcloud browsable at http://$H:18081 ..."
for _ in $(seq 1 20); do docker exec -u www-data nextcloud-app php occ status >/dev/null 2>&1 && break; sleep 3; done
docker exec -u www-data nextcloud-app php occ config:system:set trusted_domains 8 --value="$H:18081" >/dev/null 2>&1 || true
docker exec -u www-data nextcloud-app php occ config:system:set overwrite.cli.url --value="http://$H:18081" >/dev/null 2>&1 || true
docker exec -u www-data nextcloud-app php occ config:system:set overwriteprotocol --value="http" >/dev/null 2>&1 || true

echo "==================================================================="
echo "  RESTORE DONE — this is a COPY of snapshot '$SNAP', live system untouched."
echo "  Open your restored data in a browser:"
echo "    Vaultwarden (passwords) : http://$H:18082"
echo "    Nextcloud   (files)     : http://$H:18081"
echo "    Pi-hole                 : http://$H:18053/admin"
echo "  Log in with your normal credentials and confirm everything's there."
echo "  (http, not https — fine for the sandbox; Vaultwarden may show a domain"
echo "   warning banner, but your vault still opens.)"
echo "==================================================================="
