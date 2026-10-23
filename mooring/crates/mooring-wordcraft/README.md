# WordCraft native model adapter

This default-off crate is not connected to Crewship routes or the runtime. It
imports complete native WordCraft 0.10.0 JSON into Yrs 0.28.0, projects it back,
and restores the same native data and stable paragraph IDs from a full CRDT
checkpoint. Export and checkpoint bytes are candidates, never Saved receipts.
The host alone owns authentication, scope, permission checks, durable commits,
history and audit. This initial slice exposes no mutation/update entrypoint.

The model retains document metadata, explicit paragraph order and caller-issued
stable IDs. Each paragraph has a nested Y.Text, so an empty paragraph's shared
type identity survives storage recovery. The adapter converts UTF-8 byte run
lengths to UTF-16 positions at checked Unicode scalar boundaries. Basic native
character flags use separate Yrs attributes: independent bold and italic edits
can merge without replacing each other's properties. A presence attribute keeps
absent formatting distinct from explicit empty properties and explicit false.
Paragraph style/list references, marks and full native metadata are retained.

Initial support covers the qualified D1 paragraph model: basic inline flags,
paragraph style and numbering metadata. Unsupported blocks, inline objects,
review/comment content, unknown native fields and unsupported character or
paragraph properties make the **whole document read-only**, retaining its full
native data. Invalid native field types/run boundaries are refused. Inspect
`read_only_reason()` before any future mutation admission; no per-block lossy
fallback is offered. Richer supported fields require qualification first.

Host callers must generate fresh IDs once during initial import, then include
those IDs and full Yrs state in the same confirmed checkpoint. Body indexes are
not identities. `restore` accepts only host-authenticated, scope/revision-verified
storage state; it is not a remote-update endpoint. Native/checkpoint byte and
block-count bounds are additional local limits; the host must enforce tighter
negotiated bounds before decoding and after candidate serialization. The
checkpoint validates that native projection, stable IDs and CRDT state agree.

Tests distinguish synthetic model/Unicode/concurrency cases from actual upstream
engine/WASM qualification. A two-replica attribute merge is not an authenticated
multiuser integration. Undo, rejected dependencies, native UI integration and
host prepare/commit wiring remain separate work.

Offline qualification accepts explicit stable IDs rather than generating index
IDs:

```sh
crewship-heavy cargo run --manifest-path mooring/Cargo.toml \
  --config mooring/.cargo/config.toml --locked --offline \
  -p mooring-wordcraft --example roundtrip -- \
  native.json stable-ids.json existing-output-directory
```

The executable writes native projection, full checkpoint and restored native
JSON. These output files still need actual WordCraft engine/WASM load, save and
reopen checks; a JSON equality assertion alone is not that evidence.

Actual native and WASM WordCraft normalize zero-length runs away, including
properties carried only by such a run. Those encodings therefore remain
read-only, including zero-length runs in empty paragraphs. A canonical empty
paragraph with no runs and explicit paragraph-mark properties is a separate
qualification case; unit equality alone does not qualify application behavior.

The application command grammar accepts only `text.insert`, `text.delete` and
`text.format` for a stable paragraph ID. Anchors are paragraph start/end or an
existing relative Yrs item ID with an explicit before/after association. The
client and clock counters are canonical decimal strings, bounded before Yrs ID
construction. Absolute offsets, identity/authority claims, unknown fields and
raw human CRDT updates are refused. Insert properties are complete overrides;
format properties are patches (`false` is explicit, `null` clears an override).

Parsing grants no authority and does not qualify read observations. Private
candidate mechanics resolve each relative anchor against the correct paragraph
and reject Unicode scalar splits before editing an owned fork. They expose no
public apply endpoint. The forthcoming host-context wrapper must validate reads
and account for effects between operations, discard the full candidate after
any failure, and leave durable revisions and outcomes to Go. Confirmed-read
agent commands do not replace the separate human path: human proposals must
preserve their authored CRDT updates and IDs rather than reexecute this grammar.

Native reads can project an already authenticated immutable checkpoint through
`read::header` and `read::page`. The header retains every document field except
body; pages retain complete native paragraphs plus their stable IDs. Both have
explicit serialized-byte budgets (at most 262144 bytes); pages also cap paragraph
count at 256. Unknown cursors and oversized individual paragraphs/headers are
explicit errors, never truncation. The host reserves its envelope overhead and
binds each after-ID cursor to the same scope, confirmed revision and checkpoint
digest. These pure functions do not authenticate remote cursors or authorize a
content path. Read-only native documents remain readable in full.
