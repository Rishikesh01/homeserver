#!/usr/bin/env bash
# Extract Caddy's internal root CA so you can install it on each device
# (browsers + the Bitwarden/Nextcloud apps must trust it to use the apps over HTTPS).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

D="docker"; docker info >/dev/null 2>&1 || D="sudo docker"
OUT="caddy-root-ca.crt"

$D exec caddy cat /data/caddy/pki/authorities/local/root.crt > "$OUT"
[ -w "$OUT" ] || true
echo "wrote $(pwd)/$OUT"
echo
echo "Install it as a trusted CA:"
echo "  Linux (this box): sudo cp $OUT /usr/local/share/ca-certificates/ && sudo update-ca-certificates"
echo "  Phones/laptops:   copy the file over, then follow ONBOARDING.md"
