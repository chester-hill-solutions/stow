# Stow Code Standards

Stow uses ratcheted code-quality checks modeled on CallCaster and GoCanvass. Existing debt is recorded once in a checked-in baseline; new violations fail CI. Baselines may shrink when debt is removed, but they may not grow to make a change pass.

## Commands

Run the complete local gate with:

```sh
make standards
```

Individual gates:

```sh
make format-check
make lint
make check-go-quality
make check-ts-quality
make check-type-escapes
make check-dry
make check-file-size
make check-coverage
make check-version
make check-generated
```

The TypeScript wrapper also exposes the equivalent package-local commands through `packages/stow`:

```sh
npm run lint
npm run check:lint-ratchet
npm run check:type-escapes
npm run check:dry
```

## Ratchet policy

- `scripts/baselines/go-quality.json` records Go quality identities.
- `scripts/baselines/lint-ratchet.json` records TypeScript warning counts.
- `scripts/baselines/type-escapes.json` records TypeScript escape identities.
- `scripts/baselines/dry.json` records TypeScript duplication counts.
- `scripts/baselines/file-size.json` records oversized-file identities.
- `scripts/baselines/go-coverage.json` records the current Go coverage floor; improvements require an explicit baseline update and regressions fail.
- A new identity fails the gate.
- A stale identity also fails the gate, forcing the baseline to be lowered after debt is removed.
- A baseline-generation command is allowed only as an explicit maintenance action after reviewing the diff. It is not a way to approve a regression.
- CI checks out full history (`fetch-depth: 0`) and compares against the explicit parent/merge-base; a shallow checkout is a hard failure, never a skipped ratchet.
- Inline suppressions are themselves counted where the check can detect them. A suppression requires a precise explanation and does not reset the ratchet.
- Existing test fixtures and generated output are excluded only when their exclusion is documented in the check configuration.

## Go standards

The Go gate combines hard correctness checks with ratcheted structural checks:

- `gofmt -l` and `go vet ./...` are hard failures.
- `go test ./...` is a hard correctness gate.
- `packages/stow/go.mod` is an intentional nested-module boundary so Go package discovery never walks TypeScript `node_modules` after an npm install.
- `tools/quality` reports functions over 200 lines, cyclomatic complexity over 15, and functions with more than five parameters.
- `any` and `panic` use are tracked as type/safety escape hatches.
- The shared storage, S3, and run-through tests are part of the same release gate; backend-specific exemptions are not allowed without a written reason.

## TypeScript standards

The wrapper uses a flat ESLint configuration with warning-level ratchets for:

- cyclomatic complexity (`15`);
- maximum nesting depth (`3`);
- maximum parameters (`5`);
- function length (`200` lines, excluding blank lines/comments);
- `console`, non-null assertions, explicit `any`, and `as unknown as`.

The following are hard errors:

- duplicate imports;
- `@ts-ignore` and `@ts-nocheck`;
- undocumented `@ts-expect-error`;
- generated `dist` drift;
- typecheck and Node test failures.

The TypeScript escape ratchet additionally records `as any`, double casts, explicit `any`, and suppression comments by source identity. This is deliberately separate from ESLint so type-boundary escapes cannot silently disappear behind a rule configuration.

## Duplication and size

`jscpd` tracks duplicated TypeScript structure. The Go quality and file-size gates track Go structural metrics and file size. Cross-language Go clone detection is not yet part of the gate and must not be implied by this document; it is a follow-up before claiming full cross-language DRY coverage. Each implemented dimension is ratcheted independently.

Generated `packages/stow/dist` is checked into the repository for release reproducibility, but it is excluded from lint and duplication scans. The build is cleaned and regenerated, and CI fails if the checked-in output differs.

## CI

`make standards` is the local equivalent of the required CI quality job. The release workflow must run the same command after dependency installation. A quality failure is never converted into a warning-only job.
