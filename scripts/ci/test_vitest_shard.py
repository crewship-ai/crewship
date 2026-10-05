"""Fail-closed partition artifacts: no missing files, duplicate work or lost denominator."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import subprocess

SPEC = importlib.util.spec_from_file_location('vitest_shard', Path(__file__).with_name('vitest-shard.py'))
SHARD = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(SHARD)


class VitestPartitions(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.expected = ['a.test.ts', 'b.test.ts', 'c.test.ts', 'd.test.ts']
        for index, filename in enumerate(self.expected, 1):
            directory = self.root / f'shard-{index}'
            directory.mkdir()
            report = {'success': True, 'numTotalTests': 1, 'numPassedTests': 1,
                      'testResults': [{'name': str(self.root / filename), 'status': 'passed'}],
                      'coverageMap': {str(self.root / 'logic.ts'): {}, str(self.root / 'untested.ts'): {}}}
            (directory / 'report.json').write_text(json.dumps(report))
            (directory / 'blob.json').write_text('blob fixture')
            self.save_metadata(index)

    def save_metadata(self, index):
        directory = self.root / f'shard-{index}'
        files, coverage = SHARD.report_files(json.loads((directory / 'report.json').read_text()), self.root)
        metadata = {'index': index, 'count': 4, 'sha': 'source-sha', 'root': str(self.root),
                    'inventory': self.expected, 'files': files, 'coverage': coverage,
                    'counts': SHARD.test_counts(json.loads((directory / 'report.json').read_text())),
                    'blob_sha256': SHARD.digest(directory / 'blob.json'),
                    'report_sha256': SHARD.digest(directory / 'report.json')}
        (directory / 'metadata.json').write_text(json.dumps(metadata))

    def validate(self):
        return SHARD.validate(self.root, 4, self.expected, 'source-sha',
                              ['logic.ts', 'untested.ts'], merge_root=self.root)

    def change_metadata(self, **updates):
        path = self.root / 'shard-1/metadata.json'
        metadata = json.loads(path.read_text())
        metadata.update(updates)
        path.write_text(json.dumps(metadata))

    def test_complete_files_and_untested_coverage_are_preserved(self):
        blobs, coverage, counts = self.validate()
        self.assertEqual(len(blobs), 4)
        self.assertEqual(coverage, ['logic.ts', 'untested.ts'])
        self.assertEqual(counts, {'passed': 4, 'pending': 0, 'todo': 0})

    def test_missing_artifact_or_blob_fails(self):
        (self.root / 'shard-1/metadata.json').unlink()
        with self.assertRaises(ValueError):
            self.validate()
        self.save_metadata(1)
        (self.root / 'shard-1/blob.json').unlink()
        with self.assertRaises(FileNotFoundError):
            self.validate()

    def test_failed_rerun_invalidates_previous_pass_and_preserves_other_outputs(self):
        directory = self.root / 'shard-1'
        (directory / 'inventory.json').write_text('old inventory')
        (directory / 'coverage').mkdir()
        (directory / 'coverage/old.json').write_text('old coverage')
        (directory / 'unrelated.txt').write_text('keep local evidence')
        sibling_report = (self.root / 'shard-2/report.json').read_bytes()

        def fail_execution(*args):
            if args[0] == 'list':
                (directory / 'inventory.json').write_text(json.dumps([
                    {'file': str(Path.cwd() / name)} for name in self.expected
                ]))
                return
            raise subprocess.CalledProcessError(1, ['vitest', *args])

        with patch.object(SHARD, 'command', side_effect=fail_execution):
            with self.assertRaises(subprocess.CalledProcessError):
                SHARD.run(1, 4, directory, 'vitest.config.ts')
        for filename in ('metadata.json', 'blob.json', 'report.json', 'coverage'):
            self.assertFalse((directory / filename).exists(), filename)
        self.assertEqual((directory / 'unrelated.txt').read_text(), 'keep local evidence')
        self.assertEqual((self.root / 'shard-2/report.json').read_bytes(), sibling_report)
        with self.assertRaisesRegex(ValueError, 'Expected 4 partition artifacts, found 3'):
            self.validate()

    def test_failed_discovery_cleans_owned_outputs_without_following_coverage_symlink(self):
        directory = self.root / 'shard-1'
        (directory / 'inventory.json').write_text('old inventory')
        external = self.root / 'other-evidence'
        external.mkdir()
        (external / 'keep.json').write_text('preserve')
        (directory / 'coverage').symlink_to(external, target_is_directory=True)
        with patch.object(SHARD, 'command', side_effect=subprocess.CalledProcessError(1, ['vitest', 'list'])):
            with self.assertRaises(subprocess.CalledProcessError):
                SHARD.run(1, 4, directory, 'vitest.config.ts')
        for filename in ('metadata.json', 'blob.json', 'report.json', 'inventory.json', 'coverage'):
            self.assertFalse((directory / filename).exists(), filename)
        self.assertEqual((external / 'keep.json').read_text(), 'preserve')

    def test_source_count_inventory_and_duplicate_index_fail(self):
        for updates in [{'sha': 'other-source'}, {'count': 3}, {'index': 2}, {'index': 5},
                        {'inventory': self.expected[:-1]}]:
            self.save_metadata(1)
            self.change_metadata(**updates)
            with self.subTest(updates=updates), self.assertRaises(ValueError):
                self.validate()

    def test_foreign_checkout_paths_fail_before_native_blob_merge(self):
        self.change_metadata(root=str(self.root / 'foreign-checkout'))
        with self.assertRaisesRegex(ValueError, 'same checkout path'):
            self.validate()

    def test_individual_intentional_skips_remain_visible(self):
        path = self.root / 'shard-1/report.json'
        report = json.loads(path.read_text())
        report.update(numPendingTests=2, numTodoTests=1, numTotalTests=4)
        path.write_text(json.dumps(report))
        self.save_metadata(1)
        _, _, counts = self.validate()
        self.assertEqual(counts, {'passed': 4, 'pending': 2, 'todo': 1})
        self.change_metadata(counts={'passed': 99, 'pending': 0, 'todo': 0})
        with self.assertRaisesRegex(ValueError, 'counts differ'):
            self.validate()

    def test_changed_blob_or_report_fails(self):
        for filename in ['blob.json', 'report.json']:
            path = self.root / 'shard-1' / filename
            original = path.read_text()
            path.write_text(original + '\n')
            with self.subTest(filename=filename), self.assertRaises(ValueError):
                self.validate()
            path.write_text(original)

    def test_duplicate_missing_unexpected_test_files_fail(self):
        path = self.root / 'shard-1/report.json'
        for filename in ['b.test.ts', 'new.test.ts']:
            report = json.loads(path.read_text())
            report['testResults'][0]['name'] = str(self.root / filename)
            path.write_text(json.dumps(report))
            self.save_metadata(1)
            with self.subTest(filename=filename), self.assertRaises(ValueError):
                self.validate()

    def test_configured_coverage_baseline_is_required_in_each_partition(self):
        path = self.root / 'shard-1/report.json'
        report = json.loads(path.read_text())
        del report['coverageMap'][str(self.root / 'untested.ts')]
        path.write_text(json.dumps(report))
        self.save_metadata(1)
        with self.assertRaises(ValueError):
            self.validate()

    def test_baseline_cannot_disappear_from_every_partition(self):
        for index in range(1, 5):
            path = self.root / f'shard-{index}/report.json'
            report = json.loads(path.read_text())
            del report['coverageMap'][str(self.root / 'untested.ts')]
            path.write_text(json.dumps(report))
            self.save_metadata(index)
        with self.assertRaisesRegex(ValueError, 'lost configured coverage source baseline'):
            self.validate()

    def test_imported_nested_sources_are_preserved_in_full_union(self):
        for index, filename in [(2, 'nested/hooks/imported.ts'), (4, 'nested/lib/other.ts')]:
            path = self.root / f'shard-{index}/report.json'
            report = json.loads(path.read_text())
            report['coverageMap'][str(self.root / filename)] = {}
            path.write_text(json.dumps(report))
            self.save_metadata(index)
        _, coverage, _ = self.validate()
        self.assertEqual(coverage, ['logic.ts', 'nested/hooks/imported.ts', 'nested/lib/other.ts', 'untested.ts'])

    def test_empty_failed_and_uncovered_reports_fail(self):
        report = json.loads((self.root / 'shard-1/report.json').read_text())
        for updates in [{'success': False}, {'numPassedTests': 0, 'numPendingTests': 1}, {'coverageMap': None},
                        {'coverageMap': {}}, {'testResults': [{'name': str(self.root / 'a.test.ts'), 'status': 'failed'}]}]:
            with self.subTest(updates=updates), self.assertRaises(ValueError):
                SHARD.report_files({**report, **updates}, self.root)

    def test_inventory_uses_vitest_discovery_and_rejects_duplicates(self):
        path = self.root / 'inventory.json'
        path.write_text(json.dumps([{'file': str(self.root / name)} for name in self.expected]))
        self.assertEqual(SHARD.inventory(path, self.root), self.expected)
        for values in [[], [{'file': str(self.root / 'a.test.ts')}] * 2,
                       [{'file': str(self.root / 'a.test.ts'), 'projectName': 'new-project'}]]:
            path.write_text(json.dumps(values))
            with self.subTest(values=values), self.assertRaises(ValueError):
                SHARD.inventory(path, self.root)
