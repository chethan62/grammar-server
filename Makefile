# Cross-platform build for grammar-server.
# The Go binary is self-contained; harper-ls + harper-cli are platform-specific
# and must be bundled alongside the grammar-server binary for each target.

# The tag, without the "v": the released binary reports it through /status, and the
# archive is named after it. "dev" when the build does not go through make.
VERSION := $(shell git describe --tags --always 2>/dev/null | sed 's/^v//' || echo dev)
GO := go
LDFLAGS := -s -w -X grammar-server/internal/api.Version=$(VERSION)
BIN := grammar-server
HARPER_DIR := /usr/bin
PREFIX ?= $(HOME)/.local
UNITDIR ?= $(HOME)/.config/systemd/user

# Cross-compile targets
.PHONY: all bundle clean install uninstall package FORCE

all: $(BIN)

# Always rebuild. This was a source dependency once, which is the same trap twice:
# a stale ./grammar-server could be installed under a new name, and now the version
# itself is a build input — tagging a commit changes no .go file, so the release
# archive was built around the previous release's binary. Go's build cache makes an
# unchanged rebuild cost about a second, which is cheaper than a wrong artifact.
FORCE:

$(BIN): FORCE
	$(GO) build -ldflags="$(LDFLAGS)" -o $@ ./cmd/server

# Bundle: compile for a target and create a portable zip/tar.gz
# Usage: make bundle-linux-amd64 HARPER_DIR=~/harper-linux/
bundle-%: OS_ARCH = $(subst -, ,$*)
bundle-%: GOOS = $(word 1,$(OS_ARCH))
bundle-%: GOARCH = $(word 2,$(OS_ARCH))
bundle-%:
	@mkdir -p dist/$*
	GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -ldflags="$(LDFLAGS)" -o dist/$*/$(BIN) ./cmd/server
	@if [ -f $(HARPER_DIR)/harper-ls ]; then cp $(HARPER_DIR)/harper-ls dist/$*/; fi
	@if [ -f $(HARPER_DIR)/harper-cli ]; then cp $(HARPER_DIR)/harper-cli dist/$*/; fi
	@cp deployments/systemd/grammar-server.service dist/$*/ 2>/dev/null || true
	@echo "Bundle: dist/$*"
	@ls -lh dist/$*/

# Quick bundle for current platform
bundle-local:
	@mkdir -p dist/local
	$(GO) build -ldflags="$(LDFLAGS)" -o dist/local/$(BIN) ./cmd/server
	cp $(HARPER_DIR)/harper-ls $(HARPER_DIR)/harper-cli dist/local/ 2>/dev/null || true
	@echo "Local bundle: dist/local/"
	@ls -lh dist/local/

# Install for the current user: no sudo, no source tree baked into the unit.
# harper-ls is found on PATH, or beside the binary, or via --harper.
install: $(BIN)
	install -d $(PREFIX)/bin $(UNITDIR)
	install -m755 $(BIN) $(PREFIX)/bin/$(BIN)
	install -m644 deployments/systemd/grammar-server.service $(UNITDIR)/grammar-server.service
	-systemctl --user daemon-reload
	@echo "Installed $(PREFIX)/bin/$(BIN) and the user unit. Start it with:"
	@echo "  systemctl --user enable --now grammar-server"

uninstall:
	-systemctl --user disable --now grammar-server
	rm -f $(PREFIX)/bin/$(BIN) $(UNITDIR)/grammar-server.service
	-systemctl --user daemon-reload

# Distribution archive: the server, the harper pair it needs beside it (resolveHarper
# looks in its own directory first), the unit and both licences. Extracted anywhere, it
# runs with nothing installed and nothing on PATH.
#
# It is server-only, and deliberately so: the clients are a desktop card living in the
# grammar-ui repo, which needs Qt and the accessibility bus. A tar of Python scripts
# would not be a working client, and the archive that did try to ship the old browser UI
# kept copying files that had been deleted — failing the target outright.
PKG := dist/$(BIN)-$(VERSION)-linux-amd64

package: $(BIN)
	@set -e; rm -rf $(PKG); mkdir -p $(PKG)/deployments/systemd
	cp $(BIN) README.md LICENSE $(PKG)/
	cp $(HARPER_DIR)/harper-ls $(HARPER_DIR)/harper-cli $(PKG)/
	cp deployments/systemd/grammar-server.service $(PKG)/deployments/systemd/
	curl -fsSL https://raw.githubusercontent.com/Automattic/harper/master/LICENSE -o $(PKG)/LICENSE-harper
	tar -czf $(PKG).tar.gz -C dist $(notdir $(PKG))
	@echo "Package: $(PKG).tar.gz"; ls -lh $(PKG).tar.gz; tar -tzf $(PKG).tar.gz | sed 's/^/  /'

# The AppImage target is retired, not broken.
#
# It built a double-clickable file whose whole job was to start the engine, serve the old
# browser UI from the bundle and open it. That UI is gone — the clients are a desktop card
# that needs Qt and the accessibility bus, which an AppImage of Python scripts cannot
# provide — so the target could only copy files that no longer exist and fail. `package`
# above is the portable artifact; `git log -- Makefile` has the old target if a
# portable-engine AppImage is ever wanted again.

clean:
	rm -rf dist/
