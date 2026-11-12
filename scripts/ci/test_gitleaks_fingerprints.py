"""Exercise exact historical fixture exceptions with the actual scanner.

The Security Gitleaks job installs its checksum-verified scanner and requires
these tests. Other unittest discovery may skip them if that tool is absent.
All credential-shaped canaries below are generated only in an owned temporary
Git repository; no provider, live credential or application is contacted.
"""

import json
import os
from pathlib import Path
import random
import shutil
import string
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
FIXTURE = "internal/artifacts/data_runs_test.go"


class GitleaksFingerprintTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.binary = shutil.which("gitleaks")
        if not cls.binary:
            if os.environ.get("GITLEAKS_REQUIRED") == "1":
                raise RuntimeError("Security must install its verified Gitleaks binary")
            raise unittest.SkipTest("Executed with the real scanner in Security")

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="crewship-gitleaks-fixture-")
        self.addCleanup(self.temporary.cleanup)
        self.repo = Path(self.temporary.name)
        self.env = {k: v for k, v in os.environ.items()
                    if not k.startswith(("GIT_", "GITLEAKS_"))}
        self.env.update(GIT_AUTHOR_DATE="2026-01-01T12:00:00Z",
                        GIT_COMMITTER_DATE="2026-01-01T12:00:00Z")
        self.git("init", "-q")
        self.git("config", "user.name", "Synthetic scanner fixture")
        self.git("config", "user.email", "scanner@example.invalid")
        self.git("config", "commit.gpgsign", "false")
        self.git("config", "core.hooksPath", "/dev/null")
        for name in (".gitleaks.toml", ".gitleaksignore"):
            shutil.copyfile(ROOT / name, self.repo / name)

    def git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.repo), *args],
                                       env=self.env, stderr=subprocess.DEVNULL,
                                       text=True).strip()

    def write_canaries(self, seed):
        # Deliberately generated high-entropy shapes, never obtained from a
        # provider. Literal live credentials elsewhere must still be detected.
        alphabet = string.ascii_letters + string.digits
        rng = random.Random(seed)
        pat = "glpat-" + "".join(rng.sample(alphabet, 20))
        gcp = "AIza" + "".join(rng.sample(alphabet, 35))
        path = self.repo / FIXTURE
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("package artifacts\n" + "\n" * 34
                        + f'const canaryValue = "{pat}"\n'
                        + f'const serviceValue = "{gcp}"\n')

    def scan(self):
        report = self.repo / "scan.json"
        result = subprocess.run(
            [self.binary, "detect", "--no-banner", "--redact",
             "--source", str(self.repo), "--config", str(self.repo / ".gitleaks.toml"),
             "--gitleaks-ignore-path", str(self.repo / ".gitleaksignore"),
             "--log-opts=HEAD", "--report-format=json", "--report-path", str(report)],
            cwd=self.repo, env=self.env, capture_output=True, timeout=30)
        # Never put scanner output or secret fields in unittest failure text.
        self.assertIn(result.returncode, (0, 1), "scanner execution failed")
        self.assertTrue(report.is_file(), "scanner produced no report")
        findings = json.loads(report.read_text()) or []
        self.assertEqual(result.returncode, 1 if findings else 0)
        return [{key: finding[key] for key in ("RuleID", "Fingerprint", "File", "StartLine")}
                for finding in findings]

    def test_exact_exception_preserves_other_values_at_the_same_path_and_line(self):
        self.write_canaries(5188)
        self.git("add", ".gitleaks.toml", ".gitleaksignore", FIXTURE)
        self.git("commit", "-qm", "synthetic scanner canaries")
        original_commit = self.git("rev-parse", "HEAD")
        baseline = self.scan()
        self.assertEqual({f["RuleID"] for f in baseline}, {"gitlab-pat", "gcp-api-key"})
        pat = next(f for f in baseline if f["RuleID"] == "gitlab-pat")
        self.assertEqual(pat["Fingerprint"], f"{original_commit}:{FIXTURE}:gitlab-pat:36")

        # An exact exception removes only that original finding, not a second
        # credential shape in the same file or another rule.
        ignore = self.repo / ".gitleaksignore"
        with ignore.open("a") as output:
            output.write("\n" + pat["Fingerprint"] + "\n")
        self.assertEqual({f["RuleID"] for f in self.scan()}, {"gcp-api-key"})

        # Replacing the value at the SAME path, line and rule must be detected
        # again because its introducing commit is different.
        self.write_canaries(5191)
        self.git("add", FIXTURE)
        self.git("commit", "-qm", "different synthetic canary at the same line")
        replacement_commit = self.git("rev-parse", "HEAD")
        self.assertNotEqual(original_commit, replacement_commit)
        replacement = self.scan()
        self.assertIn(f"{replacement_commit}:{FIXTURE}:gitlab-pat:36",
                      {f["Fingerprint"] for f in replacement})
        self.assertIn("gcp-api-key", {f["RuleID"] for f in replacement})


if __name__ == "__main__":
    unittest.main()
