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
HISTORY = 24

# Name every sixth tick and leave the rest to the panel. `labels` accepts null
# for a tick this producer declines to name, which is what lets a 24-point
# window exist at all: this script cannot know how wide the panel is, so it
# says which ticks MEAN something and the renderer decides how many of those
# names it can fit. It thins names, never points — all 24 readings are drawn
# whatever the axis ends up saying.
LABEL_EVERY = 6


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


def axis_label(age_slots: int) -> str | None:
    """The name of a tick `age_slots` pushes ago, or None for one left unnamed.

    The newest reading is named "now" and every sixth one before it carries its
    age. Everything between is a category with a value and no name: it still
    gets a bar, a tooltip and a place on the axis — it just does not get a word
    under it.
    """
    if age_slots % LABEL_EVERY != 0:
        return None
    return "now" if age_slots == 0 else f"-{age_slots * 5}s"


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
    except OSError as e:
        # OSError, not URLError. This ran for five days and then died here:
        # the server took longer than the timeout to answer a push, urllib
        # raised a bare TimeoutError, and TimeoutError is not a URLError — so
        # the one exception a long-running producer is guaranteed to meet was
        # the one this handler did not catch.
        #
        # OSError is the parent of both URLError and TimeoutError, so this is
        # the same width `measure()` above already uses. Nothing about a
        # transient network failure should end a process whose whole job is to
        # still be running tomorrow.
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
        try:
            one_pass(history)
        except Exception as e:  # noqa: BLE001 — see below
            # A producer's job is to still be running tomorrow, so the loop
            # outlives anything one pass can raise. This is deliberately
            # broader than the handlers inside push() and measure(): those
            # name the failures we predicted, and this one exists for the
            # failures we did not.
            #
            # Nothing is swallowed — the panel is what reports the outage. A
            # producer that stops pushing goes stale on its own SLA and the
            # page says so, which is exactly what happened here and is the
            # freshness contract working, not failing.
            print(f"pass failed: {e!r}", file=sys.stderr, flush=True)

        # Five seconds is clear of the server's floor: §10b.3 allows 12 pushes
        # a minute per panel with a burst of 30, and the write itself refuses
        # anything inside two seconds. A tighter loop buys 429s, not data.
        time.sleep(5)


def one_pass(history: dict[str, list[float | None]]) -> None:
    """Measure every target once and write both panels."""
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
            # Twenty-four points, four names. A tick this producer does not
            # want named is null — NOT "", which the schema still refuses,
            # because an empty string is what a broken f-string produces
            # and a blank you meant has to be distinguishable from a blank
            # you shipped. (Same distinction as null vs 0 in `values`.)
            "labels": [axis_label(HISTORY - i - 1) for i in range(HISTORY)],
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
