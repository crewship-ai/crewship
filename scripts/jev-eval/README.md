# Decision-model experiments (archive)

This directory preserves standalone experiments from
[closed PR #2630](https://github.com/crewship-ai/crewship/pull/2630). It does not
install a decision evaluator or expose a new command in Crewship `main`.
The local SemIf/MLX Python scorers can run independently on the Mac. The hosted
CLI runner and the routine `decision` fixture require the historical PR code;
they are retained for future implementation work. The dev1 webhook bridge is
a manual experiment, and its webhook triggers and target routines are disabled.

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

The historical PR's `decision` routine step used a TypeSafe/OpenRouter
evaluator. Crewship `main` has no such step. This Mac test establishes local
model behavior; a completed score run is not an end-to-end product integration.

The [2026-09-24 Mac report](../../docs/prd/reports/semif-mac-2026-09-24/README.md)
records actual MLX results for the default 22 cases and a separate 12-case
challenge corpus (`--cases scripts/jev-eval/semif-mac-challenge.jsonl`) in both
4-bit and source precision.

The [extended feature assessment](../../docs/prd/reports/semif-mac-2026-09-24/extended/README.md)
adds 28 synthetic Keeper tool-call cases, option-order and repeatability stress,
a same-corpus Laya MLX baseline, journal input-length limits, and multi-question
MLX timing. Its result files are retained next to the report. On the Mac, run
each script from the Crewship checkout and choose a fresh output directory:

```bash
python3 scripts/jev-eval/semif-mac-eval.py --semif-root ~/AI/SemIf-OpenJev \
  --cases scripts/jev-eval/semif-keeper-cases.jsonl --output ~/AI/semif-keeper-q4
python3 scripts/jev-eval/semif-mac-stress.py --semif-root ~/AI/SemIf-OpenJev \
  --output ~/AI/semif-stress-q4
python3 scripts/jev-eval/semif-mac-multifacet.py --semif-root ~/AI/SemIf-OpenJev \
  --output ~/AI/semif-multifacet
python3 scripts/jev-eval/semif-mac-length.py --semif-root ~/AI/SemIf-OpenJev \
  --output ~/AI/semif-length
python3 scripts/jev-eval/laya-mac-baseline.py --output ~/AI/laya-crewship-baseline
```

Add `--mlx-bits none` to the first or stress command to use source precision.
The Laya command requires its separate `laya_mlx` Python environment. The
length script intentionally sends one oversized input and records its failure.

The [dev1 webhook pilot](../../docs/prd/reports/semif-mac-2026-09-24/dev1-webhook-pilot.md)
uses two small routine fixtures, a local MLX bridge, and signed webhook
deliveries to Riley and Jamie. The bridge accepts only simulated events and
uses two option orders before sending a webhook. Its signing secrets live in
a private dev1 file outside the repository. The webhook and agent step were
reached, but both agent responses failed on dev1's Claude subscription access
error; the report records the run and receipt IDs. The bridge does not make
the server's `decision` step local.

## Hosted Jev pilot

The commands on the closed branch were `crewship decisions evaluate`, `triage`,
and `rerank`. They read explicitly supplied stdin/files, print JSON, and never
modify server state. `--dry-run` prints a request without using a key/network.
They are absent from the `main` binary; the commands below require a binary
built from the historical PR.
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
