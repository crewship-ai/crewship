# kind: Page

## What it is

`kind: Page` declares a **page**: a named grid of typed, permissioned
panels that producers push data into. A page holds no query, no
datasource and no credentials — it renders the last payload something
pushed to it. That is the whole design: everything a page shows is
reachable because the producer already runs next to the data, inside a
crew container.

Each panel declares four things that carry the entire contract:

- **`schema`** — what shape the payload has, from a closed set
  (`metric.v1`, `status.v1`, `table.v1`, …). A new panel kind is a
  server release, never a user-supplied string.
- **`owner`** — `crew/<slug>`, the permission anchor. A viewer who is
  not in that crew gets a sealed placeholder in the panel's grid slot,
  filtered server-side, so the page has the same shape for everyone.
- **`producer`** — `<kind>/<ref>`, who is permitted to WRITE the panel.
  Producer authority is separate from viewer authority: a crew member
  who can see a panel cannot write it.
- **`sla`** — a duration. When nothing has arrived within it, the panel
  renders as stale. There is no default that means "never mind"; a
  panel without an SLA does not validate.

The manifest document is **the same document** `crewship page create
--file` takes. One shape, two doors: author a page in a manifest and
apply it, or post the identical YAML with the CLI. The Go type behind
`spec.panels` is the authoring type itself
(`internal/pages.PanelSpec`), so the two cannot drift.

## YAML schema

```yaml
apiVersion: crewship/v1
kind: Page
metadata:
  name: Flotila .201            # required — the page title
  slug: fleet-201               # required — the page's identity AND its URL
  description: Fleet status     # optional
spec:
  panels:                       # required — at least one, at most 24
    - id: services              # required — the address a producer pushes to
      schema: status.v1         # required — closed set, see below
      title: Jede to?           # optional — the panel heading
      owner: crew/lookout       # required — permission anchor, not a label
      producer: script/watch-services.sh   # required — who may write it
      sla: 30s                  # required — Go duration; 0 is not allowed
      span: 8                   # optional — 12-column grid, default 12
      public: false             # optional — per-panel opt-in for published pages
```

### `metadata`

| Field | Required | Notes |
|---|---|---|
| `name` | yes | Human title. |
| `slug` | yes | `^[a-z0-9][a-z0-9_-]{0,63}$`. Unique per workspace, and it is the page's URL — see [Renaming](#renaming) below. |
| `description` | no | Free text. Omitting it leaves a server-side value alone. |

### `spec.panels[]`

| Field | Required | Notes |
|---|---|---|
| `id` | yes | Slug-shaped, unique within the page. This is the push address (`crewship page set <page>/<panel>`), so it is stable across edits. |
| `schema` | yes | One of the closed set. `metric.v1`, `status.v1` and `table.v1` ship first; the rest are reserved and are refused with "reserved but not yet implemented" until their renderer lands. |
| `title` | no | Panel heading. |
| `owner` | yes | `crew/<slug>`. Must be a crew — never a user. |
| `producer` | yes | `<kind>/<ref>` with kind in `{routine, script, agent, webhook}`. There is no `sql` or `datasource` kind and there will not be one. |
| `sla` | yes | Go duration string: `30s`, `5m`, `1h`. Must be greater than zero. Sent to the server as `sla_seconds`. |
| `span` | no | 1–12. Defaults to 12 (full width); 0 would render a panel with no width. |
| `public` | no | Opts this one panel into a published page. Default deny, per panel, never per page — publishing must never be a bulk action over panels nobody looked at. |

Panel ORDER is the layout. The grid is declared, never dragged, so two
pages with the same panels in a different order are two different
pages, and reordering the list is a real change the plan will report.

## Examples

### A crew status board

```yaml
apiVersion: crewship/v1
kind: Page
metadata:
  name: Fleet 201
  slug: fleet-201
  description: Is the fleet up?
spec:
  panels:
    - id: services
      schema: status.v1
      title: Services
      owner: crew/lookout
      producer: script/watch-services.sh
      sla: 30s
      span: 8

    - id: error-rate
      schema: metric.v1
      title: Errors / min
      owner: crew/lookout
      producer: routine/error-rollup
      sla: 5m
      span: 4
```

`script/watch-services.sh` is a script inside the `lookout` crew's
container; it pushes with `crewship page set fleet-201/services --data -`.
`routine/error-rollup` is a `kind: Routine` declared in the same
manifest — the apply orders the routine first (see
[Apply behavior](#apply-behavior)).

### A cross-crew page

```yaml
apiVersion: crewship/v1
kind: Page
metadata:
  name: Release readiness
  slug: release-readiness
spec:
  panels:
    - id: build
      schema: status.v1
      owner: crew/engineering
      producer: routine/ci-status
      sla: 15m
      span: 6

    - id: incidents
      schema: table.v1
      title: Open incidents
      owner: crew/devops           # a different crew owns this slot
      producer: agent/herald
      sla: 1h
      span: 6
```

Anyone in `engineering` but not in `devops` sees the `build` panel and
a sealed placeholder where `incidents` is — same grid, same two slots,
no payload and no producer name for the panel they may not see.

## CLI reference

The per-entity command is `crewship page`, defined in
`cmd/crewship/cmd_page.go`. The manifest is the other door onto the
same endpoints.

| Command | Description |
|---|---|
| `crewship page list` | List pages with their freshness rollup. |
| `crewship page get <slug>` | One page with its panels and their last payload. |
| `crewship page create --file <yaml>` | Create from an authored document — the same document a manifest carries. |
| `crewship page update <slug> --file <yaml>` | Replace the spec; every save is a version. |
| `crewship page delete <slug> --yes` | Delete the page and its data. |
| `crewship page set <slug>/<panel> --data -` | The single write path for a payload. Provenance is attached server-side. |
| `crewship page grant` / `revoke` | Per-page access grants. |
| `crewship apply -f page.yaml` | Create or update from a manifest. |
| `crewship apply -f page.yaml --dry-run` | Plan only — reports create / update / unchanged with no mutation. |

## REST endpoint mapping

Routes are workspace-unscoped; the workspace comes from the request
context, following `saved-views` and `missions`.

| Manifest field | Wire field | Notes |
|---|---|---|
| `metadata.slug` | `slug` | Sent on create only. |
| `metadata.name` | `name` | |
| `metadata.description` | `description` | Omitted when empty. |
| `spec.panels[].id` | `panels[].id` | |
| `spec.panels[].schema` | `panels[].schema` | |
| `spec.panels[].title` | `panels[].title` | Omitted when empty. |
| `spec.panels[].owner` | `panels[].owner` | |
| `spec.panels[].producer` | `panels[].producer` | |
| `spec.panels[].sla` | `panels[].sla_seconds` | `30s` → `30`. One representation in the database, one on the wire, one for humans. |
| `spec.panels[].span` | `panels[].span` | Default applied client-side. |
| `spec.panels[].public` | `panels[].public` | Sent when true. See the caveat under [Drift detection](#drift-detection). |

Endpoints:

- `GET /api/v1/pages` — index (counts and freshness rollup, not panels)
- `GET /api/v1/pages/{slug}` — one page with panels + payloads
- `POST /api/v1/pages` — create
- `PATCH /api/v1/pages/{slug}` — update
- `DELETE /api/v1/pages/{slug}` — delete
- `PUT /api/v1/pages/{slug}/panels/{id}/data` — the producer write path

## Validation rules

Structural rules are enforced by the same validator the API and the CLI
use, so a document that validates here validates everywhere:

- `metadata.name` is required; `metadata.slug` must be slug-shaped.
- At least one panel, at most 24. A page with no panels renders nothing
  and can be pushed to by nobody.
- Panel `id` must be slug-shaped and unique within the page — a
  duplicate id means one of the two could never be pushed to.
- `schema` must be a producible member of the closed set.
- `owner` must parse as `crew/<slug>`.
- `producer` must parse as `<kind>/<ref>` with a known kind.
- `sla` must parse as a duration and be greater than zero.
- `span` must be within 1–12.

The manifest layer adds the checks only it can make, resolving each
reference against the rest of the bundle plus what the server already
has:

- every panel's `owner` crew must be declared in this manifest or
  already exist,
- `producer: routine/<slug>` must resolve to a declared or remote
  routine,
- `producer: agent/<slug>` must resolve to a declared or remote agent.

`script/…` and `webhook/…` producers are **not** checked. A script is a
path inside a crew container and a webhook token is minted after the
fact, so neither names anything the manifest models; silence on those is
correct rather than an omission.

Every failing reference in a document is reported at once, so a manifest
with three typos costs one `apply --dry-run`, not three.

## Apply behavior

| Mode | Behavior |
|---|---|
| `ApplyUpsert` (default) | Lookup by **slug**. Create if missing, PATCH if drifted, no-op if identical. |
| `ApplyStrict` | Not specialised for pages — the create path is used, and the server's `409` on a duplicate slug is what surfaces. |
| `ApplyReplace` | Not specialised for pages: a page's payload ring and version history live under the page row, and recreating it would discard both. Edit in place. |

Pages are planned **after** crews, agents and routines, and torn down
before them, so a panel never points at a producer that does not exist
yet or has already gone.

`crewship apply --dry-run` reports the planned action per page and names
what drifted (`update page "fleet-201" (name, panels)`).

### Renaming

`metadata.slug` is the page's address. The server refuses a PATCH that
changes it — "a page's slug is its address" — because a rename would
silently break every producer script pushing to it. Changing the slug in
a manifest therefore creates a SECOND page; delete the old one
deliberately if that is what you meant.

`metadata.name` renames freely.

### Drift detection

The plan compares name, description and the full panel list (in order),
and PATCHes the whole panel set when anything differs — the update
endpoint replaces panels wholesale, so a partial list would delete the
panels it left out.

Two deliberate blind spots, both stated here rather than left to be
discovered:

1. **`public` is never compared.** The read path does not serialise it,
   so the remote value always looks false. The declared value IS sent on
   create and update and does take effect; it simply cannot be verified
   from the manifest side, so a page whose `public` flag was flipped in
   the UI will not be reported as drifted.
2. **A sealed panel is compared on `id` and `span` only.** That is all a
   placeholder carries. If you apply a manifest containing a panel owned
   by a crew you are not in, drift in its schema, producer, title or SLA
   is invisible — and treating "cannot see" as "must differ" would PATCH
   the page on every single apply, minting a version nobody asked for.
   Apply as a member of the owning crews if you need the full diff.

## Round-trip via export

`ExportPages` reads the index, fetches each page, and emits one
`kind: Page` document per row with `sla_seconds` rendered back into the
duration string a human wrote (`3600` → `1h`).

Two notes:

- Export **refuses** a page containing a panel sealed to the exporting
  account, naming the panel. Emitting the document without it would
  produce YAML that silently deletes that panel the next time anyone
  applied it, and losing somebody's panel is worse than failing loudly.
- `crewship export` does not yet call it. The export CLI knows only
  `Crew` and `Workspace` today — the same is true of every per-kind
  exporter in the manifest layer — and the kinds path is scheduled work.
  The function ships written and tested so that Page is not the one kind
  missing from it on the day that lands.

## See also

- [`kind: Routine`](/manifest/routine) — referenced as
  `producer: routine/<slug>`; routines are created before the pages that
  name them.
- [`kind: Agent`](/manifest/agent) — referenced as
  `producer: agent/<slug>`.
- [`kind: Crew`](/manifest/crew) — every panel's `owner` is a crew, and
  crew membership is what decides who sees the panel.
- [`kind: SavedView`](/manifest/saved_view) — the other read surface over
  workspace data. A saved view filters rows the app already has; a page
  renders whatever a producer pushed.
