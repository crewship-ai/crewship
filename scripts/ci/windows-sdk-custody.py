#!/usr/bin/env python3
"""Require native Windows ACL and real file-staging receipts, never cross-build only."""
import json
import os
import subprocess
import sys

PACKAGE = 'github.com/crewship-ai/crewship/internal/provider/runtimestage'
ROOTS = {'TestAgentCLIWindowsDescriptorCustody',
         'TestAgentCLIWindowsRealStagingCleanAndUpgrade',
         'TestAgentCLIWindowsRejectsCustodyChanges'}
REQUIRED = ROOTS | {
    'TestAgentCLIWindowsDescriptorCustody/' + case
    for case in ('owner-file', 'public-file-read', 'foreign-file-write',
                 'foreign-file-delete', 'parent-public-read',
                 'parent-create-subdirectory', 'parent-delete-child',
                 'private-directory-public-read', 'inherit-only-not-effective',
                 'inherited-effective-write')
} | {
    'TestAgentCLIWindowsRejectsCustodyChanges/' + case
    for case in ('source-write-grant', 'source-hardlink', 'source-symlink', 'ancestor-delete-child',
                 'private-read-grant', 'private-junction', 'generation-write-grant')
}


def failures(events, returncode):
    started, passed, rejected = set(), set(), set()
    for event in events:
        if event.get('Package') != PACKAGE:
            continue
        name, action = event.get('Test'), event.get('Action')
        if name not in REQUIRED:
            continue
        if action == 'run':
            started.add(name)
        elif action == 'pass':
            passed.add(name)
        elif action in ('fail', 'skip'):
            rejected.add(name)
    errors = ['Windows go test failed'] if returncode else []
    errors += [name + ': required native test did not run and pass'
               for name in sorted(REQUIRED - (started & passed))]
    errors += [name + ': required native test failed or skipped'
               for name in sorted(rejected)]
    return errors


def main():
    if os.name != 'nt':
        print('ERROR: native Windows execution required; cross-compilation is insufficient', file=sys.stderr)
        return 1
    command = ['go', 'test', '-json', '-count=1', '-timeout=5m', '-run',
               '^(' + '|'.join(sorted(ROOTS)) + ')$', './internal/provider/runtimestage']
    try:
        result = subprocess.run(command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                text=True, timeout=480, check=False)
    except subprocess.TimeoutExpired:
        print('ERROR: native Windows qualification exceeded its execution bound', file=sys.stderr)
        return 1
    print(result.stdout, end='')
    events, malformed = [], False
    for line in result.stdout.splitlines():
        if not line.strip():
            continue
        try:
            event = json.loads(line)
            if not isinstance(event, dict):
                raise ValueError('event is not an object')
            events.append(event)
        except (ValueError, TypeError):
            malformed = True
    errors = failures(events, result.returncode)
    if malformed:
        errors.append('native Windows qualification emitted invalid JSON evidence')
    for error in errors:
        print('ERROR: ' + error, file=sys.stderr)
    if not errors:
        print(f'All {len(REQUIRED)} native Windows ACL/staging receipts passed.')
    return bool(errors)


if __name__ == '__main__':
    sys.exit(main())
