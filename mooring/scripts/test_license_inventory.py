import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import subprocess
import unittest

spec = importlib.util.spec_from_file_location('inventory', Path(__file__).with_name('license-inventory.py'))
inventory = importlib.util.module_from_spec(spec)
spec.loader.exec_module(inventory)


class IntegrityTest(unittest.TestCase):
    def test_modified_hash_extra_metadata_and_unrecorded_files_fail(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            source = directory / 'source.rs'
            source.write_text('harmless source')
            valid = {'package': 'a' * 64, 'files': {'source.rs': hashlib.sha256(source.read_bytes()).hexdigest()}}
            checksum = directory / '.cargo-checksum.json'
            checksum.write_text(json.dumps(valid))
            inventory.verify_checksums(directory, 'a' * 64)
            for bad in [{**valid, 'unexpected': 'metadata'}, {**valid, '$comment': 'arbitrary data'}, {'package': 'a' * 64, 'files': {'source.rs': 'not-a-hash'}}, {'package': 'a' * 64, 'files': {'source.rs': 'b' * 64}}]:
                checksum.write_text(json.dumps(bad))
                with self.assertRaises(ValueError):
                    inventory.verify_checksums(directory, 'a' * 64)
            checksum.write_text(json.dumps(valid))
            source.write_text('changed source')
            with self.assertRaises(ValueError):
                inventory.verify_checksums(directory, 'a' * 64)
            source.write_text('harmless source')
            extra = directory / 'unrecorded'
            extra.write_text('extra')
            with self.assertRaises(ValueError):
                inventory.verify_checksums(directory, 'a' * 64)
            extra.unlink()
            nested = directory / 'nested'
            nested.mkdir()
            (nested / '.cargo-checksum.json').write_text('unrecorded nested data')
            with self.assertRaises(ValueError):
                inventory.verify_checksums(directory, 'a' * 64)
            (nested / '.cargo-checksum.json').unlink()
            (nested / 'linked').symlink_to(source)
            with self.assertRaises(ValueError):
                inventory.verify_checksums(directory, 'a' * 64)

    def test_git_index_catches_present_but_ignored_and_staged_tampered_source(self):
        with tempfile.TemporaryDirectory() as temp:
            repository = Path(temp)
            def git(*args):
                return subprocess.check_output(['git', '-C', str(repository), *args], stderr=subprocess.DEVNULL)
            git('init', '-b', 'main')
            (repository / '.gitignore').write_text('build/\n')
            source = repository / 'mooring/vendor/example-1.0.0/build/build.rs'
            source.parent.mkdir(parents=True)
            source.write_text('verified build source')
            digest = hashlib.sha256(source.read_bytes()).hexdigest()
            expected = {str(source.relative_to(repository)): digest}
            git('add', '.gitignore')
            with self.assertRaises(ValueError):
                inventory.verify_git_index(repository, expected)
            git('add', '-f', '--', str(source.relative_to(repository)))
            inventory.verify_git_index(repository, expected)
            source.write_text('tampered build source')
            git('add', '-f', '--', str(source.relative_to(repository)))
            source.write_text('verified build source')
            with self.assertRaises(ValueError):
                inventory.verify_git_index(repository, expected)
            git('add', '-f', '--', str(source.relative_to(repository)))
            extra = source.parent / 'unrecorded.rs'
            extra.write_text('unrecorded index entry')
            git('add', '-f', '--', str(extra.relative_to(repository)))
            with self.assertRaises(ValueError):
                inventory.verify_git_index(repository, expected)

    def test_duplicate_json_keys_fail(self):
        with self.assertRaises(ValueError):
            json.loads('{"files":{},"files":{}}', object_pairs_hook=inventory.unique_object)


if __name__ == '__main__':
    unittest.main()
