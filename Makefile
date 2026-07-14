.PHONY: build test test-conformance lint

BINARY := bin/stow

build:
	go build -o $(BINARY) ./cmd/stow

test:
	go test ./...

test-conformance:
	go test ./conformance/... -count=1 -v

lint:
	go vet ./...
