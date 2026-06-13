#!/usr/bin/env bash
# Reproducible setup for the homeserver stack. This repo is a TEMPLATE — nothing
# here is tied to a particular machine. bootstrap.sh asks for everything you can
# configure (LAN IP, email, timezone, hostnames, ports, TLS mode), generates each
# service's .env with fresh random secrets, and saves your answers to setup.conf
# so later runs are non-interactive.
#
#   ./bootstrap.sh            # interactive (Enter accepts each [default])
#   ./bootstrap.sh --yes      # non-interactive: take all defaults / setup.conf / env
#   ./bootstrap.sh --force     # DESTRUCTIVE: regenerate ALL secrets (breaks live data)
#
# Defaults come from: an existing setup.conf, then matching env vars, then sensible
# autodetected values. Existing .env files are never overwritten unless --force.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
CONF="setup.conf"   # gitignored; your (non-secret) answers, safe to edit + re-run

command -v python3 >/dev/null || { echo "need python3 (for bcrypt + secrets)"; exit 1; }
python3 -c 'import bcrypt' 2>/dev/null || { echo "need python3 'bcrypt': pip install bcrypt"; exit 1; }

# ----------------------------- helpers ----------------------------------------
det_ip()  { ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src"){print $(i+1);exit}}'; }
det_tz()  { timedatectl show -p Timezone --value 2>/dev/null || cat /etc/timezone 2>/dev/null || echo "Etc/UTC"; }
busy()    { ss -ltn 2>/dev/null | grep -q ":$1 "; }                 # is a TCP port in use?
# pick <startport> -> REPLY = first port that's neither in use nor already claimed
# this run. Runs in the main shell (not a subshell) so _USED accumulates correctly.
_USED=""
pick()    { local p=$1; while busy "$p" || [[ " $_USED " == *" $p "* ]]; do p=$((p+1)); done; _USED="$_USED $p"; REPLY=$p; }
genpw()   { python3 -c "import secrets,string,sys;print(''.join(secrets.choice(string.ascii_letters+string.digits) for _ in range(int(sys.argv[1]))))" "$1"; }
bcrypt()  { python3 -c "import bcrypt,sys;print(bcrypt.hashpw(sys.argv[1].encode(),bcrypt.gensalt()).decode())" "$1"; }

ASSUME_YES=0; FORCE=0
for a in "$@"; do case "$a" in --yes|-y) ASSUME_YES=1;; --force) FORCE=1;; esac; done
INTERACTIVE=0; [ "$ASSUME_YES" -eq 0 ] && [ -e /dev/tty ] && INTERACTIVE=1

# ask VAR "prompt" "default" — preset (env/setup.conf) wins; else prompt or default
ask() {
  local var="$1" prompt="$2" def="$3" ans
  [ -n "${!var:-}" ] && return
  if [ "$INTERACTIVE" -eq 1 ]; then
    read -r -p "  $prompt [$def]: " ans </dev/tty || ans=""
    printf -v "$var" '%s' "${ans:-$def}"
  else
    printf -v "$var" '%s' "$def"
  fi
}
askyn() {  # askyn VAR "prompt" "y|n"
  local var="$1" prompt="$2" def="$3" ans
  [ -n "${!var:-}" ] && return
  if [ "$INTERACTIVE" -eq 1 ]; then
    read -r -p "  $prompt [$def]: " ans </dev/tty || ans=""; ans="${ans:-$def}"
  else ans="$def"; fi
  case "$ans" in [Yy]*) printf -v "$var" yes;; *) printf -v "$var" no;; esac
}

# ----------------------------- gather config ----------------------------------
[ -f "$CONF" ] && { echo "Using saved answers from $CONF (delete it to reconfigure)"; . "./$CONF"; }

echo "== Configure  (press Enter to accept each [default]) =="
ask SERVER_IP       "Server LAN IP"                                   "$(det_ip || echo 192.168.1.10)"
ask TZ_VAL          "Timezone"                                        "$(det_tz)"
ask ACME_EMAIL      "Admin email (Let's Encrypt contact)"             "you@example.com"
ask VAULT_HOST      "Vaultwarden hostname"                            "vault.lan"
ask CLOUD_HOST      "Nextcloud hostname"                              "cloud.lan"
ask PIHOLE_HOST     "Pi-hole hostname"                                "pihole.lan"
ask CA_HOST         "Cert-download hostname"                          "ca.lan"
askyn USE_LE        "Use Let's Encrypt instead of a local CA? (needs a real public domain)" "n"
pick 8080; _vw=$REPLY; pick 8081; _nc=$REPLY; pick 8053; _ph=$REPLY
ask VW_HTTP_PORT    "Vaultwarden host port"                           "$_vw"
ask NC_HTTP_PORT    "Nextcloud host port"                             "$_nc"
ask PIHOLE_WEB_PORT "Pi-hole web port"                                "$_ph"
if busy 53; then _dnsdef="$SERVER_IP"; else _dnsdef="0.0.0.0"; fi
ask PIHOLE_DNS_BIND "Pi-hole DNS bind (LAN IP if another resolver holds :53, else 0.0.0.0)" "$_dnsdef"
askyn _signups      "Allow open Vaultwarden signups?"                 "y"
: "${VW_SIGNUPS_ALLOWED:=$([ "$_signups" = yes ] && echo true || echo false)}"
TLS_DIRECTIVE=$([ "$USE_LE" = yes ] && echo "" || echo "tls internal")

umask 077
{ echo "# Saved by bootstrap.sh — your configuration (NOT secrets). Edit + re-run freely."
  for v in SERVER_IP TZ_VAL ACME_EMAIL VAULT_HOST CLOUD_HOST PIHOLE_HOST CA_HOST USE_LE \
           VW_HTTP_PORT NC_HTTP_PORT PIHOLE_WEB_PORT PIHOLE_DNS_BIND \
           VW_SIGNUPS_ALLOWED; do printf '%s=%q\n' "$v" "${!v}"; done; } > "$CONF"
echo "Saved $CONF"

# ----------------------------- generate .env files ----------------------------
SECRETS_NEW=".secrets.txt.new"; : > "$SECRETS_NEW"
note() { printf '%-26s %s\n' "$1" "$2" >> "$SECRETS_NEW"; }
write_env() {  # write_env <path> (content on stdin); skips existing unless --force
  local path="$1"
  if [ -f "$path" ] && [ "$FORCE" -ne 1 ]; then echo "skip   $path (exists)"; cat >/dev/null; return; fi
  cat > "$path"; echo "write  $path"
}

VW_TOKEN=$(genpw 40)
write_env vaultwarden/.env <<EOF
VW_DOMAIN=https://${VAULT_HOST}
VW_ADMIN_TOKEN=${VW_TOKEN}
VW_SIGNUPS_ALLOWED=${VW_SIGNUPS_ALLOWED}
VW_HTTP_PORT=${VW_HTTP_PORT}
EOF
[ -f vaultwarden/.env ] && grep -q "VW_ADMIN_TOKEN=${VW_TOKEN}" vaultwarden/.env && note "Vaultwarden /admin token:" "$VW_TOKEN"

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
[ -f nextcloud/.env ] && grep -q "NC_ADMIN_PASSWORD=${NC_PW}" nextcloud/.env && note "Nextcloud (user 'admin'):" "$NC_PW"

PIHOLE_PW=$(genpw 20)
write_env pihole/.env <<EOF
TZ=${TZ_VAL}
PIHOLE_PASSWORD=${PIHOLE_PW}
PIHOLE_UPSTREAMS=1.1.1.1;9.9.9.9
PIHOLE_WEB_PORT=${PIHOLE_WEB_PORT}
PIHOLE_DNS_BIND=${PIHOLE_DNS_BIND}
EOF
[ -f pihole/.env ] && grep -q "PIHOLE_PASSWORD=${PIHOLE_PW}" pihole/.env && note "Pi-hole admin:" "$PIHOLE_PW"

if [ ! -f pihole/custom.list ] || [ "$FORCE" -eq 1 ]; then
  { for h in "$VAULT_HOST" "$CLOUD_HOST" "$PIHOLE_HOST" "$CA_HOST"; do printf '%s %s\n' "$SERVER_IP" "$h"; done; } > pihole/custom.list
  echo "write  pihole/custom.list"
else echo "skip   pihole/custom.list (exists)"; fi

write_env caddy/.env <<EOF
VAULT_HOST=${VAULT_HOST}
CLOUD_HOST=${CLOUD_HOST}
PIHOLE_HOST=${PIHOLE_HOST}
TLS_DIRECTIVE=${TLS_DIRECTIVE}
ACME_EMAIL=${ACME_EMAIL}
VAULT_UPSTREAM=host.docker.internal:${VW_HTTP_PORT}
CLOUD_UPSTREAM=host.docker.internal:${NC_HTTP_PORT}
PIHOLE_UPSTREAM=host.docker.internal:${PIHOLE_WEB_PORT}
CA_HOSTS=http://${CA_HOST} http://${SERVER_IP}
EOF

# personalized quick-reference for handing to users (gitignored)
cat > WELCOME.txt <<EOF
Homeserver — quick reference
  Install the certificate first:  http://${SERVER_IP}/        (download root.crt)
  Passwords (Vaultwarden):        http://${SERVER_IP}:${VW_HTTP_PORT}   /  https://${VAULT_HOST}
  Files (Nextcloud):              http://${SERVER_IP}:${NC_HTTP_PORT}   /  https://${CLOUD_HOST}
  Pi-hole admin:                  http://${SERVER_IP}:${PIHOLE_WEB_PORT}/admin  /  https://${PIHOLE_HOST}
Step-by-step per device: see ONBOARDING.md
EOF
echo "write  WELCOME.txt"

# ----------------------------- promote secrets + summary ----------------------
if [ -s "$SECRETS_NEW" ]; then
  { echo "# Generated at setup — store these in Vaultwarden, then delete this file."; cat "$SECRETS_NEW"; } >> .secrets.txt
  echo; echo "============ NEW LOGINS (also saved to .secrets.txt) ============"; cat "$SECRETS_NEW"
  echo "================================================================="
fi
rm -f "$SECRETS_NEW"
echo; echo "Done. Next: ./up.sh   then   ./get-ca.sh"
