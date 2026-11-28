"""Verify the historical Word package used by the paired browser gate."""

import hashlib
import json
from pathlib import Path
import runpy
import sys
import tarfile


def main():
    directory = Path(sys.argv[1])
    root = Path(__file__).resolve().parents[2]
    source = json.loads((root / "tools/craft-components/retained-word-source.json").read_text())
    helpers = runpy.run_path(str(Path(__file__).with_name("verify-craft-source.py")))
    helpers["verify_signature"](source, directory)
    archive = directory / source["archive"]
    expected = source["archive_sha256"]
    lines = [line.split() for line in (directory / "checksums.txt").read_text().splitlines()]
    if [entry for entry in lines if len(entry) == 2 and entry[1] == archive.name] != [[expected, archive.name]]:
        raise ValueError("signed checksums do not certify the retained Word archive")
    digest = hashlib.sha256()
    with archive.open("rb") as file:
        for chunk in iter(lambda: file.read(1 << 20), b""):
            digest.update(chunk)
    if digest.hexdigest() != expected:
        raise ValueError("retained Word archive checksum mismatch")
    with tarfile.open(archive) as package:
        stream = package.extractfile("manifest.json")
        if stream is None:
            raise ValueError("retained Word manifest missing")
        manifest = stream.read()
    if hashlib.sha256(manifest).hexdigest() != source["pin"].rsplit("=", 1)[1]:
        raise ValueError("retained Word manifest pin mismatch")
    if json.loads(manifest)["component"]["editor"]["digest"] != source["editor_bundle"]:
        raise ValueError("retained Word editor pin mismatch")


if __name__ == "__main__":
    main()
