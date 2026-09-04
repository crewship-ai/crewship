#!/usr/bin/env python3
"""
Tests for the docs-drift detector (scripts/docs_drift.py).

The detector is a cheap filter, not a verdict — its job is to hand the agent
concrete candidates. Two properties it rests on:

  1. It catches BOTH directions of drift (phantom and gap).
  2. It does not flood — ordinary English words in backticks must not pass as
     a "configuration key", or the report is unreadable and nobody opens it.

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
SCRIPT = os.path.join(HERE, "..", "scripts", "docs_drift.py")


def run(doc_text, go_text, exit_code=False):
    with tempfile.TemporaryDirectory() as d:
        doc = os.path.join(d, "page.mdx")
        pkg = os.path.join(d, "schema.go")
        with open(doc, "w", encoding="utf-8") as f:
            f.write(doc_text)
        with open(pkg, "w", encoding="utf-8") as f:
            f.write(go_text)
        cmd = [sys.executable, SCRIPT, "--doc", doc, "--pkg", pkg]
        if exit_code:
            cmd += ["--exit-code"]
        r = subprocess.run(cmd, capture_output=True, text=True)
        if r.returncode not in (0, 1):
            raise AssertionError(f"failed ({r.returncode}): {r.stderr}")
        out = json.loads(r.stdout)
        return out, r.returncode


def keys(items):
    return {p["key"] for p in items}


GO_WITH_THREE_FIELDS = '''
package manifest

type Devcontainer struct {
    NetworkMode    string   `yaml:"network_mode,omitempty"`
    AllowedDomains []string `yaml:"allowed_domains,omitempty"`
    MemoryMB       *int     `yaml:"memory_mb,omitempty"`
}
'''


class TestGaps(unittest.TestCase):
    """The code has a field, the documentation is silent about it."""

    def test_undocumented_field_is_a_gap(self):
        doc = "Set `network_mode` and `memory_mb`.\n"
        out, _ = run(doc, GO_WITH_THREE_FIELDS)
        self.assertIn("allowed_domains", keys(out["gaps"]))
        self.assertNotIn("network_mode", keys(out["gaps"]))

    def test_documented_field_is_not_a_gap(self):
        doc = "Keys: `network_mode`, `allowed_domains`, `memory_mb`.\n"
        out, _ = run(doc, GO_WITH_THREE_FIELDS)
        self.assertEqual(out["gaps"], [])
        self.assertEqual(out["drift"], 0)

    def test_key_in_a_yaml_block_counts_as_documented(self):
        """A field mentioned only in a YAML example is not undocumented."""
        doc = "```yaml\ndevcontainer:\n  network_mode: restricted\n" \
              "  allowed_domains:\n    - api.github.com\n  memory_mb: 2048\n```\n"
        out, _ = run(doc, GO_WITH_THREE_FIELDS)
        self.assertEqual(out["gaps"], [], out)


class TestPhantoms(unittest.TestCase):
    """The documentation describes a key the code does not have."""

    def test_nonexistent_key_is_a_phantom(self):
        doc = "Feature IDs are checked against `feature_allowlist`.\n"
        out, _ = run(doc, GO_WITH_THREE_FIELDS)
        self.assertIn("feature_allowlist", keys(out["phantoms"]))

    def test_existing_key_is_not_a_phantom(self):
        doc = "Set `network_mode`.\n"
        out, _ = run(doc, GO_WITH_THREE_FIELDS)
        self.assertNotIn("network_mode", keys(out["phantoms"]))


class TestNoise(unittest.TestCase):
    """A report nobody opens is useless."""

    def test_common_english_words_do_not_pass(self):
        doc = ("Set the `value` to `true` or `false`; the `default` is a "
               "`string`. See the `example` in `yaml` format, run `bash`.\n")
        out, _ = run(doc, GO_WITH_THREE_FIELDS)
        self.assertEqual(out["phantoms"], [], out)

    def test_single_word_identifier_without_underscore_is_not_a_phantom(self):
        """
        `something` in prose is almost certainly an English word, not a
        configuration key. Only a key with an underscore is a phantom — there
        the author's intent is unambiguous.
        """
        doc = "This `endpoint` returns a `payload` describing the `container`.\n"
        out, _ = run(doc, GO_WITH_THREE_FIELDS)
        self.assertEqual(out["phantoms"], [], out)

    def test_common_word_as_go_tag_is_not_a_gap(self):
        """
        `yaml:"name"` is in the code almost everywhere and does not belong in
        a reference page. Without the stoplist every page would report
        name/type/kind as a gap.
        """
        go = ('package m\ntype T struct{\n'
              '  A string `yaml:"name"`\n'
              '  B string `yaml:"type"`\n'
              '  C string `yaml:"network_mode"`\n}\n')
        out, _ = run("Set `network_mode`.\n", go)
        self.assertEqual(out["gaps"], [], out)

    def test_test_files_do_not_count_as_code(self):
        """_test.go introduces helper fields that do not belong in the docs."""
        with tempfile.TemporaryDirectory() as d:
            with open(os.path.join(d, "schema.go"), "w", encoding="utf-8") as f:
                f.write(GO_WITH_THREE_FIELDS)
            with open(os.path.join(d, "schema_test.go"), "w", encoding="utf-8") as f:
                f.write('package m\ntype T struct{ X string `yaml:"only_in_test"` }\n')
            doc = os.path.join(d, "s.mdx")
            with open(doc, "w", encoding="utf-8") as f:
                f.write("`network_mode` `allowed_domains` `memory_mb`\n")
            r = subprocess.run([sys.executable, SCRIPT, "--doc", doc, "--pkg", d],
                               capture_output=True, text=True)
            out = json.loads(r.stdout)
        self.assertNotIn("only_in_test", keys(out["gaps"]), out)


class TestOutput(unittest.TestCase):
    def test_default_exit_is_zero_even_with_drift(self):
        """A non-zero exit would abort the run in a routine `script` step."""
        out, code = run("`feature_allowlist`\n", GO_WITH_THREE_FIELDS)
        self.assertGreater(out["drift"], 0)
        self.assertEqual(code, 0)

    def test_exit_code_only_with_the_flag(self):
        _, code = run("`feature_allowlist`\n", GO_WITH_THREE_FIELDS, exit_code=True)
        self.assertEqual(code, 1)
        _, code_ok = run("`network_mode` `allowed_domains` `memory_mb`\n",
                         GO_WITH_THREE_FIELDS, exit_code=True)
        self.assertEqual(code_ok, 0)

    def test_drift_is_the_sum_of_both_directions(self):
        doc = "`feature_allowlist` and `network_mode`\n"
        out, _ = run(doc, GO_WITH_THREE_FIELDS)
        self.assertEqual(out["drift"], len(out["phantoms"]) + len(out["gaps"]))


class TestAuditWrapper(unittest.TestCase):
    """docs_audit.sh over a local checkout: the map drives the pairs and the
    panel summary is computed from the results, never hand-written."""

    def test_audit_over_a_local_repo_reports_pairs_and_panel(self):
        audit = os.path.join(HERE, "..", "scripts", "docs_audit.sh")
        with tempfile.TemporaryDirectory() as d:
            repo = os.path.join(d, "repo")
            os.makedirs(os.path.join(repo, "docs"))
            os.makedirs(os.path.join(repo, "internal"))
            with open(os.path.join(repo, "docs", "page.mdx"), "w", encoding="utf-8") as f:
                f.write("Set `network_mode` and the phantom `feature_allowlist`.\n")
            with open(os.path.join(repo, "internal", "schema.go"), "w", encoding="utf-8") as f:
                f.write(GO_WITH_THREE_FIELDS)
            shared = os.path.join(d, "shared")
            os.makedirs(os.path.join(shared, "scripts"))
            os.makedirs(os.path.join(shared, "config"))
            with open(os.path.join(shared, "config", "docs_map.json"), "w", encoding="utf-8") as f:
                json.dump({"pairs": [
                    {"doc": "docs/page.mdx", "pkg": ["internal/schema.go"], "why": "test"},
                    {"doc": "docs/missing.mdx", "pkg": ["internal/schema.go"]},
                ]}, f)
            os.symlink(os.path.abspath(SCRIPT), os.path.join(shared, "scripts", "docs_drift.py"))
            env = dict(os.environ, SHARED_ROOT=shared, LOCAL_REPO=repo, REPO="x/y")
            r = subprocess.run(["bash", audit], capture_output=True, text=True, env=env)
            self.assertEqual(r.returncode, 0, r.stderr)
            out = json.loads(r.stdout)
        self.assertEqual(out["pairs"], 2)
        self.assertEqual(out["phantoms"], 1)
        self.assertEqual(out["gaps"], 2)
        self.assertEqual(out["total_candidates"], 3)
        # One pair could not be scanned — the panel must say so, not average
        # it away into a warning.
        self.assertEqual(out["panel"]["state"], "critical")
        self.assertIn("1 of 2", out["panel"]["label"])
        self.assertEqual(out["results"][0]["doc"], "docs/page.mdx")


if __name__ == "__main__":
    unittest.main()
