`d1-native.json` is a generated qualification document in the native WordCraft
JSON format, not a user document or an excerpt of upstream application code.
It contains Czech text, a heading, independent bold/italic complex-script flags,
bullet and numbered list references, and complete document metadata.

Baseline application: WordCraft 0.10.0, upstream
`storytold/wordcraft@668d7ce3a4b1dcf241a9c50a20754a85647a02c4`.
Fixture SHA-256:
`10597fafe2fea2f36b4608ef3d7977c5eed0e2a669b97e2a96f4b92e033c7216`.
Its native load/save/reopen was separately qualified using the actual upstream
engine and WASM. That qualification does not prove this adapter's roundtrip.
The adapter output must undergo its own actual-engine/WASM qualification.

Additional Unicode, empty-paragraph and explicit-false cases in the Rust tests
are synthetic adapter cases. Their unit tests do not imply native application
qualification or a two-user collaboration guarantee.
