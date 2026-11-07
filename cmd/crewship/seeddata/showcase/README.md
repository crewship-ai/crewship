# Seed showcase content

Files posted by the `crewship seed` showcase phase
(`cmd/crewship/cmd_seed_showcase.go`, catalogue in `../showcase.go`). Only the
`*.json` files are embedded in the binary; the `.md` and `.jsonl` files are the
sources they were produced from and are kept so the content can be edited and
regenerated rather than hand-patched.

| Directory | Files | Posted to |
| --- | --- | --- |
| `word/` | `NN-*.json` (native WordCraft) from `NN-*.md` | `POST /api/v1/pages/{slug}/craft/initialize` with `application_id: wordcraft` |
| `sheet/` | `*.json` (native GridCraft) from `*.jsonl` | `POST /api/v1/pages/{slug}/craft/initialize` with `application_id: gridcraft` |
| `art/m-*.json` | complete artifact manifests `{entrypoint, files, message}` | `POST /api/v1/pages/artifacts` |
| `art/data-*.json` | dashboard data batches `{base_version, documents}` | `POST /api/v1/pages/{slug}/artifact/data/{collection}` |
| `art/design-*.json` | Design operations `{operations}` | `POST /api/v1/pages/{slug}/design/operations` |

## Regenerating

Word documents:

1. `wordcraft-cli convert word/01-nabidka.md word/01-nabidka.json`
2. Set every table's style to `GridTable4AccentBlue`.
3. Set `core.title` to the document title.

Sheets:

```bash
gridcraft-cli run --script sheet/nabidka.jsonl --out sheet/nabidka.json
```

Artifact manifests, data batches and Design operations are authored by hand.

After regenerating, run `go test ./cmd/crewship/ -run 'TestShowcaseCatalogue|TestSeedShowcase'`
(through `crewship-heavy` on crewship-dev), then a seed against a dev server and
open each item. Slugs and names live in `../showcase.go`; changing one changes
what an already-seeded workspace considers "already present".
