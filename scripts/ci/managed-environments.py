#!/usr/bin/env python3
"""Run required synthetic runtime fixtures; missing/skipped tests are failures.

Run from the repository root with Go, tmux, Docker and local alpine:3. Creates only
unique disposable test resources and cleans them up. No provider credentials,
paid requests, production mounts, registry resolution or global prune. The
activation fixture mounts only its temporary directories and test executable. CI prepares
its pinned Alpine fixture separately; run this script as non-root with sudo
available for the managed launcher fixture and isolated UID65534 qualification; local use never pulls or retags images.
"""
import json
import os
import re
import subprocess
import sys
import stat
import tempfile
from contextlib import contextmanager

PREFIX = 'github.com/crewship-ai/crewship/'
TESTS = {
    'cmd/crewship-sidecar': {'TestDetachedLaunchSurvivesReaderAndRejectsDuplicate'},
    'internal/managedlaunch': {'TestNativeLauncherConformanceRealDocker'},
    'internal/orchestrator': {'TestManagedLaunchRealDocker'},
    'internal/provider/docker': {
        'TestSandboxRuntimeRealDocker',
        'TestSandboxDisablesImageHealthcheckRealDocker',
        'TestCacheEvictionRealDockerRetag',
        'TestImageChange_RealHeartbeatSurvivesNewImageAdmission',
        'TestImageChange_RealIdleVerifierProtectsDetachedWork',
    },
    'internal/toolchain': {'TestQualificationThroughDockerRuntime'},
    'internal/devcontainer': {'TestProvisionImmutableArtifact_RealRebuild'},
}
HOST_TESTS = {
    'internal/runoutput': {
        'TestHostCaptureSeparatesStreamsScrubsAndRejectsDuplicate',
        'TestHostCaptureSurvivesActualControllerKill',
        'TestHostCaptureNaturalCompletionWinsLateCancellation',
        'TestHostCapacityReservationsSerializeAndCollectionReleases',
        'TestHostCapacitySettlesOnlyValidatedTerminalOutput',
        'TestReplayRetainsStableKeysAcrossLostProjectionAcknowledgements',
        'TestReplayRefusesRewrittenAcknowledgedPrefix',
        'TestReplayCleansOnlyPrivateKnownCrashTemps',
        'TestHostRetentionResumesAfterLastSourceUnlinkBeforeReservationRelease',
    },
    'internal/orchestrator': {
        'TestHostCaptureQualificationFallsBackOnlyWhenUnsupported',
        'TestHostRunAgentCompletesThroughDetachedHelperAndPreservesLiteralLaunch',
        'TestHostRunAgentObserverCancellationRetainsHomeThenRecoversResult',
        'TestRetainedAnalysisUsesSemanticFailureInsteadOfZeroExit',
        'TestRetainedAnalysisIgnoresStderrAndRequiresStructuredTerminal',
        'TestRetainedAuthoritativeStatusMirrorsOverLocalTerminalObservation',
        'TestHostForcedLeaderExitRetainsOccupancyUntilWholeContainerAbsent',
    },
    'internal/provider/docker': {
        'TestHostCaptureDockerSurvivesControllerSIGKILL',
        'TestHostCaptureDockerStopsOnlyAdmittedExec',
        'TestHostCaptureDockerQualifiesTrustedStopHelper',
    },
    'internal/server': {
        'TestRetainedDockerServerRestartProjectsActualProviderExit',
        'TestRetainedOutputZeroExitInBandFailureCannotSucceed',
        'TestRetainedOutputTerminalReceiptSurvivesPruningAndLostACK',
        'TestRetainedOutputCannotSettleForeignWork',
        'TestRetainedOutputCannotSettleAnotherAgentWork',
    },
}
REQUIRED = {(PREFIX + package, name)
            for suites in (TESTS, HOST_TESTS)
            for package, names in suites.items() for name in names}
HOST_UID = 65534



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


def host_commands(artifacts, home, docker_gid):
    result = []
    for package, names in HOST_TESTS.items():
        pattern = '^(' + '|'.join(re.escape(name) for name in sorted(names)) + ')$'
        result.append([
            'go', 'tool', 'test2json', '-t', '-p', PREFIX + package,
            'sudo', '-n', 'setpriv', '--reuid=' + str(HOST_UID),
            '--regid=' + str(docker_gid), '--clear-groups',
            'env', '-i', 'PATH=' + os.environ['PATH'], 'TMPDIR=/tmp', 'HOME=' + home,
            'CREWSHIP_HOST_CAPTURE_REQUIRE_UNPRIVILEGED=1',
            'CREWSHIP_HOST_CAPTURE_BINARY=' + os.path.join(artifacts, 'crewship'),
            'CREWSHIP_HOST_CAPTURE_SIDECAR=' + os.path.join(artifacts, 'crewship-sidecar'),
            os.path.join(artifacts, package.replace('/', '-') + '.test'),
            '-test.v=test2json', '-test.run=' + pattern, '-test.timeout=3m',
        ])
    return result


@contextmanager
def prepared_host_commands():
    endpoint = os.environ.get('DOCKER_HOST', 'unix:///var/run/docker.sock')
    if not endpoint.startswith('unix://'):
        raise RuntimeError('protected capture qualification requires a local Unix Docker socket')
    socket = endpoint[len('unix://'):]
    socket_info = os.stat(socket)
    if not stat.S_ISSOCK(socket_info.st_mode):
        raise RuntimeError('Docker endpoint is not a Unix socket')
    identity = ['sudo', '-n', 'setpriv', '--reuid=' + str(HOST_UID),
                '--regid=' + str(socket_info.st_gid), '--clear-groups']
    # Only fresh build artifacts are public-readable. Test HOME and all runtime
    # spools remain private to the distinct UID; no source checkout is chowned.
    with tempfile.TemporaryDirectory(prefix='crewship-host-qualification-', dir='/tmp') as artifacts:
        os.chmod(artifacts, 0o755)
        for package, name in (('cmd/crewship', 'crewship'),
                              ('cmd/crewship-sidecar', 'crewship-sidecar')):
            build_env = dict(os.environ, CGO_ENABLED='0')
            subprocess.run(['go', 'build', '-o', os.path.join(artifacts, name), './' + package], check=True,
                           env=build_env)
        for package in HOST_TESTS:
            subprocess.run(['go', 'test', '-tags', 'integration', '-c', '-o',
                            os.path.join(artifacts, package.replace('/', '-') + '.test'),
                            './' + package], check=True)
        for name in os.listdir(artifacts):
            os.chmod(os.path.join(artifacts, name), 0o755)
        home = subprocess.check_output(identity + ['mktemp', '-d',
                    '/tmp/crewship-host-qualification-home.XXXXXXXX'], text=True).strip()
        if not re.fullmatch(r'/tmp/crewship-host-qualification-home\.[A-Za-z0-9]{8}', home):
            raise RuntimeError('unexpected private qualification HOME')
        info = os.lstat(home)
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != HOST_UID or info.st_mode & 0o077:
            raise RuntimeError('qualification HOME is not private to the test UID')
        try:
            commands = host_commands(artifacts, home, socket_info.st_gid)
            for command in commands:
                # Preserve an explicitly selected local rootless socket without
                # inheriting the runner's unrelated credentials or configuration.
                command.insert(command.index('CREWSHIP_HOST_CAPTURE_REQUIRE_UNPRIVILEGED=1'),
                               'DOCKER_HOST=' + endpoint)
            yield commands
        finally:
            subprocess.run(identity + ['python3', '-c',
                'import shutil,sys; shutil.rmtree(sys.argv[1])', home], check=True)


def main():
    if os.geteuid() == 0:
        print('ERROR: run this suite as non-root; only the managed launcher test is elevated.',
              file=sys.stderr)
        return True
    events = []
    malformed = False
    code = 0
    def run_suite(suite):
        nonlocal malformed, code
        for command in suite:
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
    try:
        run_suite(commands())
        with prepared_host_commands() as suite:
            run_suite(suite)
    except (OSError, RuntimeError, subprocess.SubprocessError) as error:
        print('ERROR: protected host qualification setup failed: ' + str(error), file=sys.stderr)
        code = 1
    errors = failures(events, code)
    if malformed:
        errors.append('go test emitted invalid JSON evidence')
    for error in errors:
        print('ERROR: ' + error, file=sys.stderr)
    if not errors:
        print(f'All {len(REQUIRED)} required managed-environment fixtures passed.')
    return bool(errors)


if __name__ == '__main__':
    sys.exit(main())
