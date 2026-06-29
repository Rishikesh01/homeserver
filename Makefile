# Test sandbox for the homeserver — a throwaway, fully isolated copy you spin up to try
# image updates, hsctl updates, restic updates, or a real restore, WITHOUT touching your
# live system. It runs its own nested Docker daemon (docker-in-docker); nothing it does
# reaches the host's Docker, containers, or volumes.
#
#   make sandbox           build + start the sandbox (admin UI on http://localhost:PORT)
#   make sandbox-restore   restore a real backup into it so you can see your data
#   make sandbox-shell     open a shell inside the sandbox
#   make sandbox-logs      follow the sandbox logs
#   make sandbox-down      stop + clean up everything
#
# Knobs (override on the command line, e.g. `make sandbox PORT=19000`):
#   IMAGES    image manifest to use            (default sandbox/images.env)
#   PORT      host port for the admin UI        (default 18088)
#   PASS      admin password in the sandbox     (default test)
#   REPO      LOCAL restic repo to restore from (default: RESTIC_REPO from backup.conf)
#   SNAPSHOT  snapshot id to restore            (default latest)

SANDBOX_IMAGE ?= hsctl-sandbox
SANDBOX_NAME  ?= hsctl-sandbox
IMAGES        ?= sandbox/images.env
PORT          ?= 18088
PASS          ?= test
# Host LAN IP the apps should trust (so the restored Nextcloud/Vaultwarden open in a browser).
ACCESS_HOST   ?= $(shell ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.*src \([0-9.]*\).*/\1/p')
PASSFILE      ?= .restic-password
SNAPSHOT      ?= latest
REPO          ?= $(shell sed -n 's/^RESTIC_REPO=//p' backup.conf 2>/dev/null)

# Mount the backup repo (read-only) + its password only when REPO is a local path present
# on disk; remote (sftp:/b2:) repos aren't wired in, and an absent path just skips it.
REPO_MOUNT := $(if $(wildcard $(REPO)),-v $(abspath $(REPO)):/backup-repo:ro -v $(abspath $(PASSFILE)):/backup-pass:ro,)

.PHONY: sandbox sandbox-restore sandbox-shell sandbox-logs sandbox-down help

help:
	@sed -n 's/^#\( \|$$\)//p' Makefile | sed -n '1,20p'

sandbox: ## build the sandbox image (with your current hsctl) and start it
	@mkdir -p sandbox/_build
	CGO_ENABLED=0 go build -C hsctl -o ../sandbox/_build/hsctl .
	docker build -f sandbox/Dockerfile -t $(SANDBOX_IMAGE) .
	-docker stop -t 8 $(SANDBOX_NAME) >/dev/null 2>&1   # graceful: lets the old loopback detach
	-docker rm -f $(SANDBOX_NAME) >/dev/null 2>&1       # belt-and-suspenders (no-op after --rm)
	docker run -d --rm --privileged --name $(SANDBOX_NAME) \
		-p $(PORT):8088 \
		-p 18082:8082 -p 18081:8081 -p 18053:8053 \
		-p 18090:8090 -p 18091:8091 -p 18092:8092 \
		-e HSCTL_UI_PASSWORD=$(PASS) -e ACCESS_HOST=$(ACCESS_HOST) \
		-v $(abspath $(IMAGES)):/sandbox/images.env:ro \
		$(REPO_MOUNT) \
		$(SANDBOX_IMAGE)
	@echo ""
	@echo "Sandbox starting: http://$(ACCESS_HOST):$(PORT)/admin   (admin / $(PASS))"
	@echo "  Bring the stack up:  in the UI -> Commands -> 'Start all services',"
	@echo "                       or (with your real data):  make sandbox-restore"
	@echo "  Then open the RESTORED apps in your browser (live ports + 10000):"
	@echo "    Vaultwarden : http://$(ACCESS_HOST):18082    (passwords)"
	@echo "    Nextcloud   : http://$(ACCESS_HOST):18081    (files)"
	@echo "    Pi-hole     : http://$(ACCESS_HOST):18053/admin"
	@if [ -z "$(REPO_MOUNT)" ]; then echo "  (no local REPO mounted — pass REPO=/mnt/restic to enable 'make sandbox-restore')"; fi

sandbox-restore: ## restore a real backup into the running sandbox (SNAPSHOT=latest)
	docker exec -it $(SANDBOX_NAME) /sandbox/restore.sh $(SNAPSHOT)

sandbox-shell: ## open a shell inside the sandbox
	docker exec -it $(SANDBOX_NAME) bash

sandbox-logs: ## follow the sandbox logs
	docker logs -f $(SANDBOX_NAME)

sandbox-down: ## stop the sandbox and sweep its loopback device off the host
	-docker stop -t 8 $(SANDBOX_NAME) >/dev/null 2>&1
	-docker run --rm --privileged --entrypoint bash $(SANDBOX_IMAGE) -c \
		'losetup -a 2>/dev/null | grep -i sandboxdisk | cut -d: -f1 | xargs -r -n1 losetup -d' >/dev/null 2>&1
	@echo "sandbox stopped; host is clean."
