#!/usr/bin/env python3
"""Experimental Mac MLX -> signed dev webhook probe; never grants permissions."""

import argparse
import hashlib
import hmac
import importlib.util
import json
import math
from pathlib import Path
import shlex
import stat
import subprocess
import time
import urllib.error
import urllib.request
from urllib.parse import urlsplit
import uuid


HERE = Path(__file__).parent
SPEC = importlib.util.spec_from_file_location("semif_mac_eval", HERE / "semif-mac-eval.py")
EVAL = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(EVAL)

REMOTE_SCORE = r'''
import json, pathlib, subprocess, sys, tempfile
rows = json.load(sys.stdin)
with tempfile.TemporaryDirectory(prefix="crewship-mlx-hook-") as directory:
    root = pathlib.Path(directory)
    source, target = root / "input.jsonl", root / "output.jsonl"
    source.write_text("".join(json.dumps(row, ensure_ascii=False) + "\n" for row in rows), encoding="utf-8")
    subprocess.run([".venv/bin/semif-score", "--backend", "mlx", "--mode", "direct",
                    "--model", "Qwen/Qwen3.5-4B", "--revision",
                    "851bf6e806efd8d0a36b00ddf55e13ccb7b8cd0a", "--mlx-bits", "4",
                    "--input", str(source), "--output", str(target)], check=True,
                   stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    print(json.dumps([json.loads(line) for line in target.read_text(encoding="utf-8").splitlines()]))
'''


def score(ssh_host, event):
    options = [{"id": key, "description": value} for key, value in EVAL.OPTIONS["webhook"].items()]
    state = "type={type} service={service} summary={summary}".format(**event)
    rows = [{"id": variant, "state": state, "question": EVAL.QUESTIONS["webhook"], "options": ordered}
            for variant, ordered in (("original", options), ("reversed", list(reversed(options))))]
    command = "cd ~/AI/SemIf-OpenJev && .venv/bin/python -c " + shlex.quote(REMOTE_SCORE)
    started = time.monotonic()
    process = subprocess.run(["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", ssh_host, command],
                             input=json.dumps(rows, ensure_ascii=False), capture_output=True,
                             text=True, timeout=180, check=True)
    results = json.loads(process.stdout)
    if len(results) != 2 or [item.get("id") for item in results] != ["original", "reversed"]:
        raise ValueError("unexpected SemIf result count or order")
    decisions = []
    for row, result in zip(rows, results):
        ids = [option["id"] for option in row["options"]]
        probs = result.get("probabilities")
        if (result.get("option_ids") != ids or not isinstance(probs, list) or len(probs) != len(ids) or
                any(not isinstance(p, (int, float)) or not math.isfinite(p) or p < 0 or p > 1 for p in probs) or
                abs(sum(probs) - 1) > .002):
            raise ValueError("invalid SemIf options or scores")
        winner = max(range(len(ids)), key=probs.__getitem__)
        decisions.append({"choice": ids[winner], "score": probs[winner]})
    route = decisions[0]["choice"] if (decisions[0]["choice"] == decisions[1]["choice"] and
                                         all(item["score"] >= .9 for item in decisions)) else "review"
    return {"route": route, "variants": decisions, "elapsed_seconds": round(time.monotonic() - started, 3)}


def fire(hook, event):
    target = urlsplit(hook["public_url"])
    if target.scheme != "http" or target.hostname not in ("localhost", "127.0.0.1") or target.port != 8081:
        raise ValueError("probe webhook must target the local dev1 API on port 8081")
    body = json.dumps(event, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    timestamp = str(int(time.time()))
    signature = hmac.new(hook["signing_secret"].encode("utf-8"),
                         timestamp.encode("ascii") + b"." + body, hashlib.sha256).hexdigest()
    request = urllib.request.Request(hook["public_url"], body, method="POST", headers={
        "Content-Type": "application/json", "X-Crewship-Timestamp": timestamp,
        "X-Crewship-Signature": "sha256=" + signature,
        "X-Crewship-Event-ID": "mlx-probe-" + uuid.uuid4().hex,
    })
    try:
        with urllib.request.urlopen(request, timeout=15) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        raise RuntimeError(f"dev1 webhook rejected delivery with HTTP {error.code}") from None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--event", type=Path, required=True, help="JSON file with type, service, summary and simulation=true")
    parser.add_argument("--hooks", type=Path, help="Private JSON map of route to webhook create responses")
    parser.add_argument("--ssh-host", default="pavelsrba@192.168.1.221")
    parser.add_argument("--fire", action="store_true", help="Send a signed event to the selected dev webhook")
    args = parser.parse_args()
    event = json.loads(args.event.read_text(encoding="utf-8"))
    if (not isinstance(event, dict) or event.get("simulation") is not True or
            any(not isinstance(event.get(key), str) or not event[key].strip() or len(event[key]) > 500
                for key in ("type", "service", "summary"))):
        parser.error("event must be a simulated object with bounded type, service and summary strings")
    result = score(args.ssh_host, event)
    if args.fire and result["route"] in ("sre", "developer"):
        if args.hooks is None:
            parser.error("--fire requires --hooks")
        if stat.S_IMODE(args.hooks.stat().st_mode) & 0o077:
            parser.error("--hooks must be readable only by its owner (mode 0600)")
        hooks = json.loads(args.hooks.read_text(encoding="utf-8"))
        status, receipt = fire(hooks[result["route"]], event)
        result.update(http_status=status, receipt=receipt)
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    main()
