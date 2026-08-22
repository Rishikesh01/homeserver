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
#   make uninstall        remove the whole homeserver install from this machine. Asks
#                         twice before touching anything (CONFIRM=yes skips the prompts):
#                         dashboard service + timer, /usr/local/bin/hsctl, every app's
#                         containers, volumes (ALL APP DATA), images, the shared docker
#                         network, orphaned containers/volumes from renamed or removed
#                         app dirs, the test sandbox, and the generated secrets/config
#                         in this repo. Backup config + repos (backup.conf,
#                         .restic-password, .backup-env, backups/) are kept so data can
#                         be restored.
#   make uninstall ALL=yes  all of the above PLUS backup.conf, .restic-password,
#   (= make uninstall-all)  .backup-env and backups/ — deleted permanently, every snapshot
#                         gone. ALWAYS asks for double confirmation, even with CONFIRM=yes.
#                         (make has no literal --all option, hence ALL=yes.)
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

.PHONY: sandbox sandbox-restore sandbox-shell sandbox-logs sandbox-down sandbox-purge uninstall uninstall-all help

help:
	@sed -n 's/^#\( \|$$\)//p' Makefile | sed -n '1,31p'

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
# Fixed container_name from each compose file — swept by name too, so a renamed/removed app
# dir can't leave its containers running after an uninstall.
APP_CONTAINERS := caddy vaultwarden nextcloud-app nextcloud-db nextcloud-redis pihole stirling-pdf it-tools imagetools
# Compose names volumes <project>_<volume> (project = app dir name). Sweep every known
# project prefix — including the canonical ones, so leftovers from a since-renamed dir go too.
COMPOSE_PROJECTS := $(sort $(APP_DIRS) caddy vaultwarden nextcloud pihole stirling it-tools imagetools)
# Generated per-host files (see .gitignore). Backup config/repos are intentionally NOT here —
# they only go with ALL=yes / `make uninstall-all`.
GENERATED  := $(wildcard */.env) .ui-password setup.conf WELCOME.txt caddy-root-ca.crt pihole/custom.list hsctl/hsctl
BACKUPS    := backup.conf .restic-password .backup-env backups

uninstall-all: ## uninstall EVERYTHING incl. backup config + restic snapshots (always double-confirms)
	@$(MAKE) --no-print-directory ALL=yes uninstall

uninstall: ## remove hsctl itself + all apps, sandbox, generated config; ALL=yes also deletes backups
	@echo "This DELETES all app data (docker volumes) for: $(APP_DIRS)"
	@echo "and removes hsctl itself ($(HSCTL_BIN) + systemd units), the test"
	@echo "sandbox and the generated config in this repo."
ifneq ($(CONFIRM),yes)
ifneq ($(ALL),yes)
	@echo ""
	@echo "Kept: backup.conf, .restic-password, .backup-env, backups/."
	@echo "(Run 'make uninstall ALL=yes' to delete those too.)"
	@echo ""
	@printf 'Confirm 1/2 — type YES to continue: '; read -r a || exit 1; \
	if [ "$$a" != "YES" ]; then echo "No match — nothing was removed."; exit 1; fi; \
	printf 'Confirm 2/2 — type YES again: '; read -r b || exit 1; \
	if [ "$$b" != "YES" ]; then echo "No match — nothing was removed."; exit 1; fi
endif
endif
ifeq ($(ALL),yes)
	@echo ""
	@echo "ALL MODE: backup.conf, .restic-password, .backup-env and backups/"
	@echo "(EVERY restic snapshot) will be PERMANENTLY deleted. There is no undo."
	@printf 'Confirm 1/2 — type DESTROY to continue: '; read -r a || exit 1; \
	if [ "$$a" != "DESTROY" ]; then echo "No match — nothing was removed."; exit 1; fi; \
	printf 'Confirm 2/2 — re-type DESTROY: '; read -r b || exit 1; \
	if [ "$$b" != "DESTROY" ]; then echo "No match — nothing was removed."; exit 1; fi
endif
	@echo ""
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
	@echo "-- sweeping orphaned app containers (renamed/removed dirs)"
	@for c in $(APP_CONTAINERS); do \
		if docker rm -f $$c >/dev/null 2>&1; then echo "   removed container $$c"; fi; \
	done
	@echo "-- sweeping orphaned volumes from old compose projects"
	@for p in $(COMPOSE_PROJECTS); do \
		for v in $$(docker volume ls --format '{{.Name}}' 2>/dev/null | awk -F'_' -v p="$$p" '$$1 == p {print}'); do \
			if docker volume rm "$$v" >/dev/null 2>&1; then echo "   removed volume $$v"; fi; \
		done; \
	done
	-docker network rm $(EDGE_NET) 2>/dev/null
	@echo "== removing the test sandbox (container, image, cached data) =="
	-docker stop -t 8 $(SANDBOX_NAME) >/dev/null 2>&1
	-docker run --rm --privileged --entrypoint bash $(SANDBOX_IMAGE) -c \
		'losetup -a 2>/dev/null | grep -i sandboxdisk | cut -d: -f1 | xargs -r -n1 losetup -d' >/dev/null 2>&1
	-docker rm -f $(SANDBOX_NAME) >/dev/null 2>&1
	-docker volume rm $(DATA_VOL) >/dev/null 2>&1 && echo "   removed volume $(DATA_VOL)"
	-docker rmi -f $(SANDBOX_IMAGE) >/dev/null 2>&1 && echo "   removed image $(SANDBOX_IMAGE)"
	rm -rf sandbox/_build
	@echo "== removing dangling images left behind by app updates =="
	-@old=$$(docker images -f dangling=true -q 2>/dev/null | wc -l); \
	if [ "$$old" -gt 0 ]; then docker image prune -f >/dev/null && echo "   pruned $$old untagged image(s)"; fi
	@echo "== removing hsctl itself (binary; dashboard state goes with the generated config below) =="
	-sudo rm -f $(HSCTL_BIN)
	@echo "== removing generated config/secrets in this repo =="
	rm -f $(GENERATED)
	find . -name '.hsctl-tmp-*' -not -path './.git/*' -delete 2>/dev/null || true
ifeq ($(ALL),yes)
	@echo "== deleting backups: $(BACKUPS) =="
	@if [ -f backup.conf ]; then \
		sed -n 's/^RESTIC_REPO=/   restic repo was: /p' backup.conf; \
	fi
	rm -rf $(BACKUPS)
endif
	@echo ""
ifeq ($(ALL),yes)
	@echo "Everything removed, including all backups and their config."
	@echo "If the restic repo lived off-box (sftp:/b2:/s3:), delete it at that location too."
else
	@echo "Uninstalled. Kept: this repo's source, backup.conf, .restic-password, .backup-env, backups/."
endif
	@echo "Docker itself was not removed."
