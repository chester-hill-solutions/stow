.PHONY: build build-wasm test test-race test-conformance test-node test-wasm test-all lint format-check check-go-quality check-ts-quality check-type-escapes check-dry check-file-size check-coverage check-version standards check-generated

BINARY := bin/stow

build:
	go build -o $(BINARY) ./cmd/stow

build-wasm:
	mkdir -p bin
	GOOS=js GOARCH=wasm go build -o bin/stow-runtime.wasm ./cmd/stow-wasm

test:
	go test ./...

test-race:
	go test -race ./...

test-conformance:
	STOW_CONFORMANCE_BACKEND=memory go test ./conformance/... -count=1 -v
	STOW_CONFORMANCE_BACKEND=filesystem go test ./conformance/... -count=1 -v

test-node: build
	cd packages/stow && npm ci && npm test

test-wasm: build-wasm
	node --test wasm/runtime.test.mjs

test-all: build test test-race test-conformance test-node test-wasm

lint:
	go vet ./...

check-generated:
	cd packages/stow && npm ci --ignore-scripts && npm run build
	@git diff --quiet -- packages/stow/dist || (git status --short -- packages/stow/dist; exit 1)
	@test -z "$$(git ls-files --others --exclude-standard -- packages/stow/dist)" || (git ls-files --others --exclude-standard -- packages/stow/dist; exit 1)

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

check-version:
	node scripts/check-version.mjs

standards: format-check lint test-race check-go-quality check-file-size check-coverage check-version check-ts-quality check-generated
