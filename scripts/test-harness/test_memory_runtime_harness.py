"""Run the real shell suite against a deterministic CLI boundary fixture."""
import os
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
  fact=re.search(r'(?:CODENAME|SPRINT|ONCALL)-[A-Z0-9]+',prompt).group()
  key='agent' if 'tier=AGENT' in prompt else 'crew' if 'tier=CREW' in prompt else 'pins'
  data[key]=fact;state.write_text(json.dumps(data));reply='persisted'
 elif 'engineering' in prompt:
  if os.environ.get('FAIL_ISOLATION')=='1':sys.exit(9)
  reply='NOT_IN_MEMORY'
 else:
  key='agent' if 'project label' in prompt else 'crew' if 'sprint label' in prompt else 'pins'
  reply=data.get(key,'NOT_IN_MEMORY')
 Path(args[args.index('--save')+1]).write_text(reply)
 sys.exit(0)
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
