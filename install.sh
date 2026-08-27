#!/bin/sh
# homeserver installer — https://github.com/Rishikesh01/homeserver
#
#   curl -fsSL https://raw.githubusercontent.com/Rishikesh01/homeserver/main/install.sh | sh
#
# ONE-TIME bootstrap for a fresh machine: it puts the prebuilt hsctl binary in
# /usr/local/bin and the repo (compose files, Caddy config, dashboard assets — hsctl
# needs them at runtime) in /opt/homeserver, owned by your user so the everyday hsctl
# commands work without sudo. It does not start anything, enable a service, or write any
# config: it leaves you one command away from a running stack and prints it. It refuses
# to touch an existing install — from then on everything is managed by hsctl and the
# dashboard (app updates land in gitignored .env files, never in the checkout).
#
# Optional knobs — they must reach the SHELL, so put them before `sh`, not before curl
# (`VERSION=… curl … | sh` sets the variable for curl only and silently does nothing):
#
#   curl -fsSL .../install.sh | VERSION=v1.8.0 sh          a specific release  (default: latest)
#   curl -fsSL .../install.sh | HOMESERVER_DIR=/srv/hs sh  where the repo goes (default: /opt/homeserver)
#   curl -fsSL .../install.sh | PREFIX=/usr sh             binary in $PREFIX/bin (default: /usr/local)

set -eu

REPO="Rishikesh01/homeserver"
HOMESERVER_DIR="${HOMESERVER_DIR:-/opt/homeserver}"
PREFIX="${PREFIX:-/usr/local}"
VERSION="${VERSION:-}"

if [ -t 1 ]; then B="$(printf '\033[1m')"; Y="$(printf '\033[33m')"; R="$(printf '\033[31m')"; N="$(printf '\033[0m')"
else B=""; Y=""; R=""; N=""; fi

say()  { printf '%s\n' "$*"; }
step() { printf '%s==>%s %s\n' "$B" "$N" "$*"; }
warn() { printf '%swarning:%s %s\n' "$Y" "$N" "$*" >&2; }
die()  { printf '%serror:%s %s\n' "$R" "$N" "$*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed${2:+ — $2}"; }

# ---------------------------------------------------------------- what are we on

[ "$(uname -s)" = "Linux" ] || die "homeserver runs on Linux only (this is $(uname -s))."

die32() {
	die "this OS is 32-bit, and IT-Tools and Stirling-PDF publish arm64 images only, so
       the stack can't run even though hsctl itself would build. On a Pi 4/5, reinstall
       with the 64-bit Raspberry Pi OS and run this again."
}

case "$(uname -m)" in
	x86_64 | amd64)  ARCH=amd64 ;;
	aarch64 | arm64) ARCH=arm64 ;;
	armv7l | armv6l | armhf) die32 ;;
	*) die "unsupported architecture $(uname -m) — releases cover x86_64 and arm64." ;;
esac

# uname -m answers for the KERNEL, and 32-bit Raspberry Pi OS boots a 64-bit kernel by
# default — so aarch64 above can still mean an armhf userland, which Docker would map to
# linux/arm/v7 and fail to pull. getconf is a userland binary, so it answers for the
# layer Docker actually uses.
[ "$(getconf LONG_BIT 2>/dev/null || echo 64)" = "32" ] && die32

# One-time only: never touch an existing install. Fail before downloading anything.
[ ! -e "$HOMESERVER_DIR" ] || die "$HOMESERVER_DIR already exists — homeserver looks installed, and this installer
       only does fresh installs. App updates come from \`hsctl updates\` and the
       dashboard. To truly start over, move $HOMESERVER_DIR aside (or set
       HOMESERVER_DIR=/somewhere/else) and run this again."

SUDO=""
if [ "$(id -u)" -ne 0 ]; then
	command -v sudo >/dev/null 2>&1 || die "not running as root and sudo isn't installed."
	SUDO="sudo"
fi

# The repo checkout must end up owned by the login user — the everyday hsctl commands
# (setup, updates --apply, get-ca, secrets) run without sudo and read/write in it.
# Under `curl | sh` that's us; under `curl | sudo sh` SUDO_USER names the real user.
if [ "$(id -u)" -eq 0 ]; then OWNER="${SUDO_USER:-root}"; else OWNER="$(id -un)"; fi

need git
need tar
need sha256sum "install coreutils"
command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1 || die "need curl or wget."

fetch() { # fetch URL FILE
	if command -v curl >/dev/null 2>&1; then curl -fsSL "$1" -o "$2"
	else wget -qO "$2" "$1"; fi
}
fetch_out() { # fetch URL -> stdout
	if command -v curl >/dev/null 2>&1; then curl -fsSL "$1"
	else wget -qO- "$1"; fi
}

# ---------------------------------------------------------------- pick the release

if [ -z "$VERSION" ]; then
	step "Looking up the latest release"
	VERSION=$(fetch_out "https://api.github.com/repos/$REPO/releases/latest" \
		| sed -n 's/.*"tag_name" *: *"\([^"]*\)".*/\1/p' | head -n1)
	[ -n "$VERSION" ] || die "couldn't read the latest release tag — pass one, e.g. \`curl … | VERSION=v1.8.0 sh\`"
fi

TARBALL="hsctl_${VERSION}_linux_${ARCH}.tar.gz"
BASE="https://github.com/$REPO/releases/download/$VERSION"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM

# ---------------------------------------------------------------- download + verify

step "Downloading hsctl $VERSION for linux/$ARCH"
fetch "$BASE/$TARBALL" "$TMP/$TARBALL" \
	|| die "couldn't download $BASE/$TARBALL.
       See https://github.com/$REPO/releases for what $VERSION actually published."
fetch "$BASE/checksums.txt" "$TMP/checksums.txt" \
	|| die "couldn't download checksums.txt for $VERSION."

# grep keeps sha256sum to the one file we downloaded; the release lists every platform.
line=$(grep " ${TARBALL}\$" "$TMP/checksums.txt") \
	|| die "checksums.txt for $VERSION has no entry for $TARBALL — was linux/$ARCH published for this release?"
( cd "$TMP" && printf '%s\n' "$line" | sha256sum -c - >/dev/null 2>&1 ) \
	|| die "checksum mismatch on $TARBALL — refusing to install."
tar -xzf "$TMP/$TARBALL" -C "$TMP"

# ---------------------------------------------------------------- repo

# hsctl finds the stack by walking up for caddy/docker-compose.yml, so the checkout has
# to sit on disk, at the same release as the binary (a release moves compose defaults
# and dashboard assets together). Cloned as $OWNER so every file belongs to the login
# user, not root.
step "Cloning the stack into $HOMESERVER_DIR"
$SUDO mkdir -p "$HOMESERVER_DIR"
$SUDO chown "$OWNER" "$HOMESERVER_DIR"
if [ "$(id -un)" = "$OWNER" ]; then
	git -c advice.detachedHead=false clone --quiet --branch "$VERSION" "https://github.com/$REPO.git" "$HOMESERVER_DIR"
else
	sudo -u "$OWNER" git -c advice.detachedHead=false clone --quiet --branch "$VERSION" "https://github.com/$REPO.git" "$HOMESERVER_DIR"
fi
say "    $HOMESERVER_DIR"

# ---------------------------------------------------------------- binary

step "Installing hsctl to $PREFIX/bin"
$SUDO mkdir -p "$PREFIX/bin"
$SUDO install -m 0755 "$TMP/hsctl" "$PREFIX/bin/hsctl"
say "    $PREFIX/bin/hsctl"

# ---------------------------------------------------------------- what's still missing

if ! command -v docker >/dev/null 2>&1; then
	warn "Docker isn't installed — the stack needs it:  curl -fsSL https://get.docker.com | sh"
elif ! docker compose version >/dev/null 2>&1; then
	warn "Docker Compose v2 isn't available (\`docker compose version\` failed) — install
         the docker-compose-plugin package."
fi
command -v restic >/dev/null 2>&1 \
	|| say "    (optional: \`sudo apt install -y restic\` to enable backups)"

say ""
say "${B}hsctl $VERSION is installed.${N} One command left:"
say ""
say "    cd $HOMESERVER_DIR && sudo hsctl install"
say ""
say "That starts the dashboard and prints its URL and your admin password; the rest of"
say "the setup happens in the browser."
