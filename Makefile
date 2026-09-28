# Cross-platform build for grammar-server.
# The Go binary is self-contained; harper-ls + harper-cli are platform-specific
# and must be bundled alongside the grammar-server binary for each target.

VERSION := $(shell git describe --tags --always 2>/dev/null || echo 0.4.0)
GO := go
BIN := grammar-server
HARPER_DIR := /usr/bin
PREFIX ?= $(HOME)/.local
UNITDIR ?= $(HOME)/.config/systemd/user

# Cross-compile targets
.PHONY: all bundle clean install uninstall package

all: $(BIN)

# Depend on the sources: without this, make saw a stale ./grammar-server, skipped the
# build, and `make install` happily installed the previous release's binary.
GOFILES := $(shell find cmd internal -name '*.go')

$(BIN): $(GOFILES) go.mod
	$(GO) build -ldflags="-s -w" -o $@ ./cmd/server

# Bundle: compile for a target and create a portable zip/tar.gz
# Usage: make bundle-linux-amd64 HARPER_DIR=~/harper-linux/
bundle-%: OS_ARCH = $(subst -, ,$*)
bundle-%: GOOS = $(word 1,$(OS_ARCH))
bundle-%: GOARCH = $(word 2,$(OS_ARCH))
bundle-%:
	@mkdir -p dist/$*
	GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -ldflags="-s -w" -o dist/$*/$(BIN) ./cmd/server
	@if [ -f $(HARPER_DIR)/harper-ls ]; then cp $(HARPER_DIR)/harper-ls dist/$*/; fi
	@if [ -f $(HARPER_DIR)/harper-cli ]; then cp $(HARPER_DIR)/harper-cli dist/$*/; fi
	@cp deployments/systemd/grammar-server.service dist/$*/ 2>/dev/null || true
	@echo "Bundle: dist/$*"
	@ls -lh dist/$*/

# Quick bundle for current platform
bundle-local:
	@mkdir -p dist/local
	$(GO) build -ldflags="-s -w" -o dist/local/$(BIN) ./cmd/server
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
# looks in its own directory first), the units, the UI's static files if the sibling
# checkout is there, and both licences. Extracted anywhere, it runs with nothing
# installed and nothing on PATH.
PKG := dist/$(BIN)-$(VERSION)-linux-amd64
UIDIR ?= ../grammar-ui

package: $(BIN)
	@set -e; rm -rf $(PKG); mkdir -p $(PKG)/deployments/systemd
	cp $(BIN) README.md LICENSE $(PKG)/
	cp $(HARPER_DIR)/harper-ls $(HARPER_DIR)/harper-cli $(PKG)/
	cp deployments/systemd/grammar-server.service $(PKG)/deployments/systemd/
	curl -fsSL https://raw.githubusercontent.com/Automattic/harper/master/LICENSE -o $(PKG)/LICENSE-harper
	@if [ -d $(UIDIR) ]; then \
		mkdir -p $(PKG)/ui/deployments/systemd; \
		cp $(UIDIR)/index.html $(UIDIR)/app.js $(UIDIR)/style.css $(UIDIR)/README.md $(UIDIR)/LICENSE $(PKG)/ui/; \
		cp $(UIDIR)/deployments/systemd/grammar-ui.service $(PKG)/ui/deployments/systemd/; \
		echo "  included the UI from $(UIDIR)"; \
	else \
		echo "  no UI at $(UIDIR) — archive is server-only"; \
	fi
	tar -czf $(PKG).tar.gz -C dist $(notdir $(PKG))
	@echo "Package: $(PKG).tar.gz"; ls -lh $(PKG).tar.gz; tar -tzf $(PKG).tar.gz | sed 's/^/  /'

clean:
	rm -rf dist/
