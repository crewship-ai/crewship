import copy
import hashlib
import json
import tempfile
from unittest.mock import patch
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('promote', Path(__file__).with_name('promote.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class PromotionTests(unittest.TestCase):
    def setUp(self):
        self.identity = {'runId': 'run-1', 'caseSourceSha256': 'harness-1', 'engineBinarySha256': 'actual-helper'}
        self.cases = [{'id': 'text.insert', 'group': 'text-enter', 'params': {'text': 'Z'}, 'parameterSchemaSha256': 'schema', 'effectOracle': 'text.insert'}]
        self.rows = [{'id': 'text.insert', 'group': 'text-enter', 'params': {'text': 'Z'}, 'parameterSchemaSha256': 'schema', 'test': 'ce_text_insert', 'passed': True, 'checks': dict.fromkeys(module.CHECKS, True), 'reason': None, 'executionIdentity': self.identity, 'actualEngineBinarySha256': 'actual-helper', 'invocationId': 'invocation-1'}]

    def test_requires_every_invariant_and_matching_fixture(self):
        self.assertEqual(list(module.checked_rows(self.cases, self.rows, self.identity, 'invocation-1')), ['text.insert'])
        for check in module.CHECKS:
            rows = copy.deepcopy(self.rows)
            rows[0]['checks'][check] = False
            with self.assertRaisesRegex(ValueError, 'incomplete'):
                module.checked_rows(self.cases, rows, self.identity, 'invocation-1')
        rows = copy.deepcopy(self.rows)
        rows[0]['params'] = {'text': 'changed'}
        with self.assertRaisesRegex(ValueError, 'mismatch'):
            module.checked_rows(self.cases, rows, self.identity, 'invocation-1')

    def test_missing_duplicate_and_unexpected_rows_cannot_promote(self):
        for rows in [[], self.rows * 2, [{**self.rows[0], 'id': 'file.open'}]]:
            with self.assertRaisesRegex(ValueError, 'inventory'):
                module.checked_rows(self.cases, rows, self.identity, 'invocation-1')

    def test_unsupported_command_cannot_be_qualified_by_forged_success(self):
        cases = [{**self.cases[0], 'id': 'file.open', 'effectOracle': 'file.open'}]
        rows = [{**self.rows[0], 'id': 'file.open', 'test': 'ce_file_open'}]
        with self.assertRaisesRegex(ValueError, 'admission domain'):
            module.checked_rows(cases, rows, self.identity, 'invocation-1')

    def test_failed_rows_do_not_enable_commands(self):
        rows = [{**self.rows[0], 'passed': False, 'reason': 'command_denied'}]
        self.assertEqual(module.checked_rows(self.cases, rows, self.identity, 'invocation-1'), {})


    def test_old_harness_or_run_cannot_supply_new_qualification(self):
        for change in [{'runId': 'run-2'}, {'caseSourceSha256': 'harness-2'}, {'engineBinarySha256': 'another-helper'}]:
            expected = {**self.identity, **change}
            with self.assertRaisesRegex(ValueError, 'execution identity'):
                module.checked_rows(self.cases, self.rows, expected, 'invocation-1')
        stale = copy.deepcopy(self.rows)
        del stale[0]['executionIdentity']
        with self.assertRaisesRegex(ValueError, 'execution identity'):
            module.checked_rows(self.cases, stale, self.identity, 'invocation-1')

    def test_old_invocation_cannot_supply_new_qualification(self):
        with self.assertRaisesRegex(ValueError, 'execution identity'):
            module.checked_rows(self.cases, self.rows, self.identity, 'invocation-2')

    def test_modified_original_runtime_cannot_be_promoted(self):
        with tempfile.TemporaryDirectory() as directory:
            prepared = Path(directory)
            runtime = prepared / 'mooring/original.rs'
            runtime.parent.mkdir()
            raw = b'original runtime'
            runtime.write_bytes(raw)
            manifest = prepared / 'source-manifest.json'
            manifest.write_text(json.dumps({'mooring/original.rs': module.digest(runtime)}))
            identity = {'runtimeSourceManifestSha256': module.digest(manifest)}
            blob = hashlib.sha1(f'blob {len(raw)}\0'.encode() + raw).hexdigest()
            with patch.object(module.subprocess, 'check_output', side_effect=[module.C1_SHA, (blob + ' mooring/original.rs\0').encode()]):
                module.verify_runtime(prepared, prepared, identity)
            runtime.write_bytes(b'changed runtime')
            with patch.object(module.subprocess, 'check_output', side_effect=[module.C1_SHA, (blob + ' mooring/original.rs\0').encode()]):
                with self.assertRaisesRegex(ValueError, 'runtime source drift'):
                    module.verify_runtime(prepared, prepared, identity)

    def test_declaration_does_not_widen_the_tested_parameter(self):
        schema = module.tested_schema({'name': 'Arial'})
        self.assertEqual(schema['properties']['name'], {'type': 'string', 'enum': ['Arial']})
        self.assertEqual(schema['required'], ['name'])
        self.assertFalse(schema['additionalProperties'])


if __name__ == '__main__':
    unittest.main()
