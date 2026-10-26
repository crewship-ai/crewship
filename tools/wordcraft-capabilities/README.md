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
