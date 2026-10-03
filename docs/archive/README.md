# Archive — superseded documents

**Purpose.** Documents preserved for the record after a newer document
replaced them. An archived document describes the system or the plan *as it
was* at the date in its header. **Editing an archived document to match the
present falsifies the record** — if it is wrong about today, that is because
today is not its subject; its replacement carries the current truth.

`go run ./scripts/docs-inventory -strict` excludes this directory for the
same reason: history may name commands and routes that no longer exist, and
nobody is allowed to "fix" that.

**Rule for a new file.** A document moves here only when (a) a specific
replacement exists, (b) the old document links to it (and the replacement
links back for history), and (c) the header states it is archived/superseded.
Age alone is not a reason; unreferenced alone is not a reason.

## Contents

No archived document is currently retained in this directory. The former
Pages research and implementation diary belong to private working context.
The public [Pages Apps contract](../specs/pages-apps.md) and
[architecture rationale](../prd/pages-apps-architecture.md) remain available.
Earlier public commits remain public history; this move does not make them
secret. Do not recreate an internal research archive in this directory.
