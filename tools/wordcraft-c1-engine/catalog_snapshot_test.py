import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('snapshot', Path(__file__).with_name('catalog_snapshot.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class CanonicalProvenanceTests(unittest.TestCase):
    def test_only_the_committed_canonical_bytes_can_name_the_source_commit(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            git = ['git', '-C', str(root)]
            subprocess.run(git + ['init', '-q'], check=True)
            source = root / 'catalog.json'
            source.write_text('{"reviewed":true}')
            subprocess.run(git + ['add', 'catalog.json'], check=True)
            subprocess.run(git + ['-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-qm', 'fixture'], check=True)
            self.assertEqual(module.canonical_input(root, 'catalog.json'), b'{"reviewed":true}')
            source.write_text('{"unreviewed":true}')
            with self.assertRaisesRegex(SystemExit, 'uncommitted canonical input'):
                module.canonical_input(root, 'catalog.json')
            (root / 'untracked.json').write_text('{}')
            with self.assertRaises(subprocess.CalledProcessError):
                module.canonical_input(root, 'untracked.json')


if __name__ == '__main__':
    unittest.main()
