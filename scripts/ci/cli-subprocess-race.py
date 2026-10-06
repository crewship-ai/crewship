#!/usr/bin/env python3
"""Run named CLI scenarios with instrumented test and acceptance binaries."""
import json
import os
from pathlib import Path
import re
import subprocess
import sys

TESTS = (
    'TestAcceptanceBinaryBuildMode',
    'TestAcceptance_AuthPairWaitsForRedemption',
    'TestAcceptance_AuthPairTimeoutIsNotSuccess',
    'TestAcceptance_AuthPairFailsFastWhenCodeExpires',
    'TestConversationSearchAcceptance',
    'TestConversationSearchAcceptance_JSON',
    'TestAcceptance_AuthPairRequiresAuth',
    'TestAcceptance_AuthPairJSONOutput',
)
PACKAGE = 'github.com/crewship-ai/crewship/cmd/crewship'


def missing_tests(events):
    passed = {e.get('Test') for e in events
              if e.get('Package') == PACKAGE and e.get('Action') == 'pass'}
    return sorted(set(TESTS) - passed)


def race_reports(directory):
    return sorted(p for p in directory.glob('race.*') if p.is_file() and p.stat().st_size)


def main():
    output = Path('.ci-results')
    output.mkdir(exist_ok=True)
    logs = (output / 'cli-subprocess-race').resolve()
    logs.mkdir(exist_ok=True)
    # Only this runner owns these reports; stale reports must not affect reruns.
    for previous in logs.glob('race.*'):
        if previous.is_file():
            previous.unlink()
    env = dict(os.environ, TEST_CREWSHIP_CLI_RACE='1', CGO_ENABLED='1',
               GORACE=f'halt_on_error=1 exitcode=66 log_path={logs / "race"}')
    selection = '^(?:' + '|'.join(re.escape(t) for t in TESTS) + ')$'
    events = []
    with (output / 'cli-subprocess-race.jsonl').open('w') as artifact:
        process = subprocess.Popen(
            ['go', 'test', '-json', '-race', '-count=1', '-timeout=10m',
             '-run', selection, './cmd/crewship'], env=env,
            stdout=subprocess.PIPE, text=True)
        for line in process.stdout:
            artifact.write(line)
            print(line, end='', flush=True)
            try:
                events.append(json.loads(line))
            except ValueError:
                # Non-JSON output is not execution evidence.
                pass
        status = process.wait()
    missing = missing_tests(events)
    if missing:
        print('::error::CLI subprocess race tests did not pass: ' + ', '.join(missing))
    reports = race_reports(logs)
    if reports:
        print('::error::Race reports from acceptance processes: ' + ', '.join(str(p) for p in reports))
    return status or bool(missing or reports)


if __name__ == '__main__':
    sys.exit(main())
