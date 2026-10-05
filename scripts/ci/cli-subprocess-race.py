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
    'TestStreamChatRun_ResumesFromLastSeqAfterDrop',
    'TestStreamChatRun_AccessRevokedIsFatalNotRetried',
)
PACKAGE = 'github.com/crewship-ai/crewship/cmd/crewship'


def missing_tests(events):
    passed = {e.get('Test') for e in events
              if e.get('Package') == PACKAGE and e.get('Action') == 'pass'}
    return sorted(set(TESTS) - passed)


def main():
    output = Path('.ci-results')
    output.mkdir(exist_ok=True)
    env = dict(os.environ, TEST_CREWSHIP_CLI_RACE='1', CGO_ENABLED='1',
               GORACE='halt_on_error=1 exitcode=66')
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
    return status or bool(missing)


if __name__ == '__main__':
    sys.exit(main())
