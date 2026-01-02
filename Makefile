.PHONY: build test lint clean install fmt vet

BINARY := tk
GO := go

build:
	$(GO) build -o $(BINARY) .

test:
	$(GO) test -race ./...

lint:
	golangci-lint run

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

clean:
	rm -f $(BINARY)

install:
	$(GO) install .

check: fmt vet lint test
