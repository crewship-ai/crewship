# WordCraft capability catalog

`schemas/wordcraft/capabilities.json` is language-neutral JSON for server and
frontend consumers. It is generated from the **compiled registry**, including
commands constructed in loops. `registry.json` retains the exact engine export;
`source-identity.json` fingerprints the engine/document source. All commands
start unqualified. This catalog alone grants no permissions.

Build the public pinned source in an owned directory, then generate/check:

```sh
crewship-heavy node tools/wordcraft-capabilities/generate.mjs --source "$WORDCRAFT_SOURCE"
crewship-heavy node tools/wordcraft-capabilities/generate.mjs --check --source "$WORDCRAFT_SOURCE"
crewship-heavy pnpm exec vitest run tools/wordcraft-capabilities/generate.test.ts
```

The generator builds the CLI itself from clean pinned sources (Rust 1.95+ and
platform dependencies are required). The source HEAD must be `7ef2633a7e84956522d1ca47b99fe6feef16c1db`.
`--check --source` fails on source or registry drift. Offline `--check`
checks the committed export and generated catalog without fetching dependencies.
An engine upgrade must regenerate and review the catalog; qualification is reset.
The offline check contacts no network or live server. Source checks may fetch
pinned Cargo build dependencies; they never contact a Crewship server.

Upstream `params` is a prose DSL, **not JSON Schema**. The generator translates
only exact simple object descriptions into closed JSON Schemas. Unresolved
schemas are `false` (reject everything) and retain `parameterDescriptor` for
review. Even an exact schema describes the registry, not runtime validation of
the command implementation. Qualification must verify actual accepted fields,
values, nested operations, targets, role/mandate, persistence, reload and undo.

Groups are conservative discovery metadata. `unclassified` retains commands
outside the requested groups without guessing permission. `document-candidate`
means only that the registry declares document mutation; it does not establish
safe or bounded effects. `non-document-unreviewed` includes commands that may
have I/O. File, font management, tools/macros and dictionary commands are always
forbidden. Server consumers must reject unknown IDs, unqualified commands,
unresolved schemas, forbidden commands and any role-disallowed action. Server
integration and qualification records belong to the product integration change.

The exported registry metadata is derived from WordCraft under its MIT license;
see `WORDCRAFT-LICENSE-MIT`.

The explicit engine contract suite compiles and compares the actual registry.
It fails when `WORDCRAFT_SOURCE` is missing; it never skips:

```sh
crewship-heavy env WORDCRAFT_SOURCE=/absolute/owned/pinned/source pnpm exec vitest run --config tools/wordcraft-capabilities/vitest.engine.config.ts
```

Run it for every engine or registry update. Ordinary offline CI checks cannot
observe changes in an external engine checkout; they check the exported registry
and catalog for internal consistency. Neither check qualifies a feature.

Source-reviewed parameter profiles live in `parameter-profiles.json`. They are
closed, bounded **catalog input profiles**, not a claim to describe every
permissive/coercing input accepted upstream. The command inventory and original
parameter descriptions still come only from the compiled registry. Each reviewed
profile pins its descriptor digest and implementation-file digests; drift fails
generation. `schemaStatus: exact` describes the emitted supported profile;
`schemaCoverage: bounded-source-profile` makes its narrower scope explicit.
For example, list level values that upstream silently clamps are rejected outside
the emitted bounds, and nested unknown fields are rejected rather than ignored.

Profiles do not qualify commands. In particular, list schemas are available while
C1 still refuses the corresponding commands. Qualification must run the actual
C1 host and must not substitute a direct engine call for host target/revision,
rejected-commit restoration or participant undo. The existing frontend's small
scalar validator also needs a real JSON Schema adapter before using enums, unions,
nested objects or arrays; ignoring those constraints is not valid profile checking.

The C-E profile set resolves all 44 previously missing schemas in the four first
feature groups. `format.set` and `para.set` expose explicit bounded subsets of
`CharProps`/`ParaProps`; other upstream fields stay outside this catalog profile.
`format.enclose.text` is limited to one or two printable ASCII characters.
`text.typeCode` deliberately allows only the five known valid code strings
`65`, `66`, `67`, `0233`, `0169`; it is not a general decimal-code parser.
Line spacing `exactly` starts at 0.7pt to avoid silent clamping. These restrictions
must remain visible in integration documentation and must not be confused with
qualification or full upstream parameter coverage.

Engine evidence is deliberately a separate `engineQualification` object. An
engine-qualified fixture keeps the top-level product `qualification: unqualified`
until the integration owner proves the full P-48 path. The engine declaration
covers only the listed parameters, selection and exact revision at the pinned C1
host; it grants no role authority and says nothing about SQL or browsers.

To reproduce the complete first-group matrix, use a clean checkout of C1 at
`9f1cda0e6609a0b7ae1c3398f846a520ca9f6481` and its unchanged helper preparation/build
tools. Set the variables below to owned source/output locations; keep raw evidence
outside the public repository. All builds and tests use the host build queue.

```sh
crewship-heavy python3 tools/wordcraft-capabilities/c1-qualification/prepare.py \
  --c1-source "$C1_SOURCE" --engine-binary "$C1_ENGINE_BINARY" \
  --catalog schemas/wordcraft/capabilities.json \
  --profiles tools/wordcraft-capabilities/parameter-profiles.json --output "$C1_PREPARED"
crewship-heavy python3 tools/wordcraft-capabilities/c1-qualification/run.py \
  --prepared "$C1_PREPARED" --engine-binary "$C1_ENGINE_BINARY" --output "$C1_EVIDENCE"
crewship-heavy python3 tools/wordcraft-capabilities/c1-qualification/promote.py \
  --prepared "$C1_PREPARED" --c1-source "$C1_SOURCE" --engine-binary "$C1_ENGINE_BINARY" \
  --rows "$C1_EVIDENCE/rows" --execution "$C1_EVIDENCE/execution.json" \
  --profiles tools/wordcraft-capabilities/parameter-profiles.json \
  --receipt "$C1_EVIDENCE/receipt.json" \
  --output tools/wordcraft-capabilities/engine-qualification-policy.json
crewship-heavy node tools/wordcraft-capabilities/generate.mjs --source "$WORDCRAFT_SOURCE"
```

`run.py` always runs all 163 actual tests without filtering or skipping. The
catalog-backed C1 pin admits document candidates instead of a hand-written
command list. The reviewed matrix has 72 passing exact fixtures and 91 failing
cases: 40 denied unreviewed/non-document effects, 28 missing semantic fixtures,
13 engine refusals and 10 no-ops on the chosen baseline. Cargo remains red.
These are actual results for this pin, not a promise about future executions.

Each passing fixture verifies target/revision execution, stale revision and
removed-block rejection, restoration to P compared with a fresh actual engine,
and participant undo preserving the other actor. Semantic validation compares
**the entire native document** against an explicit expected change, including
selected properties/runs and complete nine-level numbering definitions. Five
mutants per fixture must be refused (paragraph property, run property, text,
numbering and replacement of an expected null-valued key). This is recorded as
`oracleIntegrity`, alongside the four original invariants. No expected outcome is
inferred from a captured run. Numbering expectations follow the pinned public
`doc::numbering::levels_for` and `engine::cmd::lists::define` models.

The baseline is two plain paragraphs, `Alpha` and `Peer`; `testedSetup` records any
additional precondition (the level-change fixture starts with a numbered list).
Exact tested parameter values, target and preconditions remain visible in the
engine declaration. The registry's `edit.undo` is not qualified by the host's
`participant.undo`. Non-document controls and missing semantic oracles remain
failing cases, never automatically qualified by registry mutation metadata.

Promotion requires the complete matrix and binds every row to its compiled test
sources, preparation, invocation and actual helper binary. It verifies original
runtime files against the pinned Git blobs and does not edit them. The committed
policy is declarative fixture evidence; captured logs, binary hashes and receipts
stay external. Registry, profile, fixture or test changes stop catalog generation
until evidence is regenerated. Run the Python promotion guard regressions with:

```sh
crewship-heavy python3 -m unittest discover -s tools/wordcraft-capabilities/c1-qualification -p '*_test.py'
```
