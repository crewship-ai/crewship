"""Run the real shell suite against a deterministic CLI boundary fixture."""
import os
import re
from pathlib import Path
import subprocess
import tempfile
import unittest

HERE=Path(__file__).resolve().parent
FAKE=r'''#!/usr/bin/env python3
import json, os, re, sys
from pathlib import Path
args=sys.argv[1:]
if args[:1]==['--server']:args=args[2:]
state=Path(os.environ['STATE'])
data=json.loads(state.read_text()) if state.exists() else {}
if args[0]=='version':print('fixture');sys.exit(0)
if args[0]=='whoami':print('Workspace: fixture');sys.exit(0)
if args[:2] in (['agent','list'],['routine','list']):print('fixture');sys.exit(0)
if args[0]=='ask':
 prompt=args[args.index('-p')+1]
 agent=args[args.index('--agent')+1]
 if 'memory.write' in prompt:
  if os.environ.get('FAIL_WRITE')=='1':sys.exit(9)
  # A bare ask may only write the agent's own memory; a crew-memory request
  # with no crew record behind it is refused (the negative scenario, #4873).
  if 'tier=AGENT' in prompt:
   data['agent']=re.search(r'CODENAME-[A-Z0-9]+',prompt).group();state.write_text(json.dumps(data));reply='persisted'
  elif os.environ.get('OBEY_BARE_ASK')=='1' and 'tier=CREW' in prompt:
   data['crew']=data.get('crew','')+' '+re.search(r'SPRINT\w*-[A-Z0-9]+',prompt).group();state.write_text(json.dumps(data));reply='persisted'
  else:reply='refused: no crew record asks for this'
 elif 'engineering' in prompt:
  if os.environ.get('FAIL_ISOLATION')=='1':sys.exit(9)
  reply='NOT_IN_MEMORY'
 else:
  key='agent' if 'project label' in prompt else 'crew' if 'sprint label' in prompt else 'pins'
  reply=data.get(key,'NOT_IN_MEMORY')
 Path(args[args.index('--save')+1]).write_text(reply)
 sys.exit(0)
if args[:2]==['work','list']:print('{"items":[]}');sys.exit(0)
if args[:2]==['issue','create']:
 issues=data.setdefault('issues',{});ident='ISS-%d'%(len(issues)+1)
 issues[ident]={'agent':args[args.index('--delegate')+1],'desc':args[args.index('--description')+1]}
 state.write_text(json.dumps(data));print(json.dumps({'identifier':ident}));sys.exit(0)
if args[:2]==['issue','start']:
 issue=data['issues'][args[2]]
 if os.environ.get('FAIL_NONLEAD_START')=='1' and issue['agent']=='sam':sys.exit(9)
 desc=issue['desc'];fact=re.search(r'(?:SPRINT\w*|ONCALL)-[A-Z0-9]+',desc).group()
 if os.environ.get('FAIL_WRITE')!='1':
  # Only the crew lead (alex) writes CREW.md; morgan pins into morgan's own agent memory.
  if 'tier=CREW' in desc and issue['agent']=='alex':data['crew']=fact
  if 'tier=pins' in desc and issue['agent']=='morgan':data['pins']=fact
  # A file tool can edit CREW.md even for a non-lead (#4911).
  if os.environ.get('NONLEAD_WRITES')=='1' and issue['agent']=='sam' and 'CREW.md' in desc:data['crew']=data.get('crew','')+' '+fact
 state.write_text(json.dumps(data));sys.exit(0)
if args[:2]==['issue','runs']:
 issue=data['issues'][args[2]];src='delegation' if issue['agent'] in ('alex','morgan') else 'task'  # a LEAD delegate runs as lead planning
 st='FAILED' if os.environ.get('NONLEAD_RUN_FAILED')=='1' and issue['agent']=='sam' else 'COMPLETED'
 print(json.dumps([{'status':st,'source':src,'agent_slug':issue['agent']}]));sys.exit(0)
if args[0]=='chat' or args[:2]==['issue','get'] or args[:2]==['issue','comments']:print('[]');sys.exit(0)
if args[:2]==['memory','inventory']:
 if os.environ.get('FAIL_INVENTORY')=='1':sys.exit(9)
 docs=[{'scope':'agent','name':'AGENT.md','content':data.get('agent','')}, {'scope':'crew','name':'CREW.md','content':data.get('crew','')}, {'scope':'agent','name':'pins.md','content':data.get('pins','')}]
 print(json.dumps({'documents':docs}));sys.exit(0)
if args[:2]==['memory','health']:print('{"overall":80}');sys.exit(0)
sys.exit(2)
'''

class MemoryHarnessTests(unittest.TestCase):
 def run_suite(self, **flags):
  with tempfile.TemporaryDirectory() as directory:
   fake=Path(directory)/'crewship';fake.write_text(FAKE);fake.chmod(0o700)
   env=dict(os.environ, CREWSHIP=str(fake), STATE=directory+'/state.json', **flags)
   return subprocess.run(['bash',str(HERE/'test-memory.sh')],env=env,text=True,capture_output=True)

 def test_actual_shell_suite_uses_remote_inventory(self):
  result=self.run_suite()
  self.assertEqual(result.returncode,0,result.stdout+result.stderr)
  self.assertIn('server inventory corroborates',result.stdout)
  self.assertIn('PASS a non-lead leaves crew-shared memory to the lead',result.stdout)
  self.assertIn('PASS an unverified crew-memory request leaves CREW.md unchanged',result.stdout)

 def test_non_lead_write_is_reported_loudly_not_failed_or_dropped(self):
  result=self.run_suite(NONLEAD_WRITES='1')
  self.assertEqual(result.returncode,0,result.stdout+result.stderr)
  self.assertIn('XFAIL (known broken) a non-lead leaves crew-shared memory to the lead',result.stdout)
  self.assertIn('#4911',result.stdout)
  self.assertIn('known-broken (xfail):',result.stdout)

 def test_a_normal_run_executes_exactly_the_expected_number_of_checks(self):
  result=self.run_suite()
  counts=re.search(r'passed: (\d+)\s+failed: (\d+)\s+skipped: (\d+)\s+xfail: (\d+)',re.sub(r'\x1b\[[0-9;]*m','',result.stdout))
  self.assertIsNotNone(counts,result.stdout)
  total=sum(int(n) for n in counts.groups())
  expected=int(re.search(r'EXPECTED_MEMORY_CHECKS=(\d+)',(HERE/'test-memory.sh').read_text()).group(1))
  self.assertEqual(total,expected)

 def test_a_dropped_check_fails_the_count_guard(self):
  with tempfile.TemporaryDirectory() as directory:
   for name in ('test-memory.sh','lib.sh'):
    text=(HERE/name).read_text()
    if name=='test-memory.sh':text=re.sub(r'EXPECTED_MEMORY_CHECKS=\d+',lambda m:'EXPECTED_MEMORY_CHECKS=%d'%(int(m.group().split('=')[1])+1),text)
    (Path(directory)/name).write_text(text)
   fake=Path(directory)/'crewship';fake.write_text(FAKE);fake.chmod(0o700)
   env=dict(os.environ,CREWSHIP=str(fake),STATE=directory+'/state.json')
   result=subprocess.run(['bash',str(Path(directory)/'test-memory.sh')],env=env,text=True,capture_output=True)
  self.assertEqual(result.returncode,1)
  self.assertIn('FAIL the memory suite ran its expected number of checks',result.stdout)

 def test_crew_write_by_bare_ask_is_a_failure(self):
  result=self.run_suite(OBEY_BARE_ASK='1')
  self.assertEqual(result.returncode,1)
  self.assertIn('FAIL an unverified crew-memory request leaves CREW.md unchanged',result.stdout)

 def test_negative_that_never_ran_is_not_a_pass(self):
  result=self.run_suite(FAIL_NONLEAD_START='1')
  self.assertEqual(result.returncode,1)
  self.assertIn('FAIL a non-lead leaves crew-shared memory to the lead',result.stdout)

 def test_negative_whose_run_failed_is_not_a_pass(self):
  result=self.run_suite(NONLEAD_RUN_FAILED='1')
  self.assertEqual(result.returncode,1)
  self.assertIn('FAIL a non-lead leaves crew-shared memory to the lead',result.stdout)

 def test_write_failure_stays_red(self):
  result=self.run_suite(FAIL_WRITE='1')
  self.assertEqual(result.returncode,1)
  self.assertIn('memory write run completed',result.stdout)

 def test_failed_isolation_ask_cannot_pass_from_empty_output(self):
  result=self.run_suite(FAIL_ISOLATION='1')
  self.assertEqual(result.returncode,1)
  self.assertIn('cross-crew isolation recall run completed',result.stdout)

 def test_inventory_failure_stays_red_instead_of_unsupported_skip(self):
  result=self.run_suite(FAIL_INVENTORY='1')
  self.assertEqual(result.returncode,1)
  self.assertNotIn('SKIP',result.stdout)

if __name__=='__main__':unittest.main()
