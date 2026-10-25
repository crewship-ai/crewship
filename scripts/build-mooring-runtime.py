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
import tomllib
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


def git_environment():
    # Build inputs must never inherit another checkout's index, object store,
    # worktree or command/config overrides. Sources here are anonymous public
    # repositories with exact pins; no credential helper is needed.
    env = {name: value for name, value in os.environ.items() if not name.startswith('GIT_')}
    env.update(GIT_TERMINAL_PROMPT='0', GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL='/dev/null')
    return env


def confined(path):
    """Reject linked ancestors before any creation or replacement in the tree."""
    relative = path.relative_to(ROOT)
    current = ROOT
    for part in relative.parts:
        if part in ('', '.', '..'):
            raise ValueError('unsafe generated path')
        current = current / part
        if current.is_symlink():
            raise ValueError('linked generated path ancestor')
    if not path.resolve().is_relative_to(ROOT.resolve()):
        raise ValueError('generated path escapes source tree')
    return path


def verify_source(sha):
    if not re.fullmatch(r'[0-9a-f]{40}', sha):
        raise ValueError('exact source SHA required')
    if subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, env=git_environment(), text=True).strip() != sha:
        raise ValueError('source SHA mismatch')
    if subprocess.check_output(['git', 'status', '--porcelain', '--untracked-files=normal'], cwd=ROOT, env=git_environment()):
        raise ValueError('release source must be clean and committed')


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
            # Include unselected members and tar metadata/padding in the bound;
            # tarfile must decompress them too while locating selected tools.
            if member.offset_data + member.size > spec.get('max_expanded_bytes', 300 * 1024 * 1024):
                raise ValueError('tool archive expansion limit')
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


def native_package_closure(metadata, native):
    """Conservative normal/build closure, including proc macros; never dev-only edges.

    Cargo package IDs are opaque lookup keys. Public identities come from the
    lock, not local registry/cache paths. This is not a binary linkage analysis.
    """
    packages = {item['id']: item for item in metadata['packages']}
    nodes = {item['id']: item for item in metadata['resolve']['nodes']}
    if len(packages) != len(metadata['packages']) or len(nodes) != len(metadata['resolve']['nodes']):
        raise ValueError('duplicate native metadata identity')
    root = metadata['resolve']['root']
    package = packages[root]
    if package['name'] != 'wordcraft-web' or Path(package['manifest_path']).resolve() != (native / 'apps/wordcraft-web/Cargo.toml').resolve():
        raise ValueError('native metadata root mismatch')
    lock = tomllib.loads((native / 'Cargo.lock').read_text())
    locked = {(p['name'], p['version'], p.get('source')): p for p in lock['package']}
    pending, visited, result = [root], set(), []
    while pending:
        identity = pending.pop()
        if identity in visited:
            continue
        visited.add(identity)
        package, node = packages[identity], nodes[identity]
        source = package.get('source')
        if source is not None and not source.startswith(('registry+https://', 'sparse+https://', 'git+https://')):
            raise ValueError('unsupported native dependency source')
        package_path = None
        if source is None:
            package_path = str(Path(package['manifest_path']).resolve().parent.relative_to(native.resolve()))
        entry = locked[(package['name'], package['version'], source)]
        result.append({'name': package['name'], 'version': package['version'], 'source': source,
                       'checksum': entry.get('checksum'), 'package_path': package_path,
                       'spdx': package.get('license'), 'features': sorted(node['features'])})
        for dependency in node['deps']:
            kinds = dependency['dep_kinds']
            if not kinds or any(kind['kind'] not in (None, 'build', 'dev') for kind in kinds):
                raise ValueError('unknown native dependency kind')
            if any(kind['kind'] != 'dev' for kind in kinds):
                pending.append(dependency['pkg'])
    return sorted(result, key=lambda p: (p['name'], p['version'], p['source'] or ''))


def verified_native_text(recipe, text):
    name, expected = text['path'], text['sha256']
    if not re.fullmatch(r'licenses/[a-f0-9]{64}\.txt', name) or not re.fullmatch(r'[a-f0-9]{64}', expected):
        raise ValueError('unsafe native license path/hash')
    source = recipe
    for part in Path(name).parts:
        source = source / part
        if source.is_symlink():
            raise ValueError('linked native license source')
    if not source.is_file() or not 0 < source.stat().st_size <= 2 * 1024 * 1024 or digest(source) != expected:
        raise ValueError('native license text missing or changed')
    return source


def collect_native_licenses(destination, native, source_metadata, resolved):
    recipe = ROOT / 'tools/wordcraft-mooring-native'
    inventory_path = recipe / 'license-inventory.json'
    inventory = json.loads(inventory_path.read_text())
    if (inventory['native_base'] != source_metadata['base'] or
            inventory['native_patch_sha256'] != source_metadata['patch_sha256'] or
            inventory['cargo_lock_sha256'] != digest(native / 'Cargo.lock') or
            inventory['filter_platform'] != 'wasm32-unknown-unknown'):
        raise ValueError('native license input pin mismatch')
    expected = [entry['package'] for entry in inventory['packages']]
    if not expected or expected != native_package_closure(resolved, native):
        raise ValueError('native dependency inventory changed; review required')
    manifest = json.loads((destination / 'manifest.json').read_text())
    copied = set()
    for entry in inventory['packages']:
        if not entry['texts'] or not any(text['kind'] == 'license' for text in entry['texts']):
            raise ValueError('native package license text absent')
        for text in entry['texts']:
            source = verified_native_text(recipe, text)
            relative = 'native/' + source.name
            target = destination / relative
            if relative not in copied:
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(source, target)
                copied.add(relative)
            manifest.append({'crate': entry['package']['name'], 'version': entry['package']['version'],
                             'spdx': entry['package']['spdx'], 'component': 'wordcraft-native',
                             'path': relative, 'sha256': text['sha256']})
    for text in inventory['supplemental_texts']:
        source = verified_native_text(recipe, text)
        relative = 'native/' + source.name
        if relative not in copied:
            (destination / relative).parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source, destination / relative)
            copied.add(relative)
        manifest.append({'crate': 'wordcraft-native-supplemental', 'version': inventory['native_base'],
                         'spdx': None, 'component': 'wordcraft-native',
                         'path': relative, 'sha256': text['sha256']})
    shutil.copyfile(inventory_path, destination / 'wordcraft-native-inventory.json')
    (destination / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')


def pinned_git(url, sha, destination):
    # Fresh, build-owned metadata only; no caller checkout, branch or hooks.
    if not re.fullmatch(r'[a-f0-9]{40}', sha) or destination.exists():
        raise ValueError('invalid pinned source destination')
    source_env = git_environment()
    command(['git', 'init', '--quiet', destination], env=source_env)
    command(['git', '-c', 'core.hooksPath=/dev/null', '-C', destination,
             'fetch', '--depth=1', '--no-tags', url, sha], env=source_env)
    actual = subprocess.check_output(['git', '--no-replace-objects', '-C', destination,
                                      'rev-parse', 'FETCH_HEAD^{commit}'], env=source_env, text=True).strip()
    if actual != sha:
        raise ValueError('public source commit mismatch')


def prepare_editor_inputs(temp):
    recipe = ROOT / 'tools/wordcraft-mooring-native'
    metadata = json.loads((recipe / 'source-manifest.json').read_text())
    upstream = temp / 'wordcraft-upstream'
    pinned_git('https://github.com/storytold/wordcraft.git', metadata['base'], upstream)
    native = temp / 'wordcraft-source'
    command(['bash', recipe / 'prepare.sh', upstream, native], env=git_environment())
    fonts_pin = '8dcdacd5153e64560d109541a47d806f26f048c0'
    font_repo = temp / 'font-upstream'
    pinned_git('https://github.com/storytold/craft-fonts.git', fonts_pin, font_repo)
    fonts = temp / 'release-fonts'
    font_manifest = []
    for line in (recipe / 'release-fonts.sha256').read_text().splitlines():
        expected, name = line.split('  ', 1)
        if not re.fullmatch(r'[a-f0-9]{64}', expected) or not re.fullmatch(r'fonts/[A-Za-z0-9/_.-]+', name) or '..' in name.split('/'):
            raise ValueError('invalid release font input')
        object_name = fonts_pin + ':' + name
        size = int(subprocess.check_output(['git', '--no-replace-objects', '-C', font_repo,
                                           'cat-file', '-s', object_name], env=git_environment(), text=True))
        if size < 1 or size > 64 * 1024 * 1024:
            raise ValueError('font input size exceeds limit')
        body = subprocess.check_output(['git', '--no-replace-objects', '-C', font_repo,
                                        'show', object_name], env=git_environment())
        if len(body) != size or hashlib.sha256(body).hexdigest() != expected:
            raise ValueError('release font checksum mismatch')
        output = fonts / name
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_bytes(body)
        font_manifest.append({'path': name, 'sha256': expected, 'bytes': size})
    if (fonts / 'fonts/manifest.txt').read_bytes() != (recipe / 'release-fonts-manifest.txt').read_bytes():
        raise ValueError('release font profile mismatch')
    (fonts / 'provenance.json').write_text(json.dumps({'source': 'https://github.com/storytold/craft-fonts',
        'commit': fonts_pin, 'files': font_manifest}, sort_keys=True, indent=2) + '\n')
    return native, fonts, metadata


def assemble_editor(sha, native, fonts, metadata, native_output, sdk, child, licenses, output, config):
    # All names are flat and fixed; the same immutable bytes are checked again
    # by editororigin before serving. No runtime filesystem or private path.
    output.mkdir()
    def copy(source, name):
        if source.is_symlink() or not source.is_file():
            raise ValueError('non-regular editor input')
        shutil.copyfile(source, output / name)
    for name in ('wordcraft_web.js', 'wordcraft_web_bg.wasm'):
        copy(native_output / name, name)
    for name in ('mooring_wordcraft_client.js', 'mooring_wordcraft_client_bg.wasm'):
        copy(sdk / name, name)
    copy(child, 'child.js')
    html = (native / 'apps/wordcraft-web/index.html').read_text()
    html = re.sub(r'  <link data-trunk[^\n]*\n', '', html)
    if 'data-trunk' in html or html.count('</body>') != 1:
        raise ValueError('unsupported native web entry')
    (output / 'index.html').write_text(html.replace('</body>', '  <script type="module" src="./child.js"></script>\n</body>'))
    for name in ('LICENSE-MIT', 'NOTICE', 'ATTRIBUTION.md'):
        copy(native / name, 'wordcraft-' + name.replace('.md', '.txt'))
    for name in sorted((native / 'assets/fonts').glob('OFL*')):
        copy(name, 'wordcraft-font-' + name.name)
    copy(ROOT / 'LICENSE', 'mooring-LICENSE-APACHE.txt')
    copy(ROOT / 'NOTICE', 'mooring-NOTICE.txt')
    copy(ROOT / 'mooring/licenses/inventory.json', 'mooring-license-inventory.json')
    copy(licenses / 'wordcraft-native-inventory.json', 'wordcraft-native-license-inventory.json')
    license_text = []
    included_licenses = set()
    for item in json.loads((licenses / 'manifest.json').read_text()):
        source = licenses / item['path']
        if source.is_symlink() or not source.resolve().is_relative_to(licenses.resolve()) or digest(source) != item['sha256']:
            raise ValueError('editor dependency license mismatch')
        if item['path'] not in included_licenses:
            license_text.append(b'\n===== ' + item['path'].encode() + b' =====\n' + source.read_bytes())
            included_licenses.add(item['path'])
    (output / 'mooring-third-party-licenses.txt').write_bytes(b''.join(license_text))
    provenance = json.loads((fonts / 'provenance.json').read_text())
    for item in provenance['files']:
        name = item['path']
        if name.endswith('/OFL.txt'):
            copy(fonts / name, 'font-OFL-' + Path(name).parent.name + '.txt')
    copy(fonts / 'fonts/manifest.txt', 'font-manifest.txt')
    copy(fonts / 'provenance.json', 'font-provenance.json')
    tools = {'rustc': config['rust_version'], 'wasm_bindgen': config['tools']['wasm-bindgen']['version'],
             'wasm_opt': config['tools']['binaryen']['version'] + ' -Oz --all-features', 'sdk_source_commit': sha}
    (output / 'build-provenance.json').write_text(json.dumps({'crewship_source': sha,
        'native_upstream_source': metadata['base'], 'native_patch_sha256': metadata['patch_sha256'],
        'native_patch_commit': metadata['native_patch_commit'], 'release_fonts': 'original-six-8dcd-no-Myanmar',
        'tools': tools, 'qualification': 'Build only; requires HTTP/WS/UI/Saved acceptance'}, sort_keys=True, indent=2) + '\n')
    files = {file.name: digest(file) for file in sorted(output.iterdir())}
    raw = json.dumps({'files': files, 'source_commit': metadata['native_patch_commit'], 'toolchain': tools},
                     sort_keys=True, separators=(',', ':')).encode()
    total = len(raw) + sum(file.stat().st_size for file in output.iterdir())
    if len(files) > 512 or len(raw) > 1024 * 1024 or total > 64 * 1024 * 1024:
        raise ValueError('editor bundle exceeds existing qualification limits')
    (output / 'manifest.json').write_bytes(raw)
    return hashlib.sha256(raw).hexdigest()


def build_editor(sha, temp, build_root, cargo, env, bindgen, binaryen, sdk, embedded, config):
    native, fonts, metadata = prepare_editor_inputs(temp)
    native_env = dict(env)
    native_env['CARGO_TARGET_DIR'] = str(build_root / 'wordcraft-target')
    native_env['CRAFT_FONTS_DIR'] = str(fonts)
    # WordCraft uses its public locked dependencies; Mooring remains vendored
    # and offline. No tools or system fonts are installed by this recipe.
    native_env['CARGO_NET_OFFLINE'] = 'false'
    native_env['RUSTFLAGS'] = f'--remap-path-prefix={ROOT}=/crewship --remap-path-prefix={native}=/wordcraft'
    command([cargo, 'build', '--release', '--locked', '--target', 'wasm32-unknown-unknown', '-p', 'wordcraft-web'], cwd=native, env=native_env)
    # Resolve after the locked build fetched its public dependencies. Inventory
    # verification itself is offline and cannot float the lock or add packages.
    resolved = json.loads(subprocess.check_output([str(cargo), 'metadata', '--locked', '--offline',
        '--filter-platform', 'wasm32-unknown-unknown', '--format-version', '1',
        '--manifest-path', str(native / 'apps/wordcraft-web/Cargo.toml')], cwd=native, env=native_env))
    collect_native_licenses(embedded / 'licenses', native, metadata, resolved)
    native_output = temp / 'native-web'
    command([bindgen / 'wasm-bindgen', build_root / 'wordcraft-target/wasm32-unknown-unknown/release/wordcraft-web.wasm',
             '--target', 'web', '--out-name', 'wordcraft_web', '--out-dir', native_output])
    optimized = native_output / 'wordcraft_web_bg.optimized.wasm'
    command([binaryen / 'bin/wasm-opt', '-Oz', '--all-features', native_output / 'wordcraft_web_bg.wasm', '-o', optimized])
    optimized.replace(native_output / 'wordcraft_web_bg.wasm')
    esbuild = ROOT / 'node_modules/.bin/esbuild'
    if subprocess.check_output([esbuild, '--version'], text=True).strip() != '0.28.2':
        raise ValueError('install the frozen pnpm lockfile before building the editor')
    child = temp / 'child.js'
    command([esbuild, ROOT / 'lib/mooring/wordcraft-child-loader.ts', '--bundle', '--format=esm',
             '--platform=browser', '--target=es2022', '--outfile=' + str(child)])
    bundle = temp / 'editor-bundle'
    editor_digest = assemble_editor(sha, native, fonts, metadata, native_output, sdk, child,
                                    embedded / 'licenses', bundle, config)
    destination = embedded / 'editor' / editor_digest
    destination.parent.mkdir()
    shutil.move(bundle, destination)
    return editor_digest


def build(sha):
    verify_source(sha)
    if not (ROOT / 'lib/mooring/wordcraft-child-loader.ts').is_file():
        raise ValueError('release source lacks the actual WordCraft child integration')
    if platform.system() != 'Linux' or platform.machine() != 'x86_64':
        raise ValueError('enabled dev runtime assets require Linux x86_64')
    config = json.loads((ROOT / 'mooring/build-tools.json').read_text())
    cargo, env = rust_environment(config['rust_version'], config['target'])
    build_root = confined(ROOT / 'build/mooring')
    confined(ROOT / 'internal/mooringassets/embedded/generated')
    confined(ROOT / 'public/mooring-sdk/generated')
    build_root.mkdir(parents=True, exist_ok=True)
    env['CARGO_TARGET_DIR'] = str(build_root / 'target')
    env['RUSTFLAGS'] = f'--remap-path-prefix={ROOT}=/crewship'
    # Cargo resolves dependencies only from the checked/pinned vendored tree.
    command([cargo, 'build', '--release', '--locked', '--offline', '-p', 'mooring-collab'], cwd=ROOT / 'mooring', env=env)
    command([cargo, 'build', '--release', '--locked', '--offline', '--target', 'wasm32-unknown-unknown', '-p', 'mooring-client', '-p', 'mooring-wordcraft-client'], cwd=ROOT / 'mooring', env=env)
    with tempfile.TemporaryDirectory(prefix='prepare-', dir=build_root) as temp:
        temp = Path(temp)
        bindgen = tool_archive(config['tools']['wasm-bindgen'], temp / 'bindgen')
        binaryen = tool_archive(config['tools']['binaryen'], temp / 'binaryen')
        command([bindgen / 'wasm-bindgen', '--version'])
        command([binaryen / 'bin/wasm-opt', '--version'])
        sdk = temp / 'sdk'
        command([bindgen / 'wasm-bindgen', build_root / 'target/wasm32-unknown-unknown/release/mooring_client.wasm', '--target', 'web', '--out-dir', sdk])
        command([binaryen / 'bin/wasm-opt', '-Oz', '--all-features', sdk / 'mooring_client_bg.wasm', '-o', sdk / 'mooring_client_bg.wasm'])
        command([bindgen / 'wasm-bindgen', build_root / 'target/wasm32-unknown-unknown/release/mooring_wordcraft_client.wasm', '--target', 'web', '--out-dir', sdk])
        command([binaryen / 'bin/wasm-opt', '-Oz', '--all-features', sdk / 'mooring_wordcraft_client_bg.wasm', '-o', sdk / 'mooring_wordcraft_client_bg.wasm'])
        embedded = temp / 'embedded'
        embedded.mkdir()
        shutil.copyfile(build_root / 'target/release/mooring-collab', embedded / 'mooring-collab')
        collect_licenses(embedded / 'licenses')
        editor_digest = build_editor(sha, temp, build_root, cargo, env, bindgen, binaryen, sdk, embedded, config)
        # The wrapper has no runtime imports; remove its TypeScript-only types.
        wrapper = subprocess.check_output(['node', '--input-type=module', '-e',
            "import{readFileSync}from'node:fs';import{stripTypeScriptTypes}from'node:module';process.stdout.write(stripTypeScriptTypes(readFileSync(process.argv[1],'utf8'),{mode:'transform'}))", str(ROOT / 'mooring/client/index.ts')])
        (sdk / 'index.js').write_bytes(wrapper)
        assets = {str(path.relative_to(sdk)): digest(path) for path in sorted(sdk.rglob('*')) if path.is_file()}
        sdk_digest = hashlib.sha256(json.dumps(assets, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
        manifest = {'schema_version':1, 'source_sha':sha, 'target':config['target'], 'versions':config['versions'], 'binary_sha256':digest(embedded / 'mooring-collab'), 'editor_bundle_digest':editor_digest, 'sdk':assets, 'sdk_prefix':'/mooring-sdk/generated/' + sdk_digest, 'tools':config}
        (embedded / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
        # Only generated paths inside this disposable source are replaced.
        # Never remove the tracked disabled placeholder or unrelated public assets.
        output = ROOT / 'internal/mooringassets/embedded/generated'
        public = ROOT / 'public/mooring-sdk/generated'
        for source, target in ((embedded, output), (sdk, public)):
            confined(target)
            if target.exists():
                shutil.rmtree(target)
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copytree(source, target / sdk_digest if source == sdk else target)
    print('Mooring child, SDK and license assets prepared for source ' + sha)


if __name__ == '__main__':
    if len(sys.argv) != 3 or sys.argv[1] != '--release-dev':
        sys.exit('usage: build-mooring-runtime.py --release-dev EXACT_SHA')
    build(sys.argv[2])
