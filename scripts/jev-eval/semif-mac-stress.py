#!/usr/bin/env python3
"""Probe SemIf option-order sensitivity and repeatability on Crewship cases."""

import argparse
import importlib.util
import json
import math
from pathlib import Path
import statistics
import subprocess


HERE = Path(__file__).parent
SPEC = importlib.util.spec_from_file_location("semif_mac_eval", HERE / "semif-mac-eval.py")
EVAL = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(EVAL)


def variants(cases):
    rows = []
    for row in EVAL.prepare_rows(cases):
        options = row["options"]
        for variant, ordered in (("original", options), ("repeat", options),
                                 ("reversed", list(reversed(options))),
                                 ("rotated", options[1:] + options[:1]),
                                 ("sorted", sorted(options, key=lambda option: option["id"]))):
            rows.append({**row, "id": f"{row['id']}::{variant}", "options": ordered})
    return rows


def analyze(cases, rows, outputs, threshold):
    if len(rows) != len(outputs) or [row["id"] for row in rows] != [result.get("id") for result in outputs]:
        raise ValueError("SemIf output does not match generated variants")
    expected = {case["id"]: case["expected"] for case in cases}
    detail = []
    for row, result in zip(rows, outputs):
        option_ids = [option["id"] for option in row["options"]]
        if result.get("option_ids") != option_ids:
            raise ValueError(f"SemIf options changed for {row['id']}")
        probs = result.get("probabilities")
        if (not isinstance(probs, list) or len(probs) != len(option_ids) or
                any(not isinstance(p, (float, int)) or not math.isfinite(p) or p < 0 or p > 1 for p in probs) or
                abs(sum(probs) - 1) > .002):
            raise ValueError(f"SemIf probabilities invalid for {row['id']}")
        top = max(range(len(probs)), key=probs.__getitem__)
        choice = option_ids[top]
        detail.append({"id": row["id"], "expected": expected[row["id"].split("::")[0]],
                       "choice": choice, "route": choice if probs[top] >= threshold else "review",
                       "top_score": probs[top], "probabilities": dict(zip(option_ids, probs)),
                       "latency_ms": result["total_seconds"] * 1000})
    grouped = {}
    for item in detail:
        case_id, variant = item["id"].rsplit("::", 1)
        grouped.setdefault(case_id, {})[variant] = item
    choice_changes = []
    route_changes = []
    repeat_differences = []
    unsafe = []
    for case_id, group in grouped.items():
        original = group["original"]
        repeat = group["repeat"]
        if any(abs(original["probabilities"][key] - repeat["probabilities"][key]) > 1e-6
               for key in original["probabilities"]):
            repeat_differences.append(case_id)
        for variant in ("reversed", "rotated", "sorted"):
            item = group[variant]
            if item["choice"] != original["choice"]:
                choice_changes.append({"id": case_id, "variant": variant,
                                       "from": original["choice"], "to": item["choice"],
                                       "expected": item["expected"]})
            if item["route"] != original["route"]:
                route_changes.append({"id": case_id, "variant": variant,
                                      "from": original["route"], "to": item["route"],
                                      "expected": item["expected"]})
        for variant, item in group.items():
            if item["route"] != "review" and item["route"] != item["expected"]:
                unsafe.append({"id": case_id, "variant": variant, "route": item["route"],
                               "expected": item["expected"], "top_score": item["top_score"]})
    return detail, {
        "cases": len(cases), "scored_rows": len(rows), "threshold": threshold,
        "choice_changes": choice_changes, "route_changes": route_changes,
        "repeat_probability_differences": repeat_differences,
        "wrong_automatic_routes": unsafe,
        "median_latency_ms": statistics.median(item["latency_ms"] for item in detail),
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--semif-root", type=Path, default=Path("~/AI/SemIf-OpenJev").expanduser())
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--mlx-bits", choices=["4", "none"], default="4")
    parser.add_argument("--threshold", type=float, default=.9)
    args = parser.parse_args()
    cases = sum((EVAL.load_cases(HERE / name) for name in
                 ("semif-mac-cases.jsonl", "semif-mac-challenge.jsonl", "semif-keeper-cases.jsonl")), [])
    rows = variants(cases)
    args.output.mkdir(parents=True, exist_ok=False)
    input_path = args.output / "input.jsonl"
    output_path = args.output / "output.jsonl"
    input_path.write_text("".join(json.dumps(row, ensure_ascii=False) + "\n" for row in rows))
    command = [str(args.semif_root / ".venv/bin/semif-score"), "--backend", "mlx",
               "--mode", "direct", "--model", EVAL.MODEL, "--revision", EVAL.REVISION,
               "--input", str(input_path), "--output", str(output_path)]
    if args.mlx_bits != "none":
        command += ["--mlx-bits", args.mlx_bits]
    subprocess.run(command, check=True, cwd=args.semif_root)
    outputs = [json.loads(line) for line in output_path.read_text().splitlines()]
    detail, summary = analyze(cases, rows, outputs, args.threshold)
    summary.update(model=EVAL.MODEL, revision=EVAL.REVISION, mlx_bits=args.mlx_bits)
    (args.output / "results.jsonl").write_text("".join(json.dumps(item, ensure_ascii=False) + "\n" for item in detail))
    (args.output / "summary.json").write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps(summary, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
