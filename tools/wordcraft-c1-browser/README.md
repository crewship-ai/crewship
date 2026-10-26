# Separate C1 native renderer seam

This opt-in source recipe prepares WordCraft 7ef2633 with the reviewed C1
clock/author patch and a separate browser overlay. It does not change the legacy
668d SDK, its build recipe, the C1 helper, the canonical command catalog, HTTP routes,
permissions or delivery authority. The original MIT notice is verified by the
engine preparation recipe. No renderer artifact is embedded into the product.

The overlay uses the same engine `native_io::open_bytes/save_bytes` codec as the
helper, with exact lossless validation. Native JSON travels as a string; parsing
and reserializing it through JavaScript changes numeric representation. Stable
colon block IDs are replaced with each confirmed projection, including Enter.
The build derives exactly 72 command names from the hash-pinned canonical
`schemas/wordcraft/capabilities.json`: qualified engine fixtures, exact bounded
schemas, document candidates and no permanent prohibition. It generates a Rust
name table; no runtime catalog input or parameter validator is introduced.
Unknown and unqualified commands are refused before pending input is reserved.
Existing ribbon enabled/tooltip hooks also mark unavailable document commands
inactive with a fixed explanation. Existing blocked `ui.*` dialogs are also
inactive; no dialog/file/import path is newly enabled. Direct qualified
controls remain subject to their existing native context conditions.
This qualification allows forwarding; it does not establish browser acceptance
of all 72 commands. The actual native registry forwards command, original params and UTF8 byte
selection without optimistic document mutation. Unsupported registry commands
show a refusal. Undo is explicitly refused before reserving pending input until
its own receipt can supply a qualified native selection; it never runs local undo.

The dedicated C1 product renderer enables a bounded local typing preview. It
rebuilds that preview with the original engine using the same batch boundaries
as the stateless server helper. Consecutive text is collected for about 120ms;
Enter and formatting seal a batch and retain their position. Only one original
is sent at a time. Limits are 64KiB of queued parameter data, 256 queued commands
and 8KiB per action/batch; a refused input has an explicit message. Confirmed SQL
state remains authoritative. Unknown/refused input stays available for copying;
there is no blind mutation retry. Nonempty caret-only character formatting is
explicitly unavailable because its transient typing properties are not part of
the server checkpoint; select text to format it. The default test seam remains
nonoptimistic unless its explicit fluid-typing option is enabled.

The separate bundle starts attached mode only with `?mooring=1` and a single
`start_c1_commands(native_json, ids_json, callbacks)` call. Callbacks are
`can_edit`, `command` (returns true only if the proposal was captured), and
`failed` (fixed reason only). These are local UI callbacks, **not authorization
or a host Saved receipt**. Host transport/SQL integration is not supplied here.
The handle accepts confirmed native JSON, IDs, an own-receipt flag and actual
committed engine selection JSON (at most 2 KiB UTF8 before copy/parse). Peer
projections require an empty selection string. `projection_pending()` observes
the actual native projection queue so the product can wait before another peer
projection. It is neither a SQL receipt nor evidence of a completed GPU paint. Missing/invalid own selection is refused; it
is never inferred from text length. Peer projections cannot clear own pending.

One pending command blocks further mutating registry input with a visible
“This input was not accepted” status. There is **no preserved typing buffer**.
Capture uncertainty retains pending and stops successors. IME is explicitly
unqualified; paste is refused rather than translated into a new command.
This is not a usable rapid-typing/IME/draft solution or live GUI qualification.
The existing host projection currently omits write-command selection results;
that integration and draft lifecycle need separate reviewed work.

Prepare into a new owned directory. A source-only checkout without the canonical
catalog may supply `--capabilities /absolute/catalog.json`; its exact pinned hash
is still mandatory, and the file is only read during preparation.
Run every build/test through the host's
heavy wrapper; use the installed Rust and wasm-bindgen toolchain, never install
or update tools as part of this recipe:

```sh
crewship-heavy python3 tools/wordcraft-c1-browser/prepare.py --output /absolute/new-c1-renderer
CARGO_TARGET_DIR=/absolute/c1-target crewship-heavy cargo build --release --locked --target wasm32-unknown-unknown --manifest-path /absolute/new-c1-renderer/wasm-harness/Cargo.toml
crewship-heavy wasm-bindgen --target web --out-name harness --out-dir /absolute/c1-bindings /absolute/c1-target/wasm32-unknown-unknown/release/wordcraft_c1_browser_wasm_harness.wasm
CARGO_TARGET_DIR=/absolute/c1-web-target crewship-heavy cargo check --locked --target wasm32-unknown-unknown --manifest-path /absolute/new-c1-renderer/source/Cargo.toml -p wordcraft-web
crewship-heavy python3 tools/wordcraft-c1-browser/tests/native-fixtures.py --helper /absolute/pinned-wordcraft-c1-engine --expected-sha256 EXPECTED_HELPER_ELF_SHA256 --output /absolute/new-c1-fixtures
crewship-heavy node tools/wordcraft-c1-browser/tests/wasm-seam.cjs /absolute/c1-bindings /absolute/new-c1-fixtures/native-fixtures.json /absolute/new-c1-fixtures/command-projections.json
```

The fixture driver uses the real helper process over its private inherited FD.
The Chromium harness loads actual WASM under a routed synthetic origin with no
server, login or network access. It exercises native codec, registry forwarding,
UTF8 selection, Enter IDs, no optimistic mutation, explicit pending refusal and
peer isolation, and forwarding of all 72 qualified registry identities with
their catalog fixture parameters. It does not execute those 72 mutations or drive a rendered native canvas or real keyboard,
IME, SQL, Saved, model runs or human acceptance. Native history is retained but
is not a qualified participant-undo or local draft implementation.
