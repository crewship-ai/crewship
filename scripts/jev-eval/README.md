# Crewship Jev pilot

## Local SemIf on MacBook Air

SemIf is an independent open implementation, not TypeSafe's Jev weights. This
separate smoke test uses the already installed SemIf MLX backend and pinned
Qwen3.5-4B checkpoint on Apple Silicon. It needs no TypeSafe or OpenRouter key.
Run it from a Crewship checkout on the Mac, with `~/AI/SemIf-OpenJev` installed:

```bash
python3 scripts/jev-eval/semif-mac-eval.py \
  --semif-root ~/AI/SemIf-OpenJev \
  --output ~/AI/semif-crewship-run-$(date +%Y%m%d%H%M%S)
```

The script loads the model once and scores 22 synthetic Czech/English cases
covering signed webhook routing, journal incident assessment, and Ansible /
Terraform dry runs. Results include raw choice accuracy, route accuracy after
the experimental review threshold, coverage, and warm per-case latency. The
model's option scores are **not calibrated confidence**. The corpus is a smoke
test, not a production accuracy benchmark; no agent is started. Each output
directory must be new. `--prepare-only` validates and writes SemIf input
without loading a model, and `--mlx-bits none` uses source precision.

The current Crewship `decision` routine step still uses a TypeSafe/OpenRouter
evaluator. This Mac test establishes local behavior before a local evaluator
is connected to that step; a completed score run must not be presented as an
end-to-end webhook integration.

## Hosted Jev pilot

The integrated commands are `crewship decisions evaluate`, `triage`, and
`rerank`. They read explicitly supplied stdin/files, print JSON, and never
modify server state. `--dry-run` prints a request without using a key/network.
See [the research and runbook](../../docs/prd/jev-research-and-pilot-2026-09-20.md)
for model contracts, limitations, benchmark sources, and build commands.

`triage.jsonl` is a **synthetic smoke corpus**: 12 English/Czech pairs written
before live evaluation. It is not a production benchmark or calibration set.
`rerank.json` exercises useful evidence, an irrelevant candidate and injection.
The runner evaluates triage only; exercise rerank separately with the CLI.

```bash
python3 scripts/jev-eval/run.py --binary /tmp/crewship-jev-pilot --output /tmp/jev-dry
# Set OPENROUTER_API_KEY securely in the environment before a live run.
python3 scripts/jev-eval/run.py --binary /tmp/crewship-jev-pilot \
  --provider openrouter --live --output /tmp/jev-live
python3 -m unittest discover -s scripts/jev-eval -p 'test_*.py'
```

Runs are serial, no retries, at most 24 calls by default (hard max 100).
A new output directory is required. Results exclude input text and credentials.
The first failed call stops the experiment. Missing credentials produce a
blocked report with null metrics. A dry run is never reported as inference.
Missing provider cost stays null; list-price estimates are labeled separately.
Metrics include area accuracy, review coverage, accuracy on accepted cases,
summed multiclass Brier, top-label probability ECE, signal accuracy and latency.
`confidence` is not used as a substitute for the top-label probability in ECE.
The current majority-label baseline is 25%; there is no live LLM baseline yet.

Before live use on sensitive documents, select the provider and data scope
explicitly. The CLI bypasses server Paymaster accounting; do not run this as an
unbounded background job. No server deployment is part of this pilot.
