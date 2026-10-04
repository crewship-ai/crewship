# Specs — current contracts

**Purpose.** Current contracts and implementation references. New specifications
describe shipped behaviour. The Pages reference retains technical design sections; its presence here
does not certify that every original proposal shipped. Internal research and
execution diaries are maintained in private context.

`go run ./scripts/docs-inventory -strict` scans supported executable-looking
contexts for commands, flags, public routes and manifest kinds, subject to its
documented exclusions and explicit ignore annotations. It checks those
references, not every behavioural claim in a document. A passing gate is not
an implementation-status audit.

**Rule for a new file.** Add a document here when (a) the behaviour is
implemented, (b) the document describes what *is*, not what should be, and
(c) you are willing to keep it true. Design rationale may live inside a spec
as clearly-marked sections; if the rationale outgrows the contract, split the
rationale into an appropriate public design record, or keep internal research
in [private context](../development/private-context.md) while preserving the
public contract here. Superseded a spec? Move it to
[`docs/archive/`](../archive/README.md) with a link to its replacement.

[Documentation map](../README.md) · [Design context](../prd/README.md)

## Runtime, authority and budgets

- [Staged runtime start](staged-runtime-start.md): keeper-only pilot boot, fresh fence readback and generation-gated image execution.

- [Restricted context](restricted-context.md): scoped chat, CLI execution, memory,
  files, profiles and content-free attempt records.
- [Member resource policy](member-resource-policy.md): the administration API/CLI
  for restricted member authority; does not itself enable model execution.
- [Credentials vault](credentials-vault.md): credential types, storage and
  mounting conventions, with implementation provenance.
- [Trusted provider budgets](trusted-llm-hard-budgets.md): reservations before
  provider calls and admission rules for legacy sidecar traffic.

## Work and dispatch

- [Restricted workflows](restricted-workflows.md): private routine admission,
  filtered catalogs, receipts, Page polling and bounded delegated execution.
- [Private issue preflight](private-issue-preflight.md): atomic claims,
  budget/capacity checks, deduplication and recovery.

## Pages and project files

- [Pages](pages.md): panel schemas, freshness, permissions, data model and
  API/CLI surface. Code references its section anchors; preserve them.
  Explicit historical design sections are not claims of current implementation.
- [Pages Apps](pages-apps.md): custom applications, browser/isolation boundaries
  and remaining acceptance limits.
- [Project files](project-files.md): immutable, explicitly selected files and
  read-only restricted native mounts.

- [Managed CLI launch](managed-cli-launch.md): explicit static native pilot, executable hashes and lock-bound admission.

## Storage and lifecycle

- [Quota service backup](quota-service-backup.md): offline fixed-ext4 snapshots
  and the explicit restore/recovery release boundary.
- [Container cleanup](container-cleanup.md): installation labels, cleanup after
  crew deletion, retries and durable diagnostics.

## API documentation contracts

- [Response shapes](response-shape-contract.md): how generated OpenAPI responses
  are graded; consumed by `cmd/gen-openapi` and `scripts/api-contract`.
