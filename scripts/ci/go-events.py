#!/usr/bin/env python3
"""Keep Go's normal output and retain machine-readable test timing evidence."""
import json
import os
from pathlib import Path
import sys

directory = Path(os.environ.get('CI_RESULTS_DIR', '.ci-results'))
directory.mkdir(parents=True, exist_ok=True)
timings = []
skips = 0
with (directory / 'go-test.jsonl').open('w') as raw:
    for line in sys.stdin:
        raw.write(line)
        event = json.loads(line)
        if event.get('Output'):
            print(event['Output'], end='', flush=True)
        if event.get('Action') == 'skip':
            skips += 1
        if event.get('Action') in ('pass', 'fail') and 'Elapsed' in event:
            timings.append({k: event[k] for k in ('Package', 'Test', 'Action', 'Elapsed') if k in event})
timings.sort(key=lambda t: t['Elapsed'], reverse=True)
(directory / 'timings.json').write_text(json.dumps({'skips': skips, 'timings': timings}, indent=2) + '\n')
with open(os.environ.get('GITHUB_STEP_SUMMARY', os.devnull), 'a') as out:
    print('### Go test timings\n\n| Package / test | Seconds | Result |\n|---|---:|---|', file=out)
    for t in timings[:25]:
        name = t['Package'] + (' / ' + t['Test'] if 'Test' in t else '')
        print(f"| {name.replace('|', '/')} | {t['Elapsed']:.3f} | {t['Action']} |", file=out)
    print(f'\nSkipped events: {skips}. Full evidence is attached to this job.', file=out)
