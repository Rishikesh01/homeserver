#!/usr/bin/env bash
# Stop the whole stack in reverse order. Pass --volumes to also delete data
# volumes (DESTRUCTIVE — wipes passwords/files/VPN keys). Default keeps data.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

DC="docker compose"
docker info >/dev/null 2>&1 || DC="sudo docker compose"

EXTRA=""
[ "${1:-}" = "--volumes" ] && EXTRA="-v" && echo "!! --volumes: data volumes will be DELETED"

for s in caddy pihole nextcloud vaultwarden; do
  echo "== down: $s =="
  ( cd "$s" && $DC down $EXTRA )
done
