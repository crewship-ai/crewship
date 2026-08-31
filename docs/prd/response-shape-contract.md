# Response-shape contract — making the generated spec able to fail

**Status:** mechanism landed and proven; 227 response schemas still ungraded
**Date:** 2026-08-31
**Relates to:** #1815 (`API_CONTRACT_ADVISORY`), release-1.0 quality-bar condition 2

## 0. What happened

`GET /api/v1/approvals` serialized `harbormaster.Request`, which carried no
JSON tags, so the API answered `"ID"`, `"Kind"`, `"Status"`, `"CreatedAt"`.
Three other artifacts described the same route in snake_case: the generated
OpenAPI document, the web client's zod schema, and
`docs/api-reference/approvals.mdx`. Three agreed with each other and none with
the wire.

Every gate was green. The Go tests decoded into structs, and `encoding/json`
matches field names case-insensitively on the way in. The frontend tests each
wrote their own snake_case fixture and read it back. `/approvals` and the
inbox's approval feed rendered zero rows in production the whole time — the
surface where a human says yes or no to a destructive operation.

The document is the only one of the four that a machine checks against the
running server. It could not see this, and the reason is measurable: the
generated schema for that row declared `properties` but no `required` and no
`additionalProperties`. A body with every field renamed satisfies it. Measured
directly against the shipped spec:

```
RESULT: the PascalCase body VALIDATES against the shipped spec
```

A permissive schema does not merely fail to catch drift. It certifies it.

## 1. Why the obvious fix is wrong

The tempting change is "everything not wrapped in `nullable()` is required".
It is unsafe. Sampling eight response schemas against their real Go structs
found six with at least one non-nullable property that carries `,omitempty`:

- `run.mcp_server_errors` / `permission_denials` — *"omitted, never `[]`, when
  nothing was skipped"*; absence is the signal
- `issue.crew_name`, `crew_slug`, `created_by` — omitted for legacy rows
- `agent.created_by_user_id` — omitted for pre-v100 agents
- `skill.installed_on` — only populated on one query variant

A mechanical sweep would mark those required and produce contract failures on
responses that are perfectly valid. That is worse than no gate: a gate that
cries wolf is a gate that gets switched off, which is the position #1815 is
already in.

## 2. The mechanism

`required` must be **derived from the response struct**, never written beside
it. Hand-writing it re-creates the original defect — a description of the code,
maintained next to the code, drifting from it.

The `json` struct tag is the one artifact that cannot drift, because it *is*
the wire. A field without `,omitempty` is emitted on every response and belongs
in `required`. A field with it may be absent and must not be there.

Three tests, all landed:

| Test | Location | What it holds |
|---|---|---|
| `TestOpenAPIRequired_MatchesTheStructsOwnJSONTags` | `internal/api` | For each registered (schema pointer, response struct) pair, `required` equals the set of fields without `,omitempty`. Reflection, no judgement. |
| `TestResponseSchemas_RejectABodyWithEveryFieldRenamed` | `cmd/gen-openapi` | The property that matters, stated directly: a body with every field renamed must not validate. |
| `TestResponseSchemas_WithoutRequired_DoNotGrow` | `cmd/gen-openapi` | The ratchet. 227 today; may fall, may not rise. |
| `TestOpenAPIResponseComponents_AreGradedOrExcused` | `internal/api` | Coverage. Every component a path uses as its 200 response must have a pair **or a written reason why it cannot**. 203 today. |
| `scripts/api-contract/response_shapes.py` | live | Reality. Real 200 bodies from a running server against the schema that server publishes. 13/13 on dev2. |

**The two counts measure different things, and confusing them will waste
someone's afternoon.** The ratchet counts schemas declared **inline** under a
path's 200 response. The pair table grades **named components**, which paths
reach by `$ref`. B1 and B2 tightened 13 named components and the ratchet did
not move — that is correct, not a failure to land. A batch that targets inline
schemas is what lowers it.

## 2a. Why there is a fourth test: the mechanism was opt-in

The pair table has one architectural weakness, and it is the same shape as the
defect that started this: **nothing makes a new endpoint bring a pair with it.**
A guarantee that holds only where somebody remembered to ask for it is a
convention, not a structure, and conventions are what drifted.

`TestOpenAPIResponseComponents_AreGradedOrExcused` turns it opt-out. It
enumerates every named component a path uses as its 200 response and requires
each to be either graded by a pair or listed in `responseShapeExclusions` with
a reason. Adding a route whose response nothing can check now fails a test
today, instead of being noticed a release later or never.

**The exclusions are as valuable as the pairs.** Each one names work that has to
happen first, in a place a reader will find it:

- `PipelineRun`, `PipelineRunList` — the handler builds its body from a
  `map[string]any` literal. Needs a DTO before anything can be derived.
- `RunRecord`, `RunRecordList` — one component, two routes, two shapes.
- `WorkspacePipelineResponseV1` — 9 declared properties against a 27-field
  struct with different names. A rewrite, not a required list.
- `SkillDetail` — orphaned while `GET /skills/{skillId}` `$ref`s the wrong
  component.

Left as prose in a report, those four findings would be read once. As
exclusions they are read by anyone who touches the route, and they disappear
the day the underlying work is done.

## 3. The unit of work

Adding one pair to `responseShapeContracts` is the whole loop:

1. Add `{name, pointer, value: theResponseStruct{}}`.
2. Run the test. It goes red and **names the exact fields** — for example:
   `count, has_more, rows, unread_count`.
3. Paste those names into the schema's `object(...)` call in `cmd/gen-openapi`.
4. `go run ./cmd/gen-openapi`, commit the regenerated spec.
5. The ratchet drops.

There is no field-by-field judgement anywhere in that loop. That is the design
goal: the work is long, but none of it is delicate.

## 4. Blast radius, measured

- **972** call sites building an object schema across `cmd/gen-openapi/*.go`
- **4,991** property entries across them
- **288** named components declare properties and no `required`
- **228** routes have an inline 200-response object with properties and no
  `required` (the ratchet's metric; 227 after the first batch)
- **84** request-body components also lack `required` — a separate axis, see §8

One structural obstacle, which every batch must handle first: **`object()` is
not one function.** Each schema file defines its own closure, and **21 of them
take no `required` parameter and silently discard one**. Seven files already
have the variadic form (`schemas_core.go` is the baseline); two more were
widened for batch 1. Widening a file's closure is a three-line change and is
part of that file's batch, not a separate project.

## 5. Batches, by value not volume

Ordered by what a customer touches, not by what is easy to count.

| Batch | Scope | State |
|---|---|---|
| B1 | Human-in-the-loop: approvals row, inbox item, inbox envelope | **done** — ratchet 228 → 227 |
| B2 | The daily surfaces: crews, agents, workspaces, projects, issues, skills | **done** — 10 components, `Agent` alone gained 19 required fields |
| B3 | Admin, keeper, credentials | **partial** — admin + credentials done (8 pairs); the keeper surface is blocked, see below |
| B4 | The long tail, including components reached only by `$ref` | last |

A batch is: widen that file's `object()` closure if needed, add every pair in
it to the table, fix what goes red, regenerate, lower the ratchet in the same
commit.

## 5a. Why the pairs must be found by hand, and what would change that

The obvious shortcut is to pair a component with its Go struct by name and
generate the whole table. Measured against the shipped spec, that matches
**25 of 280** ungraded components — **8%**.

The reason is the batch naming. 246 components are called `Core…`, `Remaining…`,
`Final…` or `Workflow…` after the pass that introduced them, not after the
thing they describe: `FinalInboxList`, `RemainingCredentialField`. A name that
records when a schema was written cannot be matched to the struct it describes.

That makes #1849's renaming half — listed on the release-1.0 board as a
breaking change to do once or never — a **prerequisite for doing this cheaply**,
not a cosmetic. Renamed first, the table is largely generated and this document
describes an afternoon. Renamed never, every pair is found by reading routes,
and the batches in §5 are the honest plan.

Sequencing follows from that: if #1849 is going to happen, do it before B3.

## 5b. What B2 turned up that is not a `required` problem

Three findings from the B2 sweep are documented mismatches between the spec and
the server. A required list cannot fix any of them, and adding one on top would
make the gate fail on correct responses:

1. **`GET /api/v1/skills/{skillId}` is documented with the wrong schema.** The
   route `$ref`s `Skill`, the list shape. The handler serializes
   `skillDetailResponse` (`internal/api/skills.go:272`), which embeds
   `skillResponse` and adds eleven more fields. A matching `SkillDetail`
   component already exists and **no path references it**. Fix the `$ref` in
   `responseSchemaName` before touching required there.
2. **`RunRecordList` is shared by two routes that return different shapes.**
   `/run-records` emits `runRecordDTO`; `/runs` emits a journal-derived
   `runEntry` with different field names entirely. Marking `RunRecord`'s fields
   required would make `/runs` fail validation — a correct response reported as
   a violation, the exact false-failure this document warns about. `/runs`
   needs its own schema first.
3. **`WorkspacePipelineResponseV1` describes 9 properties; the handler
   serializes 27, with different names.** That is a schema rewrite, not a
   required list.

Also found, and worth a separate cleanup: `main.go`'s `responseComponents()`
defines `Workspace`, `Crew`, `Agent`, `Project`, `Issue` and `Skill` shapes that
`schemas_core.go` overwrites in the same merge loop. They are inert. Anyone
editing them changes nothing and is not told. Three components
(`CrewIssueCommentsResponseV1`, `RemainingCrewIssueCommentCreatedV1`,
`LabelList`) are defined and referenced by no path at all.

## 5c. A near-miss worth keeping

Applying B2's field lists with one regex across four schemas appended `Crew`'s
and `Agent`'s fields to **`Project`'s** required list instead of their own. The
cross-check caught that crew and agent were still wrong. Nothing caught the
corruption of `Project`, because `Project` had no pair in the table.

Hence the rule now written into the test file: **a schema being edited gets a
pair first.** The pair is not paperwork after the fact; it is what makes the
edit safe to make at all.

## 5d. Two limits the mechanism has, found by using it

**A response built from a function-local struct cannot be graded.**
`/admin/stats`, `/admin/workspaces` and `/admin/users` declare their response
type inside the handler function, and a function-local type cannot be named
from a test. The fix is to promote it to a package-level DTO — better practice
anyway, and the only thing that brings those routes into reach. Until then they
are invisible to the pair table and to anyone reading it, which is worse than
being excluded, because nothing says they are missing.

**A component backed by more than one struct cannot be derived from "the"
struct.** `Integration` is one component over three structs across four routes;
`CLIToken` is shared by create (returns the plaintext token) and list (never
does), and both bodies are `map[string]any` literals with conditional keys, so
there are no tags to read at all. Both are now exclusions with that reason
written down. The underlying fix is to split the schema per route.

**Three closures still block whole files**: `schemas_final_admin_platform.go`
(10 live components), `schemas_remaining_admin_system_v2.go` (the entire keeper
surface, all inline), and `schemas_credentials_connectors_auth_profile.go`
(integrations, connectors, CLI tokens, profile). Widening one is a three-line
change and belongs to that file's batch.

## 5e. Security findings from the credentials sweep

Not `required` problems, but they surfaced while deriving one and they should
not be lost in a report:

1. **`FinalCredentialReveal` carries an unconditional `value`** — the plaintext
   credential. That is the intended one-shot reveal endpoint (gated, audited,
   rate-limited), so it is not a defect, but naming it required states in the
   published contract that a secret is always in that body. Someone should
   decide that deliberately before it reaches public docs.
2. **The notification-channel schema declares a `secret` property that the list
   endpoint never emits.** `notify.Channel` tags `Secret` as `json:"-"`. The
   POST-create response is a different struct where the field does exist, with
   `omitempty`. One schema describes two realities, and the wrong half
   advertises a secret on a listing route.
3. **`env_json` / `args_json` on integrations reach any workspace member who
   can list them.** Schema and struct agree, so it is not drift — but an admin
   can put a literal secret there instead of a credential binding.

## 5f. The fourth artifact: reality

Everything above verifies the document against the *code*. Nothing verified it
against the *server*, which is where the original defect lived: the struct, the
schema and the docs can all be internally consistent and still describe
something the handler does not do.

`scripts/api-contract/response_shapes.py` closes that. It fetches
`/openapi.json` from the target instance — not the file in the repo — hits a
list of read-only GETs and validates each body against the schema that instance
publishes. Run against dev2 after B1–B3: **13 pass, 0 fail.**

The first run reported **nine failures**, all `None is not of type 'string'`.
None of them was real. JSON Schema has no `nullable` keyword; that is OpenAPI
3.0's spelling, and Schemathesis understands it where a plain validator does
not. This is the second time that trap cost an hour on this work, which is why
`denullable()` carries the explanation and the checker has a test for it.

It is deliberately NOT a Go test. A test that needs a running server is the
undeclared-local-service dependency release-1.0 condition 6 forbids; its unit
tests run in CI, and the live run belongs to the contract-gate step that already
boots an ephemeral server.

## 6. Exit criteria

1. Every response schema that a Go struct backs has a pair in
   `responseShapeContracts`, and the test is green.
2. `TestResponseSchemas_WithoutRequired_DoNotGrow`'s budget is 0.
3. `TestResponseSchemas_RejectABodyWithEveryFieldRenamed` covers at least one
   route per batch.
4. **Only then** close #1815 and delete `API_CONTRACT_ADVISORY` from `ci.yml`.

Point 4 is the reason this document exists. Closing #1815 first makes the gate
blocking without making it capable: it would go green over a wholesale renaming
of every field in a response, exactly as it did.

## 7. Risks

- **False failures from Schemathesis.** Marking a genuinely optional field
  required turns a valid response into a reported violation. §2's mechanism
  prevents this by construction — the required list comes from the same tags
  the encoder reads — but only for schemas that have a pair. A schema tightened
  by hand, without a pair, carries this risk in full.
- **`nullable` is OpenAPI 3.0, not JSON Schema.** A plain JSON Schema validator
  ignores it and will report a legitimate `null` as a type error. Schemathesis
  understands it; the fixtures in
  `cmd/gen-openapi/response_shape_contract_test.go` deliberately avoid nulls so
  the test measures field names and not the gap between two validators. This
  cost one false alarm during authoring.
- **Concurrent sessions.** This clone runs several agents at once. The ratchet
  count is the coordination point: a batch that does not lower it has not
  landed.

## 8. Non-goals

- **Request bodies.** 84 request components also lack `required`. The failure
  mode is different — a too-loose request schema makes Schemathesis generate
  bodies the server rejects, which reads as a server fault — and it deserves
  its own pass.
- **Generating schemas from structs wholesale.** That is the end state this
  points at, and it is a rewrite of `cmd/gen-openapi`, not an increment. The
  pair table is the cheap 90%: it gets the derivation guarantee without the
  rewrite, and it makes the eventual rewrite verifiable, because the tests
  already say what the answer should be.
- **`additionalProperties: false`.** Stricter still, and it would break every
  client that tolerates a field being added. Not proposed.
