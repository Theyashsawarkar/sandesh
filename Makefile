# Build:                             make
# System-wide or packaged install:   make install [PREFIX=/usr/local] [DESTDIR=]
# Per-user, without make:            ./install.sh
PREFIX ?= /usr/local
BINDIR  = $(DESTDIR)$(PREFIX)/bin
SHARE   = $(DESTDIR)$(PREFIX)/share
VERSION = $(shell sed -n 's/^const Version = "\(.*\)"/\1/p' internal/app/app.go)

.PHONY: build install uninstall check test lint dist

build:
	go build -trimpath -o bin/sandesh ./cmd/sandesh

install: build
	install -Dm755 bin/sandesh $(BINDIR)/sandesh
	install -Dm644 assets/logo.svg $(SHARE)/sandesh/logo.svg
	install -Dm644 README.md $(SHARE)/doc/sandesh/README.md
	install -Dm644 CHANGELOG.md $(SHARE)/doc/sandesh/CHANGELOG.md
	install -Dm644 LICENSE $(SHARE)/licenses/sandesh/LICENSE

uninstall:
	rm -f $(BINDIR)/sandesh
	rm -rf $(SHARE)/sandesh $(SHARE)/doc/sandesh $(SHARE)/licenses/sandesh

test:
	go test ./...

lint:
	go vet ./...
	test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	shellcheck -S warning sandesh.tmux install.sh scripts/sandesh-run

# What CI runs.
check: lint test build
	./bin/sandesh --version
	./bin/sandesh --help >/dev/null
	python3 scripts/build-site.py --check

# Release binaries (CI does this on a tag).
dist:
	mkdir -p dist
	for arch in amd64 arm64; do \
	  CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags='-s -w' -o dist/sandesh-linux-$$arch ./cmd/sandesh; \
	done
