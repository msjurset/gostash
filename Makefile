BINARY = stash
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS = -ldflags "-s -w -X main.version=$(VERSION)"
GOFLAGS = -trimpath

PLATFORMS = linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: build test clean release deploy install-completion

build:
	go build $(GOFLAGS) $(LDFLAGS) -o $(BINARY) ./cmd/stash

test:
	go test ./...

clean:
	rm -f $(BINARY)
	rm -rf dist/

release: clean test
	@mkdir -p dist
	@for platform in $(PLATFORMS); do \
		OS=$${platform%/*}; \
		ARCH=$${platform#*/}; \
		echo "Building $$OS/$$ARCH..."; \
		GOOS=$$OS GOARCH=$$ARCH go build $(GOFLAGS) $(LDFLAGS) \
			-o dist/$(BINARY)-$$OS-$$ARCH ./cmd/stash; \
		tar czf dist/$(BINARY)-$$OS-$$ARCH.tar.gz \
			-C dist $(BINARY)-$$OS-$$ARCH \
			-C .. completions/; \
	done

deploy: build install-completion
	cp $(BINARY) ~/.local/bin/

install-completion:
	install -d ~/.oh-my-zsh/custom/completions
	install -m 644 completions/stash.zsh ~/.oh-my-zsh/custom/completions/_stash
	@echo "Refreshing zsh completions..."
	@zsh -c 'autoload -U compinit && rm -f ~/.zcompdump* && compinit' 2>/dev/null || true
