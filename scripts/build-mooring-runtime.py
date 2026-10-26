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


def bundle_bindgen_snippets(native_output, output):
    # Editor origins deliberately serve only flat, manifest-verified names.
    # wasm-bindgen emits inline JS in snippets/; include those leaf modules and
    # rewrite both static imports and the generated import map to their flat names.
    module = (native_output / 'wordcraft_web.js').read_text()
    imports = {name for _, name in re.findall(r'''(['"])(\./snippets/[^'"\r\n]+)\1''', module)}
    if len(imports) > 512:
        raise ValueError('too many wasm-bindgen snippets')
    for name in sorted(imports):
        parts = name[2:].split('/')
        if (not name.endswith('.js') or any(part in ('.', '..') or
                not re.fullmatch(r'[A-Za-z0-9_.-]+', part) for part in parts)):
            raise ValueError('invalid wasm-bindgen snippet path')
        source = native_output
        for part in parts:
            source = source / part
            if source.is_symlink():
                raise ValueError('linked wasm-bindgen snippet input')
        if not source.is_file() or source.stat().st_size > 64 * 1024 * 1024:
            raise ValueError('missing or oversized wasm-bindgen snippet')
        body = source.read_bytes()
        if re.search(r'''["']\.{1,2}/''', body.decode('utf-8')):
            raise ValueError('non-leaf wasm-bindgen snippet')
        flat = 'wasm-snippet-' + hashlib.sha256(name.encode() + b'\0' + body).hexdigest() + '.js'
        (output / flat).write_bytes(body)
        for quote in ("'", '"'):
            module = module.replace(quote + name + quote, quote + './' + flat + quote)
    if './snippets/' in module:
        raise ValueError('unsupported wasm-bindgen snippet reference')
    if imports:
        (output / 'wordcraft_web.js').write_text(module)


def assemble_editor(sha, native, fonts, metadata, native_output, sdk, child, licenses, output, config,
                    native_inventory=('wordcraft-native-inventory.json', 'wordcraft-native-license-inventory.json'),
                    additional_inventories=(), provenance_extra=None):
    # All names are flat and fixed; the same immutable bytes are checked again
    # by editororigin before serving. No runtime filesystem or private path.
    output.mkdir()
    def copy(source, name):
        if source.is_symlink() or not source.is_file():
            raise ValueError('non-regular editor input')
        shutil.copyfile(source, output / name)
    for name in ('wordcraft_web.js', 'wordcraft_web_bg.wasm'):
        copy(native_output / name, name)
    bundle_bindgen_snippets(native_output, output)
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
    copy(licenses / native_inventory[0], native_inventory[1])
    for name in additional_inventories:
        copy(licenses / name, name)
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
    provenance = {'crewship_source': sha,
        'native_upstream_source': metadata['base'], 'native_patch_sha256': metadata['patch_sha256'],
        'native_patch_commit': metadata['native_patch_commit'], 'release_fonts': 'original-six-8dcd-no-Myanmar',
        'tools': tools, 'qualification': 'Build only; requires HTTP/WS/UI/Saved acceptance'}
    provenance.update(provenance_extra or {})
    (output / 'build-provenance.json').write_text(json.dumps(provenance, sort_keys=True, indent=2) + '\n')
    files = {file.name: digest(file) for file in sorted(output.iterdir())}
    raw = json.dumps({'files': files, 'source_commit': metadata['native_patch_commit'], 'toolchain': tools},
                     sort_keys=True, separators=(',', ':')).encode()
    total = len(raw) + sum(file.stat().st_size for file in output.iterdir())
    if len(files) > 512 or len(raw) > 1024 * 1024 or total > 64 * 1024 * 1024:
        raise ValueError('editor bundle exceeds existing qualification limits')
    (output / 'manifest.json').write_bytes(raw)
    return hashlib.sha256(raw).hexdigest()


def build_editor(sha, temp, build_root, cargo, env, bindgen, binaryen, sdk, embedded, config, inputs=None):
    native, fonts, metadata = inputs or prepare_editor_inputs(temp)
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


COMMAND_FORMAT = 'wordcraft-c1-v2'
COMMAND_REVISION = '7ef2633a7e84956522d1ca47b99fe6feef16c1db'
COMMAND_CLOCK_PATCH = '9c2ff901ae408c72d12bbb864895a63791f000551978bf615734a6b41e381581'


def verified_command_elf(path):
    if path.is_symlink() or not path.is_file() or not 64 <= path.stat().st_size <= 64 * 1024 * 1024:
        raise ValueError('C1 executable missing or oversized')
    with path.open('rb') as stream:
        header = stream.read(64)
    if (header[:7] != b'\x7fELF\x02\x01\x01' or
            header[16:18] not in (b'\x02\x00', b'\x03\x00') or header[18:20] != b'\x3e\x00'):
        raise ValueError('C1 executable must be Linux x86_64 ELF64')
    return digest(path)


def command_package_closure(metadata, lock_path, root_manifest, roots):
    """Actual resolved normal/build closure, not a binary linkage/license audit."""
    packages = {p['id']: p for p in metadata['packages']}
    nodes = {p['id']: p for p in metadata['resolve']['nodes']}
    found = [p['id'] for p in packages.values() if Path(p['manifest_path']).resolve() == root_manifest.resolve()]
    if len(found) != 1 or len(packages) != len(metadata['packages']) or len(nodes) != len(metadata['resolve']['nodes']):
        raise ValueError('C1 resolved package identity mismatch')
    locked = {(p['name'], p['version'], p.get('source')): p for p in tomllib.loads(lock_path.read_text())['package']}
    pending, visited, result = found[:], set(), []
    while pending:
        identity = pending.pop()
        if identity in visited:
            continue
        visited.add(identity)
        package, node = packages[identity], nodes[identity]
        source = package.get('source')
        if source is not None and not source.startswith(('registry+https://', 'sparse+https://', 'git+https://')):
            raise ValueError('unsupported C1 dependency source')
        local = None
        if source is None:
            directory = Path(package['manifest_path']).resolve().parent
            matches = [label + '/' + str(directory.relative_to(root.resolve())) for label, root in roots.items() if directory.is_relative_to(root.resolve())]
            if len(matches) != 1:
                raise ValueError('C1 local dependency outside prepared inputs')
            local = matches[0].rstrip('/.')
        lock = locked[(package['name'], package['version'], source)]
        public = {'name': package['name'], 'version': package['version'], 'source': source,
                  'checksum': lock.get('checksum'), 'package_path': local,
                  'spdx': package.get('license'), 'features': sorted(node['features'])}
        result.append((public, package))
        for dependency in node['deps']:
            kinds = dependency['dep_kinds']
            if not kinds or any(k['kind'] not in (None, 'build', 'dev') for k in kinds):
                raise ValueError('unknown C1 dependency kind')
            if any(k['kind'] != 'dev' for k in kinds):
                pending.append(dependency['pkg'])
    return sorted(result, key=lambda item: (item[0]['name'], item[0]['version'], item[0]['source'] or ''))


def command_pinned_license_texts(public, destination=None):
    """Reuse texts only for the exact immutable crate, never its old closure."""
    recipe = ROOT / 'tools/wordcraft-mooring-native'
    inventory = json.loads((recipe / 'license-inventory.json').read_text())
    keys = ('name', 'version', 'source', 'checksum', 'spdx')
    if not public.get('checksum'):
        raise ValueError('C1 missing license requires immutable registry identity')
    matches = [entry for entry in inventory['packages']
               if all(entry['package'].get(key) == public.get(key) for key in keys)]
    if len(matches) == 1 and any(t.get('kind') == 'license' for t in matches[0]['texts']):
        return [(verified_native_text(recipe, text), text) for text in matches[0]['texts']]
    # This exact registry crate omits repository-level license files. The
    # crate's .cargo_vcs_info.json binds the revision; texts are independently
    # hash-pinned, not fetched from a branch or inferred from its SPDX label.
    accesskit = ('accesskit', '0.25.1', 'registry+https://github.com/rust-lang/crates.io-index',
                 'ad442f58ee04714aaa0ba0a2768c1ea1935b29507bb22ddece8cbbc76db02932', 'MIT OR Apache-2.0')
    if destination is None or tuple(public.get(key) for key in keys) != accesskit:
        raise ValueError('C1 dependency license text absent: ' + public['name'] + '-' + public['version'])
    revision = 'ce8164ba92995cfa86005b6259115e08c8244253'
    result = []
    for name, size, expected in (
            ('LICENSE-MIT', 1023, '23f18e03dc49df91622fe2a76176497404e46ced8a715d9d2b67a7446571cca3'),
            ('LICENSE-APACHE', 9723, '62c7a1e35f56406896d7aa7ca52d0cc0d272ac022b5d2796e7d6905db8a3636a')):
        url = 'https://raw.githubusercontent.com/AccessKit/accesskit/' + revision + '/' + name
        with urllib.request.urlopen(url, timeout=60) as response:
            body = response.read(size + 1)
        if len(body) != size or hashlib.sha256(body).hexdigest() != expected:
            raise ValueError('C1 pinned supplemental license mismatch')
        target = destination / 'commands' / (expected + '.txt')
        target.parent.mkdir(parents=True, exist_ok=True)
        if target.is_symlink() or (target.exists() and digest(target) != expected):
            raise ValueError('C1 pinned supplemental license collision')
        target.write_bytes(body)
        result.append((target, {'kind': 'license', 'origin': {
            'repository': 'https://github.com/AccessKit/accesskit', 'revision': revision,
            'path': name, 'url': url}}))
    return result


def collect_command_licenses(destination, prepared, metadata, component):
    """Ship actual resolved source licenses separately from the legacy inventory.

    Public package identities/lock hashes/features, never cache paths, are
    retained. Selecting upstream WordCraft MIT is explicit and source-verified;
    transitive SPDX declarations and all discovered notices remain unchanged.
    """
    source, helper = prepared / 'source', prepared / 'helper'
    root_manifest = helper / 'Cargo.toml' if component == 'engine' else source / 'apps/wordcraft-web/Cargo.toml'
    lock = helper / 'Cargo.lock' if component == 'engine' else source / 'Cargo.lock'
    closure = command_package_closure(metadata, lock, root_manifest, {'source': source, 'helper': helper})
    inventory = {'component': component, 'engine_revision': COMMAND_REVISION,
                 'clock_patch_sha256': COMMAND_CLOCK_PATCH, 'cargo_lock_sha256': digest(lock),
                 'filter_platform': 'x86_64-unknown-linux-gnu' if component == 'engine' else 'wasm32-unknown-unknown',
                 'method': 'Conservative actual resolved normal/build/proc-macro closure; excludes dev-only edges. Not a binary linkage or complete legal audit.',
                 'packages': []}
    manifest = json.loads((destination / 'manifest.json').read_text())
    for public, package in closure:
        directory = Path(package['manifest_path']).resolve().parent
        local_wordcraft = package.get('source') is None and directory.is_relative_to(source.resolve())
        local_wrapper = directory == helper.resolve()
        selected = None
        pinned = {}
        if local_wordcraft:
            if public['spdx'] != 'MIT OR Apache-2.0' or digest(source / 'LICENSE-MIT') != digest(ROOT / 'tools/wordcraft-c1-engine/WORDCRAFT-LICENSE-MIT'):
                raise ValueError('C1 upstream MIT choice does not match pinned source')
            texts = [source / 'LICENSE-MIT', source / 'NOTICE']
            selected = 'MIT'
        elif local_wrapper:
            texts = [ROOT / 'LICENSE', ROOT / 'NOTICE']
        else:
            # Registry crates own their package root. A git crate may refer to
            # repository-level notices: stop at that exact public checkout.
            boundary = directory
            if (package.get('source') or '').startswith('git+'):
                for parent in (directory, *directory.parents):
                    boundary = parent
                    if (parent / '.git').exists():
                        break
                else:
                    raise ValueError('C1 git dependency license boundary absent')
            license_names = ('license', 'copying', 'unlicense', 'notice', 'authors', 'ofl', 'ufl', 'copyright')
            # Font crates carry mandatory OFL/Ubuntu notices below fonts/.
            # Recurse only inside this exact package, not sibling dependencies.
            candidates = [p for p in directory.rglob('*') if p.is_file() and
                          (p.name.lower().startswith(license_names) or 'license' in p.name.lower() or
                           ('fonts' in p.relative_to(directory).parts and p.suffix.lower() == '.txt'))]
            for parent in (directory, *directory.parents):
                candidates.extend(p for p in parent.iterdir() if p.is_file() and p.name.lower().startswith(license_names))
                if parent == boundary:
                    break
            if package.get('license_file'):
                named = Path(package['license_file'])
                candidates.append(named if named.is_absolute() else directory / named)
            texts = sorted(set(candidates))
            if any(not p.resolve().is_relative_to(boundary.resolve()) for p in texts):
                raise ValueError('C1 dependency license escapes source')
            # Upstream git packages may link a nested LICENSE to their own
            # repository root; copy only the bounded in-repository target.
            texts = sorted(set(p.resolve() for p in texts))
            if not texts:
                pinned = dict(command_pinned_license_texts(public, destination))
                texts = list(pinned)
        if not public['spdx']:
            raise ValueError('C1 dependency SPDX declaration absent')
        records = []
        for text in texts:
            if text.is_symlink() or not text.is_file() or not 0 < text.stat().st_size <= 2 * 1024 * 1024:
                raise ValueError('C1 license source must be bounded regular text')
            hashed = digest(text)
            relative = 'commands/' + hashed + '.txt'
            target = destination / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            if target.exists() and digest(target) != hashed:
                raise ValueError('C1 license collision')
            if not target.exists():
                shutil.copyfile(text, target)
            record = {'name': text.name, 'path': relative, 'sha256': hashed}
            if text in pinned:
                record.update(kind=pinned[text]['kind'])
                if 'origin' in pinned[text]:
                    record['origin'] = pinned[text]['origin']
            records.append(record)
            manifest.append({'crate': public['name'], 'version': public['version'], 'spdx': public['spdx'],
                             'component': 'wordcraft-command-' + component, 'path': relative, 'sha256': hashed})
        inventory['packages'].append({'package': public, 'selected_license': selected, 'texts': records})
    (destination / ('wordcraft-command-' + component + '-inventory.json')).write_text(json.dumps(inventory, sort_keys=True, indent=2) + '\n')
    (destination / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
    return inventory


def build_commands(sha, temp, build_root, cargo, env, bindgen, binaryen, sdk, embedded, fonts, config):
    entry = ROOT / 'lib/mooring/wordcraft-command-child-loader.ts'
    if not entry.is_file():
        raise ValueError('release source lacks the separate C1 child integration')
    engine_spec = json.loads((ROOT / 'tools/wordcraft-c1-engine/source.json').read_text())
    browser_spec = json.loads((ROOT / 'tools/wordcraft-c1-browser/source.json').read_text())
    if (engine_spec['rev'] != COMMAND_REVISION or engine_spec['patch_sha256'] != COMMAND_CLOCK_PATCH or
            browser_spec['revision'] != COMMAND_REVISION or
            browser_spec['engine_source_manifest_sha256'] != digest(ROOT / 'tools/wordcraft-c1-engine/source.json')):
        raise ValueError('C1 source/clock/renderer pins disagree')
    native_host = build_root / 'target/release/mooring-wordcraft-command-host'
    command([cargo, 'build', '--release', '--locked', '--offline', '-p', 'mooring-collab',
             '--features', 'wordcraft-command-experiment', '--bin', 'mooring-wordcraft-command-host'], cwd=ROOT / 'mooring', env=env)
    host_hash = verified_command_elf(native_host)
    inputs = temp / 'command-engine-inputs'
    command([sys.executable, ROOT / 'tools/wordcraft-c1-engine/prepare.py', '--output', inputs], env=git_environment())
    renderer = temp / 'command-renderer-inputs'
    command([sys.executable, ROOT / 'tools/wordcraft-c1-browser/prepare.py', '--output', renderer], env=git_environment())
    common = dict(env)
    common.update(CRAFT_FONTS_DIR=str(fonts), CARGO_NET_OFFLINE='false')
    helper_env = dict(common, CARGO_TARGET_DIR=str(build_root / 'command-engine-target'),
                      RUSTFLAGS=f'--remap-path-prefix={ROOT}=/crewship --remap-path-prefix={inputs}=/wordcraft-c1')
    # Match the qualified native helper and the pinned WASM's empty optional
    # craft-font profile; the release manifest contains no Georgian face.
    helper_env.pop('CRAFT_FONTS_DIR', None)
    helper_env.pop('CRAFT_FONTS_REQUIRED', None)
    command([cargo, 'build', '--release', '--locked'], cwd=inputs / 'helper', env=helper_env)
    helper = build_root / 'command-engine-target/release/wordcraft-c1-engine'
    engine_hash = verified_command_elf(helper)
    for prepared, component, manifest, local_env in [
            (inputs, 'engine', inputs / 'helper/Cargo.toml', helper_env)]:
        resolved = json.loads(subprocess.check_output([str(cargo), 'metadata', '--locked', '--offline', '--filter-platform', config['target'],
            '--format-version', '1', '--manifest-path', str(manifest)], cwd=manifest.parent, env=local_env))
        collect_command_licenses(embedded / 'licenses', prepared, resolved, component)
    web_env = dict(common, CARGO_TARGET_DIR=str(build_root / 'command-web-target'),
                   RUSTFLAGS=f'--remap-path-prefix={ROOT}=/crewship --remap-path-prefix={renderer}=/wordcraft-c1')
    source = renderer / 'source'
    command([cargo, 'build', '--release', '--locked', '--target', 'wasm32-unknown-unknown', '-p', 'wordcraft-web'], cwd=source, env=web_env)
    resolved = json.loads(subprocess.check_output([str(cargo), 'metadata', '--locked', '--offline', '--filter-platform', 'wasm32-unknown-unknown',
        '--format-version', '1', '--manifest-path', str(source / 'apps/wordcraft-web/Cargo.toml')], cwd=source, env=web_env))
    collect_command_licenses(embedded / 'licenses', renderer, resolved, 'renderer')
    native_output = temp / 'command-web'
    command([bindgen / 'wasm-bindgen', build_root / 'command-web-target/wasm32-unknown-unknown/release/wordcraft-web.wasm',
             '--target', 'web', '--out-name', 'wordcraft_web', '--out-dir', native_output])
    optimized = native_output / 'wordcraft_web_bg.optimized.wasm'
    command([binaryen / 'bin/wasm-opt', '-Oz', '--all-features', native_output / 'wordcraft_web_bg.wasm', '-o', optimized])
    optimized.replace(native_output / 'wordcraft_web_bg.wasm')
    child = temp / 'command-child.js'
    command([ROOT / 'node_modules/.bin/esbuild', entry, '--bundle', '--format=esm', '--platform=browser', '--target=es2022', '--outfile=' + str(child)])
    bundle = temp / 'command-editor-bundle'
    # Use the established immutable editor package layout, with the C1 entry
    # and actual dependency texts; never substitute the legacy bundle digest.
    metadata = {'base': COMMAND_REVISION, 'patch_sha256': browser_spec['patch_sha256'], 'native_patch_commit': sha}
    editor_hash = assemble_editor(sha, source, fonts, metadata, native_output, sdk, child, embedded / 'licenses', bundle, config,
        native_inventory=('wordcraft-command-renderer-inventory.json', 'wordcraft-command-renderer-inventory.json'),
        additional_inventories=('wordcraft-command-engine-inventory.json',),
        provenance_extra={'format': COMMAND_FORMAT, 'clock_patch_sha256': COMMAND_CLOCK_PATCH, 'component': 'wordcraft-command-c1',
                          'helper_optional_craft_fonts': 'none'})
    destination = embedded / 'editor' / editor_hash
    if destination.exists():
        raise ValueError('C1 editor must not replace an existing legacy bundle')
    shutil.move(bundle, destination)
    shutil.copyfile(native_host, embedded / 'command-host')
    shutil.copyfile(helper, embedded / 'command-engine')
    return {'format': COMMAND_FORMAT, 'engine_revision': COMMAND_REVISION,
            'clock_patch_sha256': COMMAND_CLOCK_PATCH, 'host_sha256': host_hash,
            'engine_sha256': engine_hash, 'editor_bundle_digest': editor_hash}


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
        editor_inputs = prepare_editor_inputs(temp)
        editor_digest = build_editor(sha, temp, build_root, cargo, env, bindgen, binaryen, sdk, embedded, config, inputs=editor_inputs)
        commands = build_commands(sha, temp, build_root, cargo, env, bindgen, binaryen, sdk, embedded, editor_inputs[1], config)
        # The wrapper has no runtime imports; remove its TypeScript-only types.
        wrapper = subprocess.check_output(['node', '--input-type=module', '-e',
            "import{readFileSync}from'node:fs';import{stripTypeScriptTypes}from'node:module';process.stdout.write(stripTypeScriptTypes(readFileSync(process.argv[1],'utf8'),{mode:'transform'}))", str(ROOT / 'mooring/client/index.ts')])
        (sdk / 'index.js').write_bytes(wrapper)
        assets = {str(path.relative_to(sdk)): digest(path) for path in sorted(sdk.rglob('*')) if path.is_file()}
        sdk_digest = hashlib.sha256(json.dumps(assets, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
        manifest = {'schema_version':1, 'source_sha':sha, 'target':config['target'], 'versions':config['versions'], 'binary_sha256':digest(embedded / 'mooring-collab'), 'editor_bundle_digest':editor_digest, 'commands':commands, 'sdk':assets, 'sdk_prefix':'/mooring-sdk/generated/' + sdk_digest, 'tools':config}
        (embedded / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
        verify_source(sha)  # Detect source drift before publishing any generated assets.
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
