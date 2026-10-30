"""Exercise owned fixture lifecycle without contacting a server."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

HERE = Path(__file__).resolve().parent

class FixtureLifecycleTests(unittest.TestCase):
    def run_fixture(self, body, *, fail_save=False, fail_delete=False, mode="normal"):
        with tempfile.TemporaryDirectory() as directory:
            env = dict(os.environ, TMPDIR=directory, FIXTURE_LOG=directory + "/calls", MODE=mode, FAIL_SAVE=str(int(fail_save)), FAIL_DELETE=str(int(fail_delete)))
            shell = r'''source "$1/lib.sh"
cs() {
  printf '%s\n' "$*" >> "$FIXTURE_LOG"
  if [[ "$1 $2" == "routine get" ]]; then
    if [[ "$MODE" == "auth-error" ]]; then
      echo 'API error (401): unauthorized' >&2
      return 1
    elif [[ "$MODE" == "collision" ]]; then
      echo '{"id":"foreign"}'
      return 0
    elif [[ -f "$TMPDIR/saved" ]]; then
      if [[ "$MODE" == "changed-id" && -f "$TMPDIR/reads" ]]; then
        echo '{"id":"foreign"}'
        return 0
      fi
      touch "$TMPDIR/reads"
      printf '{"id":"owned-id","slug":"%s"}\n' "$3"
    else
      echo 'API error (404): pipeline not found' >&2
      return 1
    fi
  elif [[ "$1 $2" == "routine save" ]]; then
    [[ "$FAIL_SAVE" == 0 ]] || return 1
    touch "$TMPDIR/saved"
    if [[ "$MODE" == "human-save" ]]; then
      printf 'Saved routine %s (id=owned-id, hash=1234)\n' "$4"
    else
      printf '{"id":"owned-id","slug":"%s"}\n' "$4"
    fi
  elif [[ "$1 $2" == "routine delete" ]]; then
    [[ "$FAIL_DELETE" == 0 ]] || return 1
    rm "$TMPDIR/saved"
  fi
}
source "$1/routine-fixtures.sh"
''' + body
            result = subprocess.run(["bash", "-c", shell, "fixture-test", str(HERE)], env=env, capture_output=True, text=True)
            log = Path(env["FIXTURE_LOG"])
            calls = log.read_text().splitlines() if log.exists() else []
            leftovers = [p.name for p in Path(directory).iterdir() if p.name not in {"calls", "saved", "reads"}]
            return result, calls, leftovers

    def test_success_removes_only_owned_unique_slug(self):
        result, calls, leftovers = self.run_fixture('harness_fixture probe ops OWNED\necho "$OWNED"\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        slug = result.stdout.strip()
        self.assertTrue(slug.startswith("harness-"))
        self.assertEqual(calls[-1], "routine delete " + slug + " --yes")
        self.assertEqual(len(calls), 5)
        self.assertEqual(leftovers, [])

    def test_failed_save_is_failure_and_never_deletes(self):
        result, calls, leftovers = self.run_fixture('harness_fixture probe ops OWNED\n', fail_save=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len(calls), 2)
        self.assertEqual(leftovers, [])

    def test_cleanup_failure_cannot_return_green(self):
        result, calls, leftovers = self.run_fixture('harness_fixture probe ops OWNED\n', fail_delete=True)
        self.assertEqual(result.returncode, 1)
        self.assertIn("Fixture cleanup failed", result.stderr)
        self.assertEqual(leftovers, [])

    def test_error_exit_preserved_after_successful_cleanup(self):
        result, calls, leftovers = self.run_fixture('harness_fixture probe ops OWNED\nexit 17\n')
        self.assertEqual(result.returncode, 17)
        self.assertTrue(calls[-1].startswith("routine delete "))
        self.assertEqual(leftovers, [])

    def test_preexisting_name_is_never_saved_or_deleted(self):
        result, calls, _ = self.run_fixture('harness_fixture probe ops OWNED\n', mode="collision")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len(calls), 1)

    def test_auth_failure_is_not_treated_as_absence(self):
        result, calls, _ = self.run_fixture('harness_fixture probe ops OWNED\n', mode="auth-error")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len(calls), 1)

    def test_replaced_identity_is_never_deleted(self):
        result, calls, _ = self.run_fixture('harness_fixture probe ops OWNED\n', mode="changed-id")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any(c.startswith("routine delete") for c in calls))
        self.assertIn("refused changed identity", result.stderr)

    def test_actual_term_runs_owned_cleanup(self):
        result, calls, leftovers = self.run_fixture('harness_fixture probe ops OWNED\nkill -TERM "$$"\n')
        self.assertEqual(result.returncode, 143)
        self.assertTrue(calls[-1].startswith("routine delete "))
        self.assertEqual(leftovers, [])

    def test_legacy_human_save_output_still_cleans_up_exact_id(self):
        result, calls, leftovers = self.run_fixture('harness_fixture probe ops OWNED\n', mode="human-save")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(calls[-1].startswith("routine delete "))
        self.assertEqual(leftovers, [])

if __name__ == "__main__":
    unittest.main()
