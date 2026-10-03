#!/usr/bin/env python3
"""Run required synthetic Docker fixtures; missing/skipped tests are failures.

Run from the repository root with Go, Docker and local alpine:3. Creates only
unique disposable test resources and cleans them up. No provider credentials,
paid requests, host mounts, registry resolution or global prune. CI prepares
its pinned Alpine fixture separately; local use never pulls or retags images.
"""
import json
import re
import subprocess
import sys

PREFIX = 'github.com/crewship-ai/crewship/'
TESTS = {
    'internal/provider/docker': {
        'TestSandboxRuntimeRealDocker',
        'TestSandboxDisablesImageHealthcheckRealDocker',
        'TestCacheEvictionRealDockerRetag',
    },
    'internal/toolchain': {'TestQualificationThroughDockerRuntime'},
    'internal/devcontainer': {'TestProvisionImmutableArtifact_RealRebuild'},
}
REQUIRED = {(PREFIX + package, name) for package, names in TESTS.items() for name in names}


def failures(events, returncode):
    started, passed, rejected = set(), set(), set()
    for event in events:
        key = (event.get('Package'), event.get('Test'))
        if key not in REQUIRED:
            continue
        action = event.get('Action')
        if action == 'run':
            started.add(key)
        elif action == 'pass':
            passed.add(key)
        elif action in ('skip', 'fail'):
            rejected.add(key)
    errors = ['go test failed'] if returncode else []
    errors.extend(f'{package}/{name}: required test did not run and pass'
                  for package, name in sorted(REQUIRED - (started & passed)))
    errors.extend(f'{package}/{name}: required test was skipped or failed'
                  for package, name in sorted(rejected))
    return errors


def main():
    pattern = '^(' + '|'.join(re.escape(name) for _, name in sorted(REQUIRED)) + ')$'
    command = ['go', 'test', '-tags', 'integration', '-p', '1', '-count=1',
               '-timeout=3m', '-json', '-run', pattern]
    command.extend('./' + package for package in TESTS)
    events = []
    malformed = False
    with subprocess.Popen(command, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                          text=True) as process:
        for line in process.stdout:
            print(line, end='', flush=True)
            try:
                event = json.loads(line)
                if not isinstance(event, dict):
                    malformed = True
                elif event.get('Action') in ('run', 'pass', 'skip', 'fail'):
                    events.append(event)
            except json.JSONDecodeError:
                malformed = True
        code = process.wait()
    errors = failures(events, code)
    if malformed:
        errors.append('go test emitted invalid JSON evidence')
    for error in errors:
        print('ERROR: ' + error, file=sys.stderr)
    if not errors:
        print(f'All {len(REQUIRED)} required managed-environment Docker fixtures passed.')
    return bool(errors)


if __name__ == '__main__':
    sys.exit(main())
