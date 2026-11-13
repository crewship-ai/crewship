# Craft application components

Recipe for the Word (WordCraft) and Sheet (GridCraft) packages published with a
Crewship release (#5315). `scripts/build-craft-components.py` reads
`components.json`, fetches each pinned public source, applies its patch and
writes one package per application, `crewship-craft-<application>.craftpkg`
(a tar), to `build/craft-components/dist/`:

- the document engine, compiled to `wasm32-wasip1` and run inside Crewship by
  wazero (no helper process, same bytes on every platform);
- the browser editor, compiled to `wasm32-unknown-unknown`, bound with the
  pinned `wasm-bindgen`, optimized with the pinned `wasm-opt`, and laid out as
  an editor-origin bundle pinned by the SHA-256 of its `manifest.json`;
- license notices of the application and an inventory of its Rust crates.

Every file is zstd-compressed and listed in the package's `manifest.json` with
the SHA-256 and size of its decompressed bytes. The script also writes
`catalog.txt`: per application the version and the SHA-256 of its
`manifest.json`. The release workflow builds the binaries with that catalog
(`-X …/craftassets.releaseCatalog`) and publishes the packages next to the
archives; Crewship downloads a package when an administrator turns the
application on and accepts it only if its manifest matches the pin. Ordinary
`go build` does not need any of this: such a binary trusts no package and
reports `component_missing`. The tar itself is deterministic: the same files
always give the same package and pin.

## Patches

`patches/*.patch` are minimal source changes for WASI, offered upstream:

| File | Change |
|---|---|
| `engine/src/io.rs` | file I/O is browser-only on `wasm32-unknown-unknown`, not on every `wasm32` |
| `engine/src/safe_save.rs` (Word) | no `process::id()` on WASI |
| `fonts/build.rs` (Word) | the web size budget applies to the browser build only; WASI embeds every craft-fonts face so server PDFs match the editor |
| `core/src/date.rs`, `core/Cargo.toml` (Sheet) | the clock comes from `std` on WASI, not `js-sys` |

## Build

```
python3 scripts/build-craft-components.py --rust-bin /path/to/toolchain/bin
```

The Rust toolchain must be `rust_version` with both `targets` installed
(`rustup target add wasm32-wasip1 wasm32-unknown-unknown`). `wasm-bindgen` and
`wasm-opt` are downloaded at the versions and digests in
`mooring/build-tools.json`. Changing a pin changes the engine and editor digests,
and documents pinned to the previous ones then need the compatibility path in
the PRD (§6) before a release may ship it.
