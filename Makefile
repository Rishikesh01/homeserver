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
#   make uninstall CONFIRM=yes   remove the whole homeserver install from this machine:
#                                dashboard service + timer, /usr/local/bin/hsctl, every app's
#                                containers, volumes (ALL APP DATA), images, the shared docker
#                                network, and the generated secrets/config in this repo.
#                                Backup config + repos (backup.conf, .restic-password,
#                                .backup-env, backups/) are kept so data can be restored.
#
# Knobs (override on the command line, e.g. `make sandbox PORT=19000`):
#   IMAGES    image manifest to use            (default sandbox/images.env)
#   PORT      host port for the admin UI        (default 18088)
#   PASS      admin password in the sandbox     (default test)
#   REPO      LOCAL restic repo to restore from (default: RESTIC_REPO from backup.conf)
#   SNAPSHOT  snapshot id to restore            (default latest)

SANDBOX_IMAGE ?= hsctl-sandbox
SANDBOX_NAME  ?= hsctl-sandbox
DATA_VOL      ?= hsctl-sandbox-data
VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
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

.PHONY: sandbox sandbox-restore sandbox-shell sandbox-logs sandbox-down sandbox-purge uninstall help

help:
	@sed -n 's/^#\( \|$$\)//p' Makefile | sed -n '1,26p'

sandbox: ## build the sandbox image (with your current hsctl) and start it
	@mkdir -p sandbox/_build
	CGO_ENABLED=0 go build -C hsctl -ldflags "-X main.version=$(VERSION)" -o ../sandbox/_build/hsctl .
	docker build -f sandbox/Dockerfile -t $(SANDBOX_IMAGE) .
	-docker stop -t 8 $(SANDBOX_NAME) >/dev/null 2>&1   # graceful: lets the old loopback detach
	-docker rm -f $(SANDBOX_NAME) >/dev/null 2>&1       # belt-and-suspenders (no-op after --rm)
	docker run -d --rm --privileged --name $(SANDBOX_NAME) \
		-p $(PORT):8088 \
		-p 18443:18443 -p 18444:18444 -p 18445:18445 \
		-p 18446:18446 -p 18447:18447 -p 18448:18448 \
		-e HSCTL_UI_PASSWORD=$(PASS) -e ACCESS_HOST=$(ACCESS_HOST) -e SANDBOX_PORT=$(PORT) \
		-v $(DATA_VOL):/var/lib/docker \
		-v $(abspath $(IMAGES)):/sandbox/images.env:ro \
		$(REPO_MOUNT) \
		$(SANDBOX_IMAGE)
	@echo ""
	@echo "Sandbox starting: http://$(ACCESS_HOST):$(PORT)/admin   (admin / $(PASS))"
	@echo "  Bring the stack up:  in the UI -> Commands -> 'Start all services',"
	@echo "                       or (with your real data):  make sandbox-restore"
	@echo "  Then open the RESTORED apps over HTTPS (your trusted cert; live ports + 10000):"
	@echo "    Vaultwarden : https://$(ACCESS_HOST):18443    (passwords)"
	@echo "    Nextcloud   : https://$(ACCESS_HOST):18444    (files)"
	@echo "    Pi-hole     : https://$(ACCESS_HOST):18445/admin"
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
	@echo "sandbox stopped; host is clean. (Nested images cached in volume '$(DATA_VOL)' for fast re-runs; 'make sandbox-purge' to delete.)"

sandbox-purge: sandbox-down ## also delete the cached nested images/data volume
	-docker volume rm $(DATA_VOL) >/dev/null 2>&1
	@echo "purged $(DATA_VOL)."

# ---- uninstall -------------------------------------------------------------------------
# Every directory with a docker-compose.yml is an app stack (matches what hsctl drives).
APP_DIRS   := $(patsubst %/docker-compose.yml,%,$(wildcard */docker-compose.yml))
UNITS      := hsctl-ui.service hsctl-backup.service hsctl-backup.timer
EDGE_NET   := homeserver-edge
HSCTL_BIN  ?= /usr/local/bin/hsctl
# Generated per-host files (see .gitignore). Backup config/repos are intentionally NOT here.
GENERATED  := $(wildcard */.env) .ui-password setup.conf WELCOME.txt caddy-root-ca.crt pihole/custom.list hsctl/hsctl

uninstall: ## remove services, binary, containers, volumes, images, network, generated config (CONFIRM=yes)
ifneq ($(CONFIRM),yes)
	@echo "This DELETES all app data (docker volumes) for: $(APP_DIRS)"
	@echo "and removes $(HSCTL_BIN), the systemd units and the generated config in this repo."
	@echo "Backups (backup.conf, .restic-password, .backup-env, backups/) are kept."
	@echo ""
	@echo "Re-run as:  make uninstall CONFIRM=yes"
	@exit 1
endif
	@echo "== stopping + removing systemd units =="
	-sudo systemctl disable --now $(UNITS) 2>/dev/null
	-sudo rm -f $(addprefix /etc/systemd/system/,$(UNITS))
	-sudo systemctl daemon-reload
	-sudo systemctl reset-failed 2>/dev/null
	@echo "== removing app containers, volumes and images =="
	@for d in $(APP_DIRS); do \
		echo "-- $$d"; \
		( cd $$d && docker compose down -v --rmi all --remove-orphans ) || true; \
	done
	-docker network rm $(EDGE_NET) 2>/dev/null
	@echo "== removing hsctl binary =="
	-sudo rm -f $(HSCTL_BIN)
	@echo "== removing generated config/secrets in this repo =="
	rm -f $(GENERATED)
	find . -name '.hsctl-tmp-*' -not -path './.git/*' -delete 2>/dev/null || true
	@echo ""
	@echo "Uninstalled. Kept: this repo's source, backup.conf, .restic-password, .backup-env, backups/."
	@echo "Docker itself was not removed. To delete the backup repo too, remove it by hand."
