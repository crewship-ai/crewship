"""Unit tests of scripts/build-craft-components.py that need no Rust build."""
import importlib.util
import json
from pathlib import Path
import unittest

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


if __name__ == '__main__':
    unittest.main()
