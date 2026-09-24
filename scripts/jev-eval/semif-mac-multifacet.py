#!/usr/bin/env python3
"""Compare direct, serial and shared MLX scoring of one journal window."""

import argparse
import json
from pathlib import Path
import subprocess


HERE = Path(__file__).parent
MODEL = "Qwen/Qwen3.5-4B"
REVISION = "851bf6e806efd8d0a36b00ddf55e13ccb7b8cd0a"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--semif-root", type=Path, default=Path("~/AI/SemIf-OpenJev").expanduser())
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    source = HERE / "semif-multifacet.jsonl"
    cases = [json.loads(line) for line in source.read_text().splitlines()]
    args.output.mkdir(parents=True, exist_ok=False)
    scores = {}
    for mode in ("direct", "serial", "shared"):
        path = args.output / f"{mode}.jsonl"
        command = [str(args.semif_root / ".venv/bin/semif-score"), "--backend", "mlx",
                   "--mode", mode, "--model", MODEL, "--revision", REVISION,
                   "--mlx-bits", "4", "--input", str(source), "--output", str(path)]
        subprocess.run(command, check=True, cwd=args.semif_root)
        results = [json.loads(line) for line in path.read_text().splitlines()]
        if [result["id"] for result in results] != [case["id"] for case in cases]:
            raise ValueError(f"{mode} output did not match input")
        choices = {}
        for case, result in zip(cases, results):
            winner = max(range(len(result["probabilities"])), key=result["probabilities"].__getitem__)
            choices[case["id"]] = {"choice": result["option_ids"][winner],
                                   "expected": case["expected"],
                                   "top_score": result["probabilities"][winner]}
        scores[mode] = {
            "choices": choices,
            "correct": sum(item["choice"] == item["expected"] for item in choices.values()),
            "model_scoring_seconds": (results[0]["shared_timing"]["total_seconds"] if mode == "shared"
                                      else sum(result["total_seconds"] for result in results)),
            "input_tokens": [result["input_tokens"] for result in results],
        }
    original = scores["direct"]["choices"]
    summary = {"model": MODEL, "revision": REVISION, "mlx_bits": 4,
               "cases": len(cases), "modes": scores,
               "choice_changes_vs_direct": {
                   mode: [case_id for case_id, item in score["choices"].items()
                          if item["choice"] != original[case_id]["choice"]]
                   for mode, score in scores.items() if mode != "direct"}}
    (args.output / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    print(json.dumps(summary, indent=2))


if __name__ == "__main__":
    main()
