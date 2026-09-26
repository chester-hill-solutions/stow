.PHONY: build build-wasm test test-race test-conformance test-node test-python test-wasm test-all lint format-check check-go-quality check-ts-quality check-type-escapes check-dry check-file-size check-coverage check-version check-install-surface standards check-generated benchmark

BINARY := bin/stow-s3

build:
	# Match the release build exactly, so a local binary is the binary that ships.
	go build -trimpath -ldflags "-s -w" -o $(BINARY) ./cmd/stow-s3

# -trimpath matches the native build and is what makes the artifact reproducible
# across directories. Without it the wasm embeds its source path, so the
# committed artifact never matches a build from a fresh clone and check-generated
# fails for a reason unrelated to the source.
build-wasm:
	mkdir -p bin
	GOOS=js GOARCH=wasm go build -buildvcs=false -trimpath -o bin/stow-runtime.wasm ./cmd/stow-wasm

test:
	go test ./...

test-race:
	go test -race ./...

test-conformance:
	STOW_CONFORMANCE_BACKEND=memory go test ./conformance/... -count=1 -v
	STOW_CONFORMANCE_BACKEND=filesystem go test ./conformance/... -count=1 -v
	STOW_CONFORMANCE_BACKEND=runtime STOW_CONFORMANCE_RUNTIME_BACKEND=memory go test ./conformance/... -count=1 -v
	STOW_CONFORMANCE_BACKEND=runtime STOW_CONFORMANCE_RUNTIME_BACKEND=filesystem go test ./conformance/... -count=1 -v

# NPM_INSTALL installs everything, then removes exactly the thing that breaks
# the tests: the four bundled carrier binaries.
#
# The obvious fix is --omit=optional everywhere, and it is wrong. The hazard was
# never optional dependencies in general - it is specifically that
# @chester-hill-solutions/stow-s3-{linux,darwin}-{x64,arm64} each carry a
# `stow-s3` binary, and resolveStowBinary checks findBundledBinary() BEFORE the
# monorepo build and STOW_BIN. Meanwhile jscpd resolves its own scanner through
# an optional platform dependency, so omitting optional also breaks check:dry
# with a scanner that cannot run.
#
# So: install in full, then delete the four carrier directories. That is
# surgical, it leaves the lockfile alone, and it states the actual problem.
#
# This was reached three ways in one session - a plain `npm ci`, CI's own
# `npm ci`, and `make check-generated` - and each time the symptom was a suite
# reporting on the PUBLISHED 0.2.0 binary while the developer believed they were
# testing the branch. Three of the four failures looked like real regressions and
# were not. A gate that cannot see the change under test is worse than no gate,
# because it is green.
NPM_INSTALL = cd packages/stow-s3 && npm ci --ignore-scripts \
	&& rm -rf node_modules/@chester-hill-solutions/stow-s3-darwin-arm64 \
	          node_modules/@chester-hill-solutions/stow-s3-darwin-x64 \
	          node_modules/@chester-hill-solutions/stow-s3-linux-arm64 \
	          node_modules/@chester-hill-solutions/stow-s3-linux-x64

test-node: build build-wasm
	$(NPM_INSTALL) && npm test

# The Python client speaks the same ready protocol as the TypeScript one, so its
# tests run against a binary built from this tree. The venv is created outside
# the source tree so nothing here is left behind by a test run.
PYTHON_VENV ?= $(CURDIR)/.cache/venv
test-python: build
	python3 -m venv --without-pip $(PYTHON_VENV)
	$(PYTHON_VENV)/bin/python -c "import pip" 2>/dev/null || curl -sS https://bootstrap.pypa.io/get-pip.py | $(PYTHON_VENV)/bin/python -
	$(PYTHON_VENV)/bin/pip install --quiet --upgrade pip
	$(PYTHON_VENV)/bin/pip install --quiet -e "packages/stow-s3-py[boto3]" pytest
	STOW_BIN=$(CURDIR)/$(BINARY) $(PYTHON_VENV)/bin/python -m pytest packages/stow-s3-py/tests

test-wasm: build-wasm
	$(NPM_INSTALL) && npm run build
	node --test wasm/runtime.test.mjs

test-all: build test test-race test-conformance test-node test-python test-wasm

lint:
	go vet ./...

check-generated: build-wasm
	$(NPM_INSTALL) && npm run build
	@git diff --quiet HEAD -- packages/stow-s3/dist || (git status --short -- packages/stow-s3/dist; exit 1)
	@test -z "$$(git ls-files --others --exclude-standard -- packages/stow-s3/dist)" || (git ls-files --others --exclude-standard -- packages/stow-s3/dist; exit 1)

format-check:
	@test -z "$$(gofmt -l $$(find cmd internal conformance tools pkg -name '*.go' -type f))" || (gofmt -l $$(find cmd internal conformance tools pkg -name '*.go' -type f); exit 1)

check-go-quality:
	go run ./tools/quality

check-ts-quality:
	$(NPM_INSTALL) && npm run check:standards

check-type-escapes:
	cd packages/stow-s3 && npm run check:type-escapes

check-dry:
	cd packages/stow-s3 && npm run check:dry

check-file-size:
	node scripts/check-file-size.mjs

check-coverage:
	node scripts/check-coverage.mjs

# Measurement, not a gate. A wall-clock threshold on shared CI would be flaky,
# so this is deliberately kept out of `standards`.
benchmark: build build-wasm
	node packages/stow-s3/scripts/benchmark-session.mjs --sessions 30 --payload-bytes 1048576
	node packages/stow-s3/scripts/benchmark-session.mjs --sweep

check-version:
	node scripts/check-version.mjs

# The documented install surface has to match what is actually published. The
# default run is offline and deterministic so it is safe as a required check;
# --online resolves each published target against its registry and belongs in
# the release path, where the network is already a dependency.
check-install-surface:
	node scripts/check-install-surface.mjs

standards: format-check lint test-race check-go-quality check-file-size check-coverage check-version check-install-surface check-ts-quality check-generated
