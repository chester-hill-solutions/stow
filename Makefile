.PHONY: build test test-conformance test-node test-all lint format-check check-go-quality check-ts-quality check-type-escapes check-dry check-file-size check-coverage standards check-generated

BINARY := bin/stow

build:
	go build -o $(BINARY) ./cmd/stow

test:
	go test ./...

test-conformance:
	go test ./conformance/... -count=1 -v

test-node:
	cd packages/stow && npm ci && npm test

test-all: test test-conformance test-node

lint:
	go vet ./...

check-generated:
	git diff --exit-code -- packages/stow/dist

format-check:
	@test -z "$$(gofmt -l $$(find cmd internal conformance tools -name '*.go' -type f))" || (gofmt -l $$(find cmd internal conformance tools -name '*.go' -type f); exit 1)

check-go-quality:
	go run ./tools/quality

check-ts-quality:
	cd packages/stow && npm ci --ignore-scripts && npm run check:standards

check-type-escapes:
	cd packages/stow && npm run check:type-escapes

check-dry:
	cd packages/stow && npm run check:dry

check-file-size:
	node scripts/check-file-size.mjs

check-coverage:
	node scripts/check-coverage.mjs

standards: format-check lint check-go-quality check-file-size check-coverage check-ts-quality
