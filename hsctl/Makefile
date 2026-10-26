# Build/install hsctl. Uses `go` from PATH, falling back to ~/sdk/go for a hand-unpacked
# toolchain; override either with GO=/path/to/go.
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
	$(GO) vet ./... && $(GO) vet -tags integration ./...

# Cross-compile the release archives into hsctl/dist/ — one tarball per platform, plus
# the checksums file. These are exactly the artifacts .github/workflows/release.yml
# attaches to a tag and install.sh downloads, so a release can be reproduced locally.
# hsctl is pure Go (no cgo), so CGO_ENABLED=0 gives a static binary that runs on any
# distro, glibc or musl.
#
# 32-bit ARM is deliberately absent: IT-Tools and Stirling-PDF publish arm64 images only,
# so a 32-bit OS can't run the full stack even though hsctl itself would build for it.
DIST_PLATFORMS ?= linux/amd64 linux/arm64

.PHONY: dist
dist:
	rm -rf dist && mkdir -p dist
	@for p in $(DIST_PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		echo "  building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/hsctl . || exit 1; \
		tar -C dist -czf dist/hsctl_$(VERSION)_$${os}_$${arch}.tar.gz hsctl || exit 1; \
		rm -f dist/hsctl; \
	done
	@cd dist && sha256sum *.tar.gz > checksums.txt && cat checksums.txt


# The tests are integration tests (build tag `integration`): table-driven, through each package's
# public API, against REAL dependencies — the filesystem, restic, a Docker daemon, root, block
# devices. They run in a throwaway privileged Docker-in-Docker container (test/run-integration.sh),
# so the host's Docker is never touched. Narrow a run with PKG=./internal/backup, TESTRUN=<regexp>,
# TESTV=1.
test: test-integration

test-integration:
	GO=$(GO) ./test/run-integration.sh

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

# Install the dashboard service + nightly backup timer. `hsctl install` is the one installer (it
# fills the repo and binary paths into the units in internal/install/systemd/); this target just
# runs the system-wide binary's copy of it from this repo.
install-services: install
	cd .. && sudo $(PREFIX)/bin/hsctl install

.PHONY: build vet test test-integration install install-services
