# Human static HTML artifacts

This slice adds human-owned static HTML to the existing Pages namespace. It
uses the storage/backup foundation in #3264. It does not enable agent jobs,
comments, copying, Boards, interactive JavaScript, Docs, Slides or export.
The human owner-transfer API and UI remain a separate release 1.0 gate.

## Human flow and authority

Choose **New HTML** in Pages, enter a name and slug, then upload HTML or edit
Source. Static preview, immutable history and restoring a retained revision
are available in the same pane. An existing empty human-owned Page can also
initialize HTML. A Page with panels, project source or a Board cannot initialize
an artifact. Existing application source and panel viewers retain their paths.

Every read requires a live human session or CLI token with `pages:read`, active
trusted workspace membership and current Page/folder access. Writes additionally
require `pages:write`, a human-owned Page and current edit permission. Session
and token identity, token scopes, membership, grants and Page identity are
rechecked after upload IO and inside the writer transaction. Private reads
verify retained blob bytes and recheck the exact credential and current access
before returning source/history. Responses use `Cache-Control: no-store`.

A user owning an artifact, Board or empty human staging Page cannot depart or
be erased through crew fallback. Those operations refuse the whole transfer
batch with HTTP 409 and `code: page_ownership_transfer_required`, explaining
that an explicit human successor is needed without exposing Page names or IDs.
This cut intentionally provides no owner-transfer operation.

## Initial public wire

The current immutable `page_id` is required, including for reads. There is no
compatibility fallback when it is absent. Recreating the same slug creates a
different Page; old editors and commands cannot write to or read that identity.

| Method | Route | Contract |
| --- | --- | --- |
| GET | `/api/v1/pages/{slug}/artifact` | Required `page_id`, optional positive retained `revision` |
| POST | `/api/v1/pages/{slug}/artifact/revisions` | `page_id`, `base_rev`, `mutation_id`, nonempty UTF-8 `html` |
| POST | `/api/v1/pages/{slug}/artifact/restore` | `page_id`, `base_rev`, `mutation_id`, retained positive `revision` |

All routes are workspace scoped. Mutation objects reject unknown, duplicate,
case-aliased and null fields, and trailing JSON. Optional `format_version: 1`
and `runtime_profile: static-html-v1` select the sole supported format; other
values are refused. A successful mutation returns `page_id`, `revision` and
`head_rev`; new writes return 201, exact retries 200. Receipts bind the original
actor and payload. A reused receipt for a different payload returns 409.

Read responses contain `page_id`, `html`, selected `revision`, `head_rev`,
`manifest`, `manifest_status`, `revisions` and advisory `can_write`. An
unpublished Page has revision/head zero and no manifest. Published manifests
bind the single `index.html`, MIME, byte count and SHA256; legacy source gets
an explicitly derived manifest. Historical authorship is provenance, never
current authority. Restore creates a new revision with `restored_from`.

## Limits and presentation

HTML is at most 10 MiB. The JSON wire limit allows bounded escaping expansion;
two upload slots cover decoding through publication. Limits are 30 revisions
per Page per rolling hour and 200 per workspace per rolling hour. At most 200
source revisions are retained per Page. Retained Board/source references pin
exact bytes; a fully pinned retention window refuses a write instead of dropping
pinned history. Publication receipts for removed source have a 24-hour replay window. Restore
retries still require their original source revision to be retained; a receipt
does not itself pin that source.
Encrypted workspace/instance backup includes source blobs; fork remaps Page
namespaces and restores requests inertly, as specified by the foundation.

Original HTML is shown only as text. Preview uses a per-call sanitizer, native
PNG/JPEG validation and an opaque iframe with an empty sandbox. Author scripts,
iframes, navigation, forms, media, external requests and fonts are disabled.
Raster preflight limits: 1 MiB/image, 4 MiB aggregate, dimensions at most 2048,
8 million aggregate pixels, 32 images. Malformed or unsupported images lose
`src`; source bytes remain available as text. CSS that could resolve resources
is removed. Static rendering does not imply interactive runtime acceptance.

The editor keeps the original Page ID and draft base revision. Refresh does
not rebase a draft. After a lost acknowledgement, **Retry exact save** sends
the same change and receipt. A definite conflict requires reviewing the latest
source and discarding the old attempt. Dirty drafts participate in the existing
navigation guard. Readers can preview and inspect history without save/restore
controls. Touch controls use the existing 44px pointer-based sizing convention.

## Human CLI

`crewship page artifact` uses the ordinary authenticated human profile. Create
returns the Page ID; supply that original ID explicitly on every following
command. A conflict never refreshes the ID or base automatically.

```sh
crewship page artifact create report --name 'Report'
crewship page artifact show report --page-id PAGE_ID --format json
crewship page artifact publish report --page-id PAGE_ID --base-rev 0 --mutation-id original-attempt --file report.html
crewship page artifact show report --page-id PAGE_ID --revision 1
crewship page artifact restore report --page-id PAGE_ID --base-rev 1 --revision 1 --mutation-id restore-attempt
```

`--file -` reads stdin. Preserve the original file, Page ID, base and mutation ID
for an explicit retry. Global `--format` controls machine output; HTML remains
escaped text. This command does not provide an agent authority fallback.
