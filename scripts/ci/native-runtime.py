#!/usr/bin/env python3
"""Run named native live contracts using an explicitly approved GHCR digest."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import uuid

ROOT = Path(__file__).resolve().parents[2]
IMAGE = re.compile(r'ghcr\.io/[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)+@sha256:[0-9a-f]{64}')
CONFIG_ID = re.compile(r'sha256:[0-9a-f]{64}')
CONTAINER_ID = re.compile(r'[0-9a-f]{64}')
CODEX_SHA256 = 'd2752c52353401f7f6efbfcea68796f4f7a3d3e4769f5d1da53fa49d4856b72f'
CODEX_VERSION = 'codex-cli 0.159.0'
TESTS = {
    'github.com/crewship-ai/crewship/internal/restricteddispatch': 'TestLiveNativeFrozenToolsAndDurableAccounting',
}


def command(*args, timeout=60, env=None):
    return subprocess.run(args, cwd=ROOT, env=env, timeout=timeout,
                          check=True, capture_output=True, text=True).stdout


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


def verify_codex(local_id):
    name = 'crewship-ci-native-identity-' + uuid.uuid4().hex
    container = command('docker', 'create', '--name', name, '--pull=never',
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
    identity = {'registry_reference': reference, 'status': 'failed'}
    try:
        command('docker', 'pull', '--platform=linux/amd64', reference, timeout=180)
        identity.update(image_identity(reference, json.loads(command('docker', 'image', 'inspect', reference))))
        identity.update(verify_codex(identity['local_config_id']))
        identity['source_sha'] = command('git', 'rev-parse', 'HEAD').strip()
        (output / 'image-identity.json').write_text(json.dumps(identity, indent=2) + '\n')
        env = os.environ.copy()
        env.update(CREWSHIP_RESTRICTED_LIVE='1',
                   CREWSHIP_RESTRICTED_NATIVE_IMAGE=identity['local_config_id'],
                   GOMAXPROCS='2')
        names = '|'.join(re.escape(name) for name in TESTS.values())
        packages = ['./' + package.split('github.com/crewship-ai/crewship/', 1)[1] for package in TESTS]
        with (output / 'go-test.jsonl').open('w') as evidence:
            result = subprocess.run(['go', 'test', '-race', '-tags', 'restrictedruntime_live',
                                     '-json', '-count=1', '-p', '2', '-timeout', '7m',
                                     '-run', '^(' + names + ')$', *packages],
                                    cwd=ROOT, env=env, stdout=evidence,
                                    stderr=subprocess.STDOUT, timeout=480)
        events = [json.loads(line) for line in (output / 'go-test.jsonl').read_text().splitlines() if line.strip()]
        if result.returncode != 0:
            raise ValueError(f'Go live acceptance failed with exit {result.returncode}; see go-test.jsonl')
        identity['required_passes'] = require_passes(events)
        identity['status'] = 'passed'
    except Exception as error:
        identity['error'] = str(error)
        raise
    finally:
        (output / 'image-identity.json').write_text(json.dumps(identity, indent=2) + '\n')
    print('Named restricted Codex native live contract passed; immutable image identity and Go JSON evidence retained.')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True, type=pinned_image)
    parser.add_argument('--output', type=Path, default=Path('.ci-results/native-runtime'))
    args = parser.parse_args()
    run(args.image, args.output)
