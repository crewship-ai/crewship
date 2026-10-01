# Documentation audit output

Dated instance audits, session handoffs, acceptance logs and experiment outputs
are retained in [private working context](../../development/private-context.md).
Public references to earlier reports use immutable historical source links;
they are evidence about that revision, not current product guarantees.

The retained [regression source fixture](codex-review-work-regressions-2026-09-11.go.txt)
explains the adversarial cases now exercised by `internal/work/binding_test.go`.
It remains available to public contributors alongside those tests.

## Generated API and CLI inventory

`release-1-0-api-cli-inventory.json` and `release-1-0-api-cli-inventory.md`
are generated locally and ignored by Git. Run `make docs-inventory` to refresh
them. CI regenerates and checks coverage with
`go run ./scripts/docs-inventory -strict`; moving historical reports does not
change that gate or require access to private documentation.

Current specifications are indexed in [docs/specs](../../specs/README.md).
New internal reports belong in private working context, not this directory.
