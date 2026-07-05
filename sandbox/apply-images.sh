#!/bin/bash
# Turn the image manifest (images.env) into docker-compose.override.yml files — one per
# service dir — so `hsctl up` inside the sandbox runs the manifest's image tags instead
# of the ones baked into each service's docker-compose.yml. Compose auto-merges an
# override file sitting next to the base compose, so this needs no change to hsctl.
#
# Only the sandbox's copy of the repo (/repo) is touched; your real repo is never edited.
set -u
MANIFEST="${1:-/sandbox/images.env}"
REPO="${2:-/repo}"

# component -> (service dir, compose service name). Keep in sync with the compose files.
declare -A DIR=(
  [vaultwarden]=vaultwarden
  [nextcloud-db]=nextcloud [nextcloud-redis]=nextcloud [nextcloud-app]=nextcloud
  [pihole]=pihole [caddy]=caddy [stirling-pdf]=stirling
  [it-tools]=it-tools [imagetools]=imagetools
)
declare -A SVC=(
  [vaultwarden]=vaultwarden
  [nextcloud-db]=db [nextcloud-redis]=redis [nextcloud-app]=app
  [pihole]=pihole [caddy]=caddy [stirling-pdf]=stirling-pdf
  [it-tools]=it-tools [imagetools]=imagetools
)

# Start clean so a removed manifest line doesn't leave a stale override behind.
for d in vaultwarden nextcloud pihole caddy stirling it-tools imagetools; do
  rm -f "$REPO/$d/docker-compose.override.yml"
done

declare -A BODY
while IFS='=' read -r key img; do
  key="$(echo "$key" | tr -d '[:space:]')"
  img="$(echo "$img" | tr -d '[:space:]')"
  [ -z "$key" ] && continue
  case "$key" in \#*) continue ;; esac
  d="${DIR[$key]:-}"; s="${SVC[$key]:-}"
  if [ -z "$d" ]; then echo "  warn: unknown component '$key' (ignored)"; continue; fi
  BODY[$d]="${BODY[$d]:-}  ${s}:"$'\n'"    image: ${img}"$'\n'
  echo "  $key -> $img"
done < "$MANIFEST"

for d in "${!BODY[@]}"; do
  { echo "services:"; printf '%s' "${BODY[$d]}"; } > "$REPO/$d/docker-compose.override.yml"
done
