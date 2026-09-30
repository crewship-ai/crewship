# Specs — current contracts

**Purpose.** Current contracts and implementation references. New specifications
describe shipped behaviour. The retained Pages reference also contains an
explicitly labelled historical design; its presence here does not certify that
every original proposal shipped.

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

## Contents

- [`pages.md`](pages.md) — the Pages implementation reference: panel schemas, freshness,
  permission model, data model, API/CLI surface (§11), wire decisions
  (§11b). Referenced from ~140 code files by section anchor — treat section
  numbers as stable identifiers. Historical design status and market
  analysis are part of the record inside the document, not a claim of current
  implementation. The current custom-dashboard contract is
  [Pages Apps v1](../prd/pages-apps-v1.md); further separation of the older
  design requires checking its code consumers and preserving section anchors.
- [`response-shape-contract.md`](response-shape-contract.md) — how generated
  OpenAPI response schemas are graded so that a renamed field cannot pass;
  consumed by `cmd/gen-openapi` and `scripts/api-contract`.

- [`trusted-llm-hard-budgets.md`](trusted-llm-hard-budgets.md) — reservations before
  trusted provider calls and admission rules for legacy sidecar traffic.
