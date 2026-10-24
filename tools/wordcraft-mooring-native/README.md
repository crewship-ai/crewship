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
The default application remains unattached.

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
