# Runs hsctl's integration tests (built with -tags integration) against REAL dependencies — a
# nested Docker daemon, restic, root, block devices — in a throwaway container, so they never touch
# the host's Docker, containers or volumes. Driven by `make test-integration` (run-integration.sh).
FROM docker:27-dind
# restic for the backups, sqlite to read Vaultwarden's database, util-linux + e2fsprogs + eudev for
# the Drives page's lsblk/mount on a loop device, iproute2 for the free-port check (ss), sudo so a
# normal user's run takes the path it takes on a real box.
RUN apk add --no-cache bash restic sqlite util-linux coreutils e2fsprogs eudev iproute2 sudo ca-certificates
ENV DOCKER_TLS_CERTDIR=""
COPY in-container.sh /in-container.sh
RUN chmod +x /in-container.sh
ENTRYPOINT ["/in-container.sh"]
