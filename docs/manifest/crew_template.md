# kind: CrewTemplate

## What it is

A `CrewTemplate` is a **deploy-only reference kind**. The manifest entry instantiates an existing template (creating a new crew + its agents from the template's blueprint) but **cannot author the template itself**. Template authoring belongs to the seeded built-in catalog under `internal/database` or to direct workspace inserts into the `crew_templates` table — both outside the manifest pipeline.

In other words: a `CrewTemplate` document is the YAML equivalent of clicking "Deploy" on a card in the templates UI.

### One-shot semantics — there is no update flow

The backend endpoint behind this kind is:

```
POST /api/v1/crew-templates/{slug}/deploy
```

It writes the crew + all agents in a **single transaction** and rejects re-deploying onto an existing crew (HTTP 409, `errCrewSlugConflict`). There is **no** `PATCH /deploy`, **no** `DELETE /deploy`, and **no** diff-and-reapply path. Once a crew exists under the `crew_slug_override` slug, this kind has nothing left to do.

As a consequence the planner emits **only two actions**:

| Situation                                                          | Plan action       | What happens                            |
|--------------------------------------------------------------------|-------------------|------------------------------------------|
| Source template exists, override slug free, `deploy: true`         | `Create`          | POST `/deploy` with the body below       |
| Override slug already names a crew                                 | `Unchanged`       | No HTTP traffic                          |
| `deploy: false` (any state)                                        | `Unchanged`       | No HTTP traffic, warning in description  |
| Source template missing                                            | Plan returns error | Apply aborts before any mutation        |

> **Idempotency is by slug alone.** The `crews` table has no `template_slug` provenance column, so a pre-existing crew whose slug happens to equal `crew_slug_override` is treated as "already deployed" — even if it was actually created by hand via `kind: Crew`. By design: the server would 409 on the deploy POST anyway, and the manifest's job is to converge to "a crew with this slug exists", not to enforce who created it.

> **`deploy: false` is inert.** Setting `deploy: false` does **not** trigger an "undeploy". There is no inverse endpoint. To remove a crew that came from a template, delete it via `kind: Crew` (or `crewship crew delete <slug>`). The CrewTemplate planner records intent and surfaces a warning in dry-run output so the no-op is visible.

## YAML schema

```yaml
apiVersion: crewship/v1
kind: CrewTemplate
metadata:
  name: Engineering team           # required — sent to the deploy handler as `crew_name`
  slug: engineering-team           # required — names the SOURCE template (must exist in /api/v1/crew-templates)
spec:
  deploy: true                     # true (default) = ensure crew exists; false = no-op
  crew_slug_override: my-eng-team  # REQUIRED — the slug the resulting crew should carry (workspace-unique)
  inputs:                          # optional — forwarded verbatim to the deploy endpoint (forward compat)
    devcontainer_image: node:20
    region: eu-west-1
```

### Field reference

| Field                       | Required | Type               | Notes |
|-----------------------------|----------|--------------------|-------|
| `apiVersion`                | yes      | string             | Always `crewship/v1`. |
| `kind`                      | yes      | string             | Always `CrewTemplate`. |
| `metadata.name`             | **yes**  | string             | Human label. Sent to the deploy handler as `crew_name` — the server 400s on empty. |
| `metadata.slug`             | **yes**  | string             | Slug of the SOURCE template. Must match a row in `GET /api/v1/crew-templates`. Existence is checked at **Plan time**, not Validate. |
| `metadata.description`      | no       | string             | Informational; not forwarded. |
| `spec.deploy`               | no       | bool               | Defaults to false in Go (`bool` zero value). Author YAML should set it explicitly. `true` = deploy if missing, `false` = no-op. |
| `spec.crew_slug_override`   | **yes**  | string (kebab-case) | The slug the new crew will carry. One template can deploy many crews, so this is what distinguishes them. Validated as `^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$` at Validate time; uniqueness is enforced server-side at Apply. |
| `spec.inputs`               | no       | map[string]any     | Forwarded verbatim to the deploy endpoint. The current server ignores unknown keys via `readJSON`'s tolerant decoding, so future template parameters can land without a manifest schema bump. |

## Examples

### Minimal example — deploy a built-in template

```yaml
apiVersion: crewship/v1
kind: CrewTemplate
metadata:
  name: Eng Team A
  slug: engineering-team        # built-in template slug
spec:
  deploy: true
  crew_slug_override: eng-team-a
```

### Deploy the same template twice under different override slugs

A single template can back many crews — `crew_slug_override` distinguishes them:

```yaml
---
apiVersion: crewship/v1
kind: CrewTemplate
metadata: { name: Eng Team A, slug: engineering-team }
spec:
  deploy: true
  crew_slug_override: eng-team-a
---
apiVersion: crewship/v1
kind: CrewTemplate
metadata: { name: Eng Team B, slug: engineering-team }
spec:
  deploy: true
  crew_slug_override: eng-team-b
```

After the first apply, both crews `eng-team-a` and `eng-team-b` exist. Re-applying the same bundle is a pure `Unchanged` plan (no HTTP traffic to the deploy endpoint).

### Deploy with inputs (forward-compatible)

```yaml
apiVersion: crewship/v1
kind: CrewTemplate
metadata:
  name: Data Platform
  slug: data-team
spec:
  deploy: true
  crew_slug_override: data-platform-eu
  inputs:
    devcontainer_image: python:3.12
    region: eu-west-1
    issue_prefix: DP
```

The `inputs` map is currently ignored server-side; future versions of the deploy handler may consume specific keys. Manifest authors can include them today without breaking compatibility.

### `deploy: false` — pin without re-applying

```yaml
apiVersion: crewship/v1
kind: CrewTemplate
metadata: { name: Eng Team A, slug: engineering-team }
spec:
  deploy: false
  crew_slug_override: eng-team-a
```

The planner emits `Unchanged` with a warning. Useful for keeping a CrewTemplate document in a versioned bundle for documentation purposes while temporarily disabling automatic deploys.

## CLI reference

The crew-template surface has a dedicated CLI under `crewship template …` (no hyphen — registered as `templateCmd` in `cmd/crewship/cmd_template.go`); manifest-level apply does not add new subcommands beyond the global `apply` / `export`.

| Command                                                | One-liner |
|--------------------------------------------------------|-----------|
| `crewship template list`                               | List the workspace's templates (built-in + workspace-authored). |
| `crewship template get <slug>`                         | Show a single template's agent list. |
| `crewship template deploy <slug> --name <name>`        | Imperative deploy (equivalent to applying one `CrewTemplate` doc). |
| `crewship apply --file <file>`                         | Declarative apply — deploys every `CrewTemplate` doc whose override slug isn't yet realised. |
| `crewship export workspace`                            | Emits `CrewTemplate` docs for crews whose slug heuristically matches a template (see "Round-trip via export"). |

## REST endpoint mapping

| Manifest field                                 | HTTP verb + path                                        | Request body |
|------------------------------------------------|---------------------------------------------------------|--------------|
| `spec.deploy: true`, override slug free        | `POST /api/v1/crew-templates/{metadata.slug}/deploy`    | `{crew_name, crew_slug, inputs?}` |
| `spec.deploy: true`, override slug taken       | —                                                       | No request issued; plan is `Unchanged` |
| `spec.deploy: false` (any state)               | —                                                       | No request issued; plan is `Unchanged` (with warning) |
| Source template existence check                | `GET /api/v1/crew-templates`                            | — |
| Override slug existence check                  | `GET /api/v1/crews`                                     | — |
| Export                                         | `GET /api/v1/crew-templates` + `GET /api/v1/crews`      | Heuristic match (see below) |

### POST body shape

The deploy handler (`internal/api/crew_templates.go`) reads two fields and tolerates extras:

```json
{
  "crew_name": "Engineering team",
  "crew_slug": "my-eng-team",
  "inputs": { "devcontainer_image": "node:20" }
}
```

- `crew_name` → `metadata.name`. Server 400s on empty.
- `crew_slug` → `spec.crew_slug_override`. Server slugifies + validates; collision returns 409.
- `inputs`     → forwarded but currently unused. Forward compatibility hook.

### DB columns touched (informational)

The deploy handler writes one row to `crews` (id, workspace_id, name, slug, icon, color, timestamps) and N rows to `agents` (one per template agent, with `agent_slug = template_agent.slug + "-" + crew_slug`). It does **not** record provenance — there is no `crews.template_slug` column.

## Validation rules

`Validate` (offline, no HTTP) enforces:

- `apiVersion` must be empty or `crewship/v1`.
- `kind` must be empty or `CrewTemplate`.
- `metadata.name` is non-empty (sent as `crew_name`).
- `metadata.slug` is non-empty (source template slug; existence is checked at Plan time).
- `spec.crew_slug_override` is non-empty.
- `spec.crew_slug_override` matches `^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$` (kebab-case, no leading/trailing hyphen, no underscores, no uppercase).

Source-template catalog membership (`metadata.slug` actually exists in `GET /api/v1/crew-templates`) is **not** checked at validate time — `WorkspaceContext` does not carry remote templates. The check is deferred to `Plan`, where the live HTTP client is available, and surfaces as a transparent "source template not found" error in dry-run output.

## Apply behavior

### Default (`ApplyUpsert`)

Plan decision table:

| `spec.deploy` | Override slug occupied? | Source template present? | Action     | Notes |
|---------------|--------------------------|--------------------------|------------|-------|
| `true`        | no                       | yes                      | `Create`   | POST `/deploy` |
| `true`        | yes                      | yes                      | `Unchanged` | No HTTP traffic |
| `true`        | —                        | no                       | Plan error | Apply aborts before any mutation |
| `false`       | no                       | yes                      | `Unchanged` | Warning: "deploy=false — no action" |
| `false`       | yes                      | yes                      | `Unchanged` | Warning: "cannot undeploy" |

### `ApplyStrict`

Strict mode adds no extra constraints for this kind. A crew already occupying the override slug is **not** a strict-mode error — it is the expected `Unchanged` outcome. Strict mode only fires for kinds whose backend supports re-creation; deploy is one-shot, so strict semantics collapse onto upsert semantics.

### `ApplyReplace`

Replace mode is also a no-op for the deploy itself: there is no DELETE endpoint at `/api/v1/crew-templates/{slug}/deploy`, so even "destructively recreate" cannot undeploy the existing crew. To remove a crew before re-deploying, declare a `kind: Crew` document with the same slug and delete it via that kind's `Replace` semantics, or use `crewship crew delete <slug>` first. When the backend grows an inverse deploy verb this kind will start emitting `Delete + Create` pairs in Replace mode without any YAML schema change.

## Round-trip via export

### Provenance gap

The `crews` schema has **no `template_slug` column** today. The deploy handler writes the crew row without recording the source template. There is therefore no authoritative way to round-trip a deployed crew back into a `kind: CrewTemplate` document.

`ExportCrewTemplates` makes a best-effort heuristic match: a crew whose slug **exactly equals** a known template's slug is reported as a CrewTemplate deployment with `crew_slug_override = crew.slug`. Crews whose slugs diverge from any template slug — the common case, since operators usually rename — are **not** exported as `CrewTemplate`; they round-trip via `kind: Crew` instead.

| Crew slug         | Matching template? | Exported as       |
|-------------------|--------------------|-------------------|
| `engineering-team` | yes (built-in)     | `kind: CrewTemplate` |
| `my-custom-crew`   | no                 | `kind: Crew` (other agent) |
| `eng-team-a`       | no                 | `kind: Crew` (other agent) |

This means an export → wipe → re-apply cycle re-creates renamed crews as raw `Crew` documents, not template deployments — losing the "deployed from X" semantic but preserving the actual state of the world.

When/if the server grows a `template_slug` column on `crews`, the heuristic in `ExportCrewTemplates` switches to an exact provenance check; the public Export\* signature stays the same.

### Emitted document shape

For each match, the export produces:

```yaml
apiVersion: crewship/v1
kind: CrewTemplate
metadata:
  name: <template.name>           # from the catalog row
  slug: <template.slug>
spec:
  deploy: true
  crew_slug_override: <crew.slug> # equals metadata.slug for heuristic matches
  # inputs intentionally omitted — the deploy body is not persisted server-side
```

Re-applying an exported `CrewTemplate` doc is a pure `Unchanged` plan (the crew is already there).

## See also

- [`docs/manifest/recipe.md`](/manifest/recipe) — sibling install-only reference kind.
- [`docs/manifest/connector.md`](/manifest/connector) — sibling install-only reference kind (integrations).
- `internal/api/crew_templates.go` — backend handler for `GET /api/v1/crew-templates`, `GET /api/v1/crew-templates/{slug}`, `POST /api/v1/crew-templates/{slug}/deploy`.
- `internal/database/seed_crew_templates.go` — source of truth for built-in templates that ship with the binary.
- `internal/manifest/kinds/crew_template.go` — this kind's Go implementation.
