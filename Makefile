BINARY = stash
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS = -ldflags "-s -w -X main.version=$(VERSION)"
GOFLAGS = -trimpath

PLATFORMS = linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: build test clean release deploy generate install-completion install-manpage install-chrome-host install-launchd uninstall-launchd

generate:
	go generate ./internal/manpage/

build: generate
	go build $(GOFLAGS) $(LDFLAGS) -o $(BINARY) ./cmd/stash

test:
	go test ./...

clean:
	rm -f $(BINARY)
	rm -rf dist/

release: clean test
	@mkdir -p dist
	cp stash.1 dist/
	@for platform in $(PLATFORMS); do \
		OS=$${platform%/*}; \
		ARCH=$${platform#*/}; \
		echo "Building $$OS/$$ARCH..."; \
		GOOS=$$OS GOARCH=$$ARCH go build $(GOFLAGS) $(LDFLAGS) \
			-o dist/$(BINARY)-$$OS-$$ARCH ./cmd/stash; \
		tar czf dist/$(BINARY)-$$OS-$$ARCH.tar.gz \
			-C dist $(BINARY)-$$OS-$$ARCH stash.1 \
			-C .. completions/; \
		rm dist/$(BINARY)-$$OS-$$ARCH; \
	done
	rm dist/stash.1

deploy: build install-manpage install-completion install-chrome-host
	@# Unload the launchd daemon BEFORE overwriting the binary.
	@# On Apple Silicon, copying over a running ad-hoc-signed
	@# executable invalidates the in-kernel code-signing state and
	@# subsequent launches die with OS_REASON_CODESIGNING. The
	@# `|| true` keeps the deploy working on a fresh machine where
	@# the daemon isn't installed yet.
	-launchctl unload $(PLIST_DEST) 2>/dev/null || true
	cp $(BINARY) ~/.local/bin/
	@# Ad-hoc sign — macOS Sequoia+ Gatekeeper SIGKILLs unsigned
	@# binaries from new locations (including ~/.local/bin) on
	@# every rebuild that changes deps / signature. Without this
	@# the Mac app's `stash list` subprocess returns no output
	@# and the items list renders empty.
	codesign --force --sign - ~/.local/bin/$(BINARY)
	$(MAKE) install-launchd
	@# Re-prime the Keychain ACL for the newly-installed binary.
	@# Each deploy changes the binary's cdhash, which invalidates
	@# any existing -T trust on the cached Gemini key — without
	@# this, the launchd-spawned `stash serve` daemon fails to
	@# read the key silently and the identify worker idles.
	@# Only runs if a reference has been saved by a prior
	@# `stash auth set-gemini op://…`; first-time setup still
	@# requires the user to run that interactively.
	@if ~/.local/bin/$(BINARY) auth show-gemini 2>/dev/null | grep -q "cached"; then \
		echo ">>> Refreshing Gemini key from saved op:// reference (TouchID may prompt)…"; \
		~/.local/bin/$(BINARY) auth refresh-gemini || \
			echo "    refresh-gemini failed — daemon will idle until you run it manually."; \
	else \
		echo ">>> No Gemini key saved yet. Run \`stash auth set-gemini op://…\` to enable auto-identify."; \
	fi

# launchd plumbing — installs / re-installs the user agent that
# keeps `stash serve` alive across reboots and deploys. Idempotent:
# safe to re-run on every `make deploy`. Pattern adapted from
# sortie's Makefile (template plist + sed substitution).
PLIST_LABEL := com.msjurseth.stash.serve
PLIST_DEST  := $(HOME)/Library/LaunchAgents/$(PLIST_LABEL).plist
BINARY_PATH := $(HOME)/.local/bin/$(BINARY)
LOG_PATH    := $(HOME)/Library/Logs/stash-serve.log

install-launchd:
	@mkdir -p "$(HOME)/Library/LaunchAgents" "$(HOME)/Library/Logs"
	sed -e 's|__BINARY_PATH__|$(BINARY_PATH)|g' \
		-e 's|__LOG_PATH__|$(LOG_PATH)|g' \
		$(PLIST_LABEL).plist.tpl > "$(PLIST_DEST)"
	launchctl load "$(PLIST_DEST)"
	@echo "stash serve daemon installed and started."
	@echo "  Plist: $(PLIST_DEST)"
	@echo "  Log:   $(LOG_PATH)"
	@echo "  Check: launchctl list | grep $(PLIST_LABEL)"

uninstall-launchd:
	@if [ -f "$(PLIST_DEST)" ]; then \
		launchctl unload "$(PLIST_DEST)" 2>/dev/null || true; \
		rm -f "$(PLIST_DEST)"; \
		echo "stash serve daemon uninstalled."; \
	else \
		echo "No daemon installed."; \
	fi

install-manpage:
	install -d /usr/local/share/man/man1
	install -m 644 stash.1 /usr/local/share/man/man1/stash.1

install-chrome-host:
	./$(BINARY) chrome-host install

install-completion:
	install -d ~/.oh-my-zsh/custom/completions
	install -m 644 completions/stash.zsh ~/.oh-my-zsh/custom/completions/_stash
	@echo "Refreshing zsh completions..."
	@zsh -c 'autoload -U compinit && rm -f ~/.zcompdump* && compinit' 2>/dev/null || true
