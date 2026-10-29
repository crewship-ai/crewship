# WordCraft Yrs acceptance harness

This is a local disposable-instance runner for nine collaboration scenarios.
It forwards browser traffic to the real product, requires independent committed
state and receipts, and never fabricates an authority reply. Missing integration
is reported as `not-run` and returns exit code 1. An executed assertion failure
is `failed`. Neither outcome counts as passing acceptance evidence.

Run infrastructure checks without a server:

```sh
crewship-heavy node --test e2e/wordcraft-yrs-night/*.unit.mjs
```

CLI invocation inside the supported disposable server lifecycle follows. The
production main adapter currently returns NOT-RUN: the owned TLS transport
contract below is not implemented. This invocation documents inputs, not a claim
that the production editor works over the helper's plaintext HTTP origin.

```sh
crewship-heavy env \
  WORDCRAFT_NIGHT_ADAPTER="$PWD/e2e/wordcraft-yrs-night/main-adapter.mjs" \
  WORDCRAFT_NIGHT_FIXTURE=/tmp/wordcraft-fixture.json \
  WORDCRAFT_NIGHT_INTEGRATION=/tmp/wordcraft-real-integration.mjs \
  WORDCRAFT_NIGHT_PRODUCT_SHA=<exact-40-character-product-commit> \
  WORDCRAFT_NIGHT_OUTPUT=/tmp/wordcraft-night-results \
  bash scripts/throwaway-server.sh run wordcraft-night \
  --binary /tmp/crewship-wordcraft -- \
  node e2e/wordcraft-yrs-night/run.mjs
```

The binary must include the real static frontend, native WordCraft editor and
Mooring release assets. The fixture/integration module must use its real product
authorization. This runner does not create users through SQL or disable access
checks. The existing C1 fixture and C1 read endpoint are incompatible with Yrs.

The version endpoint requires authentication. After fixture authentication the
runner reads `/api/v1/system/version` through each actual page context's shared
cookie `APIRequestContext`, with redirects disabled. It compares the returned
commit with the requested exact SHA and records the actual response identity.
The default adapter receives a `verifyIdentity(context)` callback for this check
before navigation; custom adapters also receive a mandatory check on their
returned page contexts before scenario input. No claimed-SHA fallback exists.

Node units use `.unit.mjs` so default Playwright discovery cannot import them as
browser tests. The discovery regression invokes the actual default configuration
with `--list`, a non-listening external loopback base and `--pass-with-no-tests`;
it starts no application server and requires an empty harness suite/error list.

## Owned local production TLS contract — not implemented

Production Next export rejects plaintext editor iframe URLs even on loopback.
The production main adapter therefore stops before opening a context. Supplying
an HTTPS environment URL or an `owned:true` declaration does not unlock it.
The existing HTTP manifest guard and `localURL` remain restricted to their exact
helper backend origin. Custom HTTP adapters can exercise compatible development
frontends; that does not qualify the production editor transport.

A later transport implementation must satisfy these boundaries:

- The runner creates an in-process Node `https.Server`, binds it to `127.0.0.1`
  on an allocated port, checks its live listening address and owns its lifecycle.
  Ownership comes from the live server instance created by the runner, not a URL,
  fixture flag or serialized manifest extension.
- The sole upstream is the exact HTTP backend from the supported running helper
  manifest. HTTPS requests and WSS upgrades forward to that backend without
  replacing application responses, receipts, authorization or document content.
- TLS credentials belong to the disposable fixture. Trust exceptions, if needed
  for its certificate, stay confined to guarded contexts using that owned origin;
  no process-wide TLS verification override is allowed.
- Browser/API/WebSocket guards recognize only the live owned HTTPS/WSS endpoint
  and its verified upstream mapping, including redirect checks. Authentication
  and build identity still use real cookie contexts through that transport.
- Cleanup closes the owned HTTPS listener, tracked sockets and WSS connections;
  backend shutdown remains the supported helper's responsibility.

Until that implementation is independently reviewed, production scenarios stay
NOT-RUN. The independent local HTTPS product probe is separate evidence and does
not make this generic CLI transport implemented.

`WORDCRAFT_NIGHT_SCENARIOS=1,9` selects scenarios; default is all nine.
`WORDCRAFT_NIGHT_SAMPLES` defaults to 30 and requires at least 20.
`WORDCRAFT_NIGHT_SOAK_MS` defaults to 3,600,000 and cannot reduce the soak below
60 minutes. `WORDCRAFT_NIGHT_DEPENDENCIES` optionally locates a dependency root
with the repository's installed `@playwright/test`.

## Integration contract

The fixture JSON has `scenarios` keyed by scenario number. Each fixture contains
`path` (same-origin document path), `identities` (two distinct humans),
`storageStates` (two private Playwright auth-state paths), and optional
`glyphCrop: {x,y,width,height}` in page coordinates. Each scenario owns a separate
disposable document. Scenario 1/9 starts with one blank paragraph; scenario 2
needs at least two paragraphs; scenario 3 needs nonempty text.

The integration module exports `connect({pages,contexts,baseURL,fixture,scenario,output})`.
Connect installs passive observation before navigation. It returns these hooks:

| Hook | Required behavior |
| --- | --- |
| `read(page)` | Independent committed `{revision:string, paragraphs:[{id,text,runs}], clientParagraphs:[same-shape]}`. Runs expose UTF-8 byte `len` and complete formatting properties. Client projection must be read from the actual engine/accessibility, not copied from the authority response. Preserve all supported formatting in comparison. |
| `receiptCursor(page)` | Passive observation cursor before input. |
| `waitSaved(page,cursor)` | Wait for original operations after cursor to finish, with no outstanding writes, real host Saved, and `{op_id,revision,durability:'committed'}` bound to the server receipt. A mere synchronized/update event is insufficient. Multi-character actions must await the final operation. |
| `selectParagraph(page,id,'start'\|'end'\|'all')` | Real native editor selection of stable paragraph ID via UI. |
| `formatSelection(page,{color})` | Use the actual formatting UI. |
| `mcp({command,paragraph,text})` | Real MCP request as separately authorized agent; result includes `transport:'mcp',principal,op_id,revision,durability`. |
| `rejectDependency()` | Restrict real mandate, hold original A before authority, and return control with `waitPending('A'\|'B'),release(),rejected()`; rejection has `{reason:'outside_mandate',authority:'product'}`. |
| `holdBeforeAuthority()` | Hold actual writes before authorization; control provides `waitPending(),release(),rejected()`; rejection reason `forbidden`. |
| `revokeWriter(identity)` | Revoke through real owner/product action; return `{authority:'product'}` after completion. |
| `retainedDraft(page)` | Read user-accessible retained proposal text. |
| `dropNextCommittedReply()` | Drop only an actual reply after actual commit. Control provides `committed(),retryOriginal(),recovered()`; preserve original packet and op ID. Never manufacture committed evidence. |
| `coldRestart()` | Kill and restart the owned server with the same database, no page reload to hide missing reconnection. Return `{beforePID,afterPID,cleanShutdown:false}`. Use only a supported lifecycle control from the parent runner. |
| `metrics()` | Real `{documentBytes,snapshotCount,snapshotBytes,journalBytes,receiptBytes,historyBytes,sqlBytes,walBytes,rssBytes,revision,pid}`. Bytes/counts are nonnegative integers; explain query/accounting method in run evidence. |
| `deadKeyInput(page,{deadKey,baseKey,expected})` | Actual platform keyboard layout input; do not insert a precomposed character and claim dead-key coverage. |
| `close()` | End owned observations/resources without deleting shared data. |
| `savedEvidence(page,opId)` | Passive evidence `{state:'Saved',op_id,revision,clock:'node-monotonic',receiptReceivedAt,enteredSavedAt,prematureSavedObserved:false}`. Correlate actual host transition with the original receipt and use the runner's Node clock. Do not read a stale Saved label after waiting for a receipt and report it as an observed transition. |
| `holdBeforeCommit()` | Hold actual SQL transaction before COMMIT; control `waitBeforeCommit()` returns original op_id, `release()` resumes real transaction. Used for explicit negative SQL/UI assertions before commit, outside timed samples. |
| `savedTransitionCursor(page)` / `savedTransitionsSince(page,cursor)` | Passive actual host state transition trace, capturing any premature Saved while the transaction is held. |
| `glyphReference({glyph,crop})` | Independently qualified PNG Buffer with `method:'standalone-pinned-engine'` or `'reviewed-pixel-oracle'`, `glyph,engineSHA,producerId,includesEntireGlyph:true,source:{path,line,sha},png`. Match actual session `engineSHA`; reference producer must differ from session `producerId`. Never calibrate identity by copying the measured canvas's unknown shape. |
| `readConfirmedGlyphText(page)` | Scenario 1's narrower independent authority read `{method:'authority-read',paragraphCount:1,text,revision}`. The measured client content oracle is the independently qualified glyph pixels, so this scenario does not pretend to have a complete client model projection. Scenarios 2–9 retain full projection requirements. |
| `splitJoinPolicy()` | Source-qualified `{concurrentInsertionAtSplit:'left'\|'right',retainedSplitId:'left',joinSurvivor:'left',source:{path,line,sha}}`. Missing policy means NOT-RUN. Test compares all prior text, per-byte run formatting, stable IDs and unrelated paragraph properties. |
| `receiptLedger()` / `retryCommittedOriginal(opId)` | Read actual document SQL receipt rows and retry the original unchanged operation packet after restart. Capacity scenario checks all operation IDs exactly once, same results/revisions, and no extra text. |
| `documentOperationCounts()` | Actual read-only SQL counts `{changes,receipts,audit}` for the owned document. Lost ACK verifies delta exactly one for a single original operation; composition verifies no preedit writes and one final commit. |
| `compositionCursor(page)` / `compositionEventsSince(page,cursor)` | Passive real trusted browser composition event trace with type/data/isTrusted. |
| `compositionState(page)` | Actual native-editor preedit `{source:'native-editor',active,text,visible}`. Do not derive this from the CDP command the test just sent. |
| `operationRecords(opId)` | Independent read-only SQL inspection `{method:'read-only-sql',changes:[],receipts:[],audit:[]}`. Each row has actual `op_id,revision,principal`; agent audit also has `mandate_id`. Do not synthesize rows from a response. |
| `crashAfterCommitBeforeBroadcast()` | Instrumented barrier after real SQL COMMIT and before any Saved reply/content broadcast; kill the owned process. Control `waitCommittedAndKilled()` returns original `op_id,broadcastCount:0,savedReplyCount:0`; `restart()` uses the same DB/live clients. |
| `crashBeforeCommit()` | Kill after change SQL is written within the transaction and before COMMIT; control `waitKilled()` returns original `op_id`; `restart()` retains the DB. The harness verifies all three row kinds absent and document unchanged. |
| `readClient(page)` | Read actual current native client model without querying server; usable while server is dead. |
| `candidateTimings(opId)` | Instrument actual candidate construction and validation separately; `{creationMs,validationMs,clock:'server-monotonic',source:{path,line,sha}}`. No screenshot/RAF/roundtrip substitutes. |
| `writeMode(page)` / `readMode(page)` | Observe actual host/native capability state: `read-only` after write revocation, `denied` after read revocation. |
| `deliveryCursor(page)` / `contentDeliveriesSince(page,cursor)` | Passive wire observation of content delivery. Return actual content-delivery records since cursor, not the current authority document. |
| `revokeReader(identity)` | Actual owner/product revocation of read access; return `{authority:'product'}`. |
| `deliveryFence(peerPage)` | Await the real post-commit delivery barrier after peer save; ensure delivery attempts preceding it have completed before asserting no content reached the revoked reader. |
| `offlineProposalFixture()` | Separate fresh writable document fixture; return `page,canvas,readCommitted(),disconnect(),revokeWriter(),reconnect(),reauthorization(),writeMode(),retainedDraft(),submissionsAfterReconnect(),close()`. Disconnect affects the real browser transport. Reauthorization must be observed from fresh product auth; submissions must be passively observed. |

`mcp` additionally returns `mandate_id` and
`authorship:{principal,mandate_id}` bound to the original accepted operation.
The harness independently matches the SQL audit's principal and mandate.

Crash gates, monotonic timings and delivery barriers are **test instrumentation**
at named real execution boundaries. Report their exact source path/line/commit in
run evidence. They cannot authorize writes, generate receipts, alter candidates,
or provide expected models. SQL rows, actual client state, real receipts, and
post-restart catchup are **product evidence**. Post-hoc row presence alone does
not prove atomicity: scenario 7 verifies the pre-COMMIT rollback and the
post-COMMIT/pre-broadcast recovery boundary separately.

The original Yrs wire receipt uses `durability:'node'`, while this adapter contract
uses the normalized assertion `committed`. Normalize only after validating the
actual original packet's operation IDs, scope, revision and receipt with the
released SDK and proving its server path commits before delivery. Do not rename
an arbitrary `node` frame or a UI label into committed evidence.

For a different experimental frontend, export `setup(...)` directly from a custom
`WORDCRAFT_NIGHT_ADAPTER`. It returns these hooks plus `pages`, `identities`,
`canvas(page)`, `glyphCrop(page)`, `resetBlank()` and `close()`. Create contexts
with `localContext` and make API calls with `localRequest` from `local.mjs`.
Every endpoint and redirect must remain on the manifest's exact loopback origin.
The guard forwards HTTP responses and WebSocket messages without modifying them.

## Measurements and limits

Scenario 1 requires an independently qualified complete `w` glyph reference,
verifies blank pixels differ, and uses Playwright `KeyW` input. A passive listener
in the actual iframe records trusted DOM keydown key/code/timeStamp/timeOrigin.
Seven browser/Node clock roundtrips provide offset bounds; the narrowest interval
is used to translate each actual event and report uncertainty. API-dispatch time
is recorded separately and never presented as the DOM event or physical keyboard
time. Exact glyph crop screenshot completion is a conservative render upper bound.
Include the full glyph; exclude caret, selection, toolbar and status. No RAF is
glyph evidence. Before timed samples, a real pre-COMMIT barrier proves no SQL
records and no Saved transition. Each sample then matches original SQL
change/receipt/audit to the actual post-receipt Saved transition. Values exceeding
50/300ms observation upper bounds produce `inconclusive` (nonzero), since they do
not establish an actual product SLO violation. `latency.json` retains raw events,
calibration, uncertainty, reference provenance, percentiles and API upper bounds.

Scenario 2 checks convergence, per-participant insertion order and real MCP
identity for same/different paragraphs, then concurrent Enter/text input and join
with stable ID/text preservation. Scenario 3 exercises Jan/Petr formatting
and Jan's Undo. Scenarios 4–6 use real authority plus transport fault controls.
Scenario 7 checks confirmed state after cold restart with live clients. Scenario
7 also kills before COMMIT (all change/receipt/audit rows absent), and after
COMMIT before broadcast (all rows present, no peer delivery, then live catchup).
It measures candidate creation/validation independently with server monotonic
instrumentation. Scenario 5 verifies write-only revocation preserves reading,
read revocation stops content delivery, and offline proposals remain accessible
without automatic replay after fresh authorization denies writing. Scenario
8 requires 5,000 distinct committed operation IDs, ten process replacements,
and a further uninterrupted hour with periodic SQL/WAL/storage/RSS evidence.
Storage growth is measured, not automatically declared acceptable: compaction,
history retention and duplicate protection require review against these series.
Scenario 9 observes native visible preedit updates/cancellation, trusted browser
composition events, no preedit SQL writes, and a single final durable transaction;
ordinary final insertText alone cannot pass. It also requires an actual platform
dead-key hook. `chromium-ime.json` preserves the partial composition result if the platform
dead-key hook is unavailable. This does not qualify the whole scenario.

Infrastructure unit tests verify fail-closed reports and target validation. They
are not product scenario results. Browser/engine qualification requires actual
fixture hooks and a full real server run; all missing boundaries stay explicit.

## Seeded model-level command generation

`fuzz.mjs` supplements targeted scenarios with deterministic 2–3 participant
rounds, serial engine/binding comparison, concurrent convergence, semantic
conservation per operation/participant, native model invariants, committed
receipts, reload and restart checks. It reads the machine-derived capabilities
catalog and requires a separate machine-generated, source-qualified external
effect policy. A `pure` flag or `document-candidate` classification grants nothing.
Unclassified and effectful commands are excluded before any engine call.

```sh
crewship-heavy env \
  WORDCRAFT_FUZZ_POLICY=/tmp/wordcraft-effect-policy.json \
  WORDCRAFT_FUZZ_ADAPTER=/tmp/wordcraft-real-model-adapter.mjs \
  WORDCRAFT_FUZZ_OUTPUT=/tmp/wordcraft-night-results/fuzz \
  WORDCRAFT_FUZZ_SEED=20261027 WORDCRAFT_FUZZ_COUNT=100 \
  node e2e/wordcraft-yrs-night/fuzz.mjs
```

The policy contains `catalogSha256`, `engineSHA`, `machineGenerated:true`,
`classifier:{path,sha256}`, and `commands`. Each admitted command has `id`,
`externalEffects:'none'`, `parameterClosure:'qualified'`,
`registryDescriptorSha256`, `sourceProof:[{path,sha256}]`, a closed bounded
`parameterSchema`, and a `meaningOracle` identifier. Source proof identities must
match the catalog; classifier bytes and exact catalog bytes are hashed. This
records provenance, while independent review must establish that the classifier
actually closes all external-effect branches. Until that review and policy exist,
the runner is blocked. It does not infer effect safety from command names.

Schemas support closed objects, bounded integers, booleans, enums, bounded arrays,
and strings with a closed `alphabet` and maximum length. The adapter exports:

- `preflight({engineSHA,localOnly:true})`: verify exact real engine, isolated
  disposable instance and approved policy profile before execution. Return actual
  `standaloneEngineSHA`, `bindingEngineSHA` and `modelEngineSHA`; all must equal
  the machine catalog's engine SHA. Derive these from the build/source identity,
  never echo the requested SHA. A mismatched upstream model dependency blocks the
  comparison before any command executes. The runner also verifies the same
  disposable local server manifest as the targeted scenario runner.
- `isValid(trace)`: check real engine preconditions without side effects; invalid
  traces and invalid shrink candidates do not become failing engine examples.
- `execute({mode,trace,seed})`: mode is `standalone`, `binding-serial`, or
  `binding-concurrent`; return `documents`, `invariantErrors`, `receipts` with
  `opId/participant/durability`, plus independently read `reloaded` and
  `restarted` models. Every mode starts from the same fresh fixture.
- `semanticConservation({trace,result})`: independent model meaning oracle for
  every operation, returning `{opId,participant,preserved,expectedMeaning,
  observedMeaning}`. Do not implement this by copying expected to observed.

Each accepted seed is compared serially first; simultaneous rounds then check
convergence and intended operation meaning without assuming one arbitrary serial
ordering for every CRDT conflict. Invalid combinations are counted separately.
On failure, deterministic delta debugging removes rounds/operations while keeping
the same failure signature; JSON regression fixtures preserve original and
minimized traces, seed, and signature. These are 1-minimal under operation/round
deletion, not a proof of the globally smallest parameterized program. Replay the
stored trace with `WORDCRAFT_FUZZ_REPLAY=/tmp/wordcraft-night-results/fuzz`
(directory) or a single regression JSON path, passed through the same `env`
invocation above. Replay verifies engine/catalog/policy/classifier hashes and
revalidates each command/parameter against the same safe closure before executing.
It records `reproduced`, `different-failure`, or `resolved` for every stored seed;
only all-resolved replay returns success. Regression files carry those identity
hashes alongside original/minimized traces and signatures.
`report.json` records requested/executed combinations, rejected inputs and failure
seeds. A test-only synthetic model checks determinism and proves that convergence
with one participant's lost work fails conservation; it is harness-unit evidence,
not engine qualification.
