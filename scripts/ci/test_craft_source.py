import hashlib
import importlib.util
import io
import json
import os
import stat
import subprocess
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "ci" / "verify-craft-source.py"
REPO, TAG, COMMIT = "example/crewship", "v1.2.3", "a" * 40
IDENTITY = f"https://github.com/{REPO}/.github/workflows/release.yml@refs/tags/{TAG}"
APPS = ("gridcraft", "wordcraft")


def package(app: str, version: str) -> tuple[bytes, str]:
    manifest = json.dumps({"application": app, "version": version}).encode()
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w") as tar:
        info = tarfile.TarInfo("manifest.json")
        info.size = len(manifest)
        tar.addfile(info, io.BytesIO(manifest))
    return buf.getvalue(), hashlib.sha256(manifest).hexdigest()


class Fixture:
    def __init__(self, root: Path, versions=None):
        self.root = root
        self.dir = root / "download"
        self.dir.mkdir()
        versions = versions or {"gridcraft": "1.0.0", "wordcraft": "2.0.0"}
        archives, pins, lines = {}, [], []
        for app in APPS:
            data, manifest_sha = package(app, versions[app])
            name = f"crewship-craft-{app}.craftpkg"
            (self.dir / name).write_bytes(data)
            digest = hashlib.sha256(data).hexdigest()
            archives[name] = digest
            lines.append(f"{digest}  {name}")
            pins.append(f"{app}={versions[app]}={manifest_sha}")
        (self.dir / "checksums.txt").write_text("\n".join(lines) + "\n")
        (self.dir / "checksums.txt.sig").write_text("sig")
        (self.dir / "checksums.txt.pem").write_text("pem")
        self.source = root / "source.json"
        self.source.write_text(json.dumps({
            "repository": REPO, "tag": TAG, "commit": COMMIT,
            "workflow": ".github/workflows/release.yml", "archives": archives}))
        self.catalog = root / "catalog.txt"
        self.catalog.write_text(",".join(pins) + "\n")
        self.cosign = root / "cosign"
        # Accepts only the pinned release workflow identity, tag, commit.
        self.cosign.write_text(
            "#!/bin/sh\n"
            "args=\" $* \"\n"
            f"case \"$args\" in *' --certificate-identity {IDENTITY} '*) ;; *) echo 'identity mismatch' >&2; exit 1;; esac\n"
            f"case \"$args\" in *' --certificate-github-workflow-sha {COMMIT} '*) ;; *) echo 'commit mismatch' >&2; exit 1;; esac\n"
            f"case \"$args\" in *' --certificate-github-workflow-ref refs/tags/{TAG} '*) ;; *) echo 'ref mismatch' >&2; exit 1;; esac\n"
            "exit 0\n")
        self.cosign.chmod(self.cosign.stat().st_mode | stat.S_IXUSR)
        self.out = root / "verified"
        self.receipt = root / "receipt.json"

    def run(self, cosign=None):
        env = dict(os.environ, COSIGN=str(cosign or self.cosign))
        return subprocess.run(
            [sys.executable, "-I", str(SCRIPT), "--source", str(self.source), "--catalog", str(self.catalog),
             "--dir", str(self.dir), "--out", str(self.out), "--receipt", str(self.receipt)],
            capture_output=True, text=True, env=env)


class VerifyCraftSource(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.f = Fixture(Path(self._tmp.name))

    def test_accepts_the_pinned_pair_and_writes_a_receipt(self):
        r = self.f.run()
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(sorted(p.name for p in self.f.out.iterdir()),
                         [f"crewship-craft-{a}.craftpkg" for a in APPS])
        receipt = json.loads(self.f.receipt.read_text())
        self.assertEqual((receipt["source_tag"], receipt["source_commit"]), (TAG, COMMIT))

    def test_a_missing_file_fails(self):
        (self.f.dir / "crewship-craft-wordcraft.craftpkg").unlink()
        r = self.f.run()
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("missing", r.stderr)
        self.assertFalse(self.f.out.exists())

    def test_a_missing_signature_fails(self):
        (self.f.dir / "checksums.txt.sig").unlink()
        self.assertNotEqual(self.f.run().returncode, 0)

    def test_an_unexpected_extra_file_fails(self):
        (self.f.dir / "extra.craftpkg").write_bytes(b"x")
        r = self.f.run()
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("extra", r.stderr)

    def test_a_changed_file_fails(self):
        path = self.f.dir / "crewship-craft-gridcraft.craftpkg"
        path.write_bytes(path.read_bytes() + b"\0")
        r = self.f.run()
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("pinned SHA-256", r.stderr)

    def test_a_file_missing_from_the_signed_checksums_fails(self):
        (self.f.dir / "checksums.txt").write_text("")
        r = self.f.run()
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("signed checksums", r.stderr)

    def test_a_different_manifest_fails(self):
        # The pair is intact and signed, but this commit's catalog pins another version.
        other = Fixture(Path(self._tmp.name) / "other", versions={"gridcraft": "1.0.1", "wordcraft": "2.0.0"}) \
            if (Path(self._tmp.name) / "other").mkdir() is None else None
        self.f.catalog.write_text(other.catalog.read_text())
        r = self.f.run()
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("catalog pins", r.stderr)
        self.assertFalse(self.f.out.exists())

    def test_a_signature_from_another_workflow_fails(self):
        wrong = Path(self._tmp.name) / "cosign-other"
        wrong.write_text("#!/bin/sh\necho 'certificate identity does not match' >&2\nexit 1\n")
        wrong.chmod(wrong.stat().st_mode | stat.S_IXUSR)
        r = self.f.run(cosign=wrong)
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("not signed by the release workflow", r.stderr)

    def test_a_source_naming_another_workflow_fails(self):
        src = json.loads(self.f.source.read_text())
        src["workflow"] = ".github/workflows/nightly.yml"
        self.f.source.write_text(json.dumps(src))
        self.assertNotEqual(self.f.run().returncode, 0)

    def test_the_committed_source_file_is_well_formed(self):
        spec = importlib.util.spec_from_file_location("verify_craft_source", SCRIPT)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        src = module.load_source(ROOT / "tools" / "craft-components" / "nightly-source.json")
        pins = (ROOT / "tools" / "craft-components" / "catalog.txt").read_text().strip().split(",")
        self.assertEqual(sorted(p.split("=")[0] for p in pins), sorted(n.split("-")[-1].split(".")[0] for n in src["archives"]))


if __name__ == "__main__":
    unittest.main()
