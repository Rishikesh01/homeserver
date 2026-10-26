#!/bin/bash
# make test-integration: compile each package's integration tests and run them as root against a
# real Docker daemon, restic and block devices inside a throwaway Docker-in-Docker container.
# Nothing touches the host's Docker. Pulled images are cached in the hsctl-itest-docker volume
# between runs (`docker volume rm hsctl-itest-docker` drops them).
#   PKG=./internal/backup  only that package (several, space-separated)
#   TESTRUN=<regexp>       only matching tests
#   TESTV=1                verbose
set -euo pipefail
cd "$(dirname "$0")/.."                       # hsctl/
root="$(cd .. && pwd)"                        # the homeserver repo
go="${GO:-go}"
out="$(mktemp -d)"
chmod 755 "$out"                              # the unprivileged re-runs (itest.Unprivileged) read it too
trap 'rm -rf "$out"' EXIT
: > "$out/index.tsv"
# shellcheck disable=SC2086  # PKG may name several packages
dirs="$("$go" list -tags integration -f '{{if or .TestGoFiles .XTestGoFiles}}{{.Dir}}{{end}}' ${PKG:-./...})"
[ -n "$dirs" ] || { echo "no integration tests in ${PKG:-./...}" >&2; exit 1; }
for dir in $dirs; do
  rel="${dir#"$root"/}"
  bin="$(echo "$rel" | tr '/' '_').test"
  CGO_ENABLED=0 "$go" test -c -tags integration -o "$out/$bin" "$dir"
  printf '%s\t%s\n' "$bin" "$rel" >> "$out/index.tsv"
done
# The real binary, for the tests that run hsctl itself (dashboard jobs).
CGO_ENABLED=0 "$go" build -o "$out/hsctl" .
docker build -q -t hsctl-itest test >/dev/null
docker run --rm --privileged \
  -e TESTRUN="${TESTRUN:-}" -e TESTV="${TESTV:-}" -e HSCTL_BIN=/tests/hsctl \
  -v hsctl-itest-docker:/var/lib/docker \
  -v "$out":/tests:ro -v "$root":/src:ro \
  hsctl-itest
