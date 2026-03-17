BINARY = stash
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS = -ldflags "-s -w -X main.version=$(VERSION)"
GOFLAGS = -trimpath

PLATFORMS = linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: build test clean release install

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

install: build
	cp $(BINARY) $(GOPATH)/bin/$(BINARY) 2>/dev/null || cp $(BINARY) ~/go/bin/$(BINARY)

install-completion:
	install -d $(DESTDIR)/usr/local/share/bash-completion/completions
	install -m 644 completions/stash.bash $(DESTDIR)/usr/local/share/bash-completion/completions/stash
	install -d $(DESTDIR)/usr/local/share/zsh/site-functions
	install -m 644 completions/stash.zsh $(DESTDIR)/usr/local/share/zsh/site-functions/_stash
