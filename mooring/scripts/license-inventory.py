#!/usr/bin/env python3
"""Record locked dependency licenses and verify Cargo vendor integrity. No I/O network."""
import hashlib
import json
from pathlib import Path
import re
import sys
import tomllib

COMMENT = 'This file only protects against accidental modifications. It is not a security mechanism and does not protect against malicious changes.'


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError('Duplicate checksum key')
        result[key] = value
    return result


def verify_checksums(directory, expected):
    checksums = json.loads((directory / '.cargo-checksum.json').read_text(), object_pairs_hook=unique_object)
    if set(checksums) not in ({'files', 'package'}, {'$comment', 'files', 'package'}):
        raise ValueError('Unexpected checksum metadata')
    if '$comment' in checksums and checksums['$comment'] != COMMENT:
        raise ValueError('Unexpected checksum comment')
    if checksums['package'] != expected or not re.fullmatch(r'[0-9a-f]{64}', expected):
        raise ValueError('Registry checksum mismatch')
    files = checksums['files']
    if not isinstance(files, dict):
        raise ValueError('Expected checksum file map')
    for filename, digest in files.items():
        if not isinstance(digest, str) or not re.fullmatch(r'[0-9a-f]{64}', digest):
            raise ValueError('Expected SHA256 file digest')
        path = (directory / filename).resolve()
        if not path.is_relative_to(directory.resolve()) or not path.is_file():
            raise ValueError('Unsafe or absent vendored path')
        if hashlib.sha256(path.read_bytes()).hexdigest() != digest:
            raise ValueError('Changed vendored file')
    actual = {str(p.relative_to(directory)) for p in directory.rglob('*') if p.is_file() and p.name != '.cargo-checksum.json'}
    if actual != set(files):
        raise ValueError('Unrecorded or absent vendored files')


def main():
    root = Path(__file__).resolve().parents[1]
    lock = tomllib.loads((root / 'Cargo.lock').read_text())
    entries = []
    for package in lock['package']:
        if 'source' not in package:
            continue
        directory = root / 'vendor' / f"{package['name']}-{package['version']}"
        verify_checksums(directory, package['checksum'])
        manifest = tomllib.loads((directory / 'Cargo.toml').read_text())['package']
        if manifest['name'] != package['name'] or manifest['version'] != package['version'] or not manifest.get('license'):
            raise ValueError('Wrong vendored identity or missing SPDX license')
        texts = sorted(p for p in directory.iterdir() if p.is_file() and p.name.lower().startswith(('license', 'copying', 'unlicense')))
        extra = None
        if package['name'] == 'r-efi':
            texts.append(directory / 'AUTHORS')  # Full MIT permission/copyright notice.
        if package['name'] == 'yrs':
            texts.append(root / 'licenses' / 'yrs-0.28.0-MIT.txt')
            extra = 'https://github.com/y-crdt/y-crdt/blob/23b7f5693bbf9e7d26340c521ee8647f79bdfba2/LICENSE'
        if not texts:
            raise ValueError('Missing license text')
        entries.append({
            'name': package['name'], 'version': package['version'],
            'spdx': manifest['license'], 'registry_checksum': package['checksum'],
            'license_source': extra,
            'texts': [{'path': str(p.relative_to(root)), 'sha256': hashlib.sha256(p.read_bytes()).hexdigest()} for p in texts],
        })
    content = json.dumps(entries, indent=2, ensure_ascii=False) + '\n'
    target = root / 'licenses' / 'inventory.json'
    if '--check' in sys.argv:
        if target.read_text() != content:
            sys.exit('License inventory differs; regenerate with mooring/scripts/license-inventory.py')
    else:
        target.write_text(content)
    print(f'{len(entries)} locked dependencies, vendor integrity and license digests verified')


if __name__ == '__main__':
    main()
