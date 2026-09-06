# Demo v2 — one feature per step

Small manifest steps, applied one at a time against a seeded dev server, so
every feature can be run again and again and checked in the web UI before
release 1.0. Lives next to the v1 seed (`crewship seed`) and never touches
it: its own crew, its own label, its own project.

```bash
export CREWSHIP_PROFILE=dev2            # or CREWSHIP_SERVER
export CREWSHIP=/tmp/crewship-2-dev     # the binary dev.sh built (default: ./crewship at the repo root)

./demo.sh init                          # after a nuke: demo@crewship.ai / password123, login, model credential — no data
./demo.sh list                          # the steps, in order
./demo.sh plan  01-issues               # dry-run
./demo.sh apply 01-issues               # create / update, never delete
./demo.sh reset 01-issues               # delete + recreate — run it again from scratch
```

`init` is the v2 counterpart of what the v1 seed does before its data: the
admin user, a CLI login (re-pinning the profile's workspace, which a nuke
invalidates), and one model credential from `SEED_ANTHROPIC_API_KEY` in the
repo's `.env.local` — nothing else. A fresh dev slot is therefore
`./dev.sh nuke --yes`, `./demo.sh init`, `./demo.sh apply 00-crew`.

| Step | What it puts in the workspace | What to check in the UI |
|---|---|---|
| `00-crew` | crew **Lab** with Mia (lead, Sonnet) and Leo (engineer, Haiku), the `demo-v2` label; binds the model credential and builds the container | Crews → Lab: agents, credential on each, provisioning progress |
| `01-issues` | project **Demo v2** and issue **LAB-1**: Leo writes `/crew/shared/demo/hello.txt` with one known line and comments on the issue | Issues: the row, its status changes; the issue: activity, session, Leo's comment; Crew Lab → Files; Inbox |

Then `crewship issue start LAB-1` (or ▶ in the UI). Verified on dev2: 33 s from
start to REVIEW, the file has the exact line, one comment from Leo.

## Rules for a step

- One `NN-name/` directory, `*.yaml` files applied in name order, each a
  plain `crewship apply` manifest (`kind: Issue`, `Routine`, `Page`, …).
- The result must be checkable without reading logs: a file with a known
  line, a status that changed, a comment that exists.
- `apply` runs with `--no-delete`; `reset` runs with `--replace`. Nothing in
  a step may depend on v1 seed data.
- A defect found while applying a step is filed, and the workaround is
  marked with a comment in the manifest that names the issue.

## Findings so far

Filed as #2426 (manifest, four defects): a standalone Project cannot name a
lead; `Project.status: active` is accepted by the manifest and refused by the
API with a 500; a Label is never idempotent (second apply stops on 409); an
Issue's `status` is ignored on create. The workarounds are in the manifests.
