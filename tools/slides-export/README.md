# Slides S2 converter qualification

A standalone, data-only experiment over the canonical Slides model. The Go entry
strictly validates the saved snapshot hash and supplied pinned raster bytes before
invoking a fixed Node converter. This is not a server API, an authorization layer,
an installed binary, or completed production export. It introduces no author
JavaScript, remote URLs or rendered external network requests.

From the repository root, using the managed resource gateway:

```sh
crewship-heavy pnpm --dir tools/slides-export/runtime install --frozen-lockfile --ignore-scripts
crewship-heavy go test ./tools/slides-export -count=1
crewship-heavy go vet ./tools/slides-export
crewship-heavy pnpm --dir tools/slides-export/runtime test
crewship-heavy go run ./tools/slides-export --corpus /tmp/my-new-slides-corpus
crewship-heavy go run ./tools/slides-export --input /tmp/my-new-slides-corpus/supported.json --output /tmp/my-new-slides-export
```

Output directories must be fresh and owned by the caller. `report.json` binds the
Page identity, explicit revision/content hash, mapping source hashes, generator
versions, layout profile and actual font/license bytes. Its hash grants no
permission. `deck.pptx` contains native editable text/shapes/tables and source
raster images; `deck.pdf` is generated directly by PDFKit with embedded subset
font/vector text/shapes/tables. No whole-slide raster fallback is used. PDF and
Office line wrapping are separate layout engines, with no pixel parity claim.
PPTX requests the pinned font family but does not embed font bytes: recipients
need that profile or separate substitution qualification. Absolute PPTX line
spacing matches the explicit PDF baseline interval; actual near-boundary Office
qualification remains required rather than inferred from PDF measurement.

The corpus includes Czech and actual LF, a long paragraph, native table LF cells,
a verified PNG, rectangle/ellipse, a zero-stroke shape, and an explicitly reported
font substitution. Negative fixtures refuse vertical overflow, a near-boundary long paragraph, a single glyph
wider than its box, and a missing glyph. Refusal leaves a report and publishes no
PPTX/PDF. The profile resolves requested fonts to packaged DejaVu Sans and reports
every substitution; it does not infer which fonts an Office recipient installed.

## Bounded evaluation profile

| Resource | Limit |
| --- | --- |
| Strict canonical deck | Existing model: 2 MiB, 100 slides, 1,000 elements, 10,000 table cells; 2,000 cells per slide |
| JSON envelope | 64 MiB, depth 32, 100,000 values; exact fields, duplicate/trailing/null rejection |
| Verified raster assets | 32 unique, 32 MiB encoded bytes, 33,554,432 decoded pixels total; existing per-image canonical limits also apply |
| Each PPTX/PDF | 64 MiB |
| Serialized report | 1 MiB including pretty whitespace/newline |
| Converter process | 120 seconds, Node heap 512 MiB |
| Evaluation concurrency | One invocation through `crewship-heavy`; no API worker pool introduced |

These are ceilings, not measured throughput or capacity promises. The PDF
preflight measures with the same packaged font and PDFKit options used to render;
server refusal does not prove PPTX application overflow behavior. PptxGenJS is a
writer; geometry, identity, text/table mapping and native application qualification
remain our responsibility.

## Optional application qualification

`office-uno.py` requires an independently verified disposable LibreOffice binary
and matching UNO Python. It creates its own profile/pipe and refuses a reused
output directory, edits native text/geometry/table cells, saves and reopens PPTX,
and exports application PDF/PNG. `--envelope` names the synthetic source envelope.
Use an owned fontconfig pointing to `runtime/fonts/` and verify `fc-match` resolves the
exact packaged font. Never install host packages or attach to a shared Office
session. Run the process through `crewship-heavy` with a 120-second timeout.

`inspect-pdf.py INPUT.pdf NEW_OUTPUT_DIR` rasterizes with an externally supplied
PyMuPDF; `qualify-pdf.py --envelope INPUT.json --pdfkit PDF --libreoffice PDF
--output REPORT.json` asserts actual content, dimensions, image RGB pixels,
fill-only zero stroke and PDFKit text bounds. Python dependencies are optional
qualification tooling, not implicit host installs. Captured reports/screenshots
belong in private evidence, not this public tool.

Native object editing through UNO is application evidence, not manual GUI editing.
LibreOffice is not PowerPoint: actual PowerPoint open/edit/layout acceptance remains
incomplete until performed in an authorized environment. Authorization/private
asset retrieval, production packaging, API integration and end-to-end export UI
remain separate gates.

## Font provenance

`runtime/fonts/provenance.json` records the official DejaVu 2.37 release archive SHA-256,
exact unmodified font bytes and license digest. `runtime/fonts/LICENSE-DejaVu.txt` retains
the upstream redistributable license notices. The converter verifies the actual
font and license bytes before use; it never downloads fonts during conversion.

Primary interfaces: [PptxGenJS text](https://gitbrent.github.io/PptxGenJS/docs/api-text/),
[PDFKit text](https://pdfkit.org/docs/text.html), [PDFKit images](https://pdfkit.org/docs/images.html),
[DejaVu official release](https://github.com/dejavu-fonts/dejavu-fonts/releases/tag/version_2_37).

## One-shot runtime foundation (image qualification pending)

The Node module and exact packaged assets live in `runtime/`. Its generated
`profile.json` binds every runtime COPY input, the shared Go admission source,
build-only legal collector and pinned base image. Regenerate after source edits
with `crewship-heavy node tools/slides-export/runtime/profile.mjs --generate`.
Go embeds and verifies the same context; an obsolete profile fails closed.
Build-only inputs are verified against the manifest inside the image build.

`internal/slidesexport` provides the shared strict envelope decoder and one-job
lease spanning admission through delivery. It introduces no HTTP routes or
permissions. Runtime frames use magic `CSLEXP1\n`, uint32BE header length,
uint64BE binary length, strict bounded JSON report/metadata, then optional raw
binary. Header limits precede allocation/conversion; hash/identity/profile/MIME
and exact complete-process EOF are mandatory. Reports use protocol2 and a
versioned canonical hash with sorted ASCII nonnumeric keys, normalized JSON
numbers and explicit U+2028/U+2029 escaping; this is independent of deck schema1/2.

Run `crewship-heavy node tools/slides-export/runtime/contract.mjs` for independent
Go/Node fractional/exponent/-0/Unicode/hash conformance and actual fixed-worker
report/PPTX/PDF framing. These are process/transport tests, not real container
lifecycle evidence. That gate must execute the full pinned image on an authorized
CI runner; local gateway rejection cannot be bypassed or counted as acceptance.

## Owned cleanup and recovery

A runtime `Runner` requires an immutable image ID, installation owner identity and
private cleanup journal directory. It holds an OS file lease on `journal.lock`
through its lifetime; another process cannot reconcile or admit jobs concurrently.
A content-free, fsynced intent precedes container creation. Names and ownership
tokens are independently random. Cleanup inspects matching owner/token labels
before removing the immutable container ID; it never removes a name alone.

Parent cancellation permits a five-second independent deadline for a submitted
create command to acknowledge its ID, plus at most three seconds of pipe teardown.
Confirmed work has a separate ten-second reconciliation deadline, with at most
one additional second of CLI pipe teardown. An unacknowledged create can outlive any finite observation
interval: reconciliation removes matching work observed during its budget but
retains uncertainty and refuses new exports. Cleanup failure also blocks delivery.
The journal contains no deck, asset, principal credential or resumable capability.
Malformed or replaced journal data fails closed and is preserved.

Trusted local maintenance can reconcile acknowledged work with:

```sh
crewship-heavy go run ./tools/slides-export/maintenance --journal OWN_DIRECTORY --owner ORIGINAL_INSTALLATION_OWNER
```

Absent uncertain work is insufficient proof of daemon quiescence. After independently
establishing that no delayed create can arrive, an operator can acknowledge exactly
one intent using `--ack-name NAME --reference DISPOSITION_ID --daemon-quiescent`.
The tool writes a durable content-free disposition before removing the intent.
This is a local operational assertion, not a user/agent export permission; no HTTP,
MCP or run CLI exposes it. Preserve unresolved journals for recovery.

Process-fixture tests cover cancellation during create and start, exact ownership,
foreign-container refusal and retained uncertainty. These do not qualify Docker.
The actual image lifecycle, resource profile and security scan remain independent
CI gates; a successful image build alone does not complete them.
