#!/usr/bin/env python3
"""Run required synthetic Docker fixtures; missing/skipped tests are failures.

Run from the repository root with Go, Docker and local alpine:3. Creates only
unique disposable test resources and cleans them up. No provider credentials,
paid requests, production mounts, registry resolution or global prune. The
activation fixture mounts only its temporary directories and test executable. CI prepares
its pinned Alpine fixture separately; run this script as non-root with sudo
available only for TestManagedLaunchRealDocker; local use never pulls or retags images.
"""
import json
import os
import re
import subprocess
import sys

PREFIX = 'github.com/crewship-ai/crewship/'
TESTS = {
    'internal/stagedstart': {'TestKeeperControlRealDocker'},
    'internal/orchestrator': {'TestManagedLaunchRealDocker'},
    'internal/provider/docker': {
        'TestSandboxRuntimeRealDocker',
        'TestStagedQualificationRealDocker',
        'TestSandboxDisablesImageHealthcheckRealDocker',
        'TestCacheEvictionRealDockerRetag',
        'TestImageChange_RealHeartbeatSurvivesNewImageAdmission',
        'TestImageChange_RealIdleVerifierProtectsDetachedWork',
    },
    'internal/toolchain': {'TestQualificationThroughDockerRuntime'},
    'internal/devcontainer': {'TestProvisionImmutableArtifact_RealRebuild'},
}
REQUIRED_TOP_LEVEL = {(PREFIX + package, name) for package, names in TESTS.items() for name in names}
# Optional explicit child catalog; a green parent cannot replace its evidence.
CHILDREN = {}
REQUIRED_CHILDREN = {(PREFIX + package, parent + '/' + child)
                     for (package, parent), children in CHILDREN.items() for child in children}
REQUIRED = REQUIRED_TOP_LEVEL | REQUIRED_CHILDREN


def failures(events, returncode):
    started, passed, rejected = set(), set(), set()
    for event in events:
        key = (event.get('Package'), event.get('Test'))
        parent = (key[0], (key[1] or '').split('/')[0])
        if key not in REQUIRED:
            if parent in REQUIRED_TOP_LEVEL and event.get('Action') in ('skip', 'fail'):
                rejected.add(key)
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


def commands():
    # Preserve the original non-root profile for all existing Docker fixtures.
    # Only the new launcher fixture needs a host UID distinct from agent 1001.
    legacy = {package: names for package, names in TESTS.items()
              if package != 'internal/orchestrator'}
    result = []
    for packages, prefix in (
            (legacy, []),
            ({'internal/orchestrator': TESTS['internal/orchestrator']},
             ['sudo', 'env', 'PATH=' + os.environ['PATH'], 'GOTOOLCHAIN=local'])):
        pattern = '^(' + '|'.join(re.escape(name) for names in packages.values()
                                 for name in sorted(names)) + ')$'
        command = prefix + ['go', 'test', '-tags', 'integration', '-p', '1', '-count=1',
                            '-timeout=3m', '-json', '-run', pattern]
        command.extend('./' + package for package in packages)
        result.append(command)
    return result


def main():
    if os.geteuid() == 0:
        print('ERROR: run this suite as non-root; only the managed launcher test is elevated.',
              file=sys.stderr)
        return True
    events = []
    malformed = False
    code = 0
    for command in commands():
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
            code = process.wait() or code
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
