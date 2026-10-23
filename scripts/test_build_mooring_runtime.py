import hashlib
import importlib.util
import io
import json
from pathlib import Path
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

    def test_default_marker_keeps_ordinary_builds_disabled(self):
        marker = json.loads((build.ROOT/'mooring/runtime-release.json').read_text())
        self.assertEqual(marker, {'schema_version':1,'enabled':False})
