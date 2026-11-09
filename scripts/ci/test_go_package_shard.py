"""Package partitions cover the listing exactly once and refuse truncation."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("go_package_shard", Path(__file__).with_name("go-package-shard.py"))
SHARD = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(SHARD)
P = SHARD.PREFIX
API = P + "internal/api"


def api_inventory(n=50):
    tests = [f"TestRoute{i:03}" for i in range(n)] + ["ExampleRequest", "FuzzRoute", "TestČeský"]
    # `go test -list .` also prints benchmarks and a package summary line.
    return tests, "\n".join(reversed(tests)) + "\nBenchmarkRoute\nok  \t" + API + "\t0.1s\n"


class GoPackageShardTests(unittest.TestCase):
    def weights(self):
        costs = json.loads(Path(__file__).with_name("general-race-costs.json").read_text())
        return dict(costs["packages"], **SHARD.EXTRA_COSTS), costs["default_seconds"]

    def test_partitions_cover_every_package_once_and_heaviest_lead(self):
        weights, default = self.weights()
        packages = sorted(set(weights) | {P + f"new/pkg{i}" for i in range(3)})
        groups, totals = SHARD.partition(packages, 3, weights, default)
        self.assertEqual(sorted(p for g in groups for p in g), packages)
        self.assertEqual(groups[0][0], API)
        self.assertLessEqual(max(totals) - min(totals), max(weights.values()))

    def test_truncated_duplicate_or_foreign_listing_fails(self):
        good = [P + f"p{i}" for i in range(SHARD.MIN_PACKAGES)]
        for packages, count in [(good[:-1], 3), (good + [good[0]], 3), (good + ["other/x"], 3), (good, 0)]:
            with self.subTest(n=len(packages), count=count), self.assertRaises(ValueError):
                SHARD.partition(packages, count, {}, 150)

    def test_split_runs_every_api_test_exactly_once_and_never_the_whole_package(self):
        weights, default = self.weights()
        packages = sorted(set(weights) | {P + f"new/pkg{i}" for i in range(3)})
        tests, inventory = api_inventory()
        for count in (1, 2, 3, 5):
            with self.subTest(count=count):
                groups, totals, names, slices = SHARD.split_plan(packages, inventory, count, weights, default)
                self.assertEqual(len(groups), count)
                self.assertEqual(len(slices), count)
                # Every other package exactly once; internal/api never as a whole package.
                flat = [p for g in groups for p in g]
                self.assertEqual(sorted(flat), sorted(set(packages) - {API}))
                self.assertNotIn(API, flat)
                # Every top-level Test/Example/Fuzz exactly once; benchmarks never selected.
                selected = [t for s in slices for t in s]
                self.assertEqual(len(selected), len(set(selected)))
                self.assertEqual(sorted(selected), sorted(tests))
                self.assertEqual(sorted(names), sorted(tests))
                self.assertNotIn("BenchmarkRoute", selected)
                self.assertTrue(all(slices) and all(groups))
                # The API slices' weight is part of each shard's balance.
                self.assertTrue(all(t >= SHARD.EXTRA_COSTS[API] / count for t in totals))

    def test_split_fails_closed_without_api_package_or_inventory(self):
        weights, default = self.weights()
        packages = sorted(set(weights))
        _, inventory = api_inventory()
        for listed, listing, count in [
            ([p for p in packages if p != API], inventory, 3),  # package vanished from go list
            (packages, "ok  \t" + API + "\t0.1s\n", 3),          # empty test inventory
            (packages, "TestOnly\n", 2),                          # a shard would run nothing
            (packages, "TestA\nTestA\nTestB\n", 2),               # duplicate parent
        ]:
            with self.subTest(listing=listing[:20], count=count), self.assertRaises(ValueError):
                SHARD.split_plan(listed, listing, count, weights, default)

    def test_split_refuses_flags_that_would_change_the_selection(self):
        # -run would silently intersect with the slice; -shuffle needs the
        # whole package for order-dependence coverage (go-shuffle keeps it).
        for extra in (["-run", "X"], ["-run=X"], ["-shuffle=on"], ["-shuffle", "on"], ["-skip", "X"], ["-list", "."]):
            with self.subTest(extra=extra), patch.object(SHARD.subprocess, "run") as run, \
                    self.assertRaises(ValueError):
                SHARD.run(0, 3, ["-count=1", *extra], split_api=True)
            run.assert_not_called()

    def events(self, directory, lines):
        Path(directory, "go-test.jsonl").write_text("".join(json.dumps(e) + "\n" for e in lines))

    def test_api_execution_must_match_the_selected_slice(self):
        selected = ["TestA", "TestB", "ExampleC"]
        passed = [{"Package": API, "Test": t, "Action": "pass", "Elapsed": 1} for t in selected]
        subtest = [{"Package": API, "Test": "TestA/sub", "Action": "pass", "Elapsed": 1}]
        result = [{"Package": API, "Action": "pass", "Elapsed": 12.5}]
        with tempfile.TemporaryDirectory() as directory:
            self.events(directory, passed + subtest + result)
            self.assertEqual(SHARD.verify_api_execution(Path(directory), selected), 12.5)
            for bad in (passed[1:] + result,                     # a selected test never ran
                        passed + [dict(passed[0], Test="TestZ")] + result,  # an unselected one did
                        passed,                                  # no package result
                        passed + [dict(result[0], Action="fail")]):
                with self.subTest(bad=len(bad)):
                    self.events(directory, bad)
                    with self.assertRaises(ValueError):
                        SHARD.verify_api_execution(Path(directory), selected)


if __name__ == "__main__":
    unittest.main()
