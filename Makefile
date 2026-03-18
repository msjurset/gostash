BINARY = stash
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS = -ldflags "-s -w -X main.version=$(VERSION)"
GOFLAGS = -trimpath

PLATFORMS = linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: build test clean release deploy generate install-completion install-manpage

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

deploy: build install-manpage install-completion
	cp $(BINARY) ~/.local/bin/

install-manpage:
	install -d /usr/local/share/man/man1
	install -m 644 stash.1 /usr/local/share/man/man1/stash.1

install-completion:
	install -d ~/.oh-my-zsh/custom/completions
	install -m 644 completions/stash.zsh ~/.oh-my-zsh/custom/completions/_stash
	@echo "Refreshing zsh completions..."
	@zsh -c 'autoload -U compinit && rm -f ~/.zcompdump* && compinit' 2>/dev/null || true
