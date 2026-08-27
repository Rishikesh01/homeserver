#!/bin/sh
# homeserver installer — https://github.com/Rishikesh01/homeserver
#
#   curl -fsSL https://raw.githubusercontent.com/Rishikesh01/homeserver/main/install.sh | sh
#
# Puts the prebuilt hsctl binary in /usr/local/bin and the repo (compose files, Caddy
# config, dashboard assets — hsctl needs them at runtime) in /opt/homeserver. It does
# not start anything, enable a service, or write any config: it leaves you one command
# away from a running stack and prints it.
#
# Optional knobs:
#   VERSION=v1.8.0            install a specific release        (default: latest)
#   HOMESERVER_DIR=/srv/hs    where the repo goes               (default: /opt/homeserver)
#   PREFIX=/usr               binary goes in $PREFIX/bin        (default: /usr/local)

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

case "$(uname -m)" in
	x86_64 | amd64)  ARCH=amd64 ;;
	aarch64 | arm64) ARCH=arm64 ;;
	armv7l | armv6l | armhf)
		die "32-bit ARM isn't supported: IT-Tools and Stirling-PDF publish arm64 images
       only, so the stack can't run even though hsctl itself would build. On a Pi 4/5,
       reinstall with the 64-bit Raspberry Pi OS and run this again." ;;
	*) die "unsupported architecture $(uname -m) — releases cover x86_64 and arm64." ;;
esac

SUDO=""
if [ "$(id -u)" -ne 0 ]; then
	command -v sudo >/dev/null 2>&1 || die "not running as root and sudo isn't installed."
	SUDO="sudo"
fi

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
	[ -n "$VERSION" ] || die "couldn't read the latest release tag — pass one, e.g. VERSION=v1.8.0"
fi

TARBALL="hsctl_${VERSION}_linux_${ARCH}.tar.gz"
BASE="https://github.com/$REPO/releases/download/$VERSION"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM

# ---------------------------------------------------------------- binary

step "Downloading hsctl $VERSION for linux/$ARCH"
fetch "$BASE/$TARBALL" "$TMP/$TARBALL" \
	|| die "couldn't download $BASE/$TARBALL (reason above).
       See https://github.com/$REPO/releases for what $VERSION actually published."
fetch "$BASE/checksums.txt" "$TMP/checksums.txt" \
	|| die "couldn't download checksums.txt for $VERSION."

# grep keeps sha256sum to the one file we downloaded; the release lists every platform.
( cd "$TMP" && grep " ${TARBALL}\$" checksums.txt | sha256sum -c - >/dev/null 2>&1 ) \
	|| die "checksum mismatch on $TARBALL — refusing to install."

tar -xzf "$TMP/$TARBALL" -C "$TMP"
$SUDO install -m 0755 "$TMP/hsctl" "$PREFIX/bin/hsctl"
say "    $PREFIX/bin/hsctl"

# ---------------------------------------------------------------- repo

# hsctl finds the stack by walking up for caddy/docker-compose.yml, so the checkout has
# to sit on disk next to the binary's working directory — and has to match the binary's
# version, since a release moves compose pins and dashboard assets together.
if [ -d "$HOMESERVER_DIR/.git" ]; then
	step "Updating $HOMESERVER_DIR to $VERSION"
	$SUDO git -C "$HOMESERVER_DIR" fetch --quiet --tags origin
	$SUDO git -C "$HOMESERVER_DIR" -c advice.detachedHead=false checkout --quiet "$VERSION" || die \
		"couldn't check out $VERSION in $HOMESERVER_DIR — there are local changes there
       (\`hsctl updates\` edits the compose files in place). Commit or stash them and
       run this again; your data volumes and config are untouched either way."
elif [ -e "$HOMESERVER_DIR" ]; then
	die "$HOMESERVER_DIR exists but isn't a git checkout — move it aside, or set
       HOMESERVER_DIR=/somewhere/else and run this again."
else
	step "Cloning the stack into $HOMESERVER_DIR"
	$SUDO git -c advice.detachedHead=false clone --quiet --branch "$VERSION" "https://github.com/$REPO.git" "$HOMESERVER_DIR"
fi
say "    $HOMESERVER_DIR"

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
