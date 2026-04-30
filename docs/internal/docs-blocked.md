# Documentation gaps deferred from the OSS-launch sweep

Items the overnight docs pass deliberately did not write, with reasons
so the next person picking them up does not start from zero.

## B6 — `docs/skills-marketplace.mdx` — skipped

`internal/skills/` exists and is real code, but it implements **skill
import and parsing**, not a marketplace. The package contents:

- `internal/skills/importer.go` — pulls skill definitions into the DB.
- `internal/skills/parser.go` — parses skill manifest files.

There is no browsing, discovery, rating, sharing, or registry surface in
the codebase today. A doc titled "skills marketplace" would have to
describe features that do not exist. Per the launch sweep's
do-not-improvise rule, the file was not written.

When the marketplace surface ships (browsing UI, registry endpoint,
publish flow), this is the spot to document:

- Skill manifest schema (cross-link from `internal/skills/parser.go`).
- Import flow (which import sources are trusted, who can publish).
- The trust boundary — same UID 1001/1002 split as the rest of the
  agent, plus whatever signing/verification the registry does.

## Doc tasks NOT attempted in this sweep

These were out of scope for the launch sweep but are obvious follow-ups:

- Per-package GoDoc beyond `keeper`, `sidecar`, `lookout`. The other
  internal packages (`paymaster`, `harbormaster`, `quartermaster`,
  `cartographer`, `lookout`, `episodic`, `presence`, etc.) already
  carry comprehensive package comments — verified during the audit.
  Worth a follow-up sweep to confirm coverage and add `doc.go` files
  where missing.
- A "Getting started for tool authors" guide explaining MCP
  registration end-to-end. The MCP endpoints are listed in
  `docs/architecture.mdx` but a tutorial is not.
- A migration guide between `release` and `main` once tagged releases
  start cutting.
