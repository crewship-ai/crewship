#!/usr/bin/env python3
"""ping.py — a Python producer for the `sit` page.

The Go program next door writes two panels of the same page and this one writes
the other two. Neither knows the other exists. That is what §0 means by the
page having no datasource: a producer is whatever runs closest to the thing
being measured, in whatever language was easiest, and the page is only where
their answers meet.

This one times HTTPS requests rather than TCP connects, so it measures
something the Go program cannot see — TLS plus the first byte — and writes a
series panel and a table.

Nothing here imports Crewship. The whole integration is one PUT with the
payload as the body; provenance and the freshness clock are the server's
(§4 rules 2 and 5) and a payload claiming either is refused.

    CREWSHIP_SERVER=http://localhost:8083 CREWSHIP_TOKEN=… \\
    CREWSHIP_WORKSPACE=… ./examples/pages/ping.py
"""

from __future__ import annotations

import json
import os
import sys
import time
import urllib.error
import urllib.request

BASE = os.environ.get("CREWSHIP_SERVER", "http://localhost:8083")
TOKEN = os.environ.get("CREWSHIP_TOKEN", "")
WORKSPACE = os.environ.get("CREWSHIP_WORKSPACE", "")

TARGETS = [
    ("google.com", "https://www.google.com/generate_204"),
    ("cloudflare", "https://1.1.1.1/"),
    ("github.com", "https://github.com/"),
]

# One reading per target per pass. series.v1 declares ONE unit for the whole
# panel (§3) — every one of these is milliseconds, so they can share an axis.
# A second unit would have to be a second panel, and the schema refuses it
# rather than drawing two axes, because a dual axis is the most common way a
# chart lies.
HISTORY = 10


def measure(url: str) -> float | None:
    """Milliseconds to first byte, or None when we could not measure at all.

    None matters: it is pushed as JSON null, which the panel renders as an em
    dash. That is a different claim from 0, which would say we measured an
    instant response. §9b.4 is the whole reason this returns an Optional rather
    than a sentinel like -1.
    """
    started = time.perf_counter()
    try:
        req = urllib.request.Request(url, headers={"User-Agent": "crewship-pages-example"})
        with urllib.request.urlopen(req, timeout=3) as resp:
            resp.read(1)
        return round((time.perf_counter() - started) * 1000, 1)
    except (urllib.error.URLError, TimeoutError, OSError):
        return None


def push(panel: str, payload: dict) -> None:
    url = f"{BASE}/api/v1/pages/sit/panels/{panel}/data?workspace_id={WORKSPACE}"
    body = json.dumps(payload).encode()
    req = urllib.request.Request(url, data=body, method="PUT")
    req.add_header("Content-Type", "application/json")
    req.add_header("Authorization", f"Bearer {TOKEN}")
    try:
        with urllib.request.urlopen(req, timeout=10):
            print(f"  {panel}: ok", flush=True)
    except urllib.error.HTTPError as e:
        # The server's own sentence beats anything this script could invent —
        # a 4xx here means the payload broke the panel's contract and the
        # message names which field and why.
        print(f"  {panel}: {e.code} {e.read()[:200].decode(errors='replace')}", file=sys.stderr, flush=True)
    except urllib.error.URLError as e:
        print(f"  {panel}: {e}", file=sys.stderr, flush=True)


def main() -> int:
    if not TOKEN or not WORKSPACE:
        print("CREWSHIP_TOKEN and CREWSHIP_WORKSPACE are required", file=sys.stderr)
        return 2

    # Per-target ring, kept by the producer because §11b.16 makes even spacing
    # of series points a contract the producer owns rather than something the
    # panel can verify.
    history: dict[str, list[float | None]] = {name: [] for name, _ in TARGETS}

    while True:
        print(time.strftime("%H:%M:%S") + " push → sit", flush=True)
        rows = []
        for name, url in TARGETS:
            ms = measure(url)
            hist = history[name]
            hist.append(ms)
            del hist[:-HISTORY]
            rows.append(
                {
                    "cil": name,
                    # A null cell is "we could not measure", rendered as an em
                    # dash. An empty string would be measured emptiness, which
                    # is a different and wrong claim.
                    "odezva": ms,
                    "stav": "ok" if ms is not None else "nedostupné",
                }
            )

        push(
            "http",
            {
                "unit": "ms",
                # Ten points, all labelled. The first version kept 24 and the
                # panel truncated every label to "-1…" because 24 do not fit
                # across a half-width panel.
                #
                # The obvious fix — send blanks for the ticks you do not want
                # named — is REFUSED by the schema: labels[] has minLength 1,
                # so a label must be a label. That is defensible, and it also
                # means series.v1 has no way to express a sparse axis today:
                # the producer must either send fewer points or send an
                # unreadable row, and only the RENDERER knows how many fit.
                # Filed rather than worked around here; this producer simply
                # keeps a shorter window.
                "labels": [f"-{(HISTORY - i - 1) * 5}s" for i in range(HISTORY)],
                "series": [
                    {"name": name, "values": ([None] * (HISTORY - len(history[name]))) + history[name]}
                    for name, _ in TARGETS
                ],
            },
        )

        push(
            "cile",
            {
                "columns": [
                    {"key": "cil", "label": "Cíl"},
                    {"key": "odezva", "label": "Odezva ms", "align": "right"},
                    {"key": "stav", "label": "Stav"},
                ],
                "rows": rows,
            },
        )

        # Five seconds is clear of the server's floor: §10b.3 allows 12 pushes
        # a minute per panel with a burst of 30, and the write itself refuses
        # anything inside two seconds. A tighter loop buys 429s, not data.
        time.sleep(5)


if __name__ == "__main__":
    raise SystemExit(main())
