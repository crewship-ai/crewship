# Minimal Rust/WASM client fixture

This is an experimental client primitive, not a network session or WordCraft
adapter. It demonstrates optimistic text insertion, UTF-16 index checks,
bounded unsaved drafts, recovery from a verified authoritative snapshot, and
schema-generated version/counter types. Only the host can issue saved receipts.
Duplicate IDs in pending drafts fail instead of silently creating a second edit.
The primitive does not yet retain confirmed outcome IDs across reconnect.

`index.ts` is the thin TypeScript layer; protocol interfaces are generated from
the committed schema. The Rust `TextClient` exposes snapshot/recovery and keeps
unsaved drafts on failed recovery. The caller authenticates the host and checks
snapshot scope and revision before invoking recovery. No transport is wired.

`fixtures/native-wasm.json` runs through the identical Rust fixture function in
native tests and the WASM ABI. It covers decimal counters above 2^53/u64 overflow,
reader SyncStep2/Update filtering and conservative A→B draft preservation. This
does not prove causal dependency extraction or full application collaboration.
The web-target test uses Node's WebAssembly engine, not a browser application.

From the repository root, using Rust with the wasm32-unknown-unknown target,
wasm-bindgen-cli 0.2.129 and Binaryen wasm-opt 131:

```sh
export MOORING_CLIENT_ARTIFACTS=/tmp/mooring-client-build
mkdir -p "$MOORING_CLIENT_ARTIFACTS/web" "$MOORING_CLIENT_ARTIFACTS/node"
crewship-heavy cargo test --manifest-path mooring/Cargo.toml --config mooring/.cargo/config.toml --locked --offline
crewship-heavy cargo build --manifest-path mooring/Cargo.toml --config mooring/.cargo/config.toml --locked --offline -p mooring-client --target wasm32-unknown-unknown --release
crewship-heavy wasm-bindgen mooring/target/wasm32-unknown-unknown/release/mooring_client.wasm --target nodejs --out-dir "$MOORING_CLIENT_ARTIFACTS/node"
crewship-heavy node mooring/client/test-wasm.cjs "$MOORING_CLIENT_ARTIFACTS/node/mooring_client.js"
crewship-heavy wasm-bindgen mooring/target/wasm32-unknown-unknown/release/mooring_client.wasm --target web --out-dir "$MOORING_CLIENT_ARTIFACTS/web"
cp "$MOORING_CLIENT_ARTIFACTS/web/mooring_client_bg.wasm" "$MOORING_CLIENT_ARTIFACTS/before-opt.wasm"
crewship-heavy wasm-opt -Oz --all-features "$MOORING_CLIENT_ARTIFACTS/web/mooring_client_bg.wasm" -o "$MOORING_CLIENT_ARTIFACTS/web/mooring_client_bg.wasm"
crewship-heavy node mooring/client/test-web-wasm.mjs "$MOORING_CLIENT_ARTIFACTS/web"
crewship-heavy node mooring/client/measure.mjs "$MOORING_CLIENT_ARTIFACTS/web" "$MOORING_CLIENT_ARTIFACTS/before-opt.wasm" "$MOORING_CLIENT_ARTIFACTS/size.json"
```

Release flags are committed in the workspace Cargo.toml: opt-level z, LTO,
one codegen unit, abort panic and stripping. The size script uses per-asset gzip
level 9 and Brotli quality 11. Runtime totals include optimized WASM, generated
web glue and the thin TS layer after Node's built-in type stripping; compile-time
interfaces are excluded. It also reports pre-optimization WASM because smaller
raw WASM does not necessarily compress better. No asset is sent to a vendor.
Node 24 or another version providing `stripTypeScriptTypes` is required for the
size measurement only. Full SDK size will change as transport and adapters grow.

Build-tool licenses: wasm-bindgen-cli 0.2.129 is MIT OR Apache-2.0 (same
project as the vendored wasm-bindgen crates). Binaryen wasm-opt 131 is Apache-2.0;
its [release license](https://github.com/WebAssembly/binaryen/blob/1f903c14babf829745b421b92ff0f286e93e4209/LICENSE)
is pinned to the version_131 source commit. Build-tool executables are not
shipped as client assets.

Required main CI runs `scripts/ci/mooring.sh`: all native workspace tests and
the committed shared cases through the actual compiled WASM ABI. Compilation
alone is not execution evidence. The CI Result gate rejects a missing, failed,
cancelled or skipped Mooring job whenever code checks are planned.
