"""Preservation and stale-plan boundaries for the explicit policy updater."""
from copy import deepcopy
from contextlib import redirect_stderr
import io
import json
import importlib.util
from pathlib import Path
import unittest
from tempfile import TemporaryDirectory
from unittest.mock import patch

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


def comparison(endpoint, status='ahead'):
    return {'status': status, 'base_commit': {'sha': endpoint.split('/compare/')[1].split('...')[0]}}


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

    def test_preserves_owner_and_stricter_review_settings_in_both_scopes_and_apply(self):
        for count, last_push in ((0, False), (2, True)):
            for security_only in (False, True):
                with self.subTest(count=count, last_push=last_push, security_only=security_only):
                    before = fixture()
                    review = before['rules'][2]['parameters']
                    review.update(required_approving_review_count=count, require_last_push_approval=last_push,
                                  require_code_owner_review=False,
                                  require_extra_approval_for_unattributed_changes=True,
                                  required_reviewers=[], future_setting={'preserve': True})
                    original = deepcopy(before)
                    plan = m.make_plan(before, 'test/repo', 7, False, code_scanning_only=security_only)
                    self.assertEqual(plan['proposed']['rules'][2], before['rules'][2])
                    self.assertEqual(before, original)
                    calls = []
                    def api(endpoint, payload=None):
                        calls.append((endpoint, payload))
                        if '/compare/' in endpoint: return comparison(endpoint, 'identical')
                        return before if payload is None else {**before, **payload}
                    m.apply_plan(plan, None if security_only else 'd' * 40, 'a' * 40, api)
                    written = next(payload for _, payload in calls if payload is not None)
                    self.assertEqual(written['rules'][2], original['rules'][2])
                    self.assertEqual(written['rules'][3], original['rules'][3])

    def test_missing_or_malformed_pull_request_rule_fails_closed(self):
        variants = [('required_approving_review_count', value) for value in (-1, True, False, 1.5, '0', None)]
        variants += [(flag, value) for flag in ('dismiss_stale_reviews_on_push', 'require_last_push_approval',
                                              'required_review_thread_resolution', 'require_code_owner_review',
                                              'require_extra_approval_for_unattributed_changes')
                     for value in (0, 1, 'false', None)]
        for flag, value in variants:
            before = fixture(); before['rules'][2]['parameters'][flag] = value
            with self.subTest(flag=flag, value=value), self.assertRaises(ValueError):
                m.proposed(before)
        for mutation in ('missing_rule', 'missing_count', 'missing_flag', 'parameters_not_object'):
            before = fixture()
            if mutation == 'missing_rule': before['rules'].pop(2)
            elif mutation == 'missing_count': del before['rules'][2]['parameters']['required_approving_review_count']
            elif mutation == 'missing_flag': del before['rules'][2]['parameters']['require_last_push_approval']
            else: before['rules'][2]['parameters'] = None
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                m.proposed(before)

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

    def test_invalid_target_missing_checks_and_malformed_review_fail(self):
        for mutation in ('target', 'conditions', 'checks', 'review', 'duplicate'):
            before = fixture()
            if mutation == 'target': before['target'] = 'tag'
            if mutation == 'conditions': before['conditions']['ref_name']['exclude'] = ['refs/heads/main']
            if mutation == 'checks': before['rules'][3]['parameters']['required_status_checks'].pop()
            if mutation == 'review': before['rules'][2]['parameters']['require_last_push_approval'] = 'false'
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
            return comparison(endpoint) if '/compare/' in endpoint else current
        with self.assertRaisesRegex(ValueError, 'changed since planning'):
            m.apply_plan(plan, 'd' * 40, 'a' * 40, api)
        self.assertTrue(all(payload is None for _, payload in calls))

    def test_apply_reads_current_then_preserves_payload_and_checks_response(self):
        before = fixture()
        plan = m.make_plan(before, 'test/repo', 7, False)
        calls = []
        def api(endpoint, payload=None):
            calls.append((endpoint, payload))
            if '/compare/' in endpoint: return comparison(endpoint, 'identical')
            return before if payload is None else {**before, **payload}
        self.assertTrue(m.apply_plan(plan, 'd' * 40, 'a' * 40, api)['changed'])
        self.assertEqual(calls[-1][1], plan['proposed'])
        self.assertEqual(len(calls), 4)

    def test_tampered_plan_and_non_main_documentation_never_write(self):
        for kind in ('tamper', 'documentation'):
            plan = m.make_plan(fixture(), 'test/repo', 7, False)
            if kind == 'tamper': plan['proposed']['bypass_actors'] = []
            def api(endpoint, payload=None):
                self.assertIsNone(payload)
                return comparison(endpoint, 'ahead' if '/compare/' + 'a' * 40 in endpoint else 'diverged')
            with self.subTest(kind=kind), self.assertRaises(ValueError):
                m.apply_plan(plan, 'd' * 40, 'a' * 40, api)

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
                if '/compare/' in endpoint:
                    return comparison(endpoint)
                return before if payload is None else {**before, **payload}
            self.assertTrue(m.apply_plan(plan, None, 'a' * 40, api)['changed'])
            self.assertEqual(len(calls), 3)

    def test_security_only_scope_cannot_be_forged(self):
        plan = m.make_plan(fixture(), 'test/repo', 7, False)
        plan['code_scanning_only'] = True
        with self.assertRaisesRegex(ValueError, 'differs'):
            m.apply_plan(plan, None, 'a' * 40, lambda *args: self.fail('unexpected request'))

    def test_analysis_prerequisite_requires_full_sha_before_any_request(self):
        for security_only in (False, True):
            for analysis in (None, '', 'a' * 39, 'x' * 40, 'a' * 40 + '/main'):
                plan = m.make_plan(fixture(), 'test/repo', 7, False, security_only)
                documentation = None if security_only else 'd' * 40
                with self.subTest(scope=security_only, analysis=analysis), self.assertRaisesRegex(ValueError, 'analysis'):
                    m.apply_plan(plan, documentation, analysis,
                                 lambda *args: self.fail('unexpected request'))

    def test_analysis_comparison_must_match_sha_and_main_ancestry_before_put(self):
        for security_only in (False, True):
            for status, base in (('behind', 'a' * 40), ('diverged', 'a' * 40),
                                 ('ahead', 'b' * 40), ('ahead', None)):
                plan = m.make_plan(fixture(), 'test/repo', 7, False, security_only)
                calls = []
                def api(endpoint, payload=None):
                    calls.append((endpoint, payload))
                    return {'status': status, 'base_commit': None if base is None else {'sha': base}}
                with self.subTest(scope=security_only, status=status, base=base), self.assertRaisesRegex(ValueError, 'analysis'):
                    m.apply_plan(plan, None if security_only else 'd' * 40, 'a' * 40, api)
                self.assertEqual(calls, [('repos/test/repo/compare/' + 'a' * 40 + '...main', None)])

    def test_security_only_rejects_documentation_instead_of_ignoring_it(self):
        plan = m.make_plan(fixture(), 'test/repo', 7, False, True)
        with self.assertRaisesRegex(ValueError, 'only valid for queue'):
            m.apply_plan(plan, 'd' * 40, 'a' * 40,
                         lambda *args: self.fail('unexpected request'))

    def test_cli_requires_analysis_in_both_scopes_and_rejects_security_documentation(self):
        with TemporaryDirectory() as directory:
            path = Path(directory) / 'policy.json'
            for security_only, extra in ((False, ['--documentation-commit', 'd' * 40]),
                                         (True, []),
                                         (True, ['--analysis-commit', 'a' * 40,
                                                 '--documentation-commit', 'd' * 40])):
                path.write_text(json.dumps(m.make_plan(fixture(), 'test/repo', 7, False, security_only)))
                argv = ['repository-policy.py', '--apply', '--plan', str(path),
                        '--repo', 'test/repo', '--ruleset-id', '7', *extra]
                with self.subTest(scope=security_only, extra=extra), patch('sys.argv', argv), \
                        patch.object(m, 'apply_plan', side_effect=AssertionError('unexpected apply')), \
                        redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as error:
                    m.main()
                self.assertEqual(error.exception.code, 2)

    def test_plan_warns_about_preserved_bypasses_and_disabled_queue_auto_merge(self):
        before = fixture()
        for security_only in (False, True):
            plan = m.make_plan(before, 'test/repo', 7, False, security_only)
            self.assertEqual(plan['proposed']['bypass_actors'], before['bypass_actors'])
            self.assertTrue(any('bypass actors can override' in warning for warning in plan['warnings']))
            self.assertEqual(any('allow_auto_merge is false' in warning for warning in plan['warnings']),
                             not security_only)
        enabled = m.make_plan(before, 'test/repo', 7, True)
        self.assertFalse(any('allow_auto_merge is false' in warning for warning in enabled['warnings']))

    def test_post_put_mismatch_explains_that_live_policy_may_have_changed(self):
        for mutation in ('policy', 'identity', 'missing', 'malformed'):
            before = fixture()
            plan = m.make_plan(before, 'test/repo', 7, False, True)
            calls = []
            def api(endpoint, payload=None):
                calls.append((endpoint, payload))
                if '/compare/' in endpoint:
                    return comparison(endpoint)
                if payload is None:
                    return before
                result = {**before, **payload}
                if mutation == 'policy': result['name'] = 'unexpected'
                if mutation == 'identity': result['id'] = 8
                if mutation == 'missing': del result['rules']
                return None if mutation == 'malformed' else result
            with self.subTest(mutation=mutation), self.assertRaisesRegex(ValueError, 'live policy may have changed; inspect live ruleset'):
                m.apply_plan(plan, None, 'a' * 40, api)
            self.assertIsNotNone(calls[-1][1])
