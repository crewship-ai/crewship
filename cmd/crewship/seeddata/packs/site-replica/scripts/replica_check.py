#!/usr/bin/env python3
"""
Deterministic acceptance check for a static site replica built by the
engineering crew (the "copy seznam.cz" demo).

The crew writes its work under one directory in the crew's shared volume:

    <dir>/index.html          the self-contained replica (required)
    <dir>/content-map.json    the analyst's section inventory (optional)

This script decides, without an LLM, whether the replica meets the bar the
issues promised: it exists, is a real HTML document, has the metadata a page
needs, is self-contained (no third-party runtime requests), and covers the
sections the analyst inventoried. Judgement calls — does it LOOK like the
original — stay with the human who opens the file.

Output: JSON on stdout with `ok`, one entry per check and a `panel` summary
in status.v1 shape. Exit code is ALWAYS 0 (routine `script` step contract);
pass `--exit-code` for shell/CI use.
"""
import argparse
import json
import os
import re
import sys
from html.parser import HTMLParser

MAX_BYTES = 2 * 1024 * 1024
DEFAULT_MIN_COVERAGE = 0.7

# Attributes that make the browser fetch something.
FETCH_ATTRS = {("script", "src"), ("link", "href"), ("img", "src"),
               ("iframe", "src"), ("video", "src"), ("audio", "src"),
               ("source", "src"), ("object", "data"), ("embed", "src")}


class Inspector(HTMLParser):
    def __init__(self):
        super().__init__()
        self.title = ""
        self._in_title = False
        self.viewport = False
        self.h1 = 0
        self.external = []
        self.text = []
        self.has_html = False
        self.lang = ""

    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if tag == "html":
            self.has_html = True
            self.lang = (a.get("lang") or "").strip()
        if tag == "title":
            self._in_title = True
        if tag == "h1":
            self.h1 += 1
        if tag == "meta" and (a.get("name") or "").lower() == "viewport":
            self.viewport = True
        for t, attr in FETCH_ATTRS:
            if tag == t:
                val = (a.get(attr) or "").strip()
                if tag == "link" and (a.get("rel") or "").lower() not in ("stylesheet", "icon", "preload", "modulepreload"):
                    continue
                if re.match(r"^(https?:)?//", val, re.I):
                    self.external.append(f"<{tag} {attr}={val[:80]}>")

    def handle_endtag(self, tag):
        if tag == "title":
            self._in_title = False

    def handle_data(self, data):
        if self._in_title:
            self.title += data
        self.text.append(data)


def check_replica(directory, min_coverage=DEFAULT_MIN_COVERAGE, min_sections=1):
    checks = []

    def add(name, ok, detail):
        checks.append({"name": name, "ok": bool(ok), "detail": detail})

    index = os.path.join(directory, "index.html")
    if not os.path.isfile(index):
        add("index.html exists", False, f"{index} not found — has the build issue run?")
        return finish(checks, coverage=None, built=False)

    size = os.path.getsize(index)
    add("index.html exists", size > 0, f"{size} bytes")
    add("size under 2 MiB", size <= MAX_BYTES, f"{size} bytes (limit {MAX_BYTES})")

    with open(index, encoding="utf-8", errors="replace") as f:
        html = f.read()
    insp = Inspector()
    insp.feed(html)

    add("html document", insp.has_html and "<!doctype html" in html.lower()[:300],
        "has <!doctype html> and an <html> element" if insp.has_html else "no <html> element")
    add("title present", insp.title.strip() != "", insp.title.strip()[:80] or "missing <title>")
    add("viewport meta", insp.viewport, "responsive viewport declared" if insp.viewport else "no <meta name=viewport>")
    add("exactly one h1", insp.h1 == 1, f"{insp.h1} <h1> element(s)")
    add("no external runtime requests", not insp.external,
        "self-contained" if not insp.external else f"{len(insp.external)} external: " + "; ".join(insp.external[:5]))
    add("no inline scripts", "<script" not in html.lower(),
        "no <script> element" if "<script" not in html.lower() else "contains <script>")

    coverage = None
    cmap = os.path.join(directory, "content-map.json")
    if os.path.isfile(cmap):
        try:
            with open(cmap, encoding="utf-8") as f:
                m = json.load(f)
            sections = [s for s in (m.get("sections") or []) if isinstance(s, str) and s.strip()]
        except (OSError, ValueError) as e:
            sections = []
            add("content-map.json parses", False, str(e)[:120])
        if sections:
            body = " ".join(insp.text).lower()
            hit = [s for s in sections if s.strip().lower() in body]
            coverage = len(hit) / len(sections)
            add("sections covered", coverage >= min_coverage and len(hit) >= min_sections,
                f"{len(hit)}/{len(sections)} inventoried sections present ({coverage:.0%}, need {min_coverage:.0%})")
            missing = [s for s in sections if s not in hit]
            if missing:
                checks[-1]["missing"] = missing[:10]
    else:
        add("content-map.json present", False,
            "analyst inventory not found — coverage not measured")

    return finish(checks, coverage=coverage, built=True)


def finish(checks, coverage, built):
    passed = sum(1 for c in checks if c["ok"])
    failed = len(checks) - passed
    ok = failed == 0
    if not built:
        state, label = "warning", "no replica built yet — start the build issue"
    elif ok:
        state, label = "ok", f"{passed}/{len(checks)} checks passed"
    else:
        state, label = "critical", f"{failed} of {len(checks)} checks failed: " + ", ".join(
            c["name"] for c in checks if not c["ok"])
    return {
        "ok": ok,
        "built": built,
        "passed": passed,
        "failed": failed,
        "coverage": coverage,
        "checks": checks,
        "panel": {"state": state, "label": label,
                  "verdict": "PASS" if ok else ("NOT BUILT" if not built else "FAIL")},
    }


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--dir", default="/crew/shared/site-replica",
                    help="directory holding index.html (+ content-map.json)")
    ap.add_argument("--min-coverage", type=float, default=DEFAULT_MIN_COVERAGE)
    ap.add_argument("--exit-code", action="store_true",
                    help="exit 1 when a check fails (shell/CI; NOT in a routine)")
    args = ap.parse_args()
    out = check_replica(args.dir, args.min_coverage)
    out["dir"] = args.dir
    print(json.dumps(out, ensure_ascii=False, indent=2))
    sys.exit(1 if (args.exit_code and not out["ok"]) else 0)


if __name__ == "__main__":
    main()
