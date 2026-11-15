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

The build stops when a crate's license expression cannot be satisfied by the
permissive allowlist in the script (`ALLOWED_LICENSES`) or when a font license
is not the SIL OFL 1.1. Each package carries `licenses/crates.json` and a
CycloneDX SBOM, `licenses/sbom.cdx.json`; both are covered by the package's pin.

## Release

The release workflow builds both packages after the tag is verified. Then
`scripts/check-craft-packages.sh` requires exactly one package per pin, every
pin a known application once and every manifest the pinned one, before
goreleaser builds the binaries with those pins. `checksums.txt`, which cosign
signs, lists the packages, and they are published next to the archives.

A release cannot ship without the packages. If the packages cannot be built
(an unreachable source commit, a font repository or toolchain outage), no
release ships, including an urgent fix. This is deliberate. A binary that
trusts no package would turn Word and Sheet off for every instance that
upgrades. There is no switch to skip the packages; the fix is to repair the
recipe or to release from a tag whose recipe still builds.

Every pull request that changes code runs the required `craft-packages-build`
and `craft-packages` jobs of CI. They install and run both packages through the
server, and then open each editor through the real host in a browser: open,
edit, save, reopen (`scripts/ci/craft-package-gate.sh`, "Browser gate" below).
A package whose editor does not connect cannot be pinned.

`catalog.txt` here holds the pins this recipe produces. The build fails when it
produces other pins, so a recipe change has to update them visibly
(`--update-catalog`), and a build that is not reproducible stops the release
and the CI jobs.

## Browser gate

A Word or Sheet package may be pinned in `tools/craft-components/catalog.txt` only
when its editor works with the real host (#5362: packages that installed and passed
every server check never connected in a browser). The required CI job
`craft-packages-build` builds both packages and checks them against the catalog;
`craft-packages` then runs `scripts/ci/craft-package-gate.sh` against the CI server
binary: a throwaway server (own data directory, `HOME` and port) trusts exactly the
built pins, the packages are
installed with `crewship admin instance applications install --file` and turned on,
and `playwright.craft-packages.config.ts` runs `e2e/craft-packages/` in full headed
Chromium under Xvfb with software WebGL. For Word and for Sheet it opens a new
document, takes over editing, types a marker, saves with ctrl+s, reopens it in a
fresh browser context, types a second marker and saves again; the server's head must
hold both markers. Any host refusal ("did not connect safely", "cannot be opened
safely") fails at once. The spec types only once the editor has taken the keyboard
(its canvas or its hidden text input has focus) and saves only once the host shows
the edit: the editor handles keys on its next frame, so on a slow runner keys sent
earlier were lost. Both jobs run with every code change: the host, server,
install and route code the gate drives is spread over the tree;
`scripts/ci/test_gates.py` proves that for every host file of the packaged editors
(`test_gate_runs_when_the_host_changes`). The packages are
cached by the hash of the recipe inputs (this directory, the build scripts,
`mooring/build-tools.json`, `scripts/check-craft-packages.sh` and the Rust pin)
and rebuilt exactly as the release builds them on a cache miss, or when `plan.py`'s
`craft` output reports a change to those inputs or to the CI controls, or a push
whose diff cannot be computed (a new branch, a forced push, a dispatch). A cached
package is still compared with `catalog.txt`, checked against its pin and installed
through the server on every run.

Locally, with a server binary that embeds the UI and a package directory:

```bash
crewship-heavy scripts/ci/craft-package-gate.sh ./crewship build/craft-components/dist "$OUT"
```

`$OUT` holds `server.log`, `seed.log`, `applications.txt`, `report.json` and
`playwright/` (screenshot, trace and browser console of a failed test).
Prefix the script with `taskset -c <one CPU>` to approximate a slow CI runner.
