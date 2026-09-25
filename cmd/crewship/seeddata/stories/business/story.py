#!/usr/bin/env python3
"""Local, repeatable business demos. Only the marketing probe uses loopback HTTP."""
import argparse
import contextlib
import json
import os
from pathlib import Path
import sqlite3
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.request import urlopen

ROOT = Path(os.environ.get('CREWSHIP_DEMO_ROOT', '/crew/shared/demo/business'))
CATALOGUE = Path(__file__).with_name('catalogue.json')


def story(slug):
    return next(s for s in json.loads(CATALOGUE.read_text()) if s['slug'] == slug)


def connect():
    ROOT.mkdir(parents=True, exist_ok=True)
    db = sqlite3.connect(ROOT / 'demo.sqlite', timeout=10)
    db.execute('CREATE TABLE IF NOT EXISTS outcomes (story TEXT PRIMARY KEY, body TEXT NOT NULL)')
    db.execute('CREATE TABLE IF NOT EXISTS drafts (story TEXT PRIMARY KEY, body TEXT NOT NULL)')
    db.execute('CREATE TABLE IF NOT EXISTS issue_sync (story TEXT PRIMARY KEY, progressed INTEGER NOT NULL DEFAULT 0, closed INTEGER NOT NULL DEFAULT 0)')
    return db


def narrative(title, *text):
    return {'verdict': title, 'blocks': [{'kind': 'paragraph', 'text': t} for t in text]}


def evaluate(s, completed=False):
    rows = s['rows']
    if s['slug'] == 'sales':
        findings = [r for r in rows if not r['replied'] and r['hours'] >= 24 and not completed]
        table = [{'id':r['id'], 'customer':r['customer'], 'detail':r['request'], 'status':'Reply saved in demo outbox' if completed and not r['replied'] else ('Replied' if r['replied'] else f"Waiting {r['hours']} hours")} for r in rows]
    elif s['slug'] == 'finance':
        paid = {}
        for p in s['payments']:
            paid[p['reference']] = paid.get(p['reference'], 0) + p['amount']
        findings = [r for r in rows if paid.get(r['id'],0) < r['amount'] and r['days_overdue'] > 0 and not completed]
        table = [{'id':r['id'], 'customer':r['customer'], 'detail':f"EUR {r['amount']} · paid EUR {paid.get(r['id'],0)}", 'status':'Paid' if paid.get(r['id'],0)>=r['amount'] else ('Reminder saved; payment still due' if completed else f"Overdue {r['days_overdue']} days")} for r in rows]
    elif s['slug'] == 'marketing':
        delivered = {r['id'] for r in rows} if completed else set(s['delivered'])
        findings = [r for r in rows if r['received'] and r['id'] not in delivered]
        table = [{'id':r['id'], 'customer':r['customer'], 'detail':'Form accepted', 'status':'Delivered' if r['id'] in delivered else 'Delivery missing'} for r in rows]
    elif s['slug'] == 'shipping':
        findings = [r for r in rows if r['late_days']>0 and r['claim_days_left']>0 and r['refund']>0 and not completed]
        table = [{'id':r['id'], 'customer':'Harbor Goods', 'detail':f"{r['late_days']} days late · EUR {r['refund']}", 'status':('Claim saved locally' if completed else f"Claim within {r['claim_days_left']} days") if r['late_days'] else 'On time'} for r in rows]
    else:
        raise ValueError('Unknown story evaluator')
    return findings, {'columns':[{'key':k,'label':v} for k,v in [('id','Record'),('customer','Customer'),('detail','Details'),('status','Status')]],'rows':table}


def marketing_probe(table):
    # An actual HTTP request and response, entirely inside this crew. No public URL.
    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            body = json.dumps(table).encode()
            self.send_response(200); self.send_header('Content-Type','application/json'); self.end_headers(); self.wfile.write(body)
        def log_message(self, *args):
            pass
    with HTTPServer(('127.0.0.1',0), Handler) as server:
        worker=threading.Thread(target=server.handle_request,daemon=True); worker.start()
        with urlopen(f'http://127.0.0.1:{server.server_port}/deliveries',timeout=3) as response:
            result=json.load(response)
        worker.join(timeout=3)
        return result


def run(slug, action, draft='', decision='approve'):
    s=story(slug)
    bindings=json.loads((ROOT/'bindings.json').read_text())
    identifier=bindings[slug]['identifier']
    with contextlib.closing(connect()) as db:
        done=db.execute('SELECT body FROM outcomes WHERE story=?',(slug,)).fetchone()
        saved=db.execute('SELECT body FROM drafts WHERE story=?',(slug,)).fetchone()
        sync=db.execute('SELECT progressed,closed FROM issue_sync WHERE story=?',(slug,)).fetchone() or (0,0)
        if action in ('mark-progress','mark-done'):
            if not done:
                raise ValueError('No approved local artifact to synchronize')
            db.execute('INSERT OR IGNORE INTO issue_sync(story) VALUES (?)',(slug,))
            field='progressed' if action=='mark-progress' else 'closed'
            db.execute(f'UPDATE issue_sync SET {field}=1 WHERE story=?',(slug,)); db.commit()
        findings, table=evaluate(s,bool(done))
        if action=='check' and slug=='marketing':
            table=marketing_probe(table)
        text=saved[0] if saved else s['sample_draft']
        kind='AI draft' if saved else 'Prepared sample template'
        pending=bool(findings)
        if action=='draft':
            if not draft.strip():
                raise ValueError('Agent returned an empty draft')
            db.execute('INSERT INTO drafts VALUES (?,?) ON CONFLICT(story) DO UPDATE SET body=excluded.body',(slug,draft)); db.commit()
            text=draft; kind='AI draft'
        if action=='complete' and pending and decision=='approve':
            # One artifact per story, even if a run is retried after a partial failure.
            artifact={'story':slug,'issue':identifier,'text':text,'source':kind,'delivery':'local demo only'}
            db.execute('INSERT OR IGNORE INTO outcomes VALUES (?,?)',(slug,json.dumps(artifact))); db.commit()
            out=ROOT/'outbox'; out.mkdir(exist_ok=True)
            tmp=out/(slug+'.tmp'); tmp.write_text(json.dumps(artifact,indent=2)+'\n'); tmp.replace(out/(slug+'.json'))
            done=(json.dumps(artifact),)
            findings,table=evaluate(s,True)
        if action=='complete' and decision!='approve' and not done:
            summary=narrative('Kept open','You chose to keep this case open. Nothing was delivered. The Issue remains available for another attempt.')
        elif done:
            summary=narrative('Completed',s['result']+'.',f"Issue {identifier}. Evidence: /crew/shared/demo/business/outbox/{slug}.json. No external message or claim was sent.")
        elif action=='draft':
            summary=narrative('AI draft ready',text,'Review it using the main action. Nothing has been sent.')
        elif action=='prepare' and pending:
            summary=narrative('Waiting for your decision',kind+': '+text,'Open Inbox to approve or keep this case open. No external message will be sent.')
        else:
            summary=narrative('Needs attention' if findings else 'No action needed',f"{len(findings)} of {len(s['rows'])} records need attention.",s['problem'] if findings else 'No pending follow-up was found.',f'Related Issue: {identifier}.')
        return dict(pending=pending,issue=identifier,records=table,summary=summary,draft=text,draft_source=kind,approved=bool(done),start_issue=bool(done) and not bool(sync[0]),finish_issue=bool(done) and not bool(sync[1]),evidence=json.dumps(findings),comment=summary['verdict']+'. '+ ' '.join(b['text'] for b in summary['blocks']))


if __name__=='__main__':
    p=argparse.ArgumentParser(); p.add_argument('story'); p.add_argument('action',choices=['check','prepare','draft','complete','mark-progress','mark-done']); p.add_argument('--draft',default=''); p.add_argument('--decision',default='approve'); args=p.parse_args()
    try:
        print(json.dumps(run(args.story,args.action,args.draft,args.decision)))
    except Exception as exc:
        print(f'Demo story could not run ({type(exc).__name__}). Check the fixture and story bindings in Crew files.',file=sys.stderr)
        sys.exit(1)
