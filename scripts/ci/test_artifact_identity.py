"""Reject artifacts from another commit even when its short hash collides."""
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
EXPECTED = 'abcdef0' + '1' * 33
OTHER = 'abcdef0' + '2' * 33


class ArtifactIdentityTests(unittest.TestCase):
    def fixture_binary(self, path):
        payload = json.dumps({'client': {'commit': OTHER}})
        path.write_text('#!/usr/bin/env python3\nprint(' + repr(payload) + ')\n')
        path.chmod(0o755)

    def test_image_rejects_matching_short_prefix(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            self.fixture_binary(root / 'docker')
            env = dict(os.environ, PATH=f'{root}:' + os.environ['PATH'])
            result = subprocess.run(['bash', str(ROOT / 'scripts/ci/smoke-image.sh'), 'fixture', EXPECTED], env=env, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('Image commit identity mismatch', result.stderr)

    def test_archive_rejects_matching_short_prefix_before_boot(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            self.fixture_binary(root / 'crewship')
            for name in ('crewship-sidecar', 'entrypoint.sh'):
                (root / name).write_text('fixture')
            path = root / 'release.tar.gz'
            with tarfile.open(path, 'w:gz') as archive:
                for name in ('crewship', 'crewship-sidecar', 'entrypoint.sh'):
                    archive.add(root / name, arcname=name)
            result = subprocess.run(['python3', str(ROOT / 'scripts/ci/smoke-archive.py'), str(path), EXPECTED], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('Archive commit identity mismatch', result.stderr)


if __name__ == '__main__':
    unittest.main()
