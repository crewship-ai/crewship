#!/usr/bin/env python3
"""Build the Word and Sheet packages published with a Crewship release (#5315).

Reads tools/craft-components/components.json and writes, per application,
crewship-craft-<application>.craftpkg: a tar of a WASI document engine, a
browser editor bundle and license notices, every file zstd-compressed and
pinned by the SHA-256 of its decompressed bytes in manifest.json. It also
writes catalog.txt, the pins the release binary is built with
("<application>=<version>=<sha256 of manifest.json>", comma separated).
Crewship downloads a package when an administrator turns the application on
and accepts it only if its manifest matches that pin. Nothing outside the
build directory and the output directory is touched.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import io
import shutil
import subprocess
import sys
import tarfile
import tempfile
import tomllib

ROOT = Path(__file__).resolve().parents[1]
RECIPE = ROOT / 'tools/craft-components'
OUTPUT = ROOT / 'build/craft-components/dist'
SCHEMA_VERSION = 2
sys.path.insert(0, str(ROOT / 'scripts'))

# Reuse the verified tool download and pinned-source helpers of the Mooring
# recipe instead of a second implementation of the same checks.
import importlib.util  # noqa: E402

_spec = importlib.util.spec_from_file_location('mooring_runtime', ROOT / 'scripts/build-mooring-runtime.py')
mooring = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(mooring)

HEX40 = re.compile(r'[0-9a-f]{40}')
HEX64 = re.compile(r'[0-9a-f]{64}')
MAX_EDITOR_BYTES = 64 * 1024 * 1024  # editororigin's bundle limit


def sha256_bytes(raw):
    return hashlib.sha256(raw).hexdigest()


def run(args, cwd=ROOT, env=None, capture=False):
    args = [str(a) for a in args]
    if capture:
        return subprocess.check_output(args, cwd=cwd, env=env)
    subprocess.run(args, cwd=cwd, env=env, check=True)
    return None


def load_recipe():
    recipe = json.loads((RECIPE / 'components.json').read_text())
    if recipe.get('schema_version') != 1:
        raise ValueError('unsupported components.json')
    if not HEX40.fullmatch(recipe['fonts']['commit']):
        raise ValueError('fonts must be pinned to a commit')
    seen = set()
    for c in recipe['components']:
        if c['application'] not in ('wordcraft', 'gridcraft') or c['application'] in seen:
            raise ValueError('unknown or repeated application')
        seen.add(c['application'])
        if not HEX40.fullmatch(c['commit']) or not HEX64.fullmatch(c['patch_sha256']):
            raise ValueError(c['application'] + ': commit and patch must be pinned')
        if sha256_bytes((RECIPE / c['patch']).read_bytes()) != c['patch_sha256']:
            raise ValueError(c['application'] + ': patch does not match its pin')
    return recipe


def rust_env(rust_bin, version, targets):
    rustc, cargo = Path(rust_bin) / 'rustc', Path(rust_bin) / 'cargo'
    if not rustc.is_file() or not cargo.is_file():
        raise ValueError('--rust-bin must contain rustc and cargo')
    actual = run([rustc, '--version'], capture=True).decode().split()
    if len(actual) < 2 or actual[1] != version:
        raise ValueError(f'Rust {version} required, found {" ".join(actual)}')
    env = {k: v for k, v in os.environ.items() if not k.startswith(('CARGO_TARGET_', 'RUSTFLAGS', 'CARGO_BUILD_'))}
    env['PATH'] = str(rust_bin) + os.pathsep + '/usr/bin:/bin'
    env['RUSTC'] = str(rustc)
    for target in targets:
        libdir = Path(run([rustc, '--print', 'target-libdir', '--target', target], capture=True).decode().strip())
        if not any(libdir.glob('libcore-*.rlib')):
            raise ValueError(f'Rust target {target} is not installed for {rustc}')
    return cargo, env, ' '.join(actual)


def compress(raw, destination):
    destination.parent.mkdir(parents=True, exist_ok=True)
    # Level 19 keeps the default 8 MiB window, which every zstd decoder accepts.
    compressed = subprocess.run(['zstd', '-19', '-q', '-c', '--no-progress'], input=raw, check=True, capture_output=True).stdout
    destination.write_bytes(compressed)
    return compressed


def add_file(stage, relative, raw):
    """Compresses raw into stage/relative.zst and returns its manifest entry."""
    if not raw:
        raise ValueError(relative + ' is empty')
    compress(raw, stage / (relative + '.zst'))
    return {'path': relative + '.zst', 'sha256': sha256_bytes(raw), 'size': len(raw)}


def fetch_source(c, temp):
    source = temp / (c['application'] + '-src')
    mooring.pinned_git(c['repository'], c['commit'], source)
    run(['git', '-C', source, 'checkout', '--quiet', 'FETCH_HEAD'], env=mooring.git_environment())
    run(['git', '-C', source, 'apply', '--whitespace=nowarn', RECIPE / c['patch']], env=mooring.git_environment())
    return source


def fetch_fonts(recipe, temp):
    fonts = temp / 'craft-fonts'
    mooring.pinned_git(recipe['fonts']['repository'], recipe['fonts']['commit'], fonts)
    run(['git', '-C', fonts, 'checkout', '--quiet', 'FETCH_HEAD'], env=mooring.git_environment())
    if not (fonts / 'fonts/manifest.txt').is_file():
        raise ValueError('craft-fonts checkout has no fonts/manifest.txt')
    return fonts


def remap(env, *pairs):
    env = dict(env)
    env['RUSTFLAGS'] = ' '.join(f'--remap-path-prefix={a}={b}' for a, b in pairs)
    return env


def build_engine(c, source, fonts, cargo, env, target_dir):
    env = remap(env, (source, '/' + c['application']), (fonts, '/craft-fonts'), (Path.home(), '/home'))
    env.update(CARGO_TARGET_DIR=str(target_dir), CRAFT_FONTS_DIR=str(fonts), CRAFT_FONTS_REQUIRED='1')
    run([cargo, 'build', '--release', '--locked', '--target', 'wasm32-wasip1', '-p', c['cli_package']], cwd=source, env=env)
    module = target_dir / 'wasm32-wasip1/release' / (c['cli_package'] + '.wasm')
    raw = module.read_bytes()
    if raw[:4] != b'\0asm':
        raise ValueError(c['application'] + ': engine is not a WebAssembly module')
    return raw


def build_editor(c, source, fonts, cargo, env, target_dir, bindgen, binaryen, temp, rustc_version, bindgen_version):
    app, package = c['application'], c['web_package']
    env = remap(env, (source, '/' + app), (fonts, '/craft-fonts'), (Path.home(), '/home'))
    env.update(CARGO_TARGET_DIR=str(target_dir), CRAFT_FONTS_DIR=str(fonts))
    run([cargo, 'build', '--release', '--locked', '--target', 'wasm32-unknown-unknown', '-p', package], cwd=source, env=env)
    out = temp / (app + '-web')
    run([bindgen / 'wasm-bindgen', target_dir / 'wasm32-unknown-unknown/release' / (package + '.wasm'),
         '--target', 'web', '--weak-refs', '--no-typescript', '--out-name', package, '--out-dir', out])
    wasm = out / (package + '_bg.wasm')
    optimized = out / (package + '_bg.opt.wasm')
    run([binaryen / 'bin/wasm-opt', '-Oz', '--all-features', wasm, '-o', optimized])
    optimized.replace(wasm)
    html = (source / 'apps' / package / 'index.html').read_text()
    icon = f'{app}.ico'
    html, icons = re.subn(r'  <link data-trunk rel="icon"[^\n]*\n', f'  <link rel="icon" href="./{icon}" />\n', html)
    html, rusts = re.subn(r'  <link data-trunk rel="rust"[^\n]*\n',
                          f'  <script type="module">import init from "./{package}.js";await init();</script>\n', html)
    if icons != 1 or rusts != 1 or 'data-trunk' in html:
        raise ValueError(app + ': unexpected editor index.html')
    (out / 'index.html').write_text(html)
    shutil.copyfile(source / 'assets/app-icon' / icon, out / icon)
    files = {str(p.relative_to(out)): sha256_bytes(p.read_bytes()) for p in sorted(out.rglob('*')) if p.is_file()}
    if sum((out / name).stat().st_size for name in files) > MAX_EDITOR_BYTES:
        raise ValueError(app + ': editor bundle exceeds the editor origin limit')
    lock = sha256_bytes((source / 'Cargo.lock').read_bytes())
    manifest = json.dumps({'files': files, 'source_commit': c['commit'], 'toolchain': {
        'cargo_lock_sha256': lock, 'fonts_commit': fonts_commit(fonts), 'patch_sha256': c['patch_sha256'],
        'profile': 'release', 'rustc': rustc_version, 'wasm_bindgen': bindgen_version}}, indent=2, sort_keys=True) + '\n'
    (out / 'manifest.json').write_text(manifest)
    return out, sha256_bytes(manifest.encode())


def fonts_commit(fonts):
    return run(['git', '-C', fonts, 'rev-parse', 'HEAD'], capture=True, env=mooring.git_environment()).decode().strip()


def crate_inventory(c, source, cargo, env):
    """Every crate linked into either target, with its declared license."""
    crates = {}
    for target in ('wasm32-wasip1', 'wasm32-unknown-unknown'):
        meta = json.loads(run([cargo, 'metadata', '--locked', '--offline', '--format-version', '1',
                               '--filter-platform', target], cwd=source, env=env, capture=True))
        packages = {p['id']: p for p in meta['packages']}
        nodes = {n['id']: n for n in meta['resolve']['nodes']}
        roots = [p['id'] for p in meta['packages'] if p['name'] in (c['cli_package'], c['web_package'])]
        stack, seen = list(roots), set()
        while stack:
            pid = stack.pop()
            if pid in seen:
                continue
            seen.add(pid)
            for dep in nodes[pid]['deps']:
                if any(k['kind'] in (None, 'normal') for k in dep['dep_kinds']):
                    stack.append(dep['pkg'])
        for pid in seen:
            p = packages[pid]
            if not p.get('license') and not p.get('license_file'):
                raise ValueError(f'{p["name"]} {p["version"]} declares no license')
            crates[(p['name'], p['version'])] = p.get('license') or ('see ' + p['license_file'])
    return [{'name': n, 'version': v, 'license': l} for (n, v), l in sorted(crates.items())]


# Licenses a crate may be distributed under in a package. An expression passes
# when the OR/AND tree can be satisfied with these alone; anything else, an
# unknown identifier or a crate that names only a license file, stops the
# build. Copyleft is never chosen: "Apache-2.0 OR GPL-2.0-only" passes through
# Apache-2.0, "GPL-2.0-only" alone does not.
ALLOWED_LICENSES = frozenset({
    '0BSD', 'Apache-2.0', 'BSD-2-Clause', 'BSD-3-Clause', 'BSL-1.0', 'CC0-1.0', 'ISC', 'MIT', 'MIT-0',
    'OFL-1.1', 'Ubuntu-font-1.0', 'Unicode-3.0', 'Unicode-DFS-2016', 'Unlicense', 'Zlib',
})
ALLOWED_EXCEPTIONS = frozenset({'LLVM-exception'})


def license_allowed(expression):
    """Whether an SPDX expression (Cargo's legacy "A/B" included) is satisfiable."""
    if not expression or not re.fullmatch(r'[A-Za-z0-9.+()/ -]+', expression):
        return False
    tokens = re.findall(r'\(|\)|[A-Za-z0-9.+-]+', re.sub(r'\s*/\s*', ' OR ', expression))
    pos = 0

    def peek():
        return tokens[pos] if pos < len(tokens) else None

    def take(expected=None):
        nonlocal pos
        tok = peek()
        if tok is None or (expected and tok != expected):
            raise ValueError(expression)
        pos += 1
        return tok

    def factor():
        if peek() == '(':
            take('(')
            value = disjunction()
            take(')')
            return value
        name = take()
        if name in ('AND', 'OR', 'WITH', ')'):
            raise ValueError(expression)
        ok = name in ALLOWED_LICENSES
        if peek() == 'WITH':
            take('WITH')
            ok = ok and take() in ALLOWED_EXCEPTIONS
        return ok

    def conjunction():
        value = factor()
        while peek() == 'AND':
            take('AND')
            value = factor() and value
        return value

    def disjunction():
        value = conjunction()
        while peek() == 'OR':
            take('OR')
            value = conjunction() or value
        return value

    try:
        value = disjunction()
        return value and pos == len(tokens)
    except ValueError:
        return False


def check_licenses(crates):
    refused = [f'{c["name"]} {c["version"]}: {c["license"]}' for c in crates if not license_allowed(c['license'])]
    if refused:
        raise ValueError('crates under a license the packages may not carry:\n  ' + '\n  '.join(refused))


def sbom(component, crates, fonts_repository, fonts_commit_id):
    """A CycloneDX 1.5 inventory of what the package is built from. It is
    deterministic (no timestamp, no serial number), so it is covered by the
    package's pin like every other file."""
    app, version = component['application'], component['version']
    return json.dumps({
        'bomFormat': 'CycloneDX', 'specVersion': '1.5', 'version': 1,
        'metadata': {'component': {
            'type': 'application', 'bom-ref': app, 'name': app, 'version': version,
            'licenses': [{'expression': component['licenses'][0]['spdx']}],
            'externalReferences': [{'type': 'vcs', 'url': component['source']['repository'] + '@' + component['source']['commit']}]}},
        'components': [
            {'type': 'library', 'bom-ref': f'pkg:cargo/{c["name"]}@{c["version"]}', 'name': c['name'], 'version': c['version'],
             'purl': f'pkg:cargo/{c["name"]}@{c["version"]}', 'licenses': [{'expression': re.sub(r'\s*/\s*', ' OR ', c['license'])}]}
            for c in crates] + [
            {'type': 'data', 'bom-ref': 'craft-fonts', 'name': 'craft-fonts', 'version': fonts_commit_id,
             'licenses': [{'expression': 'OFL-1.1'}],
             'externalReferences': [{'type': 'vcs', 'url': fonts_repository.removesuffix('.git') + '@' + fonts_commit_id}]}],
    }, indent=1, sort_keys=True).encode() + b'\n'


def font_notices(fonts):
    names = [p for p in sorted((fonts / 'fonts').rglob('*')) if p.is_file() and re.search(r'(OFL|LICEN[CS]E|COPYING)', p.name, re.I)]
    return {str(p.relative_to(fonts)): p.read_bytes() for p in names}


def asset_name(app):
    return f'crewship-craft-{app}.craftpkg'


def write_package(stage, component, toolchain, out):
    """Writes one application's package deterministically; returns its pin."""
    manifest = (json.dumps({'schema_version': SCHEMA_VERSION, 'toolchain': toolchain, 'component': component},
                           indent=2) + '\n').encode()
    files = [component['engine']] + component['editor']['files'] + component['notices']
    out.mkdir(parents=True, exist_ok=True)
    target = out / asset_name(component['application'])
    with tarfile.open(target, 'w', format=tarfile.PAX_FORMAT) as tar:
        def add(name, raw):
            info = tarfile.TarInfo(name)
            info.size, info.mode, info.mtime, info.uid, info.gid, info.uname, info.gname = len(raw), 0o644, 0, 0, 0, '', ''
            tar.addfile(info, io.BytesIO(raw))
        add('manifest.json', manifest)
        for f in sorted(files, key=lambda f: f['path']):
            add(f['path'], (stage / f['path']).read_bytes())
    return f'{component["application"]}={component["version"]}={sha256_bytes(manifest)}'


def build(rust_bin, keep, build_dir=None, out=None):
    recipe = load_recipe()
    config = json.loads((ROOT / 'mooring/build-tools.json').read_text())
    cargo, env, rustc_version = rust_env(rust_bin, recipe['rust_version'], recipe['targets'])
    build_root = Path(build_dir).resolve() if build_dir else mooring.confined(ROOT / 'build/craft-components')
    build_root.mkdir(parents=True, exist_ok=True)
    out = Path(out).resolve() if out else mooring.confined(OUTPUT)
    with tempfile.TemporaryDirectory(prefix='craft-', dir=build_root) as temp:
        temp = Path(temp)
        bindgen = mooring.tool_archive(config['tools']['wasm-bindgen'], temp / 'bindgen')
        binaryen = mooring.tool_archive(config['tools']['binaryen'], temp / 'binaryen')
        fonts = fetch_fonts(recipe, temp)
        stage = temp / 'generated'
        components = []
        for c in recipe['components']:
            app = c['application']
            source = fetch_source(c, temp)
            target_dir = build_root / ('target-' + app)
            version = tomllib.loads((source / 'Cargo.toml').read_text()).get('workspace', {}).get('package', {}).get('version') \
                or tomllib.loads((source / 'apps' / c['cli_package'] / 'Cargo.toml').read_text())['package']['version']
            engine = add_file(stage, f'{app}/engine.wasm', build_engine(c, source, fonts, cargo, env, target_dir))
            editor_dir, editor_digest = build_editor(c, source, fonts, cargo, env, target_dir, bindgen, binaryen, temp,
                                                     rustc_version, config['tools']['wasm-bindgen']['version'])
            editor_files = [add_file(stage, f'{app}/editor/{editor_digest}/{p.relative_to(editor_dir)}', p.read_bytes())
                            for p in sorted(editor_dir.rglob('*')) if p.is_file()]
            notices = [add_file(stage, f'{app}/licenses/{name}', (source / name).read_bytes()) for name in c['notices']]
            for name, raw in font_notices(fonts).items():
                notices.append(add_file(stage, f'{app}/licenses/craft-fonts/{name}', raw))
            crates = crate_inventory(c, source, cargo, env)
            check_licenses(crates)
            notices.append(add_file(stage, f'{app}/licenses/crates.json', json.dumps(crates, indent=1).encode() + b'\n'))
            component = {
                'application': app, 'version': f'{version}+{c["commit"][:12]}', 'engine_revision': c['commit'],
                'source': {'repository': c['repository'].removesuffix('.git'), 'commit': c['commit'], 'patch_sha256': c['patch_sha256']},
                'engine': engine, 'editor': {'digest': editor_digest, 'files': editor_files},
                'licenses': [c['license'], {'name': 'craft-fonts', 'spdx': 'OFL-1.1'}], 'notices': notices}
            notices.append(add_file(stage, f'{app}/licenses/sbom.cdx.json',
                                    sbom(component, crates, recipe['fonts']['repository'], fonts_commit(fonts))))
            components.append(component)
            print(f'{app}: engine {engine["sha256"]} ({engine["size"]} bytes), editor {editor_digest}', flush=True)
        toolchain = f'{rustc_version}; wasm-bindgen {config["tools"]["wasm-bindgen"]["version"]}; binaryen {config["tools"]["binaryen"]["version"]}'
        if out.exists():
            shutil.rmtree(out)
        pins = [write_package(stage, c, toolchain, out) for c in components]
        (out / 'catalog.txt').write_text(','.join(sorted(pins)) + '\n')
        if keep:
            shutil.copytree(temp, Path(keep), dirs_exist_ok=False)
    print('Craft packages written to ' + str(out))
    print('catalog: ' + ','.join(sorted(pins)))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument('--rust-bin', required=True, help='directory with the pinned rustc and cargo')
    parser.add_argument('--build-dir', help='Cargo target and scratch directory (default build/craft-components)')
    parser.add_argument('--keep', help='copy the intermediate build tree here (debugging)')
    parser.add_argument('--out', help='package directory (default build/craft-components/dist)')
    args = parser.parse_args()
    build(args.rust_bin, args.keep, args.build_dir, args.out)
