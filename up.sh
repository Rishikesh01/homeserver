#!/usr/bin/env bash
# Bring the whole stack up in dependency order: services -> Caddy -> WireGuard.
# Uses sudo automatically if your user can't reach the Docker daemon directly.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

DC="docker compose"
docker info >/dev/null 2>&1 || DC="sudo docker compose"

for s in vaultwarden nextcloud pihole caddy wireguard; do
  if [ ! -f "$s/.env" ]; then
    echo "!! $s/.env missing — run ./bootstrap.sh first"; exit 1
  fi
  echo "== up: $s =="
  ( cd "$s" && $DC up -d )
done

echo
echo "== status =="
${DC% compose} ps --format 'table {{.Names}}\t{{.Status}}' 2>/dev/null || $DC ls
