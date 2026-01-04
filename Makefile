.PHONY: build test lint clean install fmt vet

BINARY := tk
GO := go

build:
	$(GO) build -o $(BINARY) .

test:
	XDG_CONFIG_HOME=/dev/null $(GO) test -race ./...

lint:
	golangci-lint run

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

clean:
	rm -f $(BINARY)
	find tickets -name "*.lock" -type f -delete 2>/dev/null || true
	find tickets -name ".cache" -type f -delete 2>/dev/null || true

install:
	$(GO) install .

check: fmt vet lint test
