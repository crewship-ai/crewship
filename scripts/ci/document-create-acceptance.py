#!/usr/bin/env python3
"""Require native execution of a managed Document CLI acceptance scenario."""
import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
MAX_LINE = 1 << 20
EVIDENCE_DIR = Path('/tmp/crewship-pages-ci')


class Scenario:
    """One opt-in Go acceptance test that must execute and pass exactly once."""

    def __init__(self, package, directory, test, opt_in, output, measurements=None):
        self.package = package
        self.test = test
        self.opt_in = opt_in
        self.output = output
        # Optional JSON file the test itself writes; the runner names it and
        # requires a fresh JSON object there after the pass.
        self.measurements = measurements
        self.command = ['go', 'test', '-json', '-tags', 'docs_foundation', directory,
                        '-run', '^' + test + '$', '-count=1', '-timeout=5m']


SCENARIOS = {
    'create': Scenario('github.com/crewship-ai/crewship/internal/api', './internal/api',
                       'TestDocumentCreationManagedRouterSidecarCLI',
                       {'CREWSHIP_DOCUMENT_CREATE': '1'}, 'document-create.jsonl'),
    # #5130/#5136: lost acks, engine crash and stale-engine fences through the
    # real CLI binary, Router, Go authority and Node docs-service.
    'durability': Scenario('github.com/crewship-ai/crewship/cmd/crewship', './cmd/crewship',
                           'TestAcceptance_DocumentDurabilityRealEngine',
                           {'TEST_DOCUMENT_DURABILITY': '1'}, 'document-durability.jsonl',
                           measurements='document-durability-measurements.json'),
}
# The original single-scenario names remain the create scenario.
PACKAGE = SCENARIOS['create'].package
TEST = SCENARIOS['create'].test
COMMAND = SCENARIOS['create'].command


class Evidence:
    def __init__(self, scenario=None):
        self.scenario = SCENARIOS['create'] if scenario is None else scenario
        self.passes = 0
        self.failed = False

    def accept(self, line):
        event = json.loads(line.decode('utf-8'))
        if not isinstance(event, dict) or not isinstance(event.get('Action'), str):
            raise ValueError('Go JSON event requires an action')
        self.failed |= event['Action'] in {'skip', 'fail', 'build-fail'}
        self.passes += (event['Action'] == 'pass' and event.get('Package') == self.scenario.package
                        and event.get('Test') == self.scenario.test)

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


def reserve_measurements(path):
    """Create the measurements file empty so no prior or redirected file counts."""
    with open_evidence(path):
        pass
    return Path(path).absolute()


def measured(path):
    try:
        with open(path, 'rb') as report:
            value = json.loads(report.read(MAX_LINE + 1).decode('utf-8'))
    except (OSError, ValueError, RecursionError):
        return False
    # The recovery time exists only once the crash scenario has completed.
    return isinstance(value, dict) and isinstance(value.get('recover_spawn_to_ready_ms'), int)


def run(output, stream=None, scenario='create', measurements=None):
    stream = sys.stdout.buffer if stream is None else stream
    scenario = SCENARIOS[scenario]
    evidence = Evidence(scenario)
    malformed = False
    env = dict(os.environ, **scenario.opt_in)
    with open_evidence(output) as artifact:
        if scenario.measurements:
            if measurements is None:
                measurements = Path(output).absolute().with_name(scenario.measurements)
            measurements = reserve_measurements(measurements)
            env['TEST_DOCUMENT_DURABILITY_EVIDENCE'] = str(measurements)
        process = subprocess.Popen(scenario.command, cwd=ROOT, env=env,
                                   stdout=subprocess.PIPE, start_new_session=True)
        try:
            while line := process.stdout.readline(MAX_LINE + 1):
                if len(line) > MAX_LINE:
                    print('Document acceptance Go JSON line exceeds 1 MiB', file=sys.stderr)
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
        print('Document acceptance requires exactly one passing test and no malformed/skip/fail evidence', file=sys.stderr)
        return 1
    if scenario.measurements and not measured(measurements):
        print('Document acceptance passed without writing its measurements JSON object', file=sys.stderr)
        return 1
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--scenario', choices=sorted(SCENARIOS), default='create')
    parser.add_argument('--output', type=Path)
    args = parser.parse_args()
    output = args.output or EVIDENCE_DIR / SCENARIOS[args.scenario].output

    def interrupted(_signum, _frame):
        raise InterruptedError('Document acceptance interrupted')

    signal.signal(signal.SIGTERM, interrupted)
    try:
        return run(output, scenario=args.scenario)
    except (OSError, ValueError, KeyboardInterrupt) as error:
        print('Document acceptance failed: ' + str(error), file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
