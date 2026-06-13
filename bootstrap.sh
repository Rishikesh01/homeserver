#!/usr/bin/env bash
# Reproducible setup. Generates every service's .env (with FRESH random secrets)
# plus pihole/custom.list, from the single config block below.
#
#   ./bootstrap.sh                 # create any missing .env (never clobbers existing)
#   SERVER_IP=192.168.1.50 ./bootstrap.sh   # override for a different host
#   ./bootstrap.sh --force         # DESTRUCTIVE: regenerate ALL secrets (see warning)
#
# Idempotent by default: an existing .env is left untouched, so re-running is safe
# and won't desync a password from already-initialised data (e.g. the Postgres
# volume). --force regenerates secrets and WILL break a stack that already has data.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

# ----------------------------- config (override via env) ----------------------
SERVER_IP="${SERVER_IP:-192.168.0.150}"   # this host's LAN IP — DNS + Caddy upstreams
TZ_VAL="${TZ:-Asia/Kolkata}"
VAULT_HOST="${VAULT_HOST:-vault.lan}"
CLOUD_HOST="${CLOUD_HOST:-cloud.lan}"
PIHOLE_HOST="${PIHOLE_HOST:-pihole.lan}"
ACME_EMAIL="${ACME_EMAIL:-you@example.com}"
TLS_DIRECTIVE="${TLS_DIRECTIVE:-tls internal}"
# Vaultwarden is on 8082 (k8s already holds 8080 on this box). Change if needed.
VW_HTTP_PORT="${VW_HTTP_PORT:-8082}"
NC_HTTP_PORT="${NC_HTTP_PORT:-8081}"
PIHOLE_WEB_PORT="${PIHOLE_WEB_PORT:-8053}"
# systemd-resolved holds :53 on the loopback stub here, so bind Pi-hole to the LAN IP.
PIHOLE_DNS_BIND="${PIHOLE_DNS_BIND:-$SERVER_IP}"
VW_SIGNUPS_ALLOWED="${VW_SIGNUPS_ALLOWED:-true}"   # open so family can self-register
WG_SUBNET="${WG_SUBNET:-${SERVER_IP%.*}.0/24}"     # assumes a /24 LAN
# -----------------------------------------------------------------------------

FORCE=0; [ "${1:-}" = "--force" ] && FORCE=1
command -v python3 >/dev/null || { echo "need python3 (for bcrypt)"; exit 1; }
python3 -c 'import bcrypt' 2>/dev/null || { echo "need python3 'bcrypt' module: pip install bcrypt"; exit 1; }
umask 077
genpw() { python3 -c "import secrets,string,sys; print(''.join(secrets.choice(string.ascii_letters+string.digits) for _ in range(int(sys.argv[1]))))" "$1"; }
bcrypt() { python3 -c "import bcrypt,sys; print(bcrypt.hashpw(sys.argv[1].encode(), bcrypt.gensalt()).decode())" "$1"; }

SECRETS_FILE=".secrets.txt"
note_secret() { printf '%-26s %s\n' "$1" "$2" >> "$SECRETS_FILE"; }

# write_env <path>  (content on stdin). Skips if it exists unless --force.
write_env() {
  local path="$1"
  if [ -f "$path" ] && [ "$FORCE" -ne 1 ]; then echo "skip   $path (exists)"; cat >/dev/null; return; fi
  cat > "$path"; echo "write  $path"
}

: > "$SECRETS_FILE.new"   # collect this run's human logins here, promote at the end
SECRETS_FILE="$SECRETS_FILE.new"

# ---- vaultwarden ----
VW_TOKEN=$(genpw 40)
write_env vaultwarden/.env <<EOF
VW_DOMAIN=https://${VAULT_HOST}
VW_ADMIN_TOKEN=${VW_TOKEN}
VW_SIGNUPS_ALLOWED=${VW_SIGNUPS_ALLOWED}
VW_HTTP_PORT=${VW_HTTP_PORT}
EOF
[ -f vaultwarden/.env ] && grep -q "VW_ADMIN_TOKEN=${VW_TOKEN}" vaultwarden/.env && note_secret "Vaultwarden /admin token:" "$VW_TOKEN"

# ---- nextcloud ----
PG_PW=$(genpw 32); REDIS_PW=$(genpw 32); NC_PW=$(genpw 20)
write_env nextcloud/.env <<EOF
POSTGRES_DB=nextcloud
POSTGRES_USER=nextcloud
POSTGRES_PASSWORD=${PG_PW}
REDIS_PASSWORD=${REDIS_PW}
NC_ADMIN_USER=admin
NC_ADMIN_PASSWORD=${NC_PW}
NC_TRUSTED_DOMAINS=${SERVER_IP} ${CLOUD_HOST}
NC_TRUSTED_PROXIES=172.16.0.0/12
NC_HTTP_PORT=${NC_HTTP_PORT}
EOF
[ -f nextcloud/.env ] && grep -q "NC_ADMIN_PASSWORD=${NC_PW}" nextcloud/.env && note_secret "Nextcloud (user 'admin'):" "$NC_PW"

# ---- pihole ----
PIHOLE_PW=$(genpw 20)
write_env pihole/.env <<EOF
TZ=${TZ_VAL}
PIHOLE_PASSWORD=${PIHOLE_PW}
PIHOLE_UPSTREAMS=1.1.1.1;9.9.9.9
PIHOLE_WEB_PORT=${PIHOLE_WEB_PORT}
PIHOLE_DNS_BIND=${PIHOLE_DNS_BIND}
EOF
[ -f pihole/.env ] && grep -q "PIHOLE_PASSWORD=${PIHOLE_PW}" pihole/.env && note_secret "Pi-hole admin:" "$PIHOLE_PW"

# pihole/custom.list — *.lan -> this server
if [ ! -f pihole/custom.list ] || [ "$FORCE" -eq 1 ]; then
  { printf '%s %s\n' "$SERVER_IP" "$VAULT_HOST"
    printf '%s %s\n' "$SERVER_IP" "$CLOUD_HOST"
    printf '%s %s\n' "$SERVER_IP" "$PIHOLE_HOST"; } > pihole/custom.list
  echo "write  pihole/custom.list"
else echo "skip   pihole/custom.list (exists)"; fi

# ---- caddy ----
write_env caddy/.env <<EOF
VAULT_HOST=${VAULT_HOST}
CLOUD_HOST=${CLOUD_HOST}
PIHOLE_HOST=${PIHOLE_HOST}
TLS_DIRECTIVE=${TLS_DIRECTIVE}
ACME_EMAIL=${ACME_EMAIL}
VAULT_UPSTREAM=host.docker.internal:${VW_HTTP_PORT}
CLOUD_UPSTREAM=host.docker.internal:${NC_HTTP_PORT}
PIHOLE_UPSTREAM=host.docker.internal:${PIHOLE_WEB_PORT}
EOF

# ---- wireguard ----
# NOTE: Compose interpolates env_file values, so the bcrypt "$" must be doubled to
# "$$" here. printf '%s' writes the variable's literal value (no re-expansion).
WGUI_PW=$(genpw 20)
WGUI_HASH=$(bcrypt "$WGUI_PW")
WGUI_HASH_ESC=${WGUI_HASH//\$/\$\$}
if [ ! -f wireguard/.env ] || [ "$FORCE" -eq 1 ]; then
  { printf 'WG_HOST=%s\n'                 "$SERVER_IP"
    printf 'PASSWORD_HASH=%s\n'           "$WGUI_HASH_ESC"
    printf 'WG_PORT=51820\n'
    printf 'WG_DEFAULT_ADDRESS=10.8.0.x\n'
    printf 'WG_DEFAULT_DNS=%s\n'          "$SERVER_IP"
    printf 'WG_ALLOWED_IPS=%s\n'          "$WG_SUBNET"
    printf 'WG_PERSISTENT_KEEPALIVE=25\n'; } > wireguard/.env
  echo "write  wireguard/.env"
  note_secret "wg-easy web UI:" "$WGUI_PW"
else echo "skip   wireguard/.env (exists)"; fi

# promote this run's secrets file
SECRETS_FILE=".secrets.txt"
if [ -s "$SECRETS_FILE.new" ]; then
  { echo "# Generated $(date -u 2>/dev/null || echo 'at setup') — store these in Vaultwarden, then you can delete this file."
    cat "$SECRETS_FILE.new"; } >> "$SECRETS_FILE"
  echo
  echo "=================== NEW LOGINS (also saved to $SECRETS_FILE) ==================="
  cat "$SECRETS_FILE.new"
  echo "==============================================================================="
fi
rm -f "$SECRETS_FILE.new"
echo
echo "Done. Next: ./up.sh   (then ./get-ca.sh to grab the Caddy root cert)"
