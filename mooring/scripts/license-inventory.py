#!/usr/bin/env python3
"""Record locked dependency licenses and verify Cargo vendor integrity. No I/O network."""
import hashlib
import io
import json
from pathlib import Path
import re
import subprocess
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
    if any(p.is_symlink() for p in directory.rglob('*')):
        raise ValueError('Unexpected vendor symlink')
    actual = {str(p.relative_to(directory)) for p in directory.rglob('*') if p.is_file() and p != directory / '.cargo-checksum.json'}
    if actual != set(files):
        raise ValueError('Unrecorded or absent vendored files')



def verify_git_index(repository, expected):
    """Verify release bytes in Git, not merely ignored files on a developer disk."""
    raw = subprocess.check_output(['git', '-C', str(repository), 'ls-files', '--stage', '-z', '--', 'mooring/vendor'])
    indexed = {}
    for record in raw.split(b'\0'):
        if not record:
            continue
        metadata, name = record.split(b'\t', 1)
        mode, blob, stage = metadata.decode().split()
        if stage != '0' or mode not in {'100644', '100755'}:
            raise ValueError('Nonregular or conflicted vendored Git entry')
        indexed[name.decode()] = blob
    if set(indexed) != set(expected):
        missing = sorted(set(expected) - set(indexed))
        extra = sorted(set(indexed) - set(expected))
        raise ValueError(f'Vendor Git index differs: missing={missing}, extra={extra}')
    objects = list(dict.fromkeys(indexed.values()))
    requests = ('\n'.join(objects) + '\n').encode()
    sizes = {}
    checked = subprocess.check_output(['git', '-C', str(repository), 'cat-file', '--batch-check'], input=requests)
    for line in checked.decode().splitlines():
        blob, kind, size = line.split()
        if kind != 'blob':
            raise ValueError('Invalid vendored Git object')
        sizes[blob] = int(size)
    for name, blob in indexed.items():
        if sizes[blob] != (repository / name).stat().st_size:
            raise ValueError('Vendored index size differs from verified source')
    # Size preflight above prevents a staged oversized blob from allocating
    # more than the already-verified local registry sources.
    data = subprocess.check_output(['git', '-C', str(repository), 'cat-file', '--batch'], input=requests)
    reader = io.BytesIO(data)
    digests = {}
    for blob in objects:
        header = reader.readline().decode().split()
        if header != [blob, 'blob', str(sizes[blob])]:
            raise ValueError('Invalid vendored Git batch response')
        contents = reader.read(sizes[blob])
        if len(contents) != sizes[blob] or reader.read(1) != b'\n':
            raise ValueError('Truncated vendored Git blob')
        digests[blob] = hashlib.sha256(contents).hexdigest()
    if reader.read(1):
        raise ValueError('Trailing vendored Git batch response')
    for name, expected_digest in expected.items():
        if digests[indexed[name]] != expected_digest:
            raise ValueError('Vendored Git bytes differ from recorded SHA256')


def main():
    root = Path(__file__).resolve().parents[1]
    lock = tomllib.loads((root / 'Cargo.lock').read_text())
    entries = []
    expected_index = {}
    for package in lock['package']:
        if 'source' not in package:
            continue
        directory = root / 'vendor' / f"{package['name']}-{package['version']}"
        verify_checksums(directory, package['checksum'])
        checksum_path = directory / '.cargo-checksum.json'
        checksums = json.loads(checksum_path.read_text(), object_pairs_hook=unique_object)
        expected_index[str(checksum_path.relative_to(root.parent))] = hashlib.sha256(checksum_path.read_bytes()).hexdigest()
        for name, digest in checksums['files'].items():
            expected_index[str((directory / name).relative_to(root.parent))] = digest
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
        verify_git_index(root.parent, expected_index)
        if target.read_text() != content:
            sys.exit('License inventory differs; regenerate with mooring/scripts/license-inventory.py')
    else:
        target.write_text(content)
    print(f'{len(entries)} locked dependencies, vendor source/index integrity and license digests verified')


if __name__ == '__main__':
    main()
