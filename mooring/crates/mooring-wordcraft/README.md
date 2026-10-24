# WordCraft native model adapter

This default-off crate is not connected to Crewship routes or the runtime. It
imports complete native WordCraft 0.10.0 JSON into Yrs 0.28.0, projects it back,
and restores the same native data and stable paragraph IDs from a full CRDT
checkpoint. Export and checkpoint bytes are candidates, never Saved receipts.
The host alone owns authentication, scope, permission checks, durable commits,
history and audit. It exposes stateless candidate preparation, with no authoritative mutation or raw
human update entrypoint.

The model retains document metadata, explicit paragraph order and caller-issued
stable IDs. Each paragraph has a nested Y.Text, so an empty paragraph's shared
type identity survives storage recovery. The adapter converts UTF-8 byte run
lengths to UTF-16 positions at checked Unicode scalar boundaries. Model 0.2 uses action-scoped
Yrs property keys, so an equal-value peer format keeps its own identity during
selective own undo. Independent bold/italic slots and explicit empty-properties
intent retain the distinction between inheritance, explicit empty properties
and explicit false.
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
multiuser integration. Full-native selective undo/redo has model regressions for peer same/different,
inherit and explicit-empty properties. Authenticated human admission, rejected
independent intent recovery, native UI integration and host wiring remain
separate qualification.

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
public raw apply endpoint. `adapter::WordCraftAdapter` restores once into an
owned candidate, qualifies the complete host context against actual inventory,
checks bound original reads before every operation, and advances only the
host-assigned revision labels after paragraph effects. Every text command must
explicitly observe its affected paragraph. A failure discards the full candidate
prefix; invalid/incomplete context and corrupt state are nonterminal errors.
Durable revisions and outcomes remain in Go. Confirmed-read
agent commands do not replace the separate human path: human proposals must
preserve their authored CRDT updates and IDs rather than reexecute this grammar.

The caller must authenticate and bind the private context to the exact scope,
confirmed checkpoint/head and immutable change before calling `PrepareAdapter`.
The first policy supports confirmed-read agent commands only; it does not qualify
provisional reads, a human CRDT update stream or speculative causal chains.
Inventory includes all paragraph IDs plus the disjoint reserved targets
`wordcraft:document-structure` and `wordcraft:document-metadata`, including for
read-only documents. Text operations conservatively advance only the paragraph
content revision (also for a semantic no-op), never paragraph existence, order or
document metadata. Application checkpoints contain no authoritative host revision
ledger. The default-off crate is still not wired into any production route.

Native reads can project an already authenticated immutable checkpoint through
`read::header` and `read::page`. The header retains every document field except
body; pages retain complete native paragraphs plus their stable IDs. Both have
explicit serialized-byte budgets (at most 262144 bytes); pages also cap paragraph
count at 256. Unknown cursors and oversized individual paragraphs/headers are
explicit errors, never truncation. The host reserves its envelope overhead and
binds each after-ID cursor to the same scope, confirmed revision and checkpoint
digest. These pure functions do not authenticate remote cursors or authorize a
content path. Read-only native documents remain readable in full.

Model 0.2 serializes `wordcraft-0.10.0/yrs-0.28.0/v2` checkpoints with a
canonical base64 CRDT update. Decoded size and pad bits are checked before
allocation; the native JSON is parsed from original bytes, preserving opaque
integers beyond binary64 precision. Restore also rejects hidden roots and map
keys not represented by the complete native model.

Legacy `.../v1` checkpoints remain readable with their full native data, stable
paragraph IDs and original CRDT identity. They are always read-only, and export
retains the v1 marker. Importing a new v2 artifact is an explicit host operation;
retiring old sessions/origins and binding a new epoch are not performed here.
A restore never silently migrates an existing artifact into writable v2 state.

Action keys contain a canonical bounded logical stamp and origin, never a Saved
host revision. The current agent helper derives its stamp only from the
host-authoritative confirmed CRDT state. It must not be used for human proposals:
a qualified causal witness must avoid coupling an independent operation to
unrelated optimistic changes. Human origin binding, stamp/read qualification and
current authority still require their own closed protocol capability.

The earlier v1 raw-delta structural validator now compiles only in tests. Its
text-preservation checks are not full peer-format undo qualification; model-v2
production code exposes no raw human update admission. No native WordApp command
hook, browser human service or deployment is implied by these model regressions.

### Causal agent preparation

`CausalPrepareAdapter::prepare_atomic_causal` is a separate opt-in boundary. It
qualifies every original observation and the complete restored inventory through
`CausalContext`. The trusted host attests complete durable external history; the
adapter additionally checks that earlier effects within the same atomic change
are covered by its immutable dependency closure. It shares application mechanics
with strict-current preparation, but never changes original observations or
forwards between read policies. Unsupported human updates remain rejected.

This pure method supplies candidates and effects only. It does not negotiate a
private profile, authenticate host attestations, commit revisions, or emit Saved.

Checkpoint restoration preflights the complete original Yrs v1 bytes before
allocating any upstream Update: at most 4,096 encoded clients and 262,144 total
structs/deletion ranges, checked u32 clocks, canonical u53 IDs/counters, bounded
UTF-8 strings and exact frame consumption. Only this model's Map/Array/Text,
string metadata/IDs and closed scalar formatting values are accepted. Nested Any
containers, embeds, subdocuments and unknown content kinds fail nonterminally;
this is not general-purpose Yrs feature support or a human admission API. Scope,
authority, negotiated transport limits and checkpoint provenance remain the
caller's responsibility. The scan borrows the input and allocates nothing.

The optional native `DescribeCheckpoint` implementation restores the exact
provided bytes before describing them. Its complete empty external manifest is
qualified only for the closed self-contained model-v2 paragraph subset, including
known D3 page breaks and paragraph section layout. The complete original native
tree must roundtrip exactly through the pinned typed model before a disposable
classifier copy omits only those two layout fields. Every original section must
have no header/footer references, and all media/parts, external templates, linked
styles, page pictures, opaque XML, retained links and inline objects remain
unqualified. Unknown fields, numeric drift, review/protection modes and legacy
encodings fail closed; compatibility versions never fill missing historical data.

Read-only layout remains read-only after description, paging and restore. This
metadata result grants no authority, installs no state and changes no original
native data, stable IDs or checkpoint bytes. The host must authenticate/bound the
request and retain the complete description with the exact checkpoint in the same
authorized transaction. Embedded-object and table resource descriptions require
separate qualification; embedded previews alone do not prove absence of external
OLE/graphic relationships.

`origin_inventory::inspect` is a separate inactive read-only qualification for a
host-authenticated exact model-v2 checkpoint. It returns a sorted complete
allocated-client frontier and borrows the exact source bytes; it is neither
serialized authority nor a new digest domain. Deleted content retains its
origin. Candidates must be nonzero exact 53-bit integers before any upstream
ClientID construction; an occupied origin or a full 4,096-client frontier is
refused. Legacy, read-only and invalid checkpoints cannot qualify an origin.
The host must additionally reserve document-wide uniqueness against ALL issued
origins (including pending authors absent from the checkpoint), count those
reservations within capacity, and bind the original author, full checkpoint
identity and source order in its authorized writer. This helper alone issues
no allocation, participant capability, human update admission or Saved.

### D1 human producer integration

The D1 integration composes the previously qualified original-delta producer,
full native transition checks and retained-basis validator without changing the
model format or the current descriptor, read-only classifier or agent adapter.
The host supplies current authenticated connection context; numeric origins and
local roles remain data, not authority. Persistent issuer/journal work is not a
prerequisite for the ephemeral connection integration.

The E3 undo entrypoints permit own insertion and formatting only. Ordinary text
delete/backspace remains supported. Undo or redo of an action containing deletion
returns `UnsupportedUndo` before mutation or history removal; it never skips that
action to reach older history. The WASM facade uses these restricted entrypoints.
The older experimental undo methods remain research APIs, not the E3 UI path.
Native action eligibility uses existing Yrs stack-item metadata; no replacement
CRDT or deletion-ownership model is installed.

Native regression tests retain the two-client overlapping-delete case as an
explicit refusal, and compare the complete peer-only native document after own
insert/format undo, both delivery orders, redo and cold restore. These tests use
synthetic delivery context. Real HTTP/WebSocket admission, host-backed Saved,
WordApp browser wiring and the two-human-plus-agent acceptance remain separate
integration gates. Local Save, a candidate or a local role never implies Saved.
