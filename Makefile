SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
MAKEFLAGS += --warn-undefined-variables --no-builtin-rules -j
.SUFFIXES:
.DELETE_ON_ERROR:
.DEFAULT_GOAL := build

.PHONY: build test lint clean install vet install-tools check modernize bench fmt fuzz fuzz-slotcache fuzz-cli fuzz-fs

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

fmt:
	golangci-lint run --fix --enable-only=modernize
	golangci-lint fmt

lint:
	@for script in ./backpressure/*.sh; do "$$script"; done
	@golangci-lint run --fix --output.json.path stdout --show-stats=false ./... 2>/dev/null | ./.pi/golangci-format.sh

test:
	$(GO) test -race ./...

clean:
	rm -f $(BINARY) tk-bench tk-seed
	find .tickets -name "*.lock" -type f -delete 2>/dev/null || true
	find .tickets -name ".cache" -type f -delete 2>/dev/null || true

install:
	$(GO) install ./cmd/tk

install-tools:
	@echo "golangci-lint includes all needed tools (modernize, etc.)"
	@echo "Install with: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest"

check: vet lint test

# Fuzz testing - Go can only run one fuzz test at a time
# Usage:
#   make fuzz                    # run all fuzz tests (10s each)
#   make fuzz-slotcache          # run slotcache fuzz tests only
#   make fuzz-cli                # run cli fuzz tests only
#   make fuzz-fs                 # run fs fuzz tests only
#   make fuzz FUZZ_TIME=30s      # run all fuzz tests (30s each)
#   make fuzz FUZZ_TARGET=FuzzBehavior_ModelVsReal  # run specific test

FUZZ_TIME ?= 10s
FUZZ_TARGET ?=

define run_fuzz_tests
	@if [ -n "$(FUZZ_TARGET)" ]; then \
		echo "==> Fuzzing $(FUZZ_TARGET) in $(1) for $(FUZZ_TIME)"; \
		$(GO) test -fuzz="^$(FUZZ_TARGET)$$" -fuzztime=$(FUZZ_TIME) $(1) || exit 1; \
	else \
		for test in $$(grep -h "^func Fuzz" $(2) 2>/dev/null | sed 's/func \(Fuzz[^(]*\).*/\1/'); do \
			echo "==> Fuzzing $$test in $(1) for $(FUZZ_TIME)"; \
			$(GO) test -fuzz="^$$test$$" -fuzztime=$(FUZZ_TIME) $(1) || exit 1; \
		done; \
	fi
endef

fuzz-slotcache:
	$(call run_fuzz_tests,./pkg/slotcache,./pkg/slotcache/*_test.go)

fuzz-cli:
	$(call run_fuzz_tests,./internal/cli,./internal/cli/*_test.go)

fuzz-fs:
	$(call run_fuzz_tests,./pkg/fs,./pkg/fs/*_test.go)

fuzz: fuzz-slotcache fuzz-cli fuzz-fs
