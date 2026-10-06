"""Package partitions cover the listing exactly once and refuse truncation."""
import importlib.util
import json
from pathlib import Path
import unittest

SPEC = importlib.util.spec_from_file_location("go_package_shard", Path(__file__).with_name("go-package-shard.py"))
SHARD = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(SHARD)
P = SHARD.PREFIX


class GoPackageShardTests(unittest.TestCase):
    def test_partitions_cover_every_package_once_and_heaviest_lead(self):
        costs = json.loads(Path(__file__).with_name("general-race-costs.json").read_text())
        weights = dict(costs["packages"], **SHARD.EXTRA_COSTS)
        packages = sorted(set(weights) | {P + f"new/pkg{i}" for i in range(3)})
        groups, totals = SHARD.partition(packages, 3, weights, costs["default_seconds"])
        self.assertEqual(sorted(p for g in groups for p in g), packages)
        self.assertEqual(groups[0][0], P + "internal/api")
        self.assertLessEqual(max(totals) - min(totals), max(weights.values()))

    def test_truncated_duplicate_or_foreign_listing_fails(self):
        good = [P + f"p{i}" for i in range(SHARD.MIN_PACKAGES)]
        for packages, count in [(good[:-1], 3), (good + [good[0]], 3), (good + ["other/x"], 3), (good, 0)]:
            with self.subTest(n=len(packages), count=count), self.assertRaises(ValueError):
                SHARD.partition(packages, count, {}, 150)


if __name__ == "__main__":
    unittest.main()
