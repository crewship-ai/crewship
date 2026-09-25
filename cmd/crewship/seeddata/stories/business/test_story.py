import importlib.util
import json
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unittest

HERE=Path(__file__).parent
spec=importlib.util.spec_from_file_location('business_story',HERE/'story.py')
story=importlib.util.module_from_spec(spec);spec.loader.exec_module(story)

class StoryTest(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup)
        story.ROOT=Path(self.temp.name)
        (story.ROOT/'bindings.json').write_text(json.dumps({s:{'identifier':f'DEMO-{i+1}'} for i,s in enumerate(['sales','finance','marketing','shipping'])}))

    def test_all_stories_have_one_finding_and_local_completion(self):
        for slug in ['sales','finance','marketing','shipping']:
            with self.subTest(slug=slug):
                first=story.run(slug,'check');self.assertTrue(first['pending']);self.assertEqual(len(json.loads(first['evidence'])),1)
                result=story.run(slug,'complete');self.assertEqual(result['summary']['verdict'],'Completed')
                again=story.run(slug,'complete');self.assertFalse(again['pending'])
                self.assertTrue((story.ROOT/'outbox'/f'{slug}.json').exists())
        with sqlite3.connect(story.ROOT/'demo.sqlite') as db:
            self.assertEqual(db.execute('SELECT count(*) FROM outcomes').fetchone()[0],4)

    def test_completion_recovers_issue_sync_without_duplicate_delivery(self):
        first=story.run('sales','complete')
        self.assertTrue(first['start_issue']);self.assertTrue(first['finish_issue'])
        # A retry after delivery must continue Issue synchronization, without a new approval.
        retry=story.run('sales','complete',decision='')
        self.assertTrue(retry['start_issue']);self.assertTrue(retry['approved'])
        story.run('sales','mark-progress')
        retry=story.run('sales','complete',decision='')
        self.assertFalse(retry['start_issue']);self.assertTrue(retry['finish_issue'])
        story.run('sales','mark-done')
        retry=story.run('sales','complete',decision='')
        self.assertFalse(retry['start_issue']);self.assertFalse(retry['finish_issue'])
        self.assertEqual(retry['summary']['verdict'],'Completed')
        self.assertEqual(len(list((story.ROOT/'outbox').glob('*.json'))),1)

    def test_keep_open_does_not_deliver(self):
        result=story.run('sales','complete',decision='keep_open')
        self.assertFalse(result['approved']);self.assertEqual(result['summary']['verdict'],'Kept open')
        self.assertFalse((story.ROOT/'outbox').exists());self.assertTrue(story.run('sales','check')['pending'])

    def test_no_finding_does_not_prepare_reply(self):
        s=story.story('sales')
        for row in s['rows']:row['replied']=True
        findings,_=story.evaluate(s)
        self.assertEqual(findings,[])

    def test_finance_does_not_claim_payment_after_reminder(self):
        result=story.run('finance','complete')
        self.assertEqual(result['records']['rows'][-1]['status'],'Reminder saved; payment still due')

    def test_agent_draft_is_used_and_explicitly_labelled(self):
        text='A grounded reply for the customer.'
        story.run('sales','draft',draft=text)
        prepared=story.run('sales','prepare')
        self.assertEqual(prepared['draft'],text);self.assertEqual(prepared['draft_source'],'AI draft')

    def test_missing_bindings_fail_instead_of_writing_unlinked_evidence(self):
        (story.ROOT/'bindings.json').unlink()
        with self.assertRaises(FileNotFoundError):story.run('sales','complete')

    def test_mcp_integration_reads_local_records(self):
        requests=[{'jsonrpc':'2.0','id':1,'method':'initialize','params':{}},{'jsonrpc':'2.0','id':2,'method':'tools/call','params':{'name':'read_demo_records','arguments':{'project':'shipping'}}}]
        proc=subprocess.run([sys.executable,str(HERE/'connector.py')],input='\n'.join(map(json.dumps,requests))+'\n',text=True,capture_output=True,timeout=3,check=True)
        replies=[json.loads(line) for line in proc.stdout.splitlines()]
        self.assertEqual(replies[0]['result']['serverInfo']['name'],'harbor-goods-demo')
        self.assertEqual(len(json.loads(replies[1]['result']['content'][0]['text'])['rows']),5)

if __name__=='__main__':unittest.main()
