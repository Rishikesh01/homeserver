# Build/install hsctl. Go lives at ~/sdk/go on this box; override with GO=...
GO     ?= $(shell command -v go || echo $(HOME)/sdk/go/bin/go)
PREFIX ?= /usr/local

# Version baked into the binary (`hsctl --version`), derived from the git tag: exactly the
# tag on a release (v1.1.0), tag+commits+sha past it (v1.1.0-3-gabcdef), "-dirty" with local
# edits, or a short sha if untagged. Falls back to "dev". Override with `make build VERSION=…`.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o hsctl .

vet:
	$(GO) vet ./...

# Install the binary system-wide (so systemd + everyone can run `hsctl`). The web UI
# runs the binary as a long-lived process, so a new binary on disk doesn't take effect
# there until the service restarts — do that here if it's active (the backup timer runs
# the binary fresh each time, so it needs no restart).
install: build
	sudo install -m 0755 hsctl $(PREFIX)/bin/hsctl
	@echo "installed $(PREFIX)/bin/hsctl"
	@if systemctl is-active --quiet hsctl-ui.service; then \
		sudo systemctl restart hsctl-ui.service && \
		echo "restarted hsctl-ui.service (now running the new binary)"; \
	else \
		echo "hsctl-ui.service not active — skipped restart"; \
	fi

# Install the web-UI service + nightly backup timer. Edit systemd/*.service first
# (set __DIR__ to this repo and __ADDR__ to your LAN IP:port).
install-services:
	sudo cp systemd/hsctl-ui.service systemd/hsctl-backup.service systemd/hsctl-backup.timer /etc/systemd/system/
	sudo systemctl daemon-reload
	sudo systemctl enable --now hsctl-ui.service hsctl-backup.timer
	@echo "enabled hsctl-ui.service and hsctl-backup.timer"

.PHONY: build vet install install-services
