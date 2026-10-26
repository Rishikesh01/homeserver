#!/bin/bash
# Inside the integration-test container: start the nested Docker daemon, then run every compiled
# test binary from its package's source folder (tests read fixtures relative to it).
set -u
# The tests' registry (itest.StartRegistry) is reached by name, so every client uses TLS. Added
# before dockerd starts: Go programs cache /etc/hosts.
echo "127.0.0.1 registry.hsctl.test" >> /etc/hosts
dockerd-entrypoint.sh dockerd >/var/log/dockerd.log 2>&1 &
for _ in $(seq 1 60); do docker info >/dev/null 2>&1 && break; sleep 1; done
if ! docker info >/dev/null 2>&1; then
  echo "nested dockerd didn't start:"; tail -n 30 /var/log/dockerd.log; exit 1
fi
# The daemon's state is cached between runs for its images; drop anything else a crashed run left.
docker rm -f $(docker ps -aq) >/dev/null 2>&1
docker volume prune -af >/dev/null
docker network prune -f >/dev/null
[ -s /tests/index.tsv ] || { echo "no test binaries to run"; exit 1; }
failed=()
while IFS=$'\t' read -r bin dir; do
  [ -n "$bin" ] || continue
  echo "=== $dir"
  (cd "/src/$dir" && "/tests/$bin" -test.count=1 -test.timeout=20m ${TESTV:+-test.v} ${TESTRUN:+-test.run "$TESTRUN"}) || failed+=("$dir")
done < /tests/index.tsv
if [ ${#failed[@]} -gt 0 ]; then
  echo; echo "FAILED: ${failed[*]}"; exit 1
fi
echo; echo "all integration tests passed"
