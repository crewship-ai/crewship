#!/usr/bin/env python3
"""Compare SemIf's Crewship smoke corpus against the local Laya MLX port."""

import argparse
import importlib.util
import json
from pathlib import Path
import time

import laya_mlx as laya


HERE = Path(__file__).parent
SPEC = importlib.util.spec_from_file_location("semif_mac_eval", HERE / "semif-mac-eval.py")
EVAL = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(EVAL)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--threshold", type=float, default=.9)
    args = parser.parse_args()
    cases = sum((EVAL.load_cases(HERE / name) for name in
                 ("semif-mac-cases.jsonl", "semif-mac-challenge.jsonl", "semif-keeper-cases.jsonl")), [])
    args.output.mkdir(parents=True, exist_ok=False)
    start = time.perf_counter()
    agent = laya.load("aac6fef/laya-multilingual-mlx")
    load_seconds = time.perf_counter() - start
    rows = []
    for case in cases:
        options = EVAL.OPTIONS[case["area"]]
        question = {"route": {"type": "choice", "instructions": EVAL.QUESTIONS[case["area"]],
                              "criteria": options}}
        start = time.perf_counter()
        result = agent.predict(case["state"], question)
        elapsed_ms = (time.perf_counter() - start) * 1000
        answer = result["answers"]["route"]
        choice = answer["choice"]
        probabilities = answer["probabilities"]
        if choice not in options or set(probabilities) != set(options):
            raise ValueError(f"Laya returned incompatible options for {case['id']}")
        top = probabilities[choice]
        rows.append({"id": case["id"], "area": case["area"], "language": case["language"],
                     "expected": case["expected"], "raw_choice": choice,
                     "chosen": choice if top >= args.threshold else "review",
                     "top_option_score": top, "latency_ms": round(elapsed_ms, 3)})
    (args.output / "results.jsonl").write_text("".join(json.dumps(row, ensure_ascii=False) + "\n" for row in rows))
    summary = {"model": "aac6fef/laya-multilingual-mlx", "runtime": "laya_mlx (independent port)",
               "load_seconds": load_seconds, "threshold": args.threshold,
               "all": EVAL.metrics(rows),
               "by_area": {area: EVAL.metrics([row for row in rows if row["area"] == area])
                           for area in EVAL.OPTIONS}}
    (args.output / "summary.json").write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps(summary, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
