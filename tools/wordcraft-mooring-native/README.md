# Dormant WordCraft native command seam

This source bundle patches public [WordCraft](https://github.com/storytold/wordcraft)
revision `668d7ce3a4b1dcf241a9c50a20754a85647a02c4`. Its package version is
0.10.0, but that revision is 64 commits after the v0.10.0 tag. Preserve the
included MIT notice when redistributing this patch or the patched source.

The optional native seam gates text/inline commands before native bookkeeping,
checks the complete native before/after model, and retains a visible unsaved
draft if proposal capture fails. An attached seam rejects native file/window
commands and asynchronous recovery. Its read/navigation gate admits exactly 51
audited command IDs. The native file Save operation does not produce host Saved.
The default application remains unattached. The optional web entry selected by
`?mooring=1` waits for `start_collaborative(native_json, stable_ids_json, callbacks)`
instead of creating a blank document. Its native JSON inputs are exact strings;
callbacks delegate selection, full-model validation, original capture and restricted
history to an app-owned SDK. Local capture/projection failure stops edits and
reports a retained unsaved draft. This creates no authenticated host connection.

This is not a human collaboration controller. It adds no transport, credential,
host origin grant, durable receipt or browser route. Collaborative overlapping
delete/undo remains unqualified. Integrators must preserve the original SDK
proposal bytes and stable identities, including pending drafts; a whole-document
reimport or command reexecution cannot replace an authored proposal.

## Prepare source

Obtain the public Git repository and fetch the exact revision above. Run:

```sh
./tools/wordcraft-mooring-native/prepare.sh /path/to/wordcraft /new/build/source
```

The destination must not exist. The recipe archives only the pinned Git object,
disables local Git replacements, verifies the entire archived source before
extracting it, checks original and patched file hashes, and applies the exact patch. It does
not modify the input tracked files, index or branch, and fetches no dependencies.
If the chosen output is inside that checkout, it adds that explicit new
untracked directory. Output is retained on an
error for inspection. `source-manifest.json` records the complete changed-file
set; `patch.sha256` binds the patch bytes. Cargo.lock is unchanged. The
whole-source tar digest was produced by Git 2.43.0; incompatible archive output
fails before extraction rather than accepting different source bytes.

The opt-in source-only regression command is
`bash tools/wordcraft-mooring-native/prepare_test.sh /path/to/wordcraft`.
It checks nested repository isolation, caller preservation, existing-output
refusal patch tampering and whole-source archive tampering. It creates and removes only its own temporary tree;
it downloads nothing and runs no application or compiler.

## Test prerequisites

Run `cargo xtask ci` in the prepared source with its pinned Rust toolchain and
normal Cargo dependencies. The full command includes formatting, all-target
Clippy, workspace tests, asset attribution, dependency layering and WASM checks.
Give baseline and patched source **different Cargo target directories**.

For the explicit font test profile, use the six original manifest rows from
[craft-fonts revision 8dcdacd5153e64560d109541a47d806f26f048c0](https://github.com/storytold/craft-fonts/tree/8dcdacd5153e64560d109541a47d806f26f048c0),
the revision pinned in WordCraft's own CI at the source pin. Add the unchanged
Noto Sans Myanmar font and OFL from
[Google Fonts revision 8b0a1d0f5983c89bc2b93f1b5fb55f9e252744b5](https://github.com/google/fonts/tree/8b0a1d0f5983c89bc2b93f1b5fb55f9e252744b5/ofl/notosansmyanmar)
at `fonts/noto-sans-myanmar/NotoSansMyanmar.ttf` and its adjacent `OFL.txt`.
Use `test-fonts-manifest.txt` as that input's `fonts/manifest.txt`, then check
all font, manifest and license bytes with:

```sh
(cd /path/to/test-fonts && sha256sum --check /path/to/bundle/test-fonts.sha256)
export CRAFT_FONTS_DIR=/path/to/test-fonts
```

These are explicit test inputs, not newly installed system fonts or an implicit
production font bundle. Keep all original OFL notices. A newer optional font
bundle changes fallback behavior and is not interchangeable with this profile.

Two existing UI tests render pixels with wgpu. A headless test machine needs a
real software or hardware Vulkan adapter. Where an existing SwiftShader ICD is
available, Vulkan's supported `VK_ICD_FILENAMES` may select it without
installing a driver. Leave `WGPU_BACKEND` unset for the full upstream suite: its
graphics-default tests intentionally check the unmodified backend set. Use the same adapter on baseline and
patched tests and record the actual ICD/library hashes. Do not select a no-op
renderer or skip pixel assertions. Run the Myanmar, Thai and both object-drag
controls on baseline and patched source before the full patched CI.

On a managed Crewship host, invoke builds/tests through `crewship-heavy`.
Public builds require neither that private host tool nor private context.
Final native/WASM releases need their own source/patch/toolchain/input hashes
and artifact qualification; previous WordCraft engine artifacts identify
different source bytes.

## Reproducible web inputs

`source-manifest.json` now binds native patch commit
`37268b7a66199254fc64ebd557411350532ad6d5`. The patch applies to the same public
source revision above; no private Git object is needed by `prepare.sh`.
The corresponding Crewship SDK/child source is
`c66e5a61878f0942c976d26746d2e3e05fb9c063`. Its child entry is
`lib/mooring/wordcraft-child-loader.ts`; dependencies are pinned by that commit's
`pnpm-lock.yaml` and `mooring/Cargo.lock`.

Production font input is the **original six rows only** from craft-fonts
`8dcdacd5153e64560d109541a47d806f26f048c0`: use
`release-fonts-manifest.txt` as `fonts/manifest.txt` and verify
`release-fonts.sha256`. Preserve each original OFL notice. The Myanmar addition
in the full test profile above is test-only and must not enter release input.

The qualified toolchain is Rust 1.99.1, wasm-bindgen CLI 0.2.129 (matching both
locked modules), and wasm-opt version_131 with `-Oz --all-features`.
In prepared WordCraft source, build the actual web binary using the release
font directory:

```sh
CRAFT_FONTS_DIR=/path/to/release-fonts cargo build --release --locked \
  --target wasm32-unknown-unknown -p wordcraft-web
wasm-bindgen target/wasm32-unknown-unknown/release/wordcraft-web.wasm \
  --target web --out-name wordcraft_web --out-dir /new/native-output
wasm-opt -Oz --all-features /new/native-output/wordcraft_web_bg.wasm \
  -o /new/native-output/wordcraft_web_bg.optimized.wasm
```

Build `mooring-wordcraft-client` from the pinned Crewship source with its
workspace Cargo configuration and lock, then use the same wasm-bindgen web
and wasm-opt settings. Bundle the child entry with the pinned esbuild dependency
(`--bundle --format=esm --platform=browser --target=es2022`). The existing
editor-origin loader requires a flat directory containing `index.html`,
`child.js`, `wordcraft_web.js`, `wordcraft_web_bg.wasm`,
`mooring_wordcraft_client.js` and `mooring_wordcraft_client_bg.wasm`, plus their
license/provenance files. The final manifest must hash the **exact final bytes**
and stay within the existing 64MiB/512-file/1MiB-manifest limits; SDK identity and
whole editor identity are distinct. Runtime packaging/server selection is owned
by the existing Crewship editor-origin integration, not this source-only recipe.

A build or local ready event is not rendering, HTTP/WebSocket, permission,
multiplayer, undo-overlap or durable Saved qualification. Those require actual
end-to-end acceptance with the final packaged bytes. No native file save or
child status callback grants Saved.
