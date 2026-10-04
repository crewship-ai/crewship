"""Partition completeness, real runner evidence, failure propagation and budgets."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / 'scripts/ci/api-race-shard.py'
spec = importlib.util.spec_from_file_location('api_race_shard', SCRIPT)
shard = importlib.util.module_from_spec(spec)
spec.loader.exec_module(shard)


class APIRaceShards(unittest.TestCase):
    def manifests(self, names=None):
        names = names or [f'TestRoute{i:03}' for i in range(81)] + ['ExampleRequest', 'FuzzRoute', 'TestČeský']
        inventory, partitions = shard.partition('\n'.join(reversed(names)) + '\nok package 0.1s\n', 4)
        return [{'index': i, 'count': 4, 'inventory_count': len(inventory),
                 'inventory_sha256': shard.fingerprint(inventory), 'selected': selected,
                 'seconds': 10} for i, selected in enumerate(partitions)]

    def test_complete_disjoint_inventory_includes_new_tests_and_unicode(self):
        manifests = self.manifests()
        self.assertEqual(shard.validate_manifests(manifests, 4), (40, 84))
        for item in manifests:
            pattern = shard.selection_pattern(item['selected'])
            self.assertTrue(all(re.fullmatch(pattern, name) for name in item['selected']))
            self.assertFalse(re.fullmatch(pattern, item['selected'][0] + 'Extra'))
        expanded = self.manifests([f'TestNew{i}' for i in range(89)])
        self.assertEqual(shard.validate_manifests(expanded, 4)[1], 89)

    def test_invalid_empty_duplicate_inventory_and_argv_growth_fail(self):
        for inventory, count in [('', 4), ('TestOne', 4), ('TestOne\nTestOne', 1), ('TestOne', 0)]:
            with self.assertRaises(ValueError):
                shard.partition(inventory, count)
        names = ['Test' + 'a' * 40 + str(i) for i in range(7612)]
        _, two = shard.partition('\n'.join(names), 2)
        with self.assertRaisesRegex(ValueError, 'argv'):
            shard.selection_pattern(two[0])
        _, four = shard.partition('\n'.join(names), 4)
        self.assertTrue(all(len(shard.selection_pattern(part).encode()) < shard.MAX_PATTERN_BYTES for part in four))

    def test_aggregate_rejects_missing_duplicate_overlap_and_partial_evidence(self):
        good = self.manifests()
        cases = [good[:-1], good + [good[0]]]
        for mutation in ['index', 'overlap', 'missing', 'hash', 'count', 'duration']:
            items = copy.deepcopy(good)
            if mutation == 'index': items[0]['index'] = 1
            if mutation == 'overlap': items[0]['selected'][0] = items[1]['selected'][0]
            if mutation == 'missing': items[0]['selected'].pop()
            if mutation == 'hash': items[0]['inventory_sha256'] = 'bad'
            if mutation == 'count': items[0]['count'] = 2
            if mutation == 'duration': items[0]['seconds'] = float('nan')
            cases.append(items)
        for items in cases:
            with self.subTest(items=items), self.assertRaises(ValueError):
                shard.validate_manifests(items, 4)

    def invoke(self, inventory='TestOne\nTestTwo\nTestThree\nTestFour', list_exit=0, test_exit=0, omit=False):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            fake = root / 'go'
            fake.write_text('''#!/usr/bin/env python3
import json, os, re, sys
if '-list' in sys.argv:
    assert '-race' in sys.argv
    print(os.environ['INVENTORY'])
    sys.exit(int(os.environ['LIST_EXIT']))
assert '-race' in sys.argv and '-count=1' in sys.argv
assert sys.argv[sys.argv.index('-timeout') + 1] == '4600s'
pattern = sys.argv[sys.argv.index('-run') + 1]
package = 'github.com/crewship-ai/crewship/internal/api'
selected = [n for n in os.environ['INVENTORY'].splitlines() if re.fullmatch(pattern, n)]
if os.environ['OMIT'] == 'true': selected = selected[:-1]
for name in selected:
    print(json.dumps({'Package': package, 'Test': name, 'Action': 'run'}))
    print(json.dumps({'Package': package, 'Test': name, 'Action': 'pass', 'Elapsed': .1}))
print(json.dumps({'Package': package, 'Action': 'pass', 'Elapsed': 1.5}))
sys.exit(int(os.environ['TEST_EXIT']))
''')
            fake.chmod(0o755)
            results = root / 'results'
            results.mkdir()
            (results / 'api-race-shard.json').write_text('{"stale_success": true}')
            env = dict(os.environ, PATH=f'{root}:' + os.environ['PATH'], INVENTORY=inventory,
                       LIST_EXIT=str(list_exit), TEST_EXIT=str(test_exit), OMIT=str(omit).lower(),
                       CI_RESULTS_DIR=str(results), GITHUB_STEP_SUMMARY=str(root / 'summary'))
            result = subprocess.run(['python3', str(SCRIPT), 'run', '0', '4', '4600'], env=env,
                                    capture_output=True, text=True)
            manifest = results / 'api-race-shard.json'
            return result, json.loads(manifest.read_text()) if manifest.exists() else None

    def test_real_wrapper_records_selected_execution(self):
        result, manifest = self.invoke()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(manifest['seconds'], 1.5)
        self.assertEqual(manifest['selected'], ['TestFour'])

    def test_enumeration_failure_test_failure_and_unexecuted_tests_fail(self):
        for args in [{'list_exit': 7}, {'test_exit': 9}, {'omit': True}]:
            result, manifest = self.invoke(**args)
            self.assertNotEqual(result.returncode, 0)
            self.assertIsNone(manifest)
        result, _ = self.invoke(test_exit=9)
        self.assertEqual(result.returncode, 9)

    def test_budget_is_summed_and_main_only_failure_preserved(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            for manifest in self.manifests():
                directory = root / str(manifest['index'])
                directory.mkdir()
                (directory / 'api-race-shard.json').write_text(json.dumps(manifest))
            for event, expected in [('pull_request', 0), ('merge_group', 0), ('push', 1)]:
                with patch.dict(os.environ, GITHUB_EVENT_NAME=event, GITHUB_STEP_SUMMARY=os.devnull):
                    self.assertEqual(shard.report(root, 4, 20), expected)
            with patch.dict(os.environ, GITHUB_EVENT_NAME='push', GITHUB_STEP_SUMMARY=os.devnull):
                self.assertEqual(shard.report(root, 4, 100), 0)


if __name__ == '__main__':
    unittest.main()
