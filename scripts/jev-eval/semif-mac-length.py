#!/usr/bin/env python3
"""Measure local SemIf latency and bounded failure as journal state grows."""

import argparse
import importlib.util
import json
from pathlib import Path
import subprocess


HERE = Path(__file__).parent
SPEC = importlib.util.spec_from_file_location("semif_mac_eval", HERE / "semif-mac-eval.py")
EVAL = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(EVAL)

INFO = "18:32:54 pipeline.step.completed coolify-ingest write_snapshot succeeded; health check green."
ERROR = "18:33:00 production payment API returns 503 for 78% of requests in all zones; health checks fail."


def row(count):
    return {"id": f"info-{count}", "state": "\n".join([INFO] * count + [ERROR]),
            "question": "Does the latest journal evidence report an active production outage?",
            "options": [{"id": "yes", "description": "An active production outage is explicitly reported."},
                        {"id": "no", "description": "No active production outage is reported."}],
            "expected": "yes"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--semif-root", type=Path, default=Path("~/AI/SemIf-OpenJev").expanduser())
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=False)
    scorer = str(args.semif_root / ".venv/bin/semif-score")
    common = [scorer, "--backend", "mlx", "--mode", "direct", "--model", EVAL.MODEL,
              "--revision", EVAL.REVISION, "--mlx-bits", "4", "--max-tokens", "4096"]
    inputs = args.output / "bounded-input.jsonl"
    outputs = args.output / "bounded-output.jsonl"
    inputs.write_text("".join(json.dumps(row(n)) + "\n" for n in (1, 40, 120)))
    subprocess.run(common + ["--input", str(inputs), "--output", str(outputs)],
                   cwd=args.semif_root, check=True)
    results = [json.loads(line) for line in outputs.read_text().splitlines()]
    measured = [{"id": item["id"], "input_tokens": item["input_tokens"],
                 "latency_ms": item["total_seconds"] * 1000,
                 "choice": item["option_ids"][max(range(2), key=item["probabilities"].__getitem__)],
                 "top_score": max(item["probabilities"])} for item in results]
    oversized_input = args.output / "oversized-input.jsonl"
    oversized_output = args.output / "oversized-output.jsonl"
    oversized_input.write_text(json.dumps(row(300)) + "\n")
    attempt = subprocess.run(common + ["--input", str(oversized_input), "--output", str(oversized_output)],
                             cwd=args.semif_root, capture_output=True, text=True)
    summary = {"model": EVAL.MODEL, "mlx_bits": 4, "max_tokens": 4096,
               "bounded": measured,
               "oversized_rejected": attempt.returncode != 0,
               "oversized_exit_code": attempt.returncode,
               "oversized_output_rows": sum(1 for _ in oversized_output.open()) if oversized_output.exists() else 0}
    (args.output / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    print(json.dumps(summary, indent=2))


if __name__ == "__main__":
    main()
