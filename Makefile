# Cross-platform build for grammar-server.
# The Go binary is self-contained; harper-ls + harper-cli are platform-specific
# and must be bundled alongside the grammar-server binary for each target.

VERSION := $(shell git describe --tags --always 2>/dev/null || echo 0.2.0)
GO := go
BIN := grammar-server
HARPER_DIR := /usr/bin

# Cross-compile targets
.PHONY: all bundle clean

all: $(BIN)

$(BIN):
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

clean:
	rm -rf dist/
