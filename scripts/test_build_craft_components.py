"""Unit tests of scripts/build-craft-components.py that need no Rust build."""
import importlib.util
import hashlib
import io
import json
import subprocess
import tarfile
import tempfile
from pathlib import Path
import unittest
from unittest import mock

_spec = importlib.util.spec_from_file_location('build_craft_components', Path(__file__).with_name('build-craft-components.py'))
build = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(build)


class LicensePolicy(unittest.TestCase):
    def test_permissive_expressions_pass(self):
        for expression in ['MIT', 'MIT OR Apache-2.0', 'MIT/Apache-2.0', 'Apache-2.0 / MIT', 'BSD-3-Clause/MIT',
                           '(MIT OR Apache-2.0) AND Unicode-3.0', '(Apache-2.0 OR MIT) AND BSD-3-Clause',
                           'Apache-2.0 OR GPL-2.0-only', '(MIT OR Apache-2.0) AND OFL-1.1 AND Ubuntu-font-1.0',
                           'Apache-2.0 WITH LLVM-exception', 'Unlicense OR MIT', 'Zlib']:
            self.assertTrue(build.license_allowed(expression), expression)

    def test_copyleft_unknown_and_malformed_fail(self):
        for expression in ['GPL-2.0-only', 'MIT AND GPL-3.0-or-later', 'LGPL-2.1 OR MPL-2.0', 'AGPL-3.0-only',
                           'GPL-2.0+', 'Apache-2.0 WITH Classpath-exception-2.0', 'see LICENSE', '', None,
                           'MIT OR', '(MIT', 'MIT)', 'MIT AND', 'MIT, Apache-2.0', 'MIT OR Apache-2.0;', 'Proprietary']:
            self.assertFalse(build.license_allowed(expression), expression)

    def test_check_names_every_refused_crate(self):
        with self.assertRaises(ValueError) as refused:
            build.check_licenses([{'name': 'ok', 'version': '1', 'license': 'MIT'},
                                  {'name': 'gpl', 'version': '2', 'license': 'GPL-3.0-only'},
                                  {'name': 'file', 'version': '3', 'license': 'see LICENSE.txt'}])
        self.assertIn('gpl 2', str(refused.exception))
        self.assertIn('file 3', str(refused.exception))
        self.assertNotIn('ok 1', str(refused.exception))


class WordArtifactInventory(unittest.TestCase):
    def test_records_only_artifacts_for_the_requested_wasm_target(self):
        target = 'wasm32-unknown-unknown'
        directory = Path('/tmp/build-word')
        entries = [
            ('wordcraft-web', 'bin', directory / target / 'release/wordcraft-web.wasm'),
            ('jpeg-encoder', 'lib', directory / target / 'release/deps/libjpeg_encoder.rlib'),
            ('harfbuzz-subset-sys', 'lib', directory / 'release/deps/libharfbuzz_subset_sys.rlib'),
            ('host-build-script', 'custom-build', directory / target / 'release/build/build-script'),
        ]
        lines = [json.dumps({'reason': 'compiler-artifact', 'package_id': name,
                            'target': {'kind': [kind]}, 'filenames': [str(path)]})
                 for name, kind, path in entries]
        with mock.patch.object(build.subprocess, 'Popen') as popen:
            proc = popen.return_value.__enter__.return_value
            proc.stdout = io.StringIO('\n'.join(lines))
            proc.wait.return_value = 0
            artifacts = {}
            build.cargo_build('cargo', Path('/tmp/source'), {}, directory, target, 'wordcraft-web', artifacts)
        self.assertEqual(artifacts, {target: {'wordcraft-web', 'jpeg-encoder'}})

    def test_uses_only_the_observed_crates_and_requires_the_target_root(self):
        names = ['wordcraft-cli', 'wordcraft-web', 'jpeg-encoder', 'webpki-roots']
        packages = [{'id': name, 'name': name, 'version': '1', 'license': 'MIT'} for name in names]
        packages[-1]['license'] = 'CDLA-Permissive-2.0'
        component = {'cli_package': names[0], 'web_package': names[1]}
        artifacts = {'wasm32-wasip1': {names[0], names[2]}, 'wasm32-unknown-unknown': {names[1]}}
        with mock.patch.object(build, 'run', return_value=json.dumps({'packages': packages}).encode()):
            crates = build.crate_inventory(component, Path('/tmp/source'), 'cargo', {}, artifacts)
            self.assertEqual([c['name'] for c in crates], sorted(names[:3]))
            artifacts['wasm32-unknown-unknown'] = {names[0]}
            with self.assertRaisesRegex(ValueError, 'artifact evidence does not match'):
                build.crate_inventory(component, Path('/tmp/source'), 'cargo', {}, artifacts)

class FontLicenses(unittest.TestCase):
    def test_only_ofl_1_1_fonts_pass(self):
        ofl = b'Copyright 2020 X\nThis Font Software is licensed under the SIL Open Font License, Version 1.1.\n'
        build.check_font_licenses({'fonts/a/OFL.txt': ofl})
        for bad in ({}, {'fonts/b/LICENSE': b'Apache License Version 2.0'},
                    {'fonts/a/OFL.txt': ofl, 'fonts/c/COPYING': b'GNU GENERAL PUBLIC LICENSE Version 2'}):
            with self.assertRaises(ValueError):
                build.check_font_licenses(bad)


class SBOM(unittest.TestCase):
    component = {'application': 'wordcraft', 'version': '0.11.0+b0fb5cb571b6',
                 'source': {'repository': 'https://github.com/storytold/wordcraft', 'commit': 'b' * 40},
                 'licenses': [{'name': 'WordCraft', 'spdx': 'MIT OR Apache-2.0'}]}
    crates = [{'name': 'serde', 'version': '1.0.0', 'license': 'MIT/Apache-2.0'}]

    def test_lists_every_crate_and_the_fonts_deterministically(self):
        raw = build.sbom(self.component, self.crates, 'https://github.com/storytold/craft-fonts.git', 'f' * 40)
        self.assertEqual(raw, build.sbom(self.component, self.crates, 'https://github.com/storytold/craft-fonts.git', 'f' * 40))
        bom = json.loads(raw)
        self.assertEqual((bom['bomFormat'], bom['specVersion']), ('CycloneDX', '1.5'))
        self.assertNotIn('serialNumber', bom)
        self.assertNotIn('timestamp', bom['metadata'])
        self.assertEqual(bom['metadata']['component']['version'], '0.11.0+b0fb5cb571b6')
        purls = {c.get('purl') for c in bom['components']}
        self.assertIn('pkg:cargo/serde@1.0.0', purls)
        serde = next(c for c in bom['components'] if c['name'] == 'serde')
        self.assertEqual(serde['licenses'], [{'expression': 'MIT OR Apache-2.0'}])
        self.assertIn('craft-fonts', {c['name'] for c in bom['components']})


class DocumentFormat(unittest.TestCase):
    def test_a_workbook_component_says_so_and_a_native_one_says_nothing(self):
        self.assertEqual(build.document_format({'application': 'gridcraft', 'document_format': 'workbook'}), {'document_format': 'workbook'})
        self.assertEqual(build.document_format({'application': 'wordcraft'}), {})

    def test_an_unknown_format_or_a_word_workbook_fails(self):
        for c in [{'application': 'gridcraft', 'document_format': 'native'},
                  {'application': 'gridcraft', 'document_format': ''},
                  {'application': 'wordcraft', 'document_format': 'workbook'}]:
            with self.assertRaises(ValueError):
                build.document_format(c)

    def test_the_committed_sheet_recipe_is_a_workbook(self):
        recipe = build.load_recipe()
        sheet = next(c for c in recipe['components'] if c['application'] == 'gridcraft')
        self.assertEqual(build.document_format(sheet), {'document_format': 'workbook'})


class ExpectedCatalog(unittest.TestCase):
    def test_other_pins_fail_and_update_writes(self):
        with tempfile.TemporaryDirectory() as d:
            expected = Path(d) / 'catalog.txt'
            expected.write_text('wordcraft=1=' + 'a' * 64 + '\n')
            build.check_expected('wordcraft=1=' + 'a' * 64 + '\n', expected=expected)
            with self.assertRaises(SystemExit):
                build.check_expected('wordcraft=1=' + 'b' * 64 + '\n', expected=expected)
            build.check_expected('wordcraft=1=' + 'b' * 64 + '\n', update=True, expected=expected)
            self.assertIn('b' * 64, expected.read_text())

    def test_the_committed_catalog_is_well_formed(self):
        catalog = (build.EXPECTED).read_text()
        self.assertRegex(catalog, r'^gridcraft=[A-Za-z0-9.+_-]+=[0-9a-f]{64},wordcraft=[A-Za-z0-9.+_-]+=[0-9a-f]{64}\n$')


class PackageGate(unittest.TestCase):
    """scripts/check-craft-packages.sh, the release's pin-to-package gate."""
    script = Path(__file__).with_name('check-craft-packages.sh')

    def package(self, directory, app, manifest):
        with tarfile.open(Path(directory) / f'crewship-craft-{app}.craftpkg', 'w') as tar:
            info = tarfile.TarInfo('manifest.json')
            info.size = len(manifest)
            tar.addfile(info, io.BytesIO(manifest))
        return f'{app}=1.0={hashlib.sha256(manifest).hexdigest()}'

    def check(self, catalog, directory):
        return subprocess.run(['bash', str(self.script), catalog, directory], capture_output=True, text=True).returncode

    def test_exact_match_passes_and_every_mismatch_fails(self):
        with tempfile.TemporaryDirectory() as d:
            word = self.package(d, 'wordcraft', b'{"w":1}')
            sheet = self.package(d, 'gridcraft', b'{"s":1}')
            self.assertEqual(self.check(f'{word},{sheet}', d), 0)
            other = 'gridcraft=1.0=' + hashlib.sha256(b'other').hexdigest()
            for catalog in ['', word,  # a package without a pin
                            f'{word},{other}',  # a pin for different bytes
                            f'{word},{word}',  # the same application twice
                            f'{word},{sheet},powerpoint=1.0={"a" * 64}',  # unknown application
                            f'{word},gridcraft={"v" * 65}={sheet.rsplit("=", 1)[1]}',  # version too long
                            f'{word},{sheet};echo']:
                self.assertNotEqual(self.check(catalog, d), 0, catalog)
            for stray in ('stray.txt', '.hidden'):  # anything unpinned in the directory
                (Path(d) / stray).write_text('x')
                self.assertNotEqual(self.check(f'{word},{sheet}', d), 0, stray)
                (Path(d) / stray).unlink()


if __name__ == '__main__':
    unittest.main()
