#!/usr/bin/env python3
"""Require native execution of the managed Document creation CLI scenario."""
import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
PACKAGE = 'github.com/crewship-ai/crewship/internal/api'
TEST = 'TestDocumentCreationManagedRouterSidecarCLI'
MAX_LINE = 1 << 20
COMMAND = ['go', 'test', '-json', '-tags', 'docs_foundation', './internal/api',
           '-run', '^' + TEST + '$', '-count=1', '-timeout=5m']


class Evidence:
    def __init__(self):
        self.passes = 0
        self.failed = False

    def accept(self, line):
        event = json.loads(line.decode('utf-8'))
        if not isinstance(event, dict) or not isinstance(event.get('Action'), str):
            raise ValueError('Go JSON event requires an action')
        self.failed |= event['Action'] in {'skip', 'fail', 'build-fail'}
        self.passes += (event['Action'] == 'pass' and event.get('Package') == PACKAGE
                        and event.get('Test') == TEST)

    def passed(self, status):
        return status == 0 and not self.failed and self.passes == 1


def open_evidence(output):
    output = Path(output).absolute()
    # Refuse redirection through a symlink, and never truncate a prior report.
    for parent in output.parents:
        if parent.is_symlink():
            raise ValueError('evidence directory must not traverse a symlink')
    output.parent.mkdir(parents=True, exist_ok=True)
    directory = os.open(output.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        descriptor = os.open(output.name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
                             0o600, dir_fd=directory)
    finally:
        os.close(directory)
    return os.fdopen(descriptor, 'wb')


def run(output, stream=None):
    stream = sys.stdout.buffer if stream is None else stream
    evidence = Evidence()
    malformed = False
    with open_evidence(output) as artifact:
        process = subprocess.Popen(COMMAND, cwd=ROOT,
                                   env=dict(os.environ, CREWSHIP_DOCUMENT_CREATE='1'),
                                   stdout=subprocess.PIPE, start_new_session=True)
        try:
            while line := process.stdout.readline(MAX_LINE + 1):
                if len(line) > MAX_LINE:
                    print('Document create Go JSON line exceeds 1 MiB', file=sys.stderr)
                    return 1
                artifact.write(line)
                artifact.flush()
                stream.write(line)
                stream.flush()
                try:
                    evidence.accept(line)
                except (ValueError, RecursionError):
                    malformed = True
            status = process.wait()
        finally:
            # Popen establishes this session before exec: cancellation or a
            # failed Go parent must not leave its test/Node descendants alive.
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.stdout.close()
            process.wait()
    if status:
        return status if status > 0 else 128 - status
    if malformed or not evidence.passed(status):
        print('Document create requires exactly one passing test and no malformed/skip/fail evidence', file=sys.stderr)
        return 1
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, default=Path('/tmp/crewship-pages-ci/document-create.jsonl'))
    args = parser.parse_args()

    def interrupted(_signum, _frame):
        raise InterruptedError('Document create acceptance interrupted')

    signal.signal(signal.SIGTERM, interrupted)
    try:
        return run(args.output)
    except (OSError, ValueError, KeyboardInterrupt) as error:
        print('Document create acceptance failed: ' + str(error), file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
