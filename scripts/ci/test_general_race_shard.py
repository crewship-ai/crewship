"""Coverage and failure contracts for package-level race partitions."""
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("general_race", Path(__file__).with_name("general-race-shard.py"))
SHARD = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(SHARD)
P = SHARD.PREFIX
HELD = sorted(SHARD.DENIED | SHARD.DEDICATED)


class GeneralRaceShardTests(unittest.TestCase):
    def test_every_package_once_including_new_unmeasured_packages(self):
        packages = HELD + [P + name for name in ("internal/new", "internal/backup", "internal/access", "internal/other")]
        costs = {P + "internal/backup": 2000, P + "internal/access": 1700, P + "removed": 9999}
        groups, totals = SHARD.partition(packages, 2, costs, 150)
        self.assertEqual(groups[0], [P + "internal/backup"])
        self.assertEqual(groups[1], [P + "internal/access", P + "internal/new", P + "internal/other"])
        self.assertEqual(totals, [2000, 2000])
        self.assertEqual(SHARD.partition(list(reversed(packages)), 2, costs, 150), (groups, totals))
        self.assertEqual(sorted(p for g in groups for p in g), sorted(set(packages) - set(HELD)))

    def test_rejects_stale_dedicated_and_deny_entries(self):
        for omitted in HELD:
            with self.subTest(omitted=omitted), self.assertRaisesRegex(ValueError, "stale"):
                SHARD.partition([p for p in HELD if p != omitted] + [P + "new"], 1, {}, 150)

    def test_invalid_inventory_count_and_costs_fail_closed(self):
        valid = HELD + [P + "new"]
        for packages, count, costs, default in [
            ([], 1, {}, 150), (valid + [valid[-1]], 1, {}, 150),
            (valid + ["other/module"], 1, {}, 150), (valid, 0, {}, 150),
            (valid, 2, {}, 150), (valid, 1, {}, 0),
            (valid, 1, {P + "new": float("nan")}, 150),
            (valid, 1, {P + "new": -1}, 150),
        ]:
            with self.subTest(packages=packages, count=count, costs=costs), self.assertRaises(ValueError):
                SHARD.partition(packages, count, costs, default)

    def test_measured_inventory_is_balanced_and_heaviest_start_first(self):
        baseline = json.loads(Path(__file__).with_name("general-race-costs.json").read_text())
        groups, totals = SHARD.partition(HELD + list(baseline["packages"]), 2,
                                         baseline["packages"], baseline["default_seconds"])
        self.assertEqual([g[0] for g in groups], [P + "internal/backup", P + "internal/access"])
        self.assertLess(abs(totals[0] - totals[1]), 2)
        self.assertEqual(sum(map(len, groups)), len(baseline["packages"]))

    def test_executes_exact_partition_and_propagates_failure(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / "scripts/ci").mkdir(parents=True)
            (root / "scripts/ci/general-race-costs.json").write_text(json.dumps({
                "packages": {}, "default_seconds": 150, "source_run": "measured-run",
            }))
            inventory = HELD + [P + "a", P + "b", P + "c"]
            with patch.object(SHARD, "ROOT", root), patch.object(SHARD.subprocess, "run", side_effect=[
                subprocess.CompletedProcess([], 0, "\n".join(inventory)),
                subprocess.CompletedProcess([], 23),
            ]) as command:
                self.assertEqual(SHARD.run(0, 2, 2100), 23)
            record = json.loads((root / ".ci-results/general-race-shard.json").read_text())
            self.assertEqual(record["selected"], [P + "a", P + "c"])
            self.assertEqual(command.call_args_list[1].args[0], [
                "bash", "scripts/ci/go-test.sh", P + "a", P + "c", "-race", "-count=1", "-timeout", "2100s",
            ])
            self.assertEqual(record["inventory"], sorted(inventory))

    def test_go_list_failure_never_runs_partial_inventory(self):
        with patch.object(SHARD.subprocess, "run", side_effect=subprocess.CalledProcessError(1, "go list")) as command:
            with self.assertRaises(subprocess.CalledProcessError):
                SHARD.run(0, 2, 2100)
            self.assertEqual(command.call_count, 1)

    def test_bad_index_never_runs_go(self):
        with patch.object(SHARD.subprocess, "run") as command:
            for index in (-1, 2):
                with self.assertRaises(ValueError):
                    SHARD.run(index, 2, 2100)
            command.assert_not_called()


if __name__ == "__main__":
    unittest.main()
