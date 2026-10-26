#!/bin/bash
# Sandbox entrypoint: bring up an isolated, nested Docker daemon, apply the image
# manifest, generate fresh service config, and serve the hsctl admin UI — all inside
# this one throwaway container. Nothing here can reach the host's Docker or volumes.
set -u

echo "[sandbox] starting nested Docker daemon (docker-in-docker)..."
dockerd-entrypoint.sh dockerd >/var/log/dockerd.log 2>&1 &
for _ in $(seq 1 40); do docker info >/dev/null 2>&1 && break; sleep 1; done
if docker info >/dev/null 2>&1; then
  echo "[sandbox] nested dockerd ready — $(docker --version)"
else
  echo "[sandbox] WARNING: nested dockerd did not come up; tail of its log:"
  tail -n 20 /var/log/dockerd.log
fi
echo "[sandbox] restic from base image: $(restic version 2>/dev/null | awk '{print $1, $2}')"

# Apply the image manifest -> per-service compose overrides (so `up` uses these tags).
if [ -f /sandbox/images.env ]; then
  echo "[sandbox] applying image manifest:"
  /sandbox/apply-images.sh /sandbox/images.env /repo
fi

# Optional fake disk so the Drives page has something to mount (best-effort).
[ -x /sbin/udevd ] && /sbin/udevd --daemon 2>/dev/null
IMG=/var/tmp/sandboxdisk.img
[ -f "$IMG" ] || { dd if=/dev/zero of="$IMG" bs=1M count=128 status=none 2>/dev/null; mkfs.ext4 -q -L sandboxdisk "$IMG" 2>/dev/null; }
LOOP="$(losetup -f --show "$IMG" 2>/dev/null || true)"
udevadm trigger --subsystem-match=block 2>/dev/null || true
udevadm settle --timeout=5 2>/dev/null || true
cleanup() {
  [ -n "${UI_PID:-}" ] && kill -TERM "$UI_PID" 2>/dev/null || true   # forward docker stop's SIGTERM to hsctl ui
  [ -n "${UI_PID:-}" ] && wait "$UI_PID" 2>/dev/null
  [ -n "${DISK:-}" ] && { sync; umount /mnt 2>/dev/null || true; }
  [ -n "${LOOP:-}" ] && losetup -d "$LOOP" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# The real backup disk, when given (BACKUP_DISK=LABEL=Backup, say): mounted read-write at /mnt —
# restic needs to write its lock even to restore. The Makefile has checked the host doesn't have
# it mounted. The mount lives in this container only, and goes when it stops.
if [ -n "${BACKUP_DISK:-}" ]; then
  DISK="$(findfs "$BACKUP_DISK" 2>/dev/null || true)"
  if [ -z "$DISK" ]; then
    echo "[sandbox] WARNING: no disk matches BACKUP_DISK=$BACKUP_DISK — not mounted"
  elif mount "$DISK" /mnt; then
    echo "[sandbox] backup disk $DISK ($BACKUP_DISK) mounted read-write at /mnt"
  else
    echo "[sandbox] WARNING: couldn't mount $DISK at /mnt"; DISK=""
  fi
fi

# Generate service .env so `hsctl up` works, for the address you'll browse the sandbox at (so the
# certificate, Vaultwarden's domain and Nextcloud's trusted domains fit). Bind Pi-hole's DNS to
# 0.0.0.0 — the host's IP doesn't exist inside the nested daemon (so `up` would abort).
echo "[sandbox] generating service config (hsctl setup)..."
if hsctl setup --yes ${ACCESS_HOST:+--server-ip "$ACCESS_HOST"} --pihole-dns-bind 0.0.0.0 >/var/log/setup.log 2>&1; then
  echo "[sandbox] service config generated"
else
  echo "[sandbox] setup had problems; tail of its log:"; tail -n 10 /var/log/setup.log
fi

# The backups to restore from: the repo (on the mounted disk, guarded so nothing lands on the
# sandbox's own disk if the mount is gone) and its password, if one was handed in. Without a
# password file, put yours in on the Backups page — it's checked against the repository first.
if [ -n "${SANDBOX_REPO:-}" ]; then
  guard=""
  [ -n "${DISK:-}" ] && guard=/mnt
  if hsctl backup config --repo "$SANDBOX_REPO" --require-mount "$guard" >/var/log/backup-config.log 2>&1; then
    echo "[sandbox] backups: $SANDBOX_REPO"
  else
    echo "[sandbox] backup config failed:"; tail -n 5 /var/log/backup-config.log
  fi
  if [ -f /backup-pass ]; then
    install -m 600 /backup-pass /repo/.restic-password && echo "[sandbox] backup password: from your password file"
  else
    echo "[sandbox] backup password: none yet — enter it on the Backups page (Backup password)"
  fi
fi

# Serve the apps over HTTPS through the RESTORED Caddy (which carries your trusted CA), on
# ports the Makefile forwards to the host (live https ports + 10000). Vaultwarden refuses
# http when its DOMAIN is https, so we point Caddy's ports + VW_DOMAIN at the forwarded
# https URL — giving a real, trusted-cert HTTPS origin (a secure context the vault accepts).
H="${ACCESS_HOST:-localhost}"
setkv() { f="$1"; k="$2"; v="$3"; touch "$f"; grep -v "^${k}=" "$f" 2>/dev/null > "$f.tmp" || true; mv "$f.tmp" "$f"; echo "${k}=${v}" >> "$f"; }
setkv /repo/caddy/.env VAULT_HTTPS 18443
setkv /repo/caddy/.env CLOUD_HTTPS 18444
setkv /repo/caddy/.env PIHOLE_HTTPS 18445
setkv /repo/caddy/.env STIRLING_HTTPS 18446
setkv /repo/caddy/.env ITTOOLS_HTTPS 18447
setkv /repo/caddy/.env IMAGETOOLS_HTTPS 18448
setkv /repo/vaultwarden/.env VW_DOMAIN "https://${H}:18443"
echo "[sandbox] apps will be served over HTTPS at https://${H}:18443 (vault), :18444 (cloud)"

echo "==================================================================="
echo "  SANDBOX READY  (isolated; your live system is untouched)"
echo "  Admin UI : http://${H}:${SANDBOX_PORT:-18088}/admin   (admin / ${HSCTL_UI_PASSWORD:-test})"
echo "  Bring up : UI -> Commands -> 'Start all services'"
echo "  Restore  : UI -> Backups (enter the backup password if asked) -> Restore"
echo "  Once up, open the apps over HTTPS (your trusted cert):"
echo "    Vaultwarden : https://${H}:18443    Nextcloud : https://${H}:18444"
echo "    Pi-hole     : https://${H}:18445/admin"
echo "  Stop     : from the host ->  make sandbox-down   (cleans up fully)"
echo "==================================================================="

hsctl ui --addr :8088 &
UI_PID=$!
# On `docker stop`, PID 1 (this shell) gets SIGTERM and the trap forwards it to hsctl ui, so it
# shuts down cleanly instead of being hard-killed when the stop grace period ends.
wait "$UI_PID"
