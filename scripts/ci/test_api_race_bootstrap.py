"""The shared fixture bootstrap remains measured and bound to every shard."""
import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location('race_bootstrap', Path(__file__).with_name('api-race-shard.py'))
runner = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(runner)
IDENTITY = {'GITHUB_SHA': 'a' * 40, 'GITHUB_RUN_ID': '123', 'GITHUB_RUN_ATTEMPT': '2'}


def digest(value):
    return hashlib.sha256(value).hexdigest()


class BootstrapEvidence(unittest.TestCase):
    def bundle(self, directory):
        database = b'Synthetic private SQLite fixture bytes; Go tests verify real import.'
        (directory / 'template.db').write_bytes(database)
        metadata = {'version': 1, 'source_sha': 'a' * 40, 'run_id': '123', 'run_attempt': '1',
                    'migration_fingerprint': 'b' * 64, 'fixture_key': 'b' * 64 + '-' + 'c' * 32,
                    'go_version': 'go1.27.3', 'goos': 'linux', 'goarch': 'amd64', 'race': True,
                    'template_size': len(database), 'template_sha256': digest(database)}
        raw_metadata = json.dumps(metadata).encode()
        (directory / 'template-metadata.json').write_bytes(raw_metadata)
        events = [{'Action': 'run', 'Package': runner.BOOTSTRAP_PACKAGE, 'Test': runner.BOOTSTRAP_TEST},
                  {'Action': 'pass', 'Package': runner.BOOTSTRAP_PACKAGE, 'Test': runner.BOOTSTRAP_TEST, 'Elapsed': 8},
                  {'Action': 'pass', 'Package': runner.BOOTSTRAP_PACKAGE, 'Elapsed': 10}]
        raw_events = ('\n'.join(map(json.dumps, events)) + '\n').encode()
        (directory / 'go-test.jsonl').write_bytes(raw_events)
        receipt = {'version': 1, 'package': runner.BOOTSTRAP_PACKAGE, 'test': runner.BOOTSTRAP_TEST,
                   'source_sha': 'a' * 40, 'run_id': '123', 'run_attempt': '1', 'seconds': 10,
                   'race_gcflags': runner.RACE_GCFLAGS, 'metadata_sha256': digest(raw_metadata),
                   'events_sha256': digest(raw_events)}
        (directory / 'bootstrap-receipt.json').write_text(json.dumps(receipt))
        return metadata, events, receipt

    def rewrite(self, directory, metadata, events, receipt):
        raw_metadata = json.dumps(metadata).encode()
        raw_events = ('\n'.join(map(json.dumps, events)) + '\n').encode()
        (directory / 'template-metadata.json').write_bytes(raw_metadata)
        (directory / 'go-test.jsonl').write_bytes(raw_events)
        receipt['metadata_sha256'] = digest(raw_metadata)
        receipt['events_sha256'] = digest(raw_events)
        (directory / 'bootstrap-receipt.json').write_text(json.dumps(receipt))

    def test_bootstrap_includes_package_setup_and_allows_prior_attempt_same_run(self):
        with tempfile.TemporaryDirectory() as temp, patch.dict(os.environ, IDENTITY):
            directory = Path(temp)
            self.bundle(directory)
            seconds, reference = runner.validate_bootstrap(directory)
            self.assertEqual(seconds, 10)  # package elapsed, not the parent-only 8
            self.assertEqual(reference, digest((directory / 'bootstrap-receipt.json').read_bytes()))

    def test_failed_skipped_duplicate_missing_wrong_package_and_forged_elapsed_fail(self):
        for bad in ['skip', 'child-skip', 'fail', 'duplicate', 'missing', 'package', 'elapsed', 'nan', 'bool']:
            with self.subTest(bad=bad), tempfile.TemporaryDirectory() as temp, patch.dict(os.environ, IDENTITY):
                directory = Path(temp)
                metadata, events, receipt = self.bundle(directory)
                if bad in ('skip', 'fail'): events[1]['Action'] = bad
                elif bad == 'child-skip': events.insert(1, {'Action': 'skip', 'Package': runner.BOOTSTRAP_PACKAGE, 'Test': runner.BOOTSTRAP_TEST + '/migration'})
                elif bad == 'duplicate': events.insert(2, copy.deepcopy(events[1]))
                elif bad == 'missing': events.pop(1)
                elif bad == 'package': events[1]['Package'] = runner.MODULE + '/internal/api'
                elif bad == 'elapsed': receipt['seconds'] = 8
                elif bad == 'nan': events[-1]['Elapsed'] = receipt['seconds'] = float('nan')
                elif bad == 'bool': events[-1]['Elapsed'] = receipt['seconds'] = True
                self.rewrite(directory, metadata, events, receipt)
                with self.assertRaises(ValueError): runner.validate_bootstrap(directory)

    def test_identity_hash_race_and_symlink_fail_closed(self):
        for bad in ['sha', 'run', 'future-attempt', 'race', 'database', 'metadata', 'events', 'symlink']:
            with self.subTest(bad=bad), tempfile.TemporaryDirectory() as temp, patch.dict(os.environ, IDENTITY):
                directory = Path(temp)
                metadata, events, receipt = self.bundle(directory)
                if bad == 'sha': metadata['source_sha'] = receipt['source_sha'] = 'd' * 40
                elif bad == 'run': metadata['run_id'] = receipt['run_id'] = '124'
                elif bad == 'future-attempt': metadata['run_attempt'] = receipt['run_attempt'] = '3'
                elif bad == 'race': metadata['race'] = False
                self.rewrite(directory, metadata, events, receipt)
                if bad in ('database', 'metadata', 'events'):
                    name = {'database': 'template.db', 'metadata': 'template-metadata.json', 'events': 'go-test.jsonl'}[bad]
                    with (directory / name).open('ab') as output: output.write(b'corrupt')
                elif bad == 'symlink':
                    (directory / 'template.db').rename(directory / 'other.db')
                    (directory / 'template.db').symlink_to('other.db')
                with self.assertRaises((ValueError, json.JSONDecodeError)): runner.validate_bootstrap(directory)

    def test_aggregate_cannot_omit_bootstrap_or_mix_shard_references(self):
        names, partitions = runner.partition('\n'.join('TestRow' + str(i) for i in range(8)), 2)
        manifests = [{'index': i, 'count': 2, 'inventory_count': 8, 'inventory_sha256': runner.fingerprint(names),
                      'selected': selected, 'seconds': 15, 'package': runner.PACKAGE,
                      'source_sha': 'a' * 40, 'bootstrap_sha256': 'f' * 64} for i, selected in enumerate(partitions)]
        self.assertEqual(runner.validate_manifests(manifests, 2, 'a' * 40, 'f' * 64), (30, 8))
        with self.assertRaises(ValueError): runner.validate_manifests(manifests, 2, 'a' * 40)
        for value in [None, 'e' * 64]:
            bad = copy.deepcopy(manifests)
            bad[0]['bootstrap_sha256'] = value
            with self.assertRaises(ValueError): runner.validate_manifests(bad, 2, 'a' * 40, 'f' * 64)

    def test_main_budget_counts_bootstrap_once_and_refuses_missing_bundle(self):
        with tempfile.TemporaryDirectory() as temp, patch.dict(os.environ, IDENTITY | {'GITHUB_EVENT_NAME': 'push', 'GITHUB_STEP_SUMMARY': os.devnull}):
            root = Path(temp)
            bundle = root / 'bundle'
            bundle.mkdir()
            self.bundle(bundle)
            _, reference = runner.validate_bootstrap(bundle)
            names, partitions = runner.partition('TestOne\nTestTwo\nTestThree\nTestFour', 2)
            for i, selected in enumerate(partitions):
                directory = root / str(i)
                directory.mkdir()
                manifest = {'index': i, 'count': 2, 'inventory_count': 4, 'inventory_sha256': runner.fingerprint(names),
                            'selected': selected, 'seconds': 15, 'package': runner.PACKAGE,
                            'source_sha': 'a' * 40, 'bootstrap_sha256': reference}
                (directory / 'api-race-shard.json').write_text(json.dumps(manifest))
            # 30 shard seconds alone fit under 32; the measured bootstrap 10 must fail it.
            self.assertEqual(runner.report(root, 2, 20, bundle), 1)
            with self.assertRaises(ValueError): runner.report(root, 2, 20)


if __name__ == '__main__':
    unittest.main()
