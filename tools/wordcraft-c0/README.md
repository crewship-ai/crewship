# WordCraft C0 runtime conditions

Opt-in failing regression probes for six prerequisites of a shared command
session. This standalone Rust workspace is excluded from normal product builds
and CI. It does not enable a runtime, replace Yjs or implement a C1 host.

The actual unpatched upstream engine and document crates are Git dependencies
pinned to `storytold/wordcraft` commit
`7ef2633a7e84956522d1ca47b99fe6feef16c1db`. Dependencies are public;
no private context, prepared source directory or captured binary is needed.
WordCraft is MIT OR Apache-2.0; its MIT attribution is retained in
[WORDCRAFT-LICENSE-MIT](WORDCRAFT-LICENSE-MIT).

Requires Rust 1.95 or later and the platform dependencies needed by the pinned
engine. Choose your own target/output directories. From the repository root:

```sh
CARGO_TARGET_DIR=/absolute/own-target crewship-heavy python3 tools/wordcraft-c0/run.py --output /absolute/new-output
```

The runner compiles first and starts each test in a separate process. Positive
controls must execute real native commands before the relevant failing oracle.
Failures return exit1, build/harness errors exit2. Nothing uses `should_panic`,
ignored tests or expected-failure masking. Logs retain the actual assertion and
structured `C0_EVIDENCE`; all six REDs are evidence of gaps, not a passing suite.

| Probe | Executed behavior and required invariant |
| --- | --- |
| Identity | Actual duplicate paragraphs, split, join, undo and native reload; persistent unique IDs survive. The proposed read seam currently inspects native block `id`; C1 must bind it to the reviewed persistent identity/checkpoint representation. Pointer/content substitutes are prohibited. |
| Unicode | UTF8 byte target correctly selects Czech text; inside combining/ZWJ grapheme positions must be refused without snapping/selection mutation. |
| Author/time | Actual comment output already contains the original author and supplied authoritative time before host rewriting; older comment remains unchanged. Baseline has author input and no injected engine clock. |
| Catalog | Known text command works; unknown fields, unknown command IDs and process-global Ignore All are refused without document mutation. Ignore All is exercised only inside its disposable process; no file/network commands run. |
| Retention/measurements | 550 edits exercise the pinned engine's real500-entry history bound,32 pending native snapshots and the complete candidate engine are held/dropped, M2 restores exact confirmed JSON, and required retained undo survives rollback. RSS/command p95/restore times are recorded;300MB/300ms are informational, never hard assertions. |
| Participant undo | Jana and Petr both edit through the engine; Jana undo removes only Jana's contribution while preserving Petr's text. Native global undo fails this contract. |

Dropping pending snapshots and K releases their Rust ownership; the allocator
may retain pages, so RSS is a measurement rather than an assertion that memory
is returned to the OS. The clock test's desired time is a specification input,
not an injected baseline engine clock: no such public input exists. A future
host must demonstrably supply clock context before executing a command.

These are initial C0 probes, not complete condition coverage or a declaration
that engine-only measurements establish platform budgets. M2 deliberately loses
native history; a future host must independently retain qualified participant
undo. The current baseline includes no such host. The identity probe starts a separate OS process loading only native JSON;
this proves native state restart, not a Mooring service crash/restart. The runner
also isolates global state between all six probes.
