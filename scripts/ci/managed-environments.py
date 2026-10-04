#!/usr/bin/env python3
"""Run required synthetic Docker fixtures; missing/skipped tests are failures.

Run from the repository root with Go, Docker and local alpine:3. Creates only
unique disposable test resources and cleans them up. No provider credentials,
paid requests, production mounts, registry resolution or global prune. The
activation fixture mounts only its temporary directories and test executable. CI prepares
its pinned Alpine fixture separately; local use never pulls or retags images.
"""
import json
import re
import subprocess
import sys
import time

PREFIX = 'github.com/crewship-ai/crewship/'
TESTS = {
    'internal/orchestrator': {'TestManagedLaunchRealDocker'},
    'internal/provider/docker': {
        'TestStagedStartRealDocker',
        'TestSandboxRuntimeRealDocker',
        'TestSandboxDisablesImageHealthcheckRealDocker',
        'TestCacheEvictionRealDockerRetag',
        'TestImageChange_RealHeartbeatSurvivesNewImageAdmission',
        'TestImageChange_RealIdleVerifierProtectsDetachedWork',
    },
    'internal/toolchain': {'TestQualificationThroughDockerRuntime'},
    'internal/devcontainer': {'TestProvisionImmutableArtifact_RealRebuild'},
}
STAGED_PACKAGE = 'internal/provider/docker'
STAGED = 'TestStagedStartRealDocker'
# A green parent cannot replace any mandatory acceptance scenario.
STAGED_CHILDREN = {
    'same_identity_positive_control',
    'controller_restart_environment',
    'failed_kernel_readback',
    'external_backup_ready_and_restart_race',
    'restart_races',
    'unknown_result',
    'concurrent_controller_reuse',
    'auto_restart',
    'legacy_opt_in',
    'FAIL_BOOTSTRAP',
    'selector_removed',
    'HANG_BOOTSTRAP',
    'unknown_attach_reservation',
    'workload_material_before_reservation',
    'delayed_fence',
    'failing_fence_helper',
    'mid_bootstrap_restart',
}
REQUIRED_TOP_LEVEL = {(PREFIX + package, name) for package, names in TESTS.items() for name in names}
REQUIRED_CHILDREN = {(PREFIX + STAGED_PACKAGE, STAGED + '/' + child)
                     for child in STAGED_CHILDREN}
REQUIRED = REQUIRED_TOP_LEVEL | REQUIRED_CHILDREN


def commands():
    # Preserve the original fixtures' three-minute budget. Staged startup has
    # its own bounded invocation; report elapsed time so its budget is measured
    # on each qualified runner rather than treating the ceiling as evidence.
    legacy = REQUIRED_TOP_LEVEL - {(PREFIX + STAGED_PACKAGE, STAGED)}
    pattern = '^(' + '|'.join(re.escape(name) for _, name in sorted(legacy)) + ')$'
    common = ['go', 'test', '-tags', 'integration', '-p', '1', '-count=1', '-json']
    return [common + ['-timeout=3m', '-run', pattern] + ['./' + package for package in TESTS],
            common + ['-timeout=12m', '-run', '^' + STAGED + '$', './' + STAGED_PACKAGE]]


def failures(events, returncode):
    started, passed, rejected = set(), set(), set()
    for event in events:
        key = (event.get('Package'), event.get('Test'))
        parent = (key[0], (key[1] or '').split('/')[0])
        if key not in REQUIRED:
            if parent in REQUIRED and event.get('Action') in ('skip', 'fail'):
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


def main():
    events = []
    malformed = False
    code = 0
    for command in commands():
        started = time.monotonic()
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
            invocation_code = process.wait()
        code = code or invocation_code
        print(f'Managed fixture invocation elapsed_seconds={time.monotonic() - started:.3f} '
              f'exit_code={invocation_code} command={command!r}', file=sys.stderr, flush=True)
    errors = failures(events, code)
    if malformed:
        errors.append('go test emitted invalid JSON evidence')
    for error in errors:
        print('ERROR: ' + error, file=sys.stderr)
    if not errors:
        print(f'All {len(REQUIRED_TOP_LEVEL)} required managed-environment Docker fixtures '
              f'and {len(REQUIRED_CHILDREN)} staged scenarios passed.')
    return bool(errors)


if __name__ == '__main__':
    sys.exit(main())
