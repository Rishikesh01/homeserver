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
cleanup() { [ -n "${LOOP:-}" ] && losetup -d "$LOOP" 2>/dev/null || true; }
trap cleanup EXIT INT TERM

# Generate service .env so `hsctl up` works. Pin the app ports to known values so the
# Makefile can forward them out to your browser deterministically (see the URLs below).
echo "[sandbox] generating service config (hsctl setup)..."
if hsctl setup --yes --vw-port 8082 --nc-port 8081 --pihole-port 8053 >/var/log/setup.log 2>&1; then
  echo "[sandbox] service config generated"
else
  echo "[sandbox] setup had problems; tail of its log:"; tail -n 10 /var/log/setup.log
fi

H="${ACCESS_HOST:-localhost}"
echo "==================================================================="
echo "  SANDBOX READY  (isolated; your live system is untouched)"
echo "  Admin UI : http://${H}:18088/admin   (admin / ${HSCTL_UI_PASSWORD:-test})"
echo "  Bring up : UI -> Commands -> 'Start all services', or:  make sandbox-restore"
echo "  Once up, open the apps in your browser (live ports + 10000):"
echo "    Vaultwarden : http://${H}:18082     Nextcloud : http://${H}:18081"
echo "    Pi-hole     : http://${H}:18053/admin"
echo "  Stop     : from the host ->  make sandbox-down   (cleans up fully)"
echo "==================================================================="

hsctl ui --addr :8088 &
wait $!
