# kind: Skill

## What it is

`kind: Skill` declares a standalone entry in the workspace's **skill
registry** — a `SKILL.md` body (markdown + front-matter) that any
agent in any crew can later bind to. It is the inverse of the nested
`Skill` reference under `Crew.spec.agents[].skills`: the nested form
says "this agent uses skill X (by slug)" and assumes the row already
exists; the standalone `kind: Skill` document says "ensure THIS
`SKILL.md` exists in the registry."

The common pattern pairs the two: declare the Skill once at the top
level (or as a top-level document in a workspace bundle), then
reference its `slug` from as many agents as you like.

The kind is implemented in
`internal/manifest/kinds/skill.go`. The backing endpoint is the
import handler, which is a genuine upsert keyed on slug — so Create
and Update both POST the same body; the manifest only distinguishes
the two for readable dry-run output.

### One body source — pick exactly one

A Skill carries its `SKILL.md` body via **exactly one** of three
mutually-exclusive sources. Zero is a validation error (there is
nothing to import); two or more is rejected with the offending list.

| Source     | What it is | Notes |
|------------|------------|-------|
| `inline:`  | the `SKILL.md` body embedded verbatim | Capped at **8 KiB** so the YAML stays diff-friendly. Larger bodies must use `path:`. |
| `path:`    | a manifest-relative path to a `SKILL.md` file | Resolved by the bundle loader against the manifest file's directory (same `safeJoin` sandbox the nested Skill uses) *before* Validate runs. |
| `source:`  | an **HTTPS** URL to a remote `SKILL.md` | The import handler does the fetch itself (SSRF guard + SPDX license gate). The manifest layer only forwards the URL. |

## YAML schema

```yaml
apiVersion: crewship/v1     # required — always crewship/v1 for now
kind: Skill                 # required — the literal string "Skill"
metadata:
  name: Network Probe       # required — human label on the registry card
  slug: network-probe       # required — idempotency key + cross-kind reference
                            #            (^[a-z0-9][a-z0-9_-]*$)
spec:
  description: TCP/UDP probes against allow-listed hosts  # required
  display_name: Network Probe   # optional — defaults to metadata.name server-side
  category: networking          # optional — groups skills in the registry browser
  icon: radar                   # optional — lucide-react icon slug
  inline: |                     # one of inline / path / source (exactly one)
    ---
    name: network-probe
    description: TCP/UDP probes against allow-listed hosts
    license: MIT
    ---
    # Network Probe
    Probe a host:port and report reachability.
  allow_unsafe_license: false   # optional — bypass the SPDX allowlist gate
```

### Field reference

| Field | Type | Required | Notes |
|---|---|---|---|
| `metadata.name` | string | **yes** | Human label on the registry card. |
| `metadata.slug` | string | **yes** | Idempotency key + cross-kind reference. Must match `^[a-z0-9][a-z0-9_-]*$` (lowercase alphanumeric, underscore, hyphen). |
| `spec.description` | string | **yes** | One-liner shown on the card. Required at the manifest level even when the `SKILL.md` front-matter also carries one — an empty description renders a blank card. |
| `spec.display_name` | string | no | Card label override. Falls back to `metadata.name` server-side. |
| `spec.category` | string | no | Free-form group ("networking", "research", …). |
| `spec.icon` | string | no | lucide-react icon slug. |
| `spec.inline` | string | one-of | The `SKILL.md` body embedded verbatim. Max 8 KiB. |
| `spec.path` | string | one-of | Manifest-relative path to a `SKILL.md` file. |
| `spec.source` | string | one-of | HTTPS URL the import handler fetches. |
| `spec.allow_unsafe_license` | bool | no | Bypass the SPDX allowlist gate the importer otherwise applies. |

> `display_name`, `category`, and `icon` are forward-compatibility
> metadata today: the importer reads front-matter from the body
> verbatim and does not yet merge these spec fields into it. They
> still round-trip via export, so set them now if you want them
> populated when the merge lands.

## Examples

### Inline body

```yaml
apiVersion: crewship/v1
kind: Skill
metadata:
  name: Network Probe
  slug: network-probe
spec:
  description: TCP/UDP probes against allow-listed hosts
  category: networking
  inline: |
    ---
    name: network-probe
    description: TCP/UDP probes against allow-listed hosts
    license: MIT
    ---
    # Network Probe
    Probe a host:port and report reachability.
```

### Sibling file (large body)

```yaml
apiVersion: crewship/v1
kind: Skill
metadata:
  name: House Style
  slug: house-style
spec:
  description: ACME's internal code-style + commit conventions
  path: ./skills/house-style/SKILL.md
```

### Remote source

```yaml
apiVersion: crewship/v1
kind: Skill
metadata:
  name: Incident Runbook
  slug: incident-runbook
spec:
  description: Step-by-step incident response runbook
  source: https://raw.githubusercontent.com/acme/skills/main/incident/SKILL.md
```

### Cross-kind reference

Once a Skill is declared, agents reference it by slug:

```yaml
---
apiVersion: crewship/v1
kind: Skill
metadata: { name: House Style, slug: house-style }
spec:
  description: ACME's internal code-style conventions
  inline: |
    ---
    name: house-style
    description: ACME's internal code-style conventions
    ---
    # House Style
    One feature per PR; commits in imperative mood.
---
apiVersion: crewship/v1
kind: Agent
metadata: { name: Daniel, slug: daniel }
spec:
  crew_slug: code-review
  prompt: You are a senior code reviewer.
  skills: [house-style]        # ← references the Skill above by slug
```

## CLI reference

The manifest pipeline drives the same import endpoint the UI uses; no
new per-kind subcommands ship with this kind.

| Command | Description |
|---|---|
| `crewship apply --file skill.yaml` | Declarative upsert from manifest (Create or Update). |
| `crewship apply --dir ./manifests/` | Walk a directory; apply every Skill in topo order (workspace credentials + skills run first). |
| `crewship export workspace` | Round-trip — emits one `kind: Skill` document per non-`BUNDLED` row (see "Round-trip via export"). |

## REST endpoint mapping

| Manifest field | POST body field | Notes |
|---|---|---|
| `metadata.slug` | (front-matter `name`) | Idempotency key; the import handler keys the upsert on slug. |
| `spec.inline` / `spec.path` | `content` | The resolved `SKILL.md` body. |
| `spec.source` | `url` | Forwarded; the handler fetches + SSRF/license-gates. |
| `spec.allow_unsafe_license` | `allow_unsafe_license` | Bypass the SPDX gate. |

Endpoints used:

| Verb | Path | Action |
|---|---|---|
| `GET` | `/api/v1/skills` | List (for drift detection + export) |
| `POST` | `/api/v1/workspaces/{workspaceId}/skills/import` | Create OR Update (upsert) |
| `DELETE` | `/api/v1/workspaces/{workspaceId}/skills/{skillId}` | Delete (sync-mode only; not used by the default Plan) |

## Validation rules

`SkillDocument.Validate` (offline — no HTTP, no filesystem) enforces:

- `apiVersion`, when set, equals `crewship/v1`.
- `kind`, when set, equals `Skill`.
- `metadata.name` is non-empty.
- `metadata.slug` is non-empty and matches `^[a-z0-9][a-z0-9_-]*$`.
- `spec.description` is non-empty.
- **Exactly one** of `inline` / `path` / `source` is set (zero or more
  than one is an error, with the offending list named).
- `inline` body length ≤ 8 KiB.
- `source`, when set, parses as a URL, uses the `https` scheme, and
  has a host.
- `path`, when set, must have been resolved by the bundle loader (a
  hand-constructed document that never went through Load is flagged).

Validate does not consult `WorkspaceContext` — Skill is a leaf kind
with no FK references; the argument exists only for dispatcher
uniformity.

## Apply behavior

### ApplyUpsert (default)

For each declared Skill, Plan compares against the matched-by-slug
remote row:

- Remote missing → `ActionCreate` (POST import).
- Remote present and **a body source is declared** → `ActionUpdate`
  (POST import). Because the list endpoint exposes no body hash, the
  manifest cannot tell whether the body actually changed, so any
  apply that declares a body re-posts it. This is deliberate: it is
  preferable to silently losing an edit.
- Remote present, **no** body source, decoration matches →
  `ActionUnchanged` (no REST call).
- Remote row is `source: BUNDLED` → **error**. Bundled skills are
  server-seeded on every startup; the manifest refuses to touch them
  (mirrors the server-side guard). Pick a different slug.

### ApplyReplace

Replace mode can `DELETE` the registry row then re-`POST` it. Be
aware bundled rows are still off-limits.

## Round-trip via export

`crewship export workspace` calls `ExportSkills`, which emits one
`kind: Skill` document per **non-`BUNDLED`** row, sorted by slug.

**Body lossiness:** the list endpoint does NOT return the `SKILL.md`
content, so exported documents carry metadata + decoration but **no
body source** (no `inline`, `path`, or `source`). The export is
suitable for cataloguing what a workspace has, but re-applying it
as-is fails Validate ("exactly one of inline / path / source must be
set"). To get a round-trip-safe export you must re-attach a body
(materialise the `SKILL.md` files and rewrite the docs with `path:`
entries).

## See also

- [Agent](/manifest/agent) — binds skills via `spec.skills` (slug list).
- [Workspace](/manifest/workspace) — declares skills inline under `spec.skills`.
- Backend: `internal/api/skills.go`, `internal/skills/importer.go`.
- This kind's Go implementation: `internal/manifest/kinds/skill.go`.
