# Slides schema 1 foundation

This package is the typed admission authority, not a renderer or export library.
The public example and canonical hash are in `testdata/slides-schema-v1.json`.
`deck_id` must equal the existing Page ID at persistence; it is not another
catalog identity. `slide_order` owns slide position; slides are encoded sorted
by their ASCII IDs. Element array order owns paint order. IDs are globally
unique among slides/elements within a deck and never derived from titles.

All fields shown in the fixture are required. Each element has exactly one
matching `text`, `shape` or `table` payload. Images are unsupported until the
separate private asset contract; no HTML/JS/iframe/external resource field exists.
Unknown keys, incorrect key case, duplicate JSON keys, unknown versions/types,
trailing JSON, null values where strings/integers/arrays are required, and lone
UTF-16 surrogate escapes are rejected before normalization.

Limits: 2 MiB input and canonical bytes, JSON nesting32, 1–100 slides, at most
1,000 total elements, IDs1–128 ASCII letters/digits/underscore/hyphen,
64–4096px deck axes, positive element size within the deck, integer coordinates.
Text32KiB, notes16KiB and table cells4KiB are UTF-8 budgets. Tables are rectangular
1–30rows by1–20columns. Text permits LF/tab but no CR, C0 controls or DEL.
Font names are1–128 UTF-8 bytes without LF/tab, font size1–160 integer pixels,
colors exact six-digit hex, shapes rectangle/ellipse with stroke0–16px.
Overflowing text remains content; rendering/export overflow is a later gate.

Hash domain is `crewship-slides-canonical-json/v1` followed by LF and canonical
UTF-8 JSON: ASCII object keys sorted, bounded integer numbers, no insignificant
whitespace or HTML escaping, U+2028/U+2029 escaped, and no Unicode normalization.
Literal backslash-u text is distinct from the corresponding code point.
SHA256 of the domain plus bytes is lowercase hex. Element hashes use the same
encoding and domain over the complete element, without deck position; reorder
preserves them. Offsets into text are absent from this foundation, so no
UTF-16/rune-index translation is implied.

`node scripts/check-slide-model.mjs` (Node24+Go, through `crewship-heavy`) compares
actual Go and TypeScript canonical deck/element hashes against the shared
fixture. TypeScript hashing consumes admitted typed data and is not a substitute
for Go validation or live Page authorization.
