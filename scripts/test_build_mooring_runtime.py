import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
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

    def test_default_marker_keeps_ordinary_builds_disabled(self):
        marker = json.loads((build.ROOT/'mooring/runtime-release.json').read_text())
        self.assertEqual(marker, {'schema_version':1,'enabled':False})

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
