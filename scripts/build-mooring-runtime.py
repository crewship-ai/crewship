#!/usr/bin/env python3
"""Prepare pinned Linux dev2 child/WASM/license assets in this source tree only."""
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import urllib.request

ROOT = Path(__file__).resolve().parents[1]


def digest(path):
    value = hashlib.sha256()
    with path.open('rb') as source:
        for block in iter(lambda: source.read(1024 * 1024), b''):
            value.update(block)
    return value.hexdigest()


def command(args, cwd=ROOT, env=None):
    subprocess.run([str(arg) for arg in args], cwd=cwd, env=env, check=True)


def tool_archive(spec, destination):
    """Verified downloads, only named regular members, bounded decompression."""
    destination.mkdir(parents=True, exist_ok=True)
    archive = destination / 'download.tar.gz'
    with urllib.request.urlopen(spec['url'], timeout=60) as response, archive.open('xb') as output:
        size = 0
        while True:
            block = response.read(1024 * 1024)
            if not block:
                break
            size += len(block)
            if size > spec['max_archive_bytes']:
                raise ValueError('tool archive exceeds pinned limit')
            output.write(block)
    if digest(archive) != spec['sha256']:
        raise ValueError('tool archive checksum mismatch')
    with tarfile.open(archive, 'r:gz') as source:
        for name in spec['files']:
            if Path(name).is_absolute() or any(part in ('', '.', '..') for part in name.split('/')) or '\\' in name:
                raise ValueError('unsafe tool member path')
        selected = {spec['archive_prefix'] + '/' + name: name for name in spec['files']}
        seen = set()
        total = 0
        for member in source:
            if member.name not in selected:
                continue
            if member.name in seen or not member.isfile() or member.size > 256 * 1024 * 1024:
                raise ValueError('unsafe tool archive member')
            seen.add(member.name)
            total += member.size
            if total > 300 * 1024 * 1024:
                raise ValueError('tool expansion limit')
            target = destination / selected[member.name]
            if not target.resolve().is_relative_to(destination.resolve()):
                raise ValueError('unsafe tool path')
            target.parent.mkdir(parents=True, exist_ok=True)
            stream = source.extractfile(member)
            if stream is None:
                raise ValueError('tool archive content absent')
            with stream, target.open('xb') as output:
                shutil.copyfileobj(stream, output)
            target.chmod(0o500 if target.name in ('wasm-bindgen', 'wasm-opt') else 0o400)
        if seen != set(selected):
            raise ValueError('required tool archive member absent')
    return destination


def rust_environment(version, target):
    # Standard rustup layout is also usable when the deployment's deliberately
    # minimal PATH omits rustup shims. Nothing is downloaded or installed here.
    roots = []
    if shutil.which('rustc'):
        roots.append(Path(shutil.which('rustc')).resolve().parent)
    rustup = Path(os.environ.get('RUSTUP_HOME', Path.home() / '.rustup'))
    roots.append(rustup / 'toolchains' / f'{version}-{target}' / 'bin')
    for root in roots:
        rustc, cargo = root / 'rustc', root / 'cargo'
        if not rustc.is_file() or not cargo.is_file():
            continue
        actual = subprocess.check_output([str(rustc), '--version'], text=True).split()
        if len(actual) > 1 and actual[1] == version:
            env = dict(os.environ)
            env['PATH'] = str(root) + os.pathsep + env.get('PATH', '')
            env['RUSTC'] = str(rustc)
            env['CARGO_NET_OFFLINE'] = 'true'
            return cargo, env
    raise ValueError('pinned Rust toolchain unavailable; provision through managed infrastructure')


def collect_licenses(destination):
    command([sys.executable, 'scripts/license-inventory.py', '--check'], cwd=ROOT / 'mooring')
    inventory = json.loads((ROOT / 'mooring/licenses/inventory.json').read_text())
    manifest = []
    for entry in inventory:
        identity = entry['name'] + '-' + entry['version']
        if not re.fullmatch(r'[A-Za-z0-9_.+-]+', identity):
            raise ValueError('unsafe license identity')
        for index, text in enumerate(entry['texts']):
            source = ROOT / 'mooring' / text['path']
            if source.is_symlink() or not source.resolve().is_relative_to((ROOT / 'mooring').resolve()) or digest(source) != text['sha256']:
                raise ValueError('license source checksum/path mismatch')
            target = destination / identity / f'{index}-{source.name}'
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source, target)
            manifest.append({'crate':entry['name'], 'version':entry['version'], 'spdx':entry['spdx'], 'path':str(target.relative_to(destination)), 'sha256':text['sha256']})
    (destination / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')


def build(sha):
    if not re.fullmatch(r'[0-9a-f]{40}', sha):
        raise ValueError('exact source SHA required')
    if subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip() != sha:
        raise ValueError('source SHA mismatch')
    if platform.system() != 'Linux' or platform.machine() != 'x86_64':
        raise ValueError('enabled dev runtime assets require Linux x86_64')
    config = json.loads((ROOT / 'mooring/build-tools.json').read_text())
    cargo, env = rust_environment(config['rust_version'], config['target'])
    build_root = ROOT / 'build/mooring'
    build_root.mkdir(parents=True, exist_ok=True)
    env['CARGO_TARGET_DIR'] = str(build_root / 'target')
    env['RUSTFLAGS'] = f'--remap-path-prefix={ROOT}=/crewship'
    # Cargo resolves dependencies only from the checked/pinned vendored tree.
    command([cargo, 'build', '--release', '--locked', '--offline', '-p', 'mooring-collab'], cwd=ROOT / 'mooring', env=env)
    command([cargo, 'build', '--release', '--locked', '--offline', '--target', 'wasm32-unknown-unknown', '-p', 'mooring-client'], cwd=ROOT / 'mooring', env=env)
    with tempfile.TemporaryDirectory(prefix='prepare-', dir=build_root) as temp:
        temp = Path(temp)
        bindgen = tool_archive(config['tools']['wasm-bindgen'], temp / 'bindgen')
        binaryen = tool_archive(config['tools']['binaryen'], temp / 'binaryen')
        command([bindgen / 'wasm-bindgen', '--version'])
        command([binaryen / 'bin/wasm-opt', '--version'])
        sdk = temp / 'sdk'
        command([bindgen / 'wasm-bindgen', build_root / 'target/wasm32-unknown-unknown/release/mooring_client.wasm', '--target', 'web', '--out-dir', sdk])
        command([binaryen / 'bin/wasm-opt', '-Oz', '--all-features', sdk / 'mooring_client_bg.wasm', '-o', sdk / 'mooring_client_bg.wasm'])
        embedded = temp / 'embedded'
        embedded.mkdir()
        shutil.copyfile(build_root / 'target/release/mooring-collab', embedded / 'mooring-collab')
        collect_licenses(embedded / 'licenses')
        # The wrapper has no runtime imports; remove its TypeScript-only types.
        wrapper = subprocess.check_output(['node', '--input-type=module', '-e',
            "import{readFileSync}from'node:fs';import{stripTypeScriptTypes}from'node:module';process.stdout.write(stripTypeScriptTypes(readFileSync(process.argv[1],'utf8'),{mode:'transform'}))", str(ROOT / 'mooring/client/index.ts')])
        (sdk / 'index.js').write_bytes(wrapper)
        assets = {str(path.relative_to(sdk)): digest(path) for path in sorted(sdk.rglob('*')) if path.is_file()}
        sdk_digest = hashlib.sha256(json.dumps(assets, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
        manifest = {'schema_version':1, 'source_sha':sha, 'target':config['target'], 'versions':config['versions'], 'binary_sha256':digest(embedded / 'mooring-collab'), 'sdk':assets, 'sdk_prefix':'/mooring-sdk/generated/' + sdk_digest, 'tools':config}
        (embedded / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
        # Only generated paths inside this disposable source are replaced.
        # Never remove the tracked disabled placeholder or unrelated public assets.
        output = ROOT / 'internal/mooringassets/embedded/generated'
        public = ROOT / 'public/mooring-sdk/generated'
        for source, target in ((embedded, output), (sdk, public)):
            if target.is_symlink():
                raise ValueError('refusing linked generated output')
            if target.exists():
                shutil.rmtree(target)
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copytree(source, target / sdk_digest if source == sdk else target)
    print('Mooring child, SDK and license assets prepared for source ' + sha)


if __name__ == '__main__':
    if len(sys.argv) != 3 or sys.argv[1] != '--release-dev':
        sys.exit('usage: build-mooring-runtime.py --release-dev EXACT_SHA')
    build(sys.argv[2])
