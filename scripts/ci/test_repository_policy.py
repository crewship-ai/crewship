"""Preservation and stale-plan boundaries for the explicit policy updater."""
from copy import deepcopy
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('policy', Path(__file__).with_name('repository-policy.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


def fixture():
    return {'id': 7, 'source_type': 'Repository', 'source': 'test/repo',
            'name': 'main', 'target': 'branch', 'enforcement': 'active',
            'conditions': {'ref_name': {'include': ['refs/heads/main'], 'exclude': []}},
            'bypass_actors': [{'actor_type': 'RepositoryRole', 'actor_id': 5, 'bypass_mode': 'pull_request'}],
            'rules': [
                {'type': 'deletion'}, {'type': 'required_linear_history'},
                {'type': 'pull_request', 'parameters': {
                    'required_approving_review_count': 2, 'dismiss_stale_reviews_on_push': True,
                    'require_last_push_approval': True, 'required_review_thread_resolution': True,
                    'allowed_merge_methods': ['merge', 'squash'], 'require_code_owner_review': True}},
                {'type': 'required_status_checks', 'parameters': {
                    'strict_required_status_checks_policy': True, 'do_not_enforce_on_create': False,
                    'required_status_checks': [{'context': name, 'integration_id': 15368} for name in sorted(m.CHECKS)]}},
                {'type': 'commit_message_pattern', 'parameters': {'pattern': 'future', 'operator': 'contains'}}]}


class PolicyTests(unittest.TestCase):
    def test_preserves_all_existing_policy_and_input(self):
        before = fixture()
        original = deepcopy(before)
        result = m.proposed(before)
        self.assertEqual(before, original)
        self.assertEqual(result['rules'][:len(before['rules'])], before['rules'])
        for field in m.FIELDS[:-1]:
            self.assertEqual(result[field], before[field])
        self.assertEqual(result['rules'][-2]['parameters'], m.QUEUE)
        self.assertEqual(m.proposed(result), result)

    def test_preserves_stricter_codeql_and_other_tools(self):
        for security in ('medium_or_higher', 'all'):
            before = fixture()
            rule = {'type': 'code_scanning', 'parameters': {'code_scanning_tools': [
                {'tool': 'CodeQL', 'alerts_threshold': 'all', 'security_alerts_threshold': security},
                {'tool': 'Other scanner', 'alerts_threshold': 'errors', 'security_alerts_threshold': 'critical'}]}}
            before['rules'].append(rule)
            self.assertEqual(m.proposed(before)['rules'][-2], rule)

    def test_raises_weak_codeql_threshold_without_relaxing_quality(self):
        before = fixture()
        before['rules'].append({'type': 'code_scanning', 'parameters': {'code_scanning_tools': [
            {'tool': 'CodeQL', 'alerts_threshold': 'errors', 'security_alerts_threshold': 'critical'}]}})
        result = m.proposed(before)
        tool = next(r for r in result['rules'] if r['type'] == 'code_scanning')['parameters']['code_scanning_tools'][0]
        self.assertEqual(tool['security_alerts_threshold'], 'high_or_higher')
        self.assertEqual(tool['alerts_threshold'], 'errors')

    def test_invalid_target_missing_checks_and_weakened_review_fail(self):
        for mutation in ('target', 'conditions', 'checks', 'review', 'duplicate'):
            before = fixture()
            if mutation == 'target': before['target'] = 'tag'
            if mutation == 'conditions': before['conditions']['ref_name']['exclude'] = ['refs/heads/main']
            if mutation == 'checks': before['rules'][3]['parameters']['required_status_checks'].pop()
            if mutation == 'review': before['rules'][2]['parameters']['require_last_push_approval'] = False
            if mutation == 'duplicate': before['rules'].append(deepcopy(before['rules'][0]))
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                m.proposed(before)

    def test_stale_live_policy_never_writes(self):
        before = fixture()
        plan = m.make_plan(before, 'test/repo', 7, False)
        current = deepcopy(before)
        current['bypass_actors'].append({'actor_type': 'User', 'actor_id': 123, 'bypass_mode': 'always'})
        calls = []
        def api(endpoint, payload=None):
            calls.append((endpoint, payload))
            return {'status': 'ahead'} if '/compare/' in endpoint else current
        with self.assertRaisesRegex(ValueError, 'changed since planning'):
            m.apply_plan(plan, 'a' * 40, api)
        self.assertTrue(all(payload is None for _, payload in calls))

    def test_apply_reads_current_then_preserves_payload_and_checks_response(self):
        before = fixture()
        plan = m.make_plan(before, 'test/repo', 7, False)
        calls = []
        def api(endpoint, payload=None):
            calls.append((endpoint, payload))
            if '/compare/' in endpoint: return {'status': 'identical'}
            return before if payload is None else {**before, **payload}
        self.assertTrue(m.apply_plan(plan, 'a' * 40, api)['changed'])
        self.assertEqual(calls[-1][1], plan['proposed'])
        self.assertEqual(len(calls), 3)

    def test_tampered_plan_and_non_main_documentation_never_write(self):
        for kind in ('tamper', 'documentation'):
            plan = m.make_plan(fixture(), 'test/repo', 7, False)
            if kind == 'tamper': plan['proposed']['bypass_actors'] = []
            def api(endpoint, payload=None):
                self.assertIsNone(payload)
                return {'status': 'diverged'}
            with self.subTest(kind=kind), self.assertRaises(ValueError):
                m.apply_plan(plan, 'a' * 40, api)

    def test_fingerprint_ignores_read_metadata_but_preserves_rule_order(self):
        before = fixture()
        old = m.digest(m.shape(before))
        before['updated_at'] = 'later'
        self.assertEqual(m.digest(m.shape(before)), old)
        before['rules'].reverse()
        self.assertNotEqual(m.digest(m.shape(before)), old)

    def test_security_only_never_changes_queue_or_requires_documentation(self):
        for has_queue in (False, True):
            before = fixture()
            if has_queue:
                before['rules'].append({'type': 'merge_queue', 'parameters': {'future': 'preserve'}})
            plan = m.make_plan(before, 'test/repo', 7, False, code_scanning_only=True)
            self.assertEqual(plan['proposed']['rules'][:-1], before['rules'])
            calls = []
            def api(endpoint, payload=None):
                calls.append((endpoint, payload))
                self.assertNotIn('/compare/', endpoint)
                return before if payload is None else {**before, **payload}
            self.assertTrue(m.apply_plan(plan, None, api)['changed'])
            self.assertEqual(len(calls), 2)

    def test_security_only_scope_cannot_be_forged(self):
        plan = m.make_plan(fixture(), 'test/repo', 7, False)
        plan['code_scanning_only'] = True
        with self.assertRaisesRegex(ValueError, 'differs'):
            m.apply_plan(plan, None, lambda *args: self.fail('unexpected request'))
