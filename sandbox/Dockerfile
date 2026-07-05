# Throwaway test sandbox: a full homeserver-in-a-box with its OWN nested Docker daemon
# (docker-in-docker), so you can try image/hsctl/restic updates and restores without
# touching the host's Docker, containers, or volumes.
#
# restic comes from THIS base image's distro. To test a different restic version, bump
# the base tag below (e.g. a newer docker:dind, which tracks a newer Alpine) and rebuild.
FROM docker:27-dind

# Talk to the nested daemon over the local unix socket (no TLS needed inside the box).
ENV DOCKER_TLS_CERTDIR=""

# bash + the disk tools the Drives page uses, restic for backups, eudev so lsblk reports
# filesystem labels. restic is pulled from the base image's Alpine repo (see note above).
RUN apk add --no-cache bash util-linux e2fsprogs eudev restic coreutils ca-certificates

# hsctl is built on the host from the working tree (so you test YOUR hsctl) and dropped
# in by the Makefile before this build — see `make sandbox`.
COPY sandbox/_build/hsctl /usr/local/bin/hsctl
COPY sandbox/entrypoint.sh sandbox/apply-images.sh sandbox/restore.sh /sandbox/
COPY sandbox/images.env /sandbox/images.env

# The repo (compose files, services.json, …). Secrets/data are kept out by .dockerignore;
# the sandbox generates its OWN .env via `hsctl setup` at start.
COPY . /repo

RUN chmod +x /usr/local/bin/hsctl /sandbox/*.sh

WORKDIR /repo
ENV HSCTL_UI_PASSWORD=test
EXPOSE 8088
ENTRYPOINT ["/sandbox/entrypoint.sh"]
