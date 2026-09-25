#!/usr/bin/env python3
"""Small synthetic integration probe, NOT a production-quality benchmark.

Uses the actual Crewship CLI/client/recipe. No external Python dependencies.
Default: validate local requests only; --live makes at most --max-calls calls.
No retries. First API/CLI error stops the run. A fresh output directory is required.
"""
import argparse
import collections
import datetime
import hashlib
import json
import math
import os
from pathlib import Path
import statistics
import subprocess


def metrics(rows):
    if not rows:
        return {"n": 0, "accuracy": None, "coverage": None, "selective_accuracy": None}
    correct = [r["area"] == r["expected"] for r in rows]
    accepted = [r for r in rows if not r["needs_review"]]
    brier = [sum((p - (label == r["expected"])) ** 2 for label, p in r["probabilities"].items()) for r in rows]
    # ECE uses top-label probability, NOT the provider's derived confidence.
    bins = collections.defaultdict(list)
    for r, ok in zip(rows, correct):
        p = r["probabilities"][r["area"]]
        bins[min(9, int(p * 10))].append((p, ok))
    ece = sum(len(b) / len(rows) * abs(statistics.mean(p for p, _ in b) - statistics.mean(ok for _, ok in b)) for b in bins.values())
    times = sorted(r["latency_ms"] for r in rows)
    return {
        "n": len(rows), "accuracy": statistics.mean(correct),
        "coverage": len(accepted) / len(rows),
        "selective_accuracy": statistics.mean(r["area"] == r["expected"] for r in accepted) if accepted else None,
        "multiclass_brier_sum": statistics.mean(brier), "top_probability_ece_10_bins": ece,
        "p50_ms": statistics.median(times), "p95_ms": times[math.ceil(.95 * len(times)) - 1],
        "signal_accuracy": {k: statistics.mean((r["signals"][k] >= .5) == r["expected_signals"][k] for r in rows) for k in ("production_impact", "requests_destructive_action")},
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--dataset", type=Path, default=Path(__file__).with_name("triage.jsonl"))
    parser.add_argument("--provider", choices=["typesafe", "openrouter"], default="typesafe")
    parser.add_argument("--max-calls", type=int, default=24)
    parser.add_argument("--live", action="store_true")
    args = parser.parse_args()
    if not 1 <= args.max_calls <= 100:
        parser.error("max-calls must be 1..100")
    binary = Path(args.binary).resolve(strict=True)
    raw = args.dataset.read_bytes()
    cases = [json.loads(line) for line in raw.splitlines() if line.strip()]
    if not cases or len({c["id"] for c in cases}) != len(cases):
        parser.error("dataset must contain unique, nonempty cases")
    key_name = "TYPESAFE_API_KEY" if args.provider == "typesafe" else "OPENROUTER_API_KEY"
    # Missing credentials produce an explicit blocked artifact, never fake scores.
    missing_key = args.live and not os.environ.get(key_name, "").strip()
    args.output.mkdir(parents=True, exist_ok=False)
    rows, error, validated = [], None, 0
    planned = cases[:args.max_calls]
    if missing_key:
        error = f"{key_name} is missing; no API requests attempted"
    else:
        for case in planned:
            command = [str(binary), "decisions", "triage", "--provider", args.provider]
            if not args.live:
                command.append("--dry-run")
            try:
                proc = subprocess.run(command, input=case["text"], text=True, capture_output=True, timeout=15)
            except (OSError, subprocess.TimeoutExpired):
                error = f"CLI could not complete case {case['id']}; stopped without retry"
                break
            if proc.returncode:
                # Do not persist arbitrary stderr from another binary or a provider.
                error = f"CLI failed on case {case['id']} (exit {proc.returncode}); stopped without retry"
                break
            try:
                payload = json.loads(proc.stdout)
            except json.JSONDecodeError:
                error = f"CLI returned invalid JSON on case {case['id']}; stopped without retry"
                break
            if not args.live:
                validated += 1
                continue
            result = payload["result"]
            response = result["response"]
            answer = response["answers"]["area"]
            rows.append({
                "id": case["id"], "language": case["language"], "expected": case["area"],
                "area": answer["choice"], "confidence": answer["confidence"],
                "probabilities": answer["probabilities"], "needs_review": result["needs_review"],
                "reason": result["reason"], "model": response["model"], "recipe": result["recipe"],
                "latency_ms": payload["latency_ms"], "usage": response["usage"],
                "signals": {k: response["answers"][k]["noul"] for k in ("production_impact", "requests_destructive_action")},
                "expected_signals": {k: case[k] for k in ("production_impact", "requests_destructive_action")},
            })
            # Checkpoint each completed measurement, excluding text and credentials.
            with (args.output / "results.jsonl").open("a") as out:
                out.write(json.dumps(rows[-1], ensure_ascii=False) + "\n")
    costs = [r["usage"].get("cost") for r in rows]
    summary = {
        "at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "status": "blocked" if missing_key else "failed" if error else "live_completed" if args.live else "dry_run_only",
        "error": error, "provider": args.provider, "dataset_sha256": hashlib.sha256(raw).hexdigest(),
        "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
        "dataset_size": len(cases), "planned": len(planned), "local_requests_validated": validated,
        "successful_api_calls": len(rows), "unmeasured": len(cases) - len(rows),
        "all": metrics(rows),
        "by_language": {lang: metrics([r for r in rows if r["language"] == lang]) for lang in sorted({c["language"] for c in cases})},
        "majority_class_baseline": max(collections.Counter(c["area"] for c in planned).values()) / len(planned),
        "reported_cost_usd": sum(costs) if costs and all(c is not None for c in costs) else None,
        "estimated_list_cost_usd": sum(r["usage"]["input_tokens"] for r in rows) * .042 / 1_000_000 if rows else None,
        "cost_basis": "Estimate uses Jev list input price as checked 2026-09-20; reported cost is separate; failed calls may still be charged.",
        "limitations": "24 synthetic bilingual examples, not independent paired samples or held-out production data. No LLM baseline; no accuracy claim from dry run. Threshold 0.9 is uncalibrated. No automatic actions.",
    }
    (args.output / "summary.json").write_text(json.dumps(summary, indent=2, ensure_ascii=False) + "\n")
    print(json.dumps(summary, indent=2, ensure_ascii=False))
    return 1 if error else 0


if __name__ == "__main__":
    raise SystemExit(main())
