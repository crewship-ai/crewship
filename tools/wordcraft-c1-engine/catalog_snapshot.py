#!/usr/bin/env python3
"""Refresh/verify the generated C1 catalog copy; never maintain a command list.
Canonical inputs are supplied from the reviewed capability-catalog source tree.
The unchanged helper preparation copies src/, including this generated data.
"""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def canonical_input(source, relative, reference="HEAD"):
    raw = (source / relative).read_bytes()
    committed = subprocess.check_output(['git', '--no-replace-objects', '-C', str(source), 'show', reference + ':' + relative], stderr=subprocess.DEVNULL)
    if raw != committed:
        raise SystemExit('uncommitted canonical input: ' + relative)
    return raw


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source-root', type=Path)
    parser.add_argument('--source-ref', default='HEAD', help='Reviewed canonical Git revision; working bytes must still match')
    parser.add_argument('--check', action='store_true')
    args = parser.parse_args()
    here = Path(__file__).resolve().parent
    root = here.parents[1]
    paths = ['capabilities.json', 'catalog-validation-vectors.json', 'catalog-provenance.json']
    if args.source_root:
        source = args.source_root.resolve()
        raw = canonical_input(source, 'schemas/wordcraft/capabilities.json', args.source_ref)
        profile_raw = canonical_input(source, 'tools/wordcraft-capabilities/parameter-profiles.json', args.source_ref)
        profiles = json.loads(profile_raw)
        vectors = [{'id': key, 'valid': profile['validExamples'], 'invalid': profile['invalidExamples']}
                   for key, profile in profiles['commands'].items()]
        vector_raw = (json.dumps(vectors, indent=2) + '\n').encode()
        commit = subprocess.check_output(['git', '--no-replace-objects', '-C', str(source), 'rev-parse', args.source_ref], text=True).strip()
        metadata = {'engineSha': profiles['engineSha'], 'sourceCatalog': 'schemas/wordcraft/capabilities.json',
                    'sourceProfiles': 'tools/wordcraft-capabilities/parameter-profiles.json',
                    'catalogSha256': digest(raw), 'profilesSha256': digest(profile_raw), 'sourceCommit': commit,
                    'scope': 'isolated C1 candidate qualification only; grants no product permission',
                    'vectorsSha256': digest(vector_raw)}
        outputs = [raw, vector_raw, (json.dumps(metadata, indent=2) + '\n').encode()]
        for name, content in zip(paths, outputs):
            path = here / 'src' / name
            if args.check:
                if path.read_bytes() != content:
                    raise SystemExit(f'canonical catalog drift: {name}')
            else:
                path.write_bytes(content)
    elif not args.check:
        raise SystemExit('refresh requires --source-root')
    raw, vectors, metadata_raw = [(here / 'src' / name).read_bytes() for name in paths]
    metadata, catalog = json.loads(metadata_raw), json.loads(raw)
    if digest(raw) != metadata['catalogSha256'] or digest(vectors) != metadata['vectorsSha256']:
        raise SystemExit('generated catalog/vector content drift')
    if catalog['engine']['sha'] != metadata['engineSha'] or not catalog['version'].startswith('wordcraft:' + metadata['engineSha'] + ':'):
        raise SystemExit('catalog engine/version drift')
    ids = [entry['id'] for entry in catalog['commands']]
    if len(ids) != len(set(ids)):
        raise SystemExit('duplicate catalog command')
    files = [root / 'mooring/crates/mooring-collab/src/command_experiment/mod.rs', here / 'src/main.rs']
    sections = [path.read_text().split('// BEGIN shared catalog validation:', 1)[1].split('// END shared catalog validation.', 1)[0] for path in files]
    if sections[0] != sections[1]:
        raise SystemExit('host/helper catalog validator drift')
    print(json.dumps({'commands': len(ids), 'profiles': len(json.loads(vectors)), 'catalogSha256': digest(raw)}))


if __name__ == '__main__':
    main()
