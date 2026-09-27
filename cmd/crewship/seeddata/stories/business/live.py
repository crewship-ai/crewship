#!/usr/bin/env python3
"""One bounded container process: sample cgroup memory, publish to one Page webhook."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import subprocess
import sys
import time
from urllib.request import Request, urlopen

ROOT=Path('/crew/shared/demo/business')

def sample():
    value=round(int(Path('/sys/fs/cgroup/memory.current').read_text())/1024/1024)
    return value

def run():
    with (ROOT/'live.lock').open('w') as lock:
        try: fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
        except BlockingIOError: return
        os.chmod(ROOT/'live-private.json',0o600)
        history=[]; deadline=time.monotonic()+900
        while time.monotonic()<deadline and not (ROOT/'live.stop').exists():
            try:
                # Reseeding rotates the capability; pick up the new file without
                # leaving the existing singleton stuck on a revoked token.
                config=json.loads((ROOT/'live-private.json').read_text())
                history=(history+[sample()])[-20:]
                body=json.dumps({'value':history[-1],'unit':'MB','sparkline':history}).encode()
                with urlopen(Request(config['url'],data=body,headers={'Content-Type':'application/json'},method='POST'),timeout=5) as response:
                    if response.status not in (200,201,202,204): raise RuntimeError('push refused')
            except Exception as exc:
                print('Live sample not published: '+type(exc).__name__,file=sys.stderr,flush=True)
            time.sleep(5)

def main(action):
    ROOT.mkdir(parents=True,exist_ok=True)
    if action=='worker': run(); return
    if action=='stop':
        (ROOT/'live.stop').touch()
        title='Live monitor stopping';text='The local process stops within ten seconds. The last measurement stays visible.'
    else:
        if not (ROOT/'live-private.json').exists():
            raise RuntimeError('Live callback missing. Re-run the demo seed to configure this Page.')
        (ROOT/'live.stop').unlink(missing_ok=True)
        with (ROOT/'live.log').open('a') as log:
            subprocess.Popen([sys.executable,str(Path(__file__).resolve()),'worker'],stdin=subprocess.DEVNULL,stdout=log,stderr=log,start_new_session=True,close_fds=True)
        title='Live monitor started';text='A single Python process measures container memory every five seconds for 15 minutes. No LLM and no scheduled routine is used for the measurements.'
    print(json.dumps({'verdict':title,'blocks':[{'kind':'paragraph','text':text}]}))

if __name__=='__main__':
    parser=argparse.ArgumentParser();parser.add_argument('action',choices=['start','stop','worker']);args=parser.parse_args()
    try: main(args.action)
    except Exception as exc:
        print(str(exc) if isinstance(exc,RuntimeError) else type(exc).__name__,file=sys.stderr);sys.exit(1)
