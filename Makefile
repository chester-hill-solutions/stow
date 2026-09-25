.PHONY: build build-wasm test test-race test-conformance test-node test-python test-wasm test-all lint format-check check-go-quality check-ts-quality check-type-escapes check-dry check-file-size check-coverage check-version standards check-generated benchmark

BINARY := bin/stow

build:
	go build -o $(BINARY) ./cmd/stow

build-wasm:
	mkdir -p bin
	GOOS=js GOARCH=wasm go build -buildvcs=false -o bin/stow-runtime.wasm ./cmd/stow-wasm

test:
	go test ./...

test-race:
	go test -race ./...

test-conformance:
	STOW_CONFORMANCE_BACKEND=memory go test ./conformance/... -count=1 -v
	STOW_CONFORMANCE_BACKEND=filesystem go test ./conformance/... -count=1 -v
	STOW_CONFORMANCE_BACKEND=runtime STOW_CONFORMANCE_RUNTIME_BACKEND=memory go test ./conformance/... -count=1 -v
	STOW_CONFORMANCE_BACKEND=runtime STOW_CONFORMANCE_RUNTIME_BACKEND=filesystem go test ./conformance/... -count=1 -v

test-node: build build-wasm
	cd packages/stow && npm ci && npm test

# The Python client speaks the same ready protocol as the TypeScript one, so its
# tests run against a binary built from this tree. The venv is created outside
# the source tree so nothing here is left behind by a test run.
PYTHON_VENV ?= $(CURDIR)/.cache/venv
test-python: build
	python3 -m venv --without-pip $(PYTHON_VENV)
	$(PYTHON_VENV)/bin/python -c "import pip" 2>/dev/null || curl -sS https://bootstrap.pypa.io/get-pip.py | $(PYTHON_VENV)/bin/python -
	$(PYTHON_VENV)/bin/pip install --quiet --upgrade pip
	$(PYTHON_VENV)/bin/pip install --quiet -e "packages/stow-py[boto3]" pytest
	STOW_BIN=$(CURDIR)/$(BINARY) $(PYTHON_VENV)/bin/python -m pytest packages/stow-py/tests

test-wasm: build-wasm
	cd packages/stow && npm ci --ignore-scripts && npm run build
	node --test wasm/runtime.test.mjs

test-all: build test test-race test-conformance test-node test-python test-wasm

lint:
	go vet ./...

check-generated: build-wasm
	cd packages/stow && npm ci --ignore-scripts && npm run build
	@git diff --quiet HEAD -- packages/stow/dist || (git status --short -- packages/stow/dist; exit 1)
	@test -z "$$(git ls-files --others --exclude-standard -- packages/stow/dist)" || (git ls-files --others --exclude-standard -- packages/stow/dist; exit 1)

format-check:
	@test -z "$$(gofmt -l $$(find cmd internal conformance tools pkg -name '*.go' -type f))" || (gofmt -l $$(find cmd internal conformance tools pkg -name '*.go' -type f); exit 1)

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

# Measurement, not a gate. A wall-clock threshold on shared CI would be flaky,
# so this is deliberately kept out of `standards`.
benchmark: build build-wasm
	node packages/stow/scripts/benchmark-session.mjs --sessions 30 --payload-bytes 1048576
	node packages/stow/scripts/benchmark-session.mjs --sweep

check-version:
	node scripts/check-version.mjs

standards: format-check lint test-race check-go-quality check-file-size check-coverage check-version check-ts-quality check-generated
