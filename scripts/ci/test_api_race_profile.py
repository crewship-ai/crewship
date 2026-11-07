"""Execute the actual wrapper/reporter with synthetic Go; never run Go tests here."""
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import sys
import tempfile
import unittest
from unittest.mock import patch

import yaml

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location('profile_diagnostic', ROOT / 'scripts/ci/api-race-profile.py')
diagnostic = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(diagnostic)
runner = diagnostic.runner

FAKE_GO = r'''
import json,os,re,sys
from pathlib import Path
args=sys.argv[1:]
with open(os.environ['FAKE_GO_ARGS'],'a') as out:out.write(json.dumps(args)+'\n')
if '-list' in args:
    if os.environ.get('FAKE_LIST_FAIL'):sys.exit(19)
    print('\n'.join('Test'+c for c in 'ABCDEF'));sys.exit(0)
if args[:2]==['tool','pprof']:
    print('synthetic stack summary');sys.exit(0)
if args==['version']:print('go version go1.27.3 linux/amd64');sys.exit(0)
if args==['env','GOOS','GOARCH']:print('linux\namd64');sys.exit(0)
if args[0]!='test':sys.exit(23)
pattern=args[args.index('-run')+1]
package='github.com/crewship-ai/crewship/internal/api'
names=[n for n in ['Test'+c for c in 'ABCDEF'] if re.fullmatch(pattern,n)]
for name in names:
    print(json.dumps({'Action':'run','Package':package,'Test':name}))
    print(json.dumps({'Action':'pass','Package':package,'Test':name,'Elapsed':1}))
for arg in args:
    if any(arg.startswith(prefix) for prefix in ['-cpuprofile=','-memprofile=','-o=']):
        Path(arg.split('=',1)[1]).write_bytes(b'synthetic artifact, not a real profile')
code=int(os.environ.get('FAKE_NATIVE_EXIT','0'))
print(json.dumps({'Action':'fail' if code else 'pass','Package':package,'Elapsed':2}))
sys.exit(code)
'''


class ProfileForwarding(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        scripts = self.root / 'scripts/ci'
        scripts.mkdir(parents=True)
        for name in ['go-test.sh', 'go-events.py']:
            shutil.copy(ROOT / 'scripts/ci' / name, scripts / name)
        binary = self.root / 'bin'
        binary.mkdir()
        go = binary / 'go'
        go.write_text('#!' + sys.executable + '\n' + FAKE_GO)
        go.chmod(0o700)
        self.log = self.root / 'forwarded.jsonl'
        self.results = self.root / 'results'
        self.profile_dir = self.root / 'profiles'
        self.profile_dir.mkdir()
        self.env = patch.dict(os.environ, {'PATH': str(binary) + os.pathsep + os.environ['PATH'],
                                         'CI_RESULTS_DIR': str(self.results),
                                         'FAKE_GO_ARGS': str(self.log), 'RACE_GCFLAGS': runner.RACE_GCFLAGS})
        self.env.start()
        self.addCleanup(self.env.stop)
        root = patch.object(runner, 'ROOT', self.root)
        root.start()
        self.addCleanup(root.stop)
        self.bundle = patch.object(runner, 'validate_bootstrap', return_value=(3, 'a' * 64))
        self.bundle.start()
        self.addCleanup(self.bundle.stop)

    def executed(self):
        return [json.loads(line) for line in self.log.read_text().splitlines() if '-run' in json.loads(line)]

    def test_default_real_wrapper_forwards_unchanged_race_and_exact_selection(self):
        self.assertEqual(runner.run(2, 6, 4600, 'bundle'), 0)
        args, = self.executed()
        self.assertEqual(args[:7], ['test', '-json', runner.RACE_GCFLAGS, runner.PACKAGE, '-race', '-count=1', '-timeout'])
        self.assertEqual(args[7:9], ['4600s', '-run'])
        self.assertEqual([n for n in ['TestA', 'TestB', 'TestC', 'TestD', 'TestE', 'TestF'] if re.fullmatch(args[9], n)], ['TestC'])
        self.assertEqual(len(args), 10)
        receipt = json.loads((self.results / 'api-race-shard.json').read_text())
        self.assertEqual(receipt['selected'], ['TestC'])
        self.assertNotIn('diagnostic_profile', receipt)

    def test_profile_forwards_cpu_and_only_explicit_allocations(self):
        for allocations in [False, True]:
            with self.subTest(allocations=allocations):
                self.log.unlink(missing_ok=True)
                self.assertEqual(runner.run(2, 6, 4600, 'bundle', profile_directory=self.profile_dir, allocations=allocations), 0)
                args, = self.executed()
                self.assertIn('-cpuprofile=' + str(self.profile_dir / 'cpu.pprof'), args)
                self.assertEqual(any(x.startswith('-memprofile=') for x in args), allocations)
                self.assertEqual('-memprofilerate=524288' in args, allocations)
                self.assertEqual([n for n in ['TestB', 'TestC', 'TestD'] if re.fullmatch(args[9], n)], ['TestC'])
                self.assertTrue(json.loads((self.results / 'api-race-shard.json').read_text())['diagnostic_profile'])

    def test_actual_pipefail_preserves_native_failure_and_invalidates_old_success(self):
        (self.results).mkdir()
        (self.results / 'api-race-shard.json').write_text('{"old_success":true}')
        with patch.dict(os.environ, FAKE_NATIVE_EXIT='17'):
            self.assertEqual(runner.run(2, 6, 4600, 'bundle', profile_directory=self.profile_dir), 17)
        self.assertFalse((self.results / 'api-race-shard.json').exists())

    def test_enumeration_failure_never_executes_partition(self):
        import subprocess
        with patch.dict(os.environ, FAKE_LIST_FAIL='1'), self.assertRaises(subprocess.CalledProcessError):
            runner.run(2, 6, 4600, 'bundle', profile_directory=self.profile_dir)
        self.assertEqual(self.executed(), [])
        self.assertFalse((self.results / 'api-race-shard.json').exists())

    def test_profile_requires_measured_api_six_shards_and_no_symlink(self):
        for count, bundle in [(5, 'bundle'), (6, None)]:
            with self.assertRaises(ValueError):
                runner.run(0, count, 4600, bundle, profile_directory=self.profile_dir)
        link = self.root / 'link'
        link.symlink_to(self.profile_dir)
        with self.assertRaises(ValueError):
            runner.run(0, 6, 4600, 'bundle', profile_directory=link)
        self.assertFalse(self.log.exists())

    def test_diagnostic_executes_wrapper_and_pprof_then_retains_bound_receipt_not_binary(self):
        output = self.root / 'exclusive-output'
        identity = {'GITHUB_SHA': 'a' * 40, 'GITHUB_RUN_ID': '123', 'GITHUB_RUN_ATTEMPT': '1'}
        with patch.object(diagnostic, 'guarded_identity', return_value=identity), \
                patch.object(diagnostic, 'telemetry', side_effect=[{'phase': 'before'}, {'phase': 'after'}]), \
                patch.dict(os.environ, identity):
            self.assertEqual(diagnostic.profile(2, 'bundle', output, allocations=False), 0)
        receipt = json.loads((output / 'profile-receipt.json').read_text())
        self.assertEqual(receipt['status'], 'profile_complete_not_budget_evidence')
        self.assertEqual(receipt['selected_count'], 1)
        self.assertEqual(receipt['inventory_count'], 6)
        self.assertEqual(receipt['parent_outcomes'], {'pass': 1})
        self.assertEqual(receipt['bootstrap_sha256'], 'a' * 64)
        self.assertFalse((output / 'api.test').exists())
        self.assertTrue((output / 'cpu-top.txt').read_text().startswith('synthetic stack summary'))
        self.assertFalse((output / 'alloc.pprof').exists())
        self.assertEqual(receipt['before'], {'phase': 'before'})
        self.assertEqual(receipt['after'], {'phase': 'after'})
        with patch.object(diagnostic, 'guarded_identity', return_value=identity):
            with self.assertRaises(FileExistsError):
                diagnostic.profile(2, 'bundle', output)

    def test_diagnostic_native_failure_stays_failed_and_no_summary_or_binary_upload(self):
        output = self.root / 'failed-output'
        with patch.object(diagnostic, 'guarded_identity', return_value={}), \
                patch.object(diagnostic, 'telemetry', return_value={}), \
                patch.dict(os.environ, FAKE_NATIVE_EXIT='17'):
            self.assertEqual(diagnostic.profile(2, 'bundle', output), 17)
        receipt = json.loads((output / 'profile-receipt.json').read_text())
        self.assertEqual(receipt['status'], 'incomplete')
        self.assertEqual(receipt['native_exit'], 17)
        self.assertFalse((output / 'cpu-top.txt').exists())
        self.assertFalse((output / 'api.test').exists())


class DiagnosticBoundary(unittest.TestCase):
    def test_diagnostic_cannot_replace_ordinary_aggregate(self):
        with self.assertRaisesRegex(ValueError, 'diagnostic'):
            runner.validate_manifests([{'index': 0, 'diagnostic_profile': True}], 1)

    def test_dispatch_guard_refuses_unprotected_pr_and_nonmain(self):
        cases = [('pull_request', 'refs/heads/main', 'true'), ('workflow_dispatch', 'refs/heads/topic', 'true'),
                 ('workflow_dispatch', 'refs/heads/main', 'false')]
        for event, ref, protected in cases:
            with patch.dict(os.environ, GITHUB_EVENT_NAME=event, GITHUB_REF=ref, GITHUB_REF_PROTECTED=protected):
                with self.assertRaisesRegex(ValueError, 'protected main'):
                    diagnostic.guarded_identity()

    def test_workflow_is_dispatch_only_pinned_readonly_and_uploads_no_binary(self):
        workflow = yaml.safe_load((ROOT / '.github/workflows/api-race-profile.yml').read_text())
        trigger = workflow.get('on', workflow.get(True))
        self.assertEqual(set(trigger), {'workflow_dispatch'})
        inputs = trigger['workflow_dispatch']['inputs']
        self.assertEqual(inputs['shard']['options'], [str(i) for i in range(6)])
        self.assertFalse(inputs['allocations']['default'])
        self.assertEqual(workflow['permissions'], {'contents': 'read'})
        ordinary = yaml.safe_load((ROOT / '.github/workflows/ci.yml').read_text())
        self.assertEqual(workflow['env']['GO_VERSION'], ordinary['env']['GO_VERSION'])
        self.assertEqual(set(workflow['jobs']), {'template', 'profile'})
        for job in workflow['jobs'].values():
            self.assertEqual(job['timeout-minutes'], 85)
            self.assertIn('github.ref_protected', job['if'])
            checkout = job['steps'][0]
            self.assertEqual(checkout['with'], {'ref': '${{ github.sha }}', 'persist-credentials': False})
            for step in job['steps']:
                if 'uses' in step and not step['uses'].startswith('./'):
                    self.assertRegex(step['uses'], r'@[a-f0-9]{40}$')
        upload = workflow['jobs']['profile']['steps'][-1]['with']['path']
        self.assertNotIn('api.test', upload)
        self.assertNotIn('go-test.jsonl', upload)
        self.assertNotIn('**', upload)


if __name__ == '__main__':
    unittest.main()
