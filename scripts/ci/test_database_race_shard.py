"""Exercise real shard selection with a deterministic Go command substitute."""
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class DatabaseRaceShards(unittest.TestCase):
    def run_shard(self, index, count, inventory, list_exit=0, test_exit=0):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            fake = root / 'go'
            fake.write_text('''#!/usr/bin/env python3
import json, os, sys
if '-list' in sys.argv:
    print(os.environ['INVENTORY'])
    sys.exit(int(os.environ['LIST_EXIT']))
with open(os.environ['ARGS_FILE'], 'w') as f:
    json.dump(sys.argv[1:], f)
print(json.dumps({'Action': 'pass', 'Package': 'database', 'Elapsed': 1}))
sys.exit(int(os.environ['TEST_EXIT']))
''')
            fake.chmod(0o755)
            args_file = root / 'args.json'
            env = dict(os.environ, PATH=f'{root}:' + os.environ['PATH'],
                       INVENTORY=inventory, LIST_EXIT=str(list_exit),
                       TEST_EXIT=str(test_exit), ARGS_FILE=str(args_file),
                       CI_RESULTS_DIR=str(root / 'results'))
            result = subprocess.run(['bash', str(ROOT / 'scripts/ci/database-race-shard.sh'),
                                     str(index), str(count)], env=env, capture_output=True, text=True)
            args = json.loads(args_file.read_text()) if args_file.exists() else None
            return result, args

    def test_partitions_cover_every_parent_exactly_once(self):
        names = [f'TestMigration{i:03}' for i in range(81)] + ['ExampleUpgrade', 'FuzzSQL', 'TestČeský']
        inventory = '\n'.join(reversed(names)) + '\nok database 0.1s\n'
        covered = []
        for index in range(4):
            result, args = self.run_shard(index, 4, inventory)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn('-race', args)
            self.assertIn('-count=1', args)
            pattern = args[args.index('-run') + 1]
            selected = [name for name in names if re.fullmatch(pattern, name)]
            self.assertEqual(len(selected), 21)
            self.assertFalse(re.fullmatch(pattern, 'TestMigration000Extra'))
            covered.extend(selected)
        self.assertCountEqual(covered, names)

    def test_enumeration_failure_cannot_run_partial_inventory(self):
        result, args = self.run_shard(0, 4, 'TestPartial', list_exit=7)
        self.assertEqual(result.returncode, 7)
        self.assertIsNone(args)

    def test_empty_or_invalid_shards_fail(self):
        for index, count, inventory in [(0, 4, 'ok database'), (3, 4, 'TestOne'),
                                         (4, 4, 'TestOne'), ('bad', 4, 'TestOne')]:
            with self.subTest(index=index, count=count):
                result, args = self.run_shard(index, count, inventory)
                self.assertNotEqual(result.returncode, 0)
                self.assertIsNone(args)

    def test_test_failure_is_preserved(self):
        result, _ = self.run_shard(0, 1, 'TestFailure', test_exit=9)
        self.assertEqual(result.returncode, 9)


if __name__ == '__main__':
    unittest.main()
