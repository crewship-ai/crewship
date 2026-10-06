"""No PR execution: trusted snapshots, API metadata and administrator decisions."""
import base64
from copy import deepcopy
import hashlib
import importlib.util
import io
import json
import subprocess
import sys
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('guard', ROOT / 'scripts/ci/inventory-guard.py')
m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)


class GuardTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(); self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        manifest = json.loads((ROOT / m.MANIFEST).read_text())
        for path in set(manifest['protected_files']) | set(m.SELF):
            target = self.root / path; target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes((ROOT / path).read_bytes())
        self.repo = m.REPOSITORY; self.head = 'a' * 40
        self.event = {'repository': {'full_name': self.repo, 'default_branch': 'main'},
                      'number': 12, 'pull_request': {'head': {'sha': self.head}}}
        self.env = {'GITHUB_REPOSITORY': self.repo, 'GITHUB_EVENT_NAME': 'pull_request_target',
                    'GITHUB_SHA': 'b' * 40, 'GITHUB_REF': 'refs/heads/main',
                    'GITHUB_ACTOR': 'contributor', 'GITHUB_TRIGGERING_ACTOR': 'contributor', 'GITHUB_RUN_ID': '1'}
        self.pr = {'number': 12, 'state': 'open', 'base': {'ref': 'main', 'repo': {'full_name': self.repo}},
                   'head': {'sha': self.head, 'repo': {'full_name': 'fork-owner/crewship'}}}
        self.changes = {}; self.calls = []; self.permission = 'admin'; self.live_reads = 0
        self.advance = False; self.bad_content = None
        self.queue = None; self.queue_reads = 0
        self.tree_mutation = None

    def api(self, endpoint, payload=None):
        self.calls.append((endpoint, payload))
        if '/collaborators/' in endpoint:
            actor = endpoint.split('/collaborators/')[1].split('/')[0]
            return {'permission': self.permission, 'user': {'login': actor}}
        if '/git/ref/' in endpoint:
            ref = 'refs/' + endpoint.split('/git/ref/', 1)[1]
            if ref == 'refs/heads/main': return {'ref': ref, 'object': {'type': 'commit', 'sha': self.env.get('GUARD_TRUSTED_SHA', self.env['GITHUB_SHA'])}}
            self.queue_reads += 1
            return {'ref': ref, 'object': {'type': 'commit', 'sha': 'c' * 40 if self.advance and self.queue_reads > 1 else self.head}}
        if '/pulls/' in endpoint:
            self.live_reads += 1
            value = deepcopy(self.pr)
            if self.advance and self.live_reads > 1: value['head']['sha'] = 'c' * 40
            return value
        if '/git/trees/' in endpoint:
            self.assertTrue(endpoint.endswith('/' + self.head + '?recursive=1'))
            entries = []
            for path in sorted(set(m.PROTECTED) | set(m.SELF)):
                raw = self.changes.get(path, (self.root / path).read_bytes())
                entries.append(dict(path=path, mode='100644', type='blob', size=len(raw),
                                    sha=hashlib.sha1(b'blob ' + str(len(raw)).encode() + b'\0' + raw).hexdigest()))
            tree = dict(truncated=False, tree=entries)
            if self.tree_mutation: self.tree_mutation(tree)
            return tree
        if '/contents/' in endpoint:
            self.assertTrue(endpoint.startswith('repos/' + (self.repo if self.queue else 'fork-owner/crewship') + '/'))
            self.assertTrue(endpoint.endswith('?ref=' + self.head))
            path = endpoint.split('/contents/', 1)[1].split('?')[0]
            if self.bad_content is not None: return self.bad_content
            data = self.changes.get(path, (self.root / path).read_bytes())
            return {'path': path, 'type': 'file', 'size': len(data), 'encoding': 'base64',
                    'content': base64.b64encode(data).decode(),
                    'sha': hashlib.sha1(b'blob ' + str(len(data)).encode() + b'\0' + data).hexdigest()}
        if '/statuses/' in endpoint:
            self.assertTrue(endpoint.endswith('/statuses/' + self.head))
            return {'state': payload['state']}
        raise AssertionError(endpoint)

    def inspect(self): return m.inspect(self.event, self.env, self.api, self.root)

    def refresh_candidate(self):
        snapshot = {path: self.changes.get(path, (self.root / path).read_bytes()) for path in m.PROTECTED}
        self.changes[m.MANIFEST] = (json.dumps(m.refreshed(snapshot), indent=2) + '\n').encode()

    def dispatch(self, approval='true'):
        self.env.update(GITHUB_EVENT_NAME='workflow_dispatch', GITHUB_ACTOR='Srbino', GITHUB_TRIGGERING_ACTOR='Srbino')
        self.event['inputs'] = {'pr_number': '12', 'expected_head_sha': self.head, 'approve_control_changes': approval}

    def test_unchanged_inventory_uses_exact_fork_sha_as_data(self):
        report = self.inspect()
        self.assertEqual(report['state'], 'success')
        self.assertEqual(report['changed_control_files'], [])
        self.assertEqual(set(report['baseline']), set(report['candidate']))
        self.assertTrue(all(payload is None for _, payload in self.calls))

    def test_control_narrowing_and_guard_self_change_fail_without_dispatch(self):
        for path in ('scripts/ci/plan.py', 'scripts/ci/verdict.py', *m.SELF):
            with self.subTest(path=path):
                self.changes = {path: (self.root / path).read_bytes() + b'\n# change\n'}
                self.assertEqual(self.inspect()['state'], 'failure')
        path = '.github/workflows/ci.yml'
        self.changes = {path: (self.root / path).read_bytes().replace(b'go-shuffle, ', b'')}
        report = self.inspect()
        self.assertEqual(report['state'], 'failure')
        self.assertEqual(report['candidate'][path]['removed_required_jobs'], ['go-shuffle'])

    def test_separate_administrator_dispatch_approves_exact_reported_changes_only(self):
        path = 'scripts/ci/verdict.py'; self.changes[path] = b'# malicious-looking data is never executed\nraise RuntimeError()\n'
        self.refresh_candidate()
        self.dispatch()
        report = self.inspect()
        self.assertEqual(report['state'], 'success')
        self.assertEqual(report['decision'], 'explicit administrator exception')
        self.assertEqual(set(report['changed_control_files']), {path, m.MANIFEST})
        self.event['inputs']['approve_control_changes'] = 'false'
        self.assertEqual(self.inspect()['state'], 'failure')

    def test_stale_candidate_inventory_cannot_be_manually_approved_or_wedge_next_baseline(self):
        path = 'scripts/ci/verdict.py'
        self.changes[path] = (self.root / path).read_bytes() + b'\n# reviewed control change\n'
        self.dispatch()
        stale = self.inspect()
        self.assertEqual(stale['state'], 'failure')
        self.assertTrue(stale['candidate_inventory_errors'])
        self.assertIn(path, stale['changed_control_files'])
        self.refresh_candidate()
        fresh = self.inspect()
        self.assertEqual(fresh['state'], 'success')
        self.assertEqual(fresh['candidate_inventory_errors'], [])
        # Model the approved candidate becoming trusted main: its baseline is usable.
        for path, raw in self.changes.items(): (self.root / path).write_bytes(raw)
        self.changes.clear()
        self.assertEqual(self.inspect()['state'], 'success')

    def test_manifest_shape_paths_hashes_and_jobs_fail_closed(self):
        source = json.loads((self.root / m.MANIFEST).read_text())
        variants = []
        for version in (True, '1', 2):
            data = deepcopy(source); data['version'] = version; variants.append(data)
        for value in ('not-a-sha', 1, None):
            data = deepcopy(source); data['protected_files']['scripts/ci/plan.py'] = value; variants.append(data)
        data = deepcopy(source); data['protected_files']['../outside'] = 'a' * 64; variants.append(data)
        for jobs in ('go', [], ['go', 'go'], [1], ['../go']):
            data = deepcopy(source); data['workflows']['.github/workflows/ci.yml']['required_jobs'] = jobs; variants.append(data)
        for data in variants:
            with self.subTest(data=data), self.assertRaises(m.Rejected):
                m.manifest_data(json.dumps(data).encode())

    def test_trusted_refresh_never_runs_candidate_code(self):
        (self.root / 'scripts/ci/verdict.py').write_text("raise RuntimeError('MUST NOT RUN')\n")
        result = subprocess.run([sys.executable, str(ROOT / 'scripts/ci/inventory-guard.py'),
                                            '--refresh-inventory', str(self.root)], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        data = m.manifest_data((self.root / m.MANIFEST).read_bytes())
        self.assertEqual(data, m.refreshed({path: (self.root / path).read_bytes() for path in m.PROTECTED}))

    def test_wrong_repository_default_branch_and_missing_trusted_baseline_rejected(self):
        for mutate in ('repo', 'default', 'sha', 'baseline'):
            event, env = deepcopy(self.event), deepcopy(self.env)
            if mutate == 'repo': event['repository']['full_name'] = 'attacker/repo'
            elif mutate == 'default': event['repository']['default_branch'] = 'other'
            elif mutate == 'sha': env['GITHUB_SHA'] = 'refs/pull/12/head'
            else: (self.root / m.MANIFEST).unlink()
            with self.subTest(mutate=mutate), self.assertRaises((m.Rejected, OSError)):
                m.inspect(event, env, self.api, self.root)

    def test_dispatch_wrong_ref_actor_expected_head_and_boolean_rejected(self):
        self.dispatch()
        for kind in ('ref', 'permission', 'actor', 'rerun_actor', 'head', 'boolean'):
            env, event = deepcopy(self.env), deepcopy(self.event); self.permission = 'admin'
            if kind == 'ref': env['GITHUB_REF'] = 'refs/heads/untrusted'
            elif kind == 'permission': self.permission = 'write'
            elif kind == 'actor': env['GITHUB_ACTOR'] = 'bad/name'
            elif kind == 'rerun_actor': env['GITHUB_TRIGGERING_ACTOR'] = 'bad/name'
            elif kind == 'head': event['inputs']['expected_head_sha'] = 'c' * 40
            else: event['inputs']['approve_control_changes'] = 'yes'
            with self.subTest(kind=kind), self.assertRaises(m.Rejected):
                m.inspect(event, env, self.api, self.root)

    def test_api_failure_missing_oversized_binary_and_invalid_content_rejected(self):
        for bad in ({}, {'type': 'symlink'}, {'type': 'file', 'path': '.github/workflows/ci-inventory.yml',
                                          'size': m.LIMIT + 1, 'encoding': 'base64', 'content': ''}):
            self.bad_content = bad
            with self.assertRaises(m.Rejected): self.inspect()
        self.bad_content = None
        for raw in (b'\0bad', b'\xff', b'x' * (m.LIMIT + 1)):
            self.changes['.github/workflows/ci-inventory.yml'] = raw
            with self.assertRaises(m.Rejected): self.inspect()
        def failed(*args): raise m.Rejected('API unavailable')
        with self.assertRaises(m.Rejected): m.inspect(self.event, self.env, failed, self.root)

    def test_git_modes_prevent_contents_symlink_dereference_even_for_equal_bytes(self):
        # The Contents API still reports type=file and identical content for a link.
        for mode, kind in (('120000', 'blob'), ('160000', 'commit'), ('040000', 'tree')):
            self.tree_mutation = lambda tree, mode=mode, kind=kind: tree['tree'][0].update(mode=mode, type=kind)
            with self.subTest(mode=mode), self.assertRaises(m.Rejected): self.inspect()
            self.assertFalse(any('/contents/' in endpoint for endpoint, _ in self.calls))
        self.tree_mutation = lambda tree: tree['tree'][0].update(mode='100755')
        self.assertEqual(self.inspect()['state'], 'success')

    def test_git_tree_truncation_missing_duplicate_and_hash_mismatch_fail_closed(self):
        mutations = (lambda tree: tree.update(truncated=True),
                     lambda tree: tree.update(truncated=0),
                     lambda tree: tree['tree'].pop(),
                     lambda tree: tree['tree'].append(deepcopy(tree['tree'][0])),
                     lambda tree: tree['tree'][0].update(sha='c' * 40))
        for mutation in mutations:
            self.tree_mutation = mutation
            with self.assertRaises(m.Rejected): self.inspect()
        self.tree_mutation = None
        raw = (self.root / '.github/workflows/ci-inventory.yml').read_bytes()
        self.bad_content = dict(path='.github/workflows/ci-inventory.yml', type='file', size=len(raw),
                                encoding='base64', content=base64.b64encode(raw).decode(), sha='c' * 40)
        with self.assertRaises(m.Rejected): self.inspect()

    def test_realistic_tree_response_exceeds_contents_budget_but_remains_bounded(self):
        # Current repository tree is ~2.6 MiB/10k entries: tiny API stubs miss this.
        raw = json.dumps({'truncated': False, 'tree': [], 'padding': 'x' * 2_600_000}).encode()
        def open_response(*args, **kwargs): return io.BytesIO(raw)
        opener = type('Opener', (), {'open': staticmethod(open_response)})()
        with patch.object(m.urllib.request, 'build_opener', return_value=opener):
            api = m.API('synthetic-key')
            self.assertFalse(api('repos/' + self.repo + '/git/trees/' + self.head + '?recursive=1')['truncated'])
            with self.assertRaises(m.Rejected): api('repos/' + self.repo + '/contents/file')
            raw = b'x' * (m.TREE_LIMIT + 1)
            with self.assertRaises(m.Rejected): api('repos/' + self.repo + '/git/trees/' + self.head + '?recursive=1')

    def test_missing_jobs_or_trusted_inventory_corruption_rejected(self):
        self.changes['.github/workflows/ci.yml'] = b'jobs:\n'
        with self.assertRaises(m.Rejected): self.inspect()
        self.changes.clear()
        (self.root / 'scripts/ci/plan.py').write_text('changed trusted baseline')
        with self.assertRaises(m.Rejected): self.inspect()

    def test_head_change_before_publication_never_posts_status(self):
        self.advance = True
        with self.assertRaises(m.Rejected): m.execute(self.event, self.env, self.api, self.root)
        self.assertFalse(any(payload is not None for _, payload in self.calls))

    def merge_group(self):
        self.queue = 'refs/heads/gh-readonly-queue/main/pr-12-abcdef'
        self.env.update(GITHUB_EVENT_NAME='merge_group', GITHUB_SHA=self.head, GUARD_TRUSTED_SHA='b' * 40)
        self.event['merge_group'] = dict(base_ref='refs/heads/main', base_sha='b' * 40,
                                        head_sha=self.head, head_ref=self.queue)

    def test_queue_checks_trusted_main_base_and_attaches_exact_candidate_status(self):
        self.merge_group()
        report = self.inspect()
        self.assertEqual(report['state'], 'success')
        self.assertEqual(report['queue_ref'], self.queue)
        self.assertEqual(report['trusted_sha'], 'b' * 40)
        self.assertIsNone(report['pr_number'])
        # Isolate the report file; a fake API proves no actual GitHub write occurs.
        import os
        previous = os.getcwd()
        try:
            os.chdir(self.root)
            m.execute(self.event, self.env, self.api, self.root)
        finally: os.chdir(previous)
        status = next((endpoint, payload) for endpoint, payload in self.calls if payload is not None)
        self.assertTrue(status[0].endswith('/statuses/' + self.head))
        self.assertEqual(status[1]['state'], 'success')
        self.assertTrue((self.root / '.ci-results/ci-inventory-report.json').is_file())

    def test_queue_control_change_requires_its_own_admin_dispatch(self):
        self.merge_group(); path = 'scripts/ci/verdict.py'
        self.changes[path] = (self.root / path).read_bytes() + b'\n# deliberate\n'; self.refresh_candidate()
        self.assertEqual(self.inspect()['state'], 'failure')
        self.dispatch()
        self.env['GITHUB_SHA'] = 'b' * 40
        self.event['inputs'].update(pr_number='', queue_ref=self.queue, expected_base_sha='b' * 40)
        self.assertEqual(self.inspect()['state'], 'success')
        self.event['inputs']['expected_base_sha'] = 'c' * 40
        with self.assertRaises(m.Rejected): self.inspect()

    def test_queue_wrong_base_ref_branch_or_moving_head_fails_closed(self):
        self.merge_group()
        original = deepcopy(self.event)
        for field, value in (('base_ref', 'refs/heads/other'), ('base_sha', 'c' * 40),
                             ('head_ref', 'refs/heads/feature'), ('head_sha', 'c' * 40)):
            self.event = deepcopy(original); self.event['merge_group'][field] = value
            with self.subTest(field=field), self.assertRaises(m.Rejected): self.inspect()
        self.event = original; self.advance = True; self.queue_reads = 0
        with self.assertRaises(m.Rejected): m.execute(self.event, self.env, self.api, self.root)
        self.assertFalse(any(payload is not None for _, payload in self.calls))

    def test_workflow_privilege_boundary_never_checks_out_pr_or_installs_dependencies(self):
        workflow = (ROOT / '.github/workflows/ci-inventory.yml').read_text()
        self.assertIn('pull_request_target:', workflow)
        self.assertIn("ref: ${{ github.event_name == 'merge_group' && github.event.merge_group.base_sha || github.sha }}", workflow)
        self.assertIn('merge_group:', workflow)
        self.assertIn("if: github.event_name != 'workflow_dispatch' || github.ref == 'refs/heads/main'", workflow)
        self.assertIn('persist-credentials: false', workflow)
        for forbidden in ('head.sha', 'refs/pull/', 'pip install', 'pnpm', 'secrets.', 'checks: write', 'contents: write'):
            self.assertNotIn(forbidden, workflow)
        self.assertIn('statuses: write', workflow)


if __name__ == '__main__': unittest.main()
