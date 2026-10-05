"""Check throughput evidence boundaries, not live GitHub timing."""
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('throughput', Path(__file__).with_name('pr-throughput.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class ThroughputTests(unittest.TestCase):
    def pr(self, commits, events=()):
        return {'mergedAt': '2026-10-05T12:00:00Z',
                'commits': {'nodes': [{'commit': c} for c in commits], 'pageInfo': {'hasNextPage': False}},
                'timelineItems': {'nodes': list(events), 'pageInfo': {'hasNextPage': False}}}

    def commit(self, message='fix: behavior', pushed=None, parents=1):
        return {'oid': message, 'messageHeadline': message, 'pushedDate': pushed,
                'parents': {'totalCount': parents}}

    def test_missing_push_is_not_author_timestamp_or_workflow_start(self):
        pr = self.pr([self.commit()])
        self.assertIsNone(m.push_evidence(pr)[0])

    def test_automatic_main_sync_does_not_reset_substantive_push(self):
        pr = self.pr([self.commit(pushed='2026-10-05T10:00:00Z'),
                      {**self.commit('sync trunk', '2026-10-05T11:00:00Z', 2),
                       'main_parent_provenance': 'verified_main_parent'}])
        self.assertEqual(m.push_evidence(pr)[0], '2026-10-05T10:00:00Z')
        self.assertTrue(m.substantive(self.commit('chore(deps): update dependencies')))

    def test_main_parent_proof_overrides_headline(self):
        for message, status, expected in [('sync trunk', 'ahead', False),
                                           ("Merge branch 'main'", 'diverged', True)]:
            commit = self.commit(message, parents=2)
            commit['parents']['nodes'] = [{'oid': 'feature'}, {'oid': 'parent'}]
            pr = self.pr([commit])
            pr.update(number=1, mergeCommit={'parents': {'nodes': [{'oid': 'base'}]}})
            class API:
                def request(self, endpoint):
                    return {'status': status}
            m.annotate_main_parents(pr, 'test/repo', API(), [], {})
            self.assertEqual(m.substantive(commit), expected)

    def test_unavailable_parent_proof_cannot_produce_push_latency(self):
        pr = self.pr([self.commit('sync trunk', '2026-10-05T11:00:00Z', 2)])
        self.assertIsNone(m.push_evidence(pr)[0])

    def test_final_substantive_commit_needs_its_own_evidence(self):
        pr = self.pr([self.commit('fix: old', '2026-10-05T10:00:00Z'), self.commit('fix: new')])
        self.assertIsNone(m.push_evidence(pr)[0])

    def test_force_push_and_truncation(self):
        pr = self.pr([self.commit()], [{'createdAt': '2026-10-05T11:00:00Z',
                                       'afterCommit': {'oid': 'fix: behavior'}}])
        self.assertEqual(m.push_evidence(pr)[0], '2026-10-05T11:00:00Z')
        pr['commits']['pageInfo']['hasNextPage'] = True
        self.assertIsNone(m.push_evidence(pr)[0])

    def test_attempt_queue_execution_and_failure_are_separate(self):
        run = {'id': 1, 'run_attempt': 2, 'path': 'ci.yml', 'head_sha': 'abc',
               'event': 'pull_request', 'conclusion': 'failure', 'created_at': '2026-10-05T10:00:00Z'}
        jobs = [{'name': 'race', 'started_at': '2026-10-05T10:01:00Z',
                 'completed_at': '2026-10-05T10:04:00Z', 'conclusion': 'timed_out', 'steps': [{}]},
                {'name': 'skipped', 'started_at': '2026-10-05T10:00:00Z',
                 'completed_at': '2026-10-05T10:00:00Z', 'conclusion': 'skipped', 'steps': []}]
        result = m.attempt_metrics(run, jobs)
        self.assertIsNone(result['queue_seconds'])
        self.assertIsNone(result['elapsed_seconds'])
        self.assertEqual(result['execution_wall_seconds'], 180)
        run['run_attempt'] = 1
        self.assertEqual(m.attempt_metrics(run, jobs)['queue_seconds'], 60)
        self.assertEqual(result['timed_out_jobs'], 1)
        self.assertEqual(result['attempt'], 2)

    def test_request_budget_fails_before_another_api_call(self):
        api = m.GitHub(1)
        with patch.object(m.subprocess, 'check_output', return_value=b'{}') as call:
            api.request('repos/test/repo')
            with self.assertRaisesRegex(RuntimeError, 'budget'):
                api.request('repos/test/repo')
            self.assertEqual(call.call_count, 1)

    def test_pagination_cap_reports_incomplete_evidence(self):
        api = m.GitHub(2)
        warnings = []
        with patch.object(api, 'request', return_value={'jobs': [{}] * 100}):
            self.assertEqual(len(api.pages('jobs', 'jobs', 1, warnings)), 100)
        self.assertIn('incomplete', warnings[0])

    def test_percentiles_include_slow_failures_and_ignore_unknown(self):
        self.assertEqual(m.percentile([1, 2, 3, 100, None], .9), 100)
        self.assertIsNone(m.percentile([None], .5))
