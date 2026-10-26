import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('build_mooring', Path(__file__).with_name('build-mooring-runtime.py'))
build = importlib.util.module_from_spec(spec)
spec.loader.exec_module(build)


class ToolArchiveTests(unittest.TestCase):
    def archive(self, linked=False, duplicate=False):
        stream = io.BytesIO()
        with tarfile.open(fileobj=stream, mode='w:gz') as archive:
            for _ in range(2 if duplicate else 1):
                entry = tarfile.TarInfo('pin/wasm-bindgen')
                content = b'fixture tool'
                if linked:
                    entry.type = tarfile.SYMTYPE
                    entry.linkname = '/outside'
                else:
                    entry.size = len(content)
                archive.addfile(entry, None if linked else io.BytesIO(content))
        return stream.getvalue()

    def prepare(self, raw, destination, **overrides):
        config = {'url':'https://example.invalid/pinned', 'sha256':hashlib.sha256(raw).hexdigest(), 'archive_prefix':'pin', 'files':['wasm-bindgen'], 'max_archive_bytes':1024*1024}
        config.update(overrides)
        with patch.object(build.urllib.request, 'urlopen', return_value=io.BytesIO(raw)):
            return build.tool_archive(config, destination)

    def test_verified_tool_only_selected_regular_file(self):
        with tempfile.TemporaryDirectory() as temp:
            result = self.prepare(self.archive(), Path(temp))
            self.assertEqual((result/'wasm-bindgen').read_bytes(), b'fixture tool')
            self.assertEqual((result/'wasm-bindgen').stat().st_mode & 0o777, 0o500)

    def test_wrong_digest_link_duplicate_or_oversize_never_accepted(self):
        for name in ('checksum','link','duplicate','limit','path'):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temp:
                raw = self.archive(linked=name=='link', duplicate=name=='duplicate')
                overrides = {'sha256':'0'*64} if name=='checksum' else {'max_archive_bytes':1} if name=='limit' else {'files':['../escape']} if name=='path' else {}
                with self.assertRaises(ValueError):
                    self.prepare(raw, Path(temp), **overrides)

    def test_explicit_dev_release_marker_requests_pinned_assets(self):
        # This dev2 deployment branch explicitly builds the reviewed assets.
        # The integration branch retains its default-off marker and assertion;
        # application enablement still requires the separate runtime flag.
        marker = json.loads((build.ROOT/'mooring/runtime-release.json').read_text())
        self.assertEqual(marker, {'schema_version':1,'enabled':True})

    def test_unselected_members_count_toward_expansion_limit(self):
        raw = io.BytesIO()
        with tarfile.open(fileobj=raw, mode='w:gz') as archive:
            member = tarfile.TarInfo('pin/unselected')
            member.size = 2048
            archive.addfile(member, io.BytesIO(b'x'*2048))
        with tempfile.TemporaryDirectory() as temp:
            with self.assertRaisesRegex(ValueError, 'expansion limit'):
                self.prepare(raw.getvalue(), Path(temp), max_expanded_bytes=1024)
            self.assertFalse((Path(temp)/'wasm-bindgen').exists())

    def test_generated_and_build_ancestors_cannot_escape_source(self):
        for linked in ('build', 'public/mooring-sdk', 'internal/mooringassets'):
            with self.subTest(linked=linked), tempfile.TemporaryDirectory() as temp:
                root, outside = Path(temp)/'source', Path(temp)/'outside'
                root.mkdir()
                outside.mkdir()
                sentinel = outside/'generated/preserve'
                sentinel.parent.mkdir()
                sentinel.write_bytes(b'unrelated data')
                link = root/linked
                link.parent.mkdir(parents=True, exist_ok=True)
                link.symlink_to(outside, target_is_directory=True)
                with patch.object(build, 'ROOT', root):
                    with self.assertRaisesRegex(ValueError, 'linked generated'):
                        build.confined(link/('mooring' if linked=='build' else 'generated'))
                self.assertEqual(sentinel.read_bytes(), b'unrelated data')

    def test_source_requires_exact_clean_committed_identity(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            def git(*args):
                return subprocess.check_output(['git', '-C', str(root), *args], stderr=subprocess.DEVNULL)
            git('init', '-b', 'main')
            source = root/'source'
            source.write_text('committed')
            git('add', 'source')
            git('-c', 'user.name=fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-qm', 'fixture')
            sha = git('rev-parse', 'HEAD').decode().strip()
            with patch.object(build, 'ROOT', root):
                build.verify_source(sha)
                with self.assertRaisesRegex(ValueError, 'SHA mismatch'):
                    build.verify_source('0'*40)
                source.write_text('dirty')
                with self.assertRaisesRegex(ValueError, 'clean and committed'):
                    build.verify_source(sha)
                git('add', 'source')
                with self.assertRaisesRegex(ValueError, 'clean and committed'):
                    build.verify_source(sha)
                source.write_text('committed')
                git('add', 'source')
                (root/'untracked').write_text('unadmitted source')
                with self.assertRaisesRegex(ValueError, 'clean and committed'):
                    build.verify_source(sha)


class PinnedSourceIsolation(unittest.TestCase):
    def test_inherited_git_metadata_cannot_redirect_source_fetch(self):
        with tempfile.TemporaryDirectory(prefix='mooring-build-isolation-') as temporary:
            root = Path(temporary)
            env = build.git_environment()
            def git(*args):
                return subprocess.check_output(['git', *map(str, args)], env=env, text=True).strip()
            for name in ('upstream', 'caller'):
                source = root / name
                git('init', '--quiet', source)
                (source / 'input').write_text(name)
                git('-C', source, 'add', 'input')
                git('-c', 'user.name=fixture', '-c', 'user.email=fixture@example.test', '-C', source, 'commit', '--quiet', '-m', 'fixture')
            caller = root / 'caller'
            def snapshot():
                return {str(p.relative_to(caller)): hashlib.sha256(p.read_bytes()).hexdigest() for p in caller.rglob('*') if p.is_file()}
            before = snapshot()
            pin = git('-C', root / 'upstream', 'rev-parse', 'HEAD')
            destination = root / 'fetched'
            hostile = {'GIT_DIR': str(caller / '.git'), 'GIT_WORK_TREE': str(caller),
                       'GIT_INDEX_FILE': str(caller / '.git/index'), 'GIT_COMMON_DIR': str(caller / '.git'),
                       'GIT_OBJECT_DIRECTORY': str(caller / '.git/objects'),
                       'GIT_ALTERNATE_OBJECT_DIRECTORIES': str(caller / '.git/objects')}
            with patch.dict(os.environ, hostile):
                build.pinned_git((root / 'upstream').as_uri(), pin, destination)
            self.assertEqual(before, snapshot())
            self.assertTrue((destination / '.git/FETCH_HEAD').is_file())
            self.assertEqual(pin, git('-C', destination, 'rev-parse', 'FETCH_HEAD'))
            self.assertEqual('commit', git('-C', destination, 'cat-file', '-t', pin))


class NativeLicenseTests(unittest.TestCase):
    def fixture(self, root):
        native, recipe, destination = root/'native', root/'tools/wordcraft-mooring-native', root/'licenses'
        (native/'apps/wordcraft-web').mkdir(parents=True)
        (native/'apps/wordcraft-web/Cargo.toml').write_text('[package]\nname="wordcraft-web"\nversion="1.0.0"\n')
        lock = 'version=4\n[[package]]\nname="wordcraft-web"\nversion="1.0.0"\n'
        packages, nodes = [], []
        for name, kind in [('wordcraft-web', None), ('runtime', None), ('macro', 'build'), ('dev-only', 'dev')]:
            source = None if name == 'wordcraft-web' else 'registry+https://example.invalid/index'
            if source:
                lock += f'[[package]]\nname="{name}"\nversion="1.0.0"\nsource="{source}"\nchecksum="'+'a'*64+'"\n'
            packages.append({'id':name, 'name':name, 'version':'1.0.0', 'source':source,
                             'license':'MIT', 'manifest_path':str(native/'apps/wordcraft-web/Cargo.toml') if source is None else '/unused/cache/'+name+'/Cargo.toml'})
            nodes.append({'id':name, 'features':[], 'deps':[]})
            if kind is not None or name == 'runtime':
                nodes[0]['deps'].append({'pkg':name, 'dep_kinds':[{'kind':kind}]})
        (native/'Cargo.lock').write_text(lock)
        metadata = {'packages':packages, 'resolve':{'root':'wordcraft-web', 'nodes':nodes}}
        recipe.mkdir(parents=True)
        text = b'Copyright fixture author\nPermission fixture text\n'
        sha = hashlib.sha256(text).hexdigest()
        (recipe/'licenses').mkdir()
        (recipe/'licenses'/f'{sha}.txt').write_bytes(text)
        inventory = {'native_base':'b'*40, 'native_patch_sha256':'c'*64,
                     'cargo_lock_sha256':build.digest(native/'Cargo.lock'), 'filter_platform':'wasm32-unknown-unknown',
                     'packages':[{'package':p, 'texts':[{'path':f'licenses/{sha}.txt', 'sha256':sha, 'kind':'license'}]}
                                 for p in build.native_package_closure(metadata, native)], 'supplemental_texts':[]}
        destination.mkdir()
        (destination/'manifest.json').write_text('[]')
        return native, recipe, destination, metadata, inventory

    def test_native_normal_build_closure_excludes_dev_and_checks_resolved_features(self):
        with tempfile.TemporaryDirectory() as temp:
            native, recipe, destination, metadata, inventory = self.fixture(Path(temp))
            self.assertEqual([p['package']['name'] for p in inventory['packages']], ['macro','runtime','wordcraft-web'])
            (recipe/'license-inventory.json').write_text(json.dumps(inventory))
            with patch.object(build, 'ROOT', Path(temp)):
                build.collect_native_licenses(destination, native, {'base':'b'*40,'patch_sha256':'c'*64}, metadata)
                manifest = json.loads((destination/'manifest.json').read_text())
                self.assertEqual(len(manifest), 3)
                self.assertEqual(len(list((destination/'native').iterdir())), 1)
                self.assertEqual(json.loads((destination/'wordcraft-native-inventory.json').read_text()), inventory)
                metadata['resolve']['nodes'][1]['features'].append('new-unreviewed-feature')
                with self.assertRaisesRegex(ValueError, 'inventory changed'):
                    build.collect_native_licenses(destination, native, {'base':'b'*40,'patch_sha256':'c'*64}, metadata)

    def test_native_source_lock_inventory_and_text_changes_fail_closed(self):
        for problem in ('lock', 'pin', 'missing-package', 'missing-license', 'text', 'path', 'link', 'unknown-kind'):
            with self.subTest(problem=problem), tempfile.TemporaryDirectory() as temp:
                native, recipe, destination, metadata, inventory = self.fixture(Path(temp))
                text = inventory['packages'][0]['texts'][0]
                if problem == 'lock':
                    (native/'Cargo.lock').write_text((native/'Cargo.lock').read_text()+'\n# changed\n')
                elif problem == 'pin':
                    inventory['native_base'] = 'd'*40
                elif problem == 'missing-package':
                    inventory['packages'].pop()
                elif problem == 'missing-license':
                    text['kind'] = 'notice'
                elif problem == 'text':
                    (recipe/text['path']).write_bytes(b'changed license')
                elif problem == 'path':
                    text['path'] = '../outside'
                elif problem == 'link':
                    source = recipe/text['path']
                    outside = Path(temp)/'outside'
                    outside.write_bytes(source.read_bytes())
                    source.unlink()
                    source.symlink_to(outside)
                elif problem == 'unknown-kind':
                    metadata['resolve']['nodes'][0]['deps'][0]['dep_kinds'][0]['kind'] = 'unknown'
                (recipe/'license-inventory.json').write_text(json.dumps(inventory))
                with patch.object(build, 'ROOT', Path(temp)):
                    with self.assertRaises(ValueError):
                        build.collect_native_licenses(destination, native, {'base':'b'*40,'patch_sha256':'c'*64}, metadata)


class EditorAssemblyTests(unittest.TestCase):
    def assemble(self, root, problem=None):
        native, compiled, sdk, fonts, licenses = [root/name for name in ('native','compiled','sdk','fonts','licenses')]
        files = {
            'native/apps/wordcraft-web/index.html': '<body></body>',
            'native/LICENSE-MIT': 'MIT', 'native/NOTICE': 'notice', 'native/ATTRIBUTION.md': 'authors',
            'compiled/wordcraft_web.js': 'import * as a from "./snippets/eframe-pin/inline0.js";\nexport const imports={"./snippets/eframe-pin/inline0.js":a};\n',
            'compiled/wordcraft_web_bg.wasm': 'wasm',
            'compiled/snippets/eframe-pin/inline0.js': 'export function activate() {}\n',
            'sdk/mooring_wordcraft_client.js': 'sdk', 'sdk/mooring_wordcraft_client_bg.wasm': 'wasm',
            'fonts/fonts/manifest.txt': 'fonts', 'fonts/provenance.json': '{"files":[]}',
            'licenses/manifest.json': '[]', 'licenses/wordcraft-native-inventory.json': '{}',
            'LICENSE': 'Apache', 'NOTICE': 'notice', 'mooring/licenses/inventory.json': '{}', 'child.js': 'child',
        }
        for name, content in files.items():
            path = root/name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content)
        snippet = compiled/'snippets/eframe-pin/inline0.js'
        if problem == 'missing':
            snippet.unlink()
        elif problem == 'link':
            snippet.unlink()
            snippet.symlink_to(root/'LICENSE')
        elif problem == 'traversal':
            (compiled/'wordcraft_web.js').write_text('import "./snippets/../../LICENSE";')
        elif problem == 'dependency':
            snippet.write_text('export {x} from "./missing.js";')
        with patch.object(build, 'ROOT', root):
            bundle_digest = build.assemble_editor('a'*40, native, fonts,
                {'base':'b'*40, 'patch_sha256':'c'*64, 'native_patch_commit':'d'*40},
                compiled, sdk, root/'child.js', licenses, root/'bundle',
                {'rust_version':'pinned', 'tools':{'wasm-bindgen':{'version':'pinned'}, 'binaryen':{'version':'pinned'}}})
        return root/'bundle', bundle_digest

    def test_compiled_module_imports_are_in_the_verified_flat_bundle(self):
        with tempfile.TemporaryDirectory() as temp:
            bundle, digest = self.assemble(Path(temp))
            manifest = json.loads((bundle/'manifest.json').read_text())
            self.assertEqual(build.digest(bundle/'manifest.json'), digest)
            module = (bundle/'wordcraft_web.js').read_text()
            self.assertIn('"./snippets/eframe-pin/inline0.js":a', module)
            imports = set(re.findall(r'(?m)^import[^\n]+from ["\']\./([^"\']+\.js)["\']', module))
            self.assertEqual(len(imports), 1)
            for name in imports:
                self.assertNotIn('/', name)
                self.assertEqual(manifest['files'][name], build.digest(bundle/name))
                self.assertEqual((bundle/name).read_text(), 'export function activate() {}\n')
            # Execute the packaged ES module graph and instantiate a real WASM
            # import using its original ABI name, independently of fetch URLs.
            runtime = Path(temp)/'runtime'
            runtime.mkdir()
            (runtime/'package.json').write_text('{"type":"module"}')
            for name in manifest['files']:
                if name.endswith('.js'):
                    shutil.copyfile(bundle/name, runtime/name)
            namespace = b'./snippets/eframe-pin/inline0.js'
            imports = b'\x01' + bytes([len(namespace)]) + namespace + b'\x08activate\x00\x00'
            wasm = b'\x00asm\x01\x00\x00\x00\x01\x04\x01\x60\x00\x00\x02' + bytes([len(imports)]) + imports
            script = ('import {pathToFileURL} from "node:url"; '
                'const {imports}=await import(pathToFileURL(process.argv[1]).href); '
                'new WebAssembly.Instance(new WebAssembly.Module(Buffer.from(process.argv[2],"hex")),imports);')
            result = subprocess.run(['node','--input-type=module','-e',script,
                str(runtime/'wordcraft_web.js'),wasm.hex()], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_incomplete_or_escaping_module_graph_is_not_released(self):
        for problem in ('missing', 'link', 'traversal', 'dependency'):
            with self.subTest(problem=problem), tempfile.TemporaryDirectory() as temp:
                with self.assertRaises(ValueError):
                    self.assemble(Path(temp), problem)


class CommandAssetBuildTests(unittest.TestCase):
    def test_executables_require_qualified_elf_not_just_matching_hash(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp)/'host'
            header = bytearray(64)
            header[:7] = b'\x7fELF\x02\x01\x01'
            header[16:20] = b'\x03\x00\x3e\x00'
            path.write_bytes(header)
            self.assertEqual(build.verified_command_elf(path), build.digest(path))
            for offset, value in ((4, 1), (5, 2), (18, 3)):
                wrong = header[:]
                wrong[offset] = value
                path.write_bytes(wrong)
                with self.assertRaises(ValueError):
                    build.verified_command_elf(path)
            path.write_bytes(header)
            linked = Path(temp)/'linked'
            linked.symlink_to(path)
            with self.assertRaises(ValueError):
                build.verified_command_elf(linked)

    def test_helper_uses_qualified_optional_font_profile_despite_parent_environment(self):
        class HelperReached(Exception):
            pass
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            inputs = root/'command-engine-inputs'
            inherited = {'PATH':'/test/bin', 'CRAFT_FONTS_DIR':'/untrusted/parent-fonts',
                         'CRAFT_FONTS_REQUIRED':'1'}
            def invoked(args, cwd=build.ROOT, env=None):
                if cwd == inputs/'helper':
                    self.assertNotIn('CRAFT_FONTS_DIR', env)
                    self.assertNotIn('CRAFT_FONTS_REQUIRED', env)
                    self.assertEqual(env['CARGO_TARGET_DIR'], str(root/'build/command-engine-target'))
                    self.assertEqual(args, ['cargo','build','--release','--locked'])
                    raise HelperReached()
            with patch.object(build, 'command', side_effect=invoked), \
                    patch.object(build, 'verified_command_elf', return_value='a'*64):
                with self.assertRaises(HelperReached):
                    build.build_commands('a'*40, root, root/'build', 'cargo', inherited,
                        root/'bindgen', root/'binaryen', root/'sdk', root/'embedded', root/'fonts', {})
            self.assertEqual(inherited['CRAFT_FONTS_DIR'], '/untrusted/parent-fonts')
            self.assertEqual(inherited['CRAFT_FONTS_REQUIRED'], '1')

    def test_actual_command_closure_keeps_runtime_build_features_not_dev(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            native, _, _, metadata, _ = NativeLicenseTests().fixture(root)
            result = build.command_package_closure(metadata, native/'Cargo.lock',
                native/'apps/wordcraft-web/Cargo.toml', {'source':native})
            self.assertEqual([public['name'] for public, _ in result], ['macro','runtime','wordcraft-web'])
            metadata['resolve']['nodes'][1]['features'] = ['actual-feature']
            result = build.command_package_closure(metadata, native/'Cargo.lock',
                native/'apps/wordcraft-web/Cargo.toml', {'source':native})
            self.assertEqual(next(p['features'] for p, _ in result if p['name']=='runtime'), ['actual-feature'])
            metadata['resolve']['nodes'][0]['deps'][0]['dep_kinds'][0]['kind'] = 'unknown'
            with self.assertRaises(ValueError):
                build.command_package_closure(metadata, native/'Cargo.lock',
                    native/'apps/wordcraft-web/Cargo.toml', {'source':native})

    def test_missing_crate_license_reuses_only_identical_verified_text(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            _, recipe, _, _, inventory = NativeLicenseTests().fixture(root)
            entry = next(e for e in inventory['packages'] if e['package']['name']=='runtime')
            entry['texts'][0]['origin'] = {'repository':'https://example.invalid/public', 'revision':'b'*40, 'path':'LICENSE'}
            (recipe/'license-inventory.json').write_text(json.dumps(inventory))
            public = dict(entry['package'], features=['new-actual-feature'])
            with patch.object(build, 'ROOT', root):
                texts = build.command_pinned_license_texts(public)
                self.assertEqual(texts[0][1]['origin'], entry['texts'][0]['origin'])
                for key in ('name','version','source','checksum','spdx'):
                    with self.subTest(key=key), self.assertRaises(ValueError):
                        build.command_pinned_license_texts(dict(public, **{key:'changed'}))
                texts[0][0].write_bytes(b'changed license')
                with self.assertRaises(ValueError):
                    build.command_pinned_license_texts(public)

    def test_c1_never_falls_back_to_legacy_child(self):
        with tempfile.TemporaryDirectory() as temp, patch.object(build, 'ROOT', Path(temp)):
            with self.assertRaisesRegex(ValueError, 'separate C1 child'):
                build.build_commands('a'*40, None, None, None, None, None, None, None, None, None, None)

    def test_supplemental_license_is_exact_package_and_exact_bytes(self):
        public = {'name':'accesskit','version':'0.25.1',
            'source':'registry+https://github.com/rust-lang/crates.io-index',
            'checksum':'ad442f58ee04714aaa0ba0a2768c1ea1935b29507bb22ddece8cbbc76db02932',
            'spdx':'MIT OR Apache-2.0'}
        with tempfile.TemporaryDirectory() as temp, patch.object(build.urllib.request, 'urlopen', return_value=io.BytesIO(b'wrong text')) as fetch:
            with self.assertRaisesRegex(ValueError, 'supplemental license mismatch'):
                build.command_pinned_license_texts(public, Path(temp))
            self.assertIn('/ce8164ba92995cfa86005b6259115e08c8244253/', fetch.call_args.args[0])
            fetch.reset_mock()
            with self.assertRaisesRegex(ValueError, 'license text absent'):
                build.command_pinned_license_texts(dict(public, checksum='0'*64), Path(temp))
            fetch.assert_not_called()

    def test_font_dependency_includes_nested_mandatory_notices(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            prepared, directory, destination = root/'prepared', root/'crate', root/'licenses'
            (prepared/'source').mkdir(parents=True)
            (prepared/'helper').mkdir()
            (prepared/'helper/Cargo.lock').write_text('version=4\n')
            (directory/'fonts').mkdir(parents=True)
            (directory/'LICENSE-MIT').write_text('MIT fixture')
            (directory/'fonts/UFL.txt').write_text('Ubuntu fixture')
            (directory/'fonts/Hack-Regular.txt').write_text('Copyright and license fixture')
            (directory/'fonts/LICENSE').symlink_to('../LICENSE-MIT')
            destination.mkdir()
            (destination/'manifest.json').write_text('[]')
            public = {'name':'font-fixture','version':'1.0.0','spdx':'MIT AND Ubuntu-font-1.0'}
            package = {'source':'registry+https://example.invalid/index','manifest_path':str(directory/'Cargo.toml')}
            with patch.object(build, 'command_package_closure', return_value=[(public, package)]):
                inventory = build.collect_command_licenses(destination, prepared, {}, 'engine')
            self.assertEqual({t['name'] for t in inventory['packages'][0]['texts']},
                {'LICENSE-MIT','UFL.txt','Hack-Regular.txt'})
            outside = root/'outside-license'
            outside.write_text('outside')
            (directory/'fonts/LICENSE').unlink()
            (directory/'fonts/LICENSE').symlink_to(outside)
            with patch.object(build, 'command_package_closure', return_value=[(public, package)]):
                with self.assertRaisesRegex(ValueError, 'escapes source'):
                    build.collect_command_licenses(destination, prepared, {}, 'engine')


class FlatNativeBundleTests(unittest.TestCase):
    def test_snippet_is_bundled_before_manifest_hashing(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            native, fonts, generated, sdk, licenses = [root/name for name in
                ('native', 'fonts', 'generated', 'sdk', 'licenses')]
            for directory in (native/'apps/wordcraft-web', native/'assets/fonts', fonts/'fonts',
                              generated/'snippets/eframe-fixture', sdk, licenses):
                directory.mkdir(parents=True)
            (native/'apps/wordcraft-web/index.html').write_text('<html><body></body></html>')
            for name in ('LICENSE-MIT', 'NOTICE', 'ATTRIBUTION.md'):
                (native/name).write_text('fixture license')
            (fonts/'fonts/manifest.txt').write_text('fixture fonts')
            (fonts/'provenance.json').write_text('{"files": []}')
            (licenses/'manifest.json').write_text('[]')
            (licenses/'wordcraft-native-inventory.json').write_text('{}')
            for name in ('mooring_wordcraft_client.js', 'mooring_wordcraft_client_bg.wasm'):
                (sdk/name).write_bytes(b'fixture SDK')
            child = root/'child.js'
            child.write_text('export const child = true;')
            # A real WASM import names the original snippet module. That ABI
            # identity is distinct from an ES module URL and must survive bundling.
            module_name = b'./snippets/eframe-fixture/inline0.js'
            imports = b'\x01' + bytes([len(module_name)]) + module_name + b'\x07snippet\x00\x00'
            wasm = (b'\x00asm\x01\x00\x00\x00\x01\x05\x01\x60\x00\x01\x7f' +
                    b'\x02' + bytes([len(imports)]) + imports + b'\x07\x07\x01\x03run\x00\x00')
            (generated/'wordcraft_web_bg.wasm').write_bytes(wasm)
            (generated/'snippets/eframe-fixture/inline0.js').write_text(
                'export function snippet(value) { return value + 7; }')
            (generated/'wordcraft_web.js').write_text(
                'import {snippet} from "./snippets/eframe-fixture/inline0.js";\n'
                'export const wasmURL = new URL("wordcraft_web_bg.wasm", import.meta.url).href;\n'
                'export class WebHandle { value() { return snippet(35); } }\n'
                'export default async function init(bytes) {\n'
                '  const imports = {"./snippets/eframe-fixture/inline0.js": {snippet: () => snippet(35)}};\n'
                '  return (await WebAssembly.instantiate(bytes, imports)).instance.exports.run();\n'
                '}\n')
            output = root/'bundle'
            config = {'rust_version':'fixture', 'tools':{'wasm-bindgen':{'version':'fixture'},
                                                       'binaryen':{'version':'fixture'}}}
            metadata = {'base':'b'*40, 'patch_sha256':'c'*64, 'native_patch_commit':'d'*40}
            bundle_hash = build.assemble_editor('a'*40, native, fonts, metadata, generated, sdk,
                                                child, licenses, output, config)
            manifest = json.loads((output/'manifest.json').read_text())
            self.assertEqual(bundle_hash, build.digest(output/'manifest.json'))
            self.assertEqual(manifest['files']['wordcraft_web.js'], build.digest(output/'wordcraft_web.js'))
            self.assertEqual(manifest['files']['wordcraft_web_bg.wasm'], build.digest(generated/'wordcraft_web_bg.wasm'))
            self.assertTrue(all(path.is_file() for path in output.iterdir()))
            self.assertFalse(any('/' in name for name in manifest['files']))
            self.assertEqual(set(manifest['files']), {path.name for path in output.iterdir()} - {'manifest.json'})
            for name, expected in manifest['files'].items():
                self.assertEqual(expected, build.digest(output/name), name)
            self.assertEqual(len(list(output.glob('wasm-snippet-*.js'))), 1)
            # Remove the original sources: importing the published flat package
            # must evaluate the snippet and preserve wasm-bindgen's public API.
            shutil.rmtree(generated)
            (root/'package.json').write_text('{"type":"module"}')
            script = ('import {readFile} from "node:fs/promises"; '
                      'const m = await import(process.argv[1]); '
                      'const result = await m.default(await readFile(new URL(m.wasmURL))); '
                      'console.log(JSON.stringify([new m.WebHandle().value(), result, m.wasmURL]));')
            result = json.loads(subprocess.check_output(
                ['node', '--input-type=module', '-e', script, (output/'wordcraft_web.js').as_uri()], text=True))
            self.assertEqual(result, [42, 42, (output/'wordcraft_web_bg.wasm').as_uri()])
