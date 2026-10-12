#!/usr/bin/env python3
"""Run named native live contracts using an explicitly approved GHCR digest."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import sys
from contextlib import contextmanager
import subprocess
import tempfile
import uuid

ROOT = Path(__file__).resolve().parents[2]
IMAGE = re.compile(r'ghcr\.io/[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)+@sha256:[0-9a-f]{64}')
CONFIG_ID = re.compile(r'sha256:[0-9a-f]{64}')
CONTAINER_ID = re.compile(r'[0-9a-f]{64}')
RUN_LABEL = 'ai.crewship.native-acceptance.run'
RESOURCE_NAME = re.compile(r'[a-zA-Z0-9][a-zA-Z0-9_.-]*')
CODEX_SHA256 = 'd2752c52353401f7f6efbfcea68796f4f7a3d3e4769f5d1da53fa49d4856b72f'
CODEX_VERSION = 'codex-cli 0.159.0'
TESTS = {
    'github.com/crewship-ai/crewship/internal/restricteddispatch': 'TestLiveNativeFrozenToolsAndDurableAccounting',
}


def bounded_process(args, *, timeout, env=None, stdout=subprocess.PIPE, check=True):
    """Bound the entire process group, including descendants retaining pipes."""
    process = subprocess.Popen(args, cwd=ROOT, env=env, stdout=stdout,
                               stderr=subprocess.STDOUT, text=True, start_new_session=True)
    try:
        output, _ = process.communicate(timeout=timeout)
    finally:
        # Kill descendants even if their parent exited without waiting for them.
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.wait(timeout=5)
        if process.stdout is not None:
            process.stdout.close()
    if check and process.returncode:
        raise subprocess.CalledProcessError(process.returncode, args)
    return subprocess.CompletedProcess(args, process.returncode, output)


def command(*args, timeout=60, env=None):
    return bounded_process(args, timeout=timeout, env=env).stdout


class CancelledError(Exception):
    """Distinct from OSError so selector loops cannot swallow cancellation."""


@contextmanager
def cancellation():
    def interrupted(signum, frame):
        # Further cancellation must not interrupt the bounded cleanup.
        for number in (signal.SIGINT, signal.SIGTERM):
            signal.signal(number, signal.SIG_IGN)
        raise CancelledError(f'Native acceptance cancelled by signal {signum}')
    previous = {number: signal.signal(number, interrupted)
                for number in (signal.SIGINT, signal.SIGTERM)}
    try:
        yield
    finally:
        for number, handler in previous.items():
            signal.signal(number, handler)


def proxy_args(args, run_id):
    if not re.fullmatch(r'[0-9a-f]{32}', run_id):
        raise ValueError('Invalid run ownership')
    if args[:1] in (['create'], ['run']):
        return args[:1] + ['--label', RUN_LABEL + '=' + run_id] + args[1:]
    if args[:2] == ['volume', 'create']:
        return args[:2] + ['--label', RUN_LABEL + '=' + run_id] + args[2:]
    return args


def cleanup_owned(run_id):
    """Selectors use a random invocation label, never global name prefixes."""
    selector = 'label=' + RUN_LABEL + '=' + run_id
    removed = {'containers': [], 'volumes': []}
    for kind in ('containers', 'volumes'):
        listing = ('ps', '-aq', '--no-trunc') if kind == 'containers' else ('volume', 'ls', '-q')
        names = command('docker', *listing, '--filter', selector, timeout=10).split()
        if len(names) > 32:
            raise ValueError('Unexpected run-owned resource count')
        for name in names:
            pattern = CONTAINER_ID if kind == 'containers' else RESOURCE_NAME
            if not pattern.fullmatch(name):
                raise ValueError('Malformed owned Docker resource identifier')
        if names:
            removal = ('rm', '--force') if kind == 'containers' else ('volume', 'rm')
            command('docker', *removal, *names, timeout=20)
        if command('docker', *listing, '--filter', selector, timeout=10).strip():
            raise ValueError('Run-owned Docker resources survived cleanup')
        removed[kind] = names
    return removed



def pinned_image(value):
    if not IMAGE.fullmatch(value):
        raise ValueError('Image must be ghcr.io/owner/repository@sha256:<64 lowercase hex>; tags/local builds are forbidden')
    return value


def image_identity(reference, inspected):
    if len(inspected) != 1:
        raise ValueError('Image inspection must identify exactly one image')
    image = inspected[0]
    if reference not in image.get('RepoDigests', []):
        raise ValueError('Pulled image does not attest the requested registry digest')
    if image.get('Os') != 'linux' or image.get('Architecture') != 'amd64':
        raise ValueError('Native acceptance requires a linux/amd64 image')
    local_id = image.get('Id', '')
    if not CONFIG_ID.fullmatch(local_id):
        raise ValueError('Image inspection did not return an immutable local config ID')
    return {'registry_reference': reference, 'local_config_id': local_id,
            'os': image['Os'], 'architecture': image['Architecture']}


def verify_codex(local_id, run_id):
    name = 'crewship-ci-native-identity-' + uuid.uuid4().hex
    container = command('docker', 'create', '--label', RUN_LABEL + '=' + run_id, '--name', name, '--pull=never',
                        '--network=none', '--read-only', '--cap-drop=ALL',
                        '--security-opt=no-new-privileges', '--user=1001:1001',
                        '--cpus=0.5', '--memory=128m', '--pids-limit=32',
                        '--entrypoint=/opt/codex', local_id, '--version').strip()
    try:
        if not CONTAINER_ID.fullmatch(container):
            raise ValueError('Docker did not return an owned container ID')
        with tempfile.TemporaryDirectory(prefix='crewship-native-identity-') as folder:
            binary = Path(folder) / 'codex'
            command('docker', 'cp', container + ':/opt/codex', str(binary))
            if not binary.is_file() or binary.is_symlink():
                raise ValueError('Image does not contain the expected regular Codex binary')
            checksum = hashlib.sha256(binary.read_bytes()).hexdigest()
            if checksum != CODEX_SHA256:
                raise ValueError('Actual image Codex checksum differs from the reviewed native pin')
        # Only execute the binary after checking its copied bytes on the host.
        version = command('docker', 'start', '--attach', container, timeout=30).strip()
        state = json.loads(command('docker', 'inspect', container))[0]['State']
        if state.get('Running') is not False or state.get('ExitCode') != 0 or version != CODEX_VERSION:
            raise ValueError('Pinned Codex version probe did not finish successfully with the expected version')
        return {'codex_sha256': checksum, 'codex_version': version}
    finally:
        # Remove only the exact container this invocation created. No prune,
        # image deletion, host mounts, credentials, or Docker socket exposure.
        command('docker', 'rm', '--force', container if CONTAINER_ID.fullmatch(container) else name)


def require_passes(events):
    required = set(TESTS.items())
    passed = set()
    packages = set()
    for event in events:
        package, test = event.get('Package'), event.get('Test')
        if event.get('Action') == 'fail' or 'WARNING: DATA RACE' in event.get('Output', ''):
            raise ValueError('Live acceptance reported a failure or data race')
        if event.get('Action') == 'skip':
            raise ValueError('Selected live acceptance or subtest was skipped')
        if (package, test) in required:
            if event.get('Action') == 'pass':
                passed.add((package, test))
        if event.get('Action') == 'pass' and not test:
            packages.add(package)
    if passed != required or not set(TESTS).issubset(packages):
        raise ValueError(f'Missing package-qualified terminal live passes: {sorted(required - passed)}; '
                         f'missing successful packages: {sorted(set(TESTS) - packages)}')
    return sorted(({'package': package, 'test': test} for package, test in passed),
                  key=lambda item: (item['package'], item['test']))


def run(reference, output):
    reference = pinned_image(reference)
    output.mkdir(parents=True, exist_ok=False)
    run_id = uuid.uuid4().hex
    identity = {'registry_reference': reference, 'run_id': run_id, 'status': 'failed'}
    try:
        command('docker', 'pull', '--platform=linux/amd64', reference, timeout=180)
        identity.update(image_identity(reference, json.loads(command('docker', 'image', 'inspect', reference))))
        identity.update(verify_codex(identity['local_config_id'], run_id))
        identity['source_sha'] = command('git', 'rev-parse', 'HEAD').strip()
        (output / 'image-identity.json').write_text(json.dumps(identity, indent=2) + '\n')
        env = os.environ.copy()
        env.update(CREWSHIP_RESTRICTED_LIVE='1',
                   CREWSHIP_RESTRICTED_NATIVE_IMAGE=identity['local_config_id'],
                   GOMAXPROCS='2')
        names = '|'.join(re.escape(name) for name in TESTS.values())
        packages = ['./' + package.split('github.com/crewship-ai/crewship/', 1)[1] for package in TESTS]
        # Only the child test uses this proxy; cleanup uses the real CLI.
        with tempfile.TemporaryDirectory(prefix='crewship-native-docker-') as folder:
            docker = shutil.which('docker')
            if not docker:
                raise ValueError('Docker CLI is missing')
            proxy = Path(folder) / 'docker'
            proxy.write_text('#!' + sys.executable + '\nimport os, sys\n'
                             + 'sys.path.insert(0, ' + repr(str(Path(__file__).parent)) + ')\n'
                             + 'import importlib.util\n'
                             + 'spec = importlib.util.spec_from_file_location("native_proxy", '
                             + repr(str(Path(__file__).resolve())) + ')\n'
                             + 'runner = importlib.util.module_from_spec(spec)\n'
                             + 'spec.loader.exec_module(runner)\n'
                             + 'args = runner.proxy_args(sys.argv[1:], ' + repr(run_id) + ')\n'
                             + 'os.execv(' + repr(docker) + ', [' + repr(docker) + '] + args)\n')
            proxy.chmod(0o700)
            env['PATH'] = folder + os.pathsep + env.get('PATH', '')
            with (output / 'go-test.jsonl').open('w') as evidence:
                result = bounded_process(['go', 'test', '-race', '-tags', 'restrictedruntime_live',
                                          '-json', '-count=1', '-p', '2', '-timeout', '7m',
                                          '-run', '^(' + names + ')$', *packages],
                                         env=env, stdout=evidence, timeout=480, check=False)
        events = [json.loads(line) for line in (output / 'go-test.jsonl').read_text().splitlines() if line.strip()]
        if result.returncode != 0:
            raise ValueError(f'Go live acceptance failed with exit {result.returncode}; see go-test.jsonl')
        identity['required_passes'] = require_passes(events)
        identity['status'] = 'passed'
    except BaseException as error:
        identity['error'] = str(error)
        raise
    finally:
        try:
            identity['cleanup'] = cleanup_owned(run_id)
        except BaseException as error:
            identity['status'] = 'failed'
            identity['cleanup_error'] = str(error)
            raise
        finally:
            (output / 'image-identity.json').write_text(json.dumps(identity, indent=2) + '\n')
    print('Named restricted Codex native live contract passed; immutable image identity and Go JSON evidence retained.')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True, type=pinned_image)
    parser.add_argument('--output', type=Path, default=Path('.ci-results/native-runtime'))
    args = parser.parse_args()
    with cancellation():
        run(args.image, args.output)
