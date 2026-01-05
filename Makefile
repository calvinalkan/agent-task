SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
MAKEFLAGS += --warn-undefined-variables --no-builtin-rules -j
.SUFFIXES:
.DELETE_ON_ERROR:
.DEFAULT_GOAL := build

.PHONY: build test lint clean install vet install-tools check modernize bench fmt

BINARY := tk
GO := go

build:
	$(GO) build -o $(BINARY) ./cmd/tk
	@[ -e ~/.local/bin/$(BINARY) ] || ln -sf $(CURDIR)/$(BINARY) ~/.local/bin/$(BINARY)

bench: build
	$(GO) build -o tk-bench ./cmd/tk-bench
	$(GO) build -o tk-seed ./cmd/tk-seed
	./tk-bench

modernize:
	# go modernize fully ignores go: ignore directives, so we cant run it for bench.
	modernize -fix ./...

vet:
	$(GO) vet ./...

fmt: modernize
	golangci-lint fmt

lint:
	golangci-lint config verify
	@./backpressure/no-lint-suppress.sh
	golangci-lint run --fix ./...

test:
	$(GO) test -race ./...

clean:
	rm -f $(BINARY) tk-bench tk-seed
	find .tickets -name "*.lock" -type f -delete 2>/dev/null || true
	find .tickets -name ".cache" -type f -delete 2>/dev/null || true

install:
	$(GO) install ./cmd/tk

install-tools:
	$(GO) install golang.org/x/tools/gopls/internal/analysis/modernize/cmd/modernize@latest

check: vet lint test
