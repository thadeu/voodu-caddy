# voodu-caddy — Makefile
#
# Build targets produce the `voodu-caddy` binary under bin/, which is
# the real plugin command. The shell wrappers (bin/apply, bin/remove,
# bin/list, bin/reload) call it with the right subcommand — that's how
# the Voodu plugin loader discovers each command by name.

BIN      := bin/voodu-caddy
PKG      := ./cmd/voodu-caddy
DIST     := dist
VERSION  := $(shell grep '^version:' plugin.yml | awk '{print $$2}')

GO       := go
LDFLAGS  := -s -w -X main.version=$(VERSION)

.PHONY: build test lint cross clean install-local

build:
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BIN) $(PKG)

test:
	$(GO) test ./...

lint:
	$(GO) vet ./...

# cross produces the two release binaries we ship. CGO disabled so the
# binary runs on any glibc/musl Linux without plugin-side surprises.
cross: $(DIST)/voodu-caddy_linux_amd64 $(DIST)/voodu-caddy_linux_arm64

$(DIST)/voodu-caddy_linux_amd64:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -ldflags '$(LDFLAGS)' -o $@ $(PKG)

$(DIST)/voodu-caddy_linux_arm64:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -ldflags '$(LDFLAGS)' -o $@ $(PKG)

# install-local drops the current build into a plugins root that
# mirrors the server layout. Useful for testing against a local Voodu
# controller without going through `voodu plugins:install`.
install-local: build
	@if [ -z "$(PLUGINS_ROOT)" ]; then \
		echo "PLUGINS_ROOT is required (e.g. /opt/voodu/plugins)"; exit 1; \
	fi
	@mkdir -p $(PLUGINS_ROOT)/caddy/bin
	cp $(BIN) $(PLUGINS_ROOT)/caddy/bin/voodu-caddy
	cp bin/apply bin/remove bin/list bin/reload $(PLUGINS_ROOT)/caddy/bin/
	chmod +x $(PLUGINS_ROOT)/caddy/bin/*
	cp plugin.yml $(PLUGINS_ROOT)/caddy/
	cp install uninstall $(PLUGINS_ROOT)/caddy/ 2>/dev/null || true
	@echo "installed into $(PLUGINS_ROOT)/caddy"

clean:
	rm -rf $(BIN) $(DIST)
