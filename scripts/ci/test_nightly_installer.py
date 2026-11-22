"""The installer must not replace a prefix with a valid archive from another nightly."""
import hashlib
import os
import platform
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path

INSTALLER = Path(__file__).resolve().parents[1] / "install.sh"

class NightlyInstallerTest(unittest.TestCase):
    def test_other_nightly_is_rejected_before_install(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            prefix = root / "prefix"
            prefix.mkdir()
            (prefix / "crewship").write_text("existing installation")
            binary = root / "crewship"
            binary.write_text("#!/bin/sh\necho 'crewship nightly-20260901-r10001'\n")
            binary.chmod(0o755)
            arch = {"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[platform.machine()]
            archive = root / f"crewship_0.1.0+build_{platform.system().lower()}_{arch}.tar.gz"
            with tarfile.open(archive, "w:gz") as out:
                out.add(binary, arcname="crewship")
            (root / "checksums.txt").write_text(f"{hashlib.sha256(archive.read_bytes()).hexdigest()}  {archive.name}\n")
            for name in (archive.name, "checksums.txt"):
                for ext in (".pem", ".sig"):
                    (root / (name + ext)).touch()
            # Isolate the tag check after successful signature verification.
            curl = root / "curl"
            curl.write_text('#!/bin/bash\nargs=("$@"); n=${#args[@]}; url=${args[n-1]}; cp "$FIXTURE/${url##*/}" "${args[n-2]}"\n')
            cosign = root / "cosign"
            cosign.write_text("#!/bin/sh\nexit 0\n")
            curl.chmod(0o755)
            cosign.chmod(0o755)
            env = dict(os.environ, PATH=f"{root}:{os.environ['PATH']}", FIXTURE=str(root),
                       CREWSHIP_VERSION="nightly-20260902-r10101", CREWSHIP_INSTALL_DIR=str(prefix))
            result = subprocess.run(["bash", INSTALLER], env=env, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("signed archive version differs", result.stderr)
            self.assertEqual((prefix / "crewship").read_text(), "existing installation")
