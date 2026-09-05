#!/usr/bin/env python3
"""
Tests for the site-replica acceptance check (scripts/replica_check.py).

The check is the deterministic half of the "copy a website" demo: an agent
crew builds the replica, this script says whether it meets the bar the issue
promised. What it must never do is pass a page that phones home or a page
that was never built.

Run:
    python3 -m unittest discover -s tests -v
"""
import json
import os
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "..", "scripts", "replica_check.py")

GOOD = """<!doctype html>
<html lang="cs"><head><meta charset="utf-8"><title>Seznam replica</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>body{font-family:sans-serif}</style></head>
<body><h1>Seznam</h1><nav>Zprávy Počasí Email</nav>
<section>Zprávy dne</section><section>Počasí</section><footer>Sport</footer>
</body></html>"""


def run(html=None, content_map=None, exit_code=False, extra=()):
    with tempfile.TemporaryDirectory() as d:
        if html is not None:
            with open(os.path.join(d, "index.html"), "w", encoding="utf-8") as f:
                f.write(html)
        if content_map is not None:
            with open(os.path.join(d, "content-map.json"), "w", encoding="utf-8") as f:
                f.write(content_map if isinstance(content_map, str) else json.dumps(content_map))
        cmd = [sys.executable, SCRIPT, "--dir", d, *extra]
        if exit_code:
            cmd += ["--exit-code"]
        r = subprocess.run(cmd, capture_output=True, text=True)
        if r.returncode not in (0, 1):
            raise AssertionError(f"failed ({r.returncode}): {r.stderr}")
        return json.loads(r.stdout), r.returncode


def names(out, ok):
    return {c["name"] for c in out["checks"] if c["ok"] == ok}


class TestNotBuilt(unittest.TestCase):
    def test_missing_replica_is_not_a_pass_and_not_a_crash(self):
        out, code = run(html=None)
        self.assertFalse(out["ok"])
        self.assertFalse(out["built"])
        self.assertEqual(out["panel"]["state"], "warning")
        self.assertEqual(out["panel"]["verdict"], "NOT BUILT")
        self.assertEqual(code, 0)


class TestGoodReplica(unittest.TestCase):
    def test_self_contained_page_with_full_coverage_passes(self):
        out, _ = run(GOOD, {"sections": ["Zprávy", "Počasí", "Sport"]})
        self.assertTrue(out["ok"], out)
        self.assertEqual(out["failed"], 0)
        self.assertEqual(out["coverage"], 1.0)
        self.assertEqual(out["panel"]["state"], "ok")
        self.assertEqual(out["panel"]["verdict"], "PASS")

    def test_missing_content_map_is_reported_not_assumed(self):
        out, _ = run(GOOD)
        self.assertFalse(out["ok"])
        self.assertIn("content-map.json present", names(out, False))
        self.assertIsNone(out["coverage"])


class TestFailures(unittest.TestCase):
    def test_external_script_fails_self_containment(self):
        html = GOOD.replace("</head>", '<script src="https://cdn.example.com/x.js"></script></head>')
        out, _ = run(html, {"sections": ["Zprávy"]})
        self.assertIn("no external runtime requests", names(out, False))
        self.assertIn("no inline scripts", names(out, False))
        self.assertEqual(out["panel"]["state"], "critical")

    def test_protocol_relative_url_counts_as_external(self):
        html = GOOD.replace("</head>", '<img src="//cdn.example.com/a.png"></head>')
        out, _ = run(html, {"sections": ["Zprávy"]})
        self.assertIn("no external runtime requests", names(out, False))

    def test_relative_and_data_urls_are_fine(self):
        html = GOOD.replace("</head>", '<img src="logo.png"><img src="data:image/png;base64,AAAA"></head>')
        out, _ = run(html, {"sections": ["Zprávy"]})
        self.assertIn("no external runtime requests", names(out, True))

    def test_missing_viewport_and_title_fail(self):
        html = GOOD.replace('<meta name="viewport" content="width=device-width, initial-scale=1">', "") \
                   .replace("<title>Seznam replica</title>", "")
        out, _ = run(html, {"sections": ["Zprávy"]})
        self.assertIn("viewport meta", names(out, False))
        self.assertIn("title present", names(out, False))

    def test_two_h1_fail(self):
        html = GOOD.replace("<h1>Seznam</h1>", "<h1>Seznam</h1><h1>Again</h1>")
        out, _ = run(html, {"sections": ["Zprávy"]})
        self.assertIn("exactly one h1", names(out, False))

    def test_low_section_coverage_fails_and_names_the_missing(self):
        out, _ = run(GOOD, {"sections": ["Zprávy", "Počasí", "Sport", "Horoskopy", "TV program"]})
        self.assertIn("sections covered", names(out, False))
        missing = [c for c in out["checks"] if c["name"] == "sections covered"][0]["missing"]
        self.assertEqual(sorted(missing), ["Horoskopy", "TV program"])

    def test_coverage_threshold_is_configurable(self):
        out, _ = run(GOOD, {"sections": ["Zprávy", "Počasí", "Sport", "Horoskopy", "TV program"]},
                     extra=["--min-coverage", "0.5"])
        self.assertIn("sections covered", names(out, True))

    def test_broken_content_map_is_a_named_failure(self):
        out, _ = run(GOOD, "{not json")
        self.assertIn("content-map.json parses", names(out, False))


class TestExitCode(unittest.TestCase):
    def test_default_exit_is_zero_even_on_failure(self):
        _, code = run("<p>not a document</p>", {"sections": ["x"]})
        self.assertEqual(code, 0)

    def test_exit_code_flag(self):
        _, bad = run("<p>not a document</p>", {"sections": ["x"]}, exit_code=True)
        _, good = run(GOOD, {"sections": ["Zprávy"]}, exit_code=True)
        self.assertEqual(bad, 1)
        self.assertEqual(good, 0)


if __name__ == "__main__":
    unittest.main()
