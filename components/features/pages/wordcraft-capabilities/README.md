# Catalog-driven WordCraft controls

These opt-in components support the C1 command host. They are not mounted by the
legacy editor, and do not change its Yjs path.

`WordCraftCatalogRibbon` renders command labels and groups from the generated
catalog. No hand-maintained ribbon command list is required. The generated
catalog qualifies nothing. An integrator can supply a qualified catalog **only
after** each command's full-path qualification; effective `allowedCommands`
come from the existing server authorization for this participant and document.
No new roles or role database are introduced.

A command is active only when its catalog and participant versions match the
pinned engine/catalog, its schema is exact, it is qualified and it is in the
participant's effective command list. Permanent prohibitions in the generated
catalog cannot be removed by a supplied catalog. Every inactive button explains
`nemáte oprávnění`. The same `createWordCraftCommandGate` validates parameters
and dispatches button actions and keyboard shortcuts. Changed permissions must
replace the participant input; no grant is cached beyond that gate instance.

Supply `parameters` for commands that require fields. Selection, base revision,
operation identity and participant context must be captured by the C1 host at
actual dispatch, and verified by the server. Mouse activation preserves editor
focus/selection. The gate rejects unknown fields in the supported closed schemas.

Attach `wordCraftShortcutHandler` in the actual editor's capture/dispatch path,
with the platform (`mac` for Command, `other` for Control) and the same gate.
It consumes denied registered shortcuts, refuses ambiguous mappings and leaves
IME composition untouched. It must not be attached globally to unrelated UI or
to the ribbon's normal button navigation. A parent React listener cannot observe
keyboard events within a cross-origin iframe; the engine/iframe adapter must
wire its own command path to this gate or the equivalent catalog decision.

Mounting this ribbon does **not** disable the engine's existing Rust ribbon,
intercept its direct shortcuts or enforce server security. Product integration
must gate every native engine action, including text input, before sending it.
The server must independently refuse the same unqualified, role-disallowed,
unknown, schema-invalid and permanently forbidden commands over every ingress.
UI state never authorizes a mutation.

Run the focused UI/shortcut suite:

```sh
crewship-heavy pnpm exec vitest run components/features/pages/wordcraft-capabilities
```

The separate [CLI/MCP acceptance suite](../../../../tools/wordcraft-capabilities/acceptance/README.md)
requires a disposable server and fails when integration/configuration is absent.
