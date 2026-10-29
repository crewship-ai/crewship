#!/usr/bin/env python3
"""Add qualification TESTS to a new archive of the pinned C1 workspace.
The existing C1 runtime and engine helper are never changed. Build/run separately
through crewship-heavy; no service, database, environment or network listener.
"""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tarfile
import uuid

C1_SHA = '9f1cda0e6609a0b7ae1c3398f846a520ca9f6481'
ENGINE_SHA = '7ef2633a7e84956522d1ca47b99fe6feef16c1db'
GROUPS = {'text-enter', 'character-format', 'paragraphs', 'lists'}


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--c1-source', required=True, type=Path)
    parser.add_argument('--catalog', required=True, type=Path)
    parser.add_argument('--profiles', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--engine-binary', required=True, type=Path)
    args = parser.parse_args()
    source = args.c1_source.resolve()
    git = lambda *options: subprocess.check_output(['git', '--no-replace-objects', '-C', str(source), *options], text=True).strip()
    if git('rev-parse', 'HEAD') != C1_SHA or git('status', '--porcelain', '--untracked-files=all'):
        raise SystemExit('C1 source must be the clean pinned commit')
    catalog = json.loads(args.catalog.read_text())
    profiles = json.loads(args.profiles.read_text())
    if catalog['engine']['sha'] != ENGINE_SHA or profiles['engineSha'] != ENGINE_SHA:
        raise SystemExit('engine catalog identity mismatch')
    here = Path(__file__).resolve().parent
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    archive = output / 'source.tar'
    with archive.open('wb') as stream:
        subprocess.run(['git', '--no-replace-objects', '-C', str(source), 'archive', C1_SHA, 'mooring', 'tools/wordcraft-c1-engine'], stdout=stream, check=True)
    with tarfile.open(archive) as tar:
        tar.extractall(output, filter='data')
    archive.unlink()
    original_files = {str(path.relative_to(output)): digest(path) for directory in ['mooring', 'tools/wordcraft-c1-engine'] for path in sorted((output / directory).rglob('*')) if path.is_file()}
    (output / 'source-manifest.json').write_text(json.dumps(original_files, sort_keys=True, indent=2) + '\n')
    selected = [command for command in catalog['commands'] if command['group'] in GROUPS]
    if len(selected) != 163:
        raise SystemExit('C-E registry inventory drift: review qualification fixtures')
    ids = [command['id'] for command in selected]
    names = [re.sub('[^a-zA-Z0-9_]', '_', value).lower() for value in ids]
    if len(set(ids)) != len(ids) or len(set(names)) != len(names) or any(not re.fullmatch(r'[A-Za-z][A-Za-z0-9_.]{0,199}', value) for value in ids):
        raise SystemExit('unsafe or duplicated qualification command identity')
    supported = json.loads((here / 'fixtures.json').read_text())
    cases = []
    for command in selected:
        command_id = command['id']
        if command_id in supported:
            fixture = supported[command_id]
            params, start, end = fixture['params'], fixture['start'], fixture['end']
        else:
            profile = profiles['commands'].get(command_id)
            params = profile['validExamples'][0] if profile else {}
            if not profile:
                schema = command['parameterSchema']
                if schema:
                    for name in schema.get('required', []):
                        kind = schema.get('properties', {}).get(name, {}).get('type')
                        params[name] = {'string': 'fixture', 'number': 1, 'integer': 1, 'boolean': True}.get(kind)
            start, end = 1, 3
        cases.append({'id': command_id, 'group': command['group'], 'params': params, 'start': start, 'end': end,
                      'effectOracle': command_id if command_id in supported else None,
                      'setup': supported.get(command_id, {}).get('setup', []), 'oracle': supported.get(command_id, {}).get('oracle'),
                      'parameterSchemaSha256': hashlib.sha256(json.dumps(command['parameterSchema'], sort_keys=True, separators=(',', ':')).encode()).hexdigest()})
    tests = output / 'mooring/crates/mooring-collab/tests'
    (tests / 'ce_catalog_cases.json').write_text(json.dumps(cases, indent=2) + '\n')
    code = (here / 'qualification.rs').read_text()
    for case in cases:
        name = 'ce_' + re.sub('[^a-zA-Z0-9_]', '_', case['id']).lower()
        code += f'\n#[test]\nfn {name}() {{ qualify("{case["id"]}"); }}\n'
    (tests / 'ce_catalog.rs').write_text(code)
    receipt = {'schemaVersion': 2, 'runId': str(uuid.uuid4()), 'engineBinarySha256': digest(args.engine_binary),
               'runtimeSourceManifestSha256': digest(output / 'source-manifest.json'), 'engineSha': ENGINE_SHA, 'c1Sha': C1_SHA, 'registryDigest': catalog['registryDigest'],
               'profileSha256': digest(args.profiles), 'caseSourceSha256': digest(here / 'qualification.rs'), 'fixturesSha256': digest(here / 'fixtures.json'),
               'prepareSourceSha256': digest(Path(__file__)), 'generatedTestsSha256': digest(tests / 'ce_catalog.rs'),
               'casesSha256': digest(tests / 'ce_catalog_cases.json'), 'commands': len(cases),
               'scope': 'actual isolated C1 host and helper; no UI, SQL persistence or production authority claim'}
    (output / 'preparation.json').write_text(json.dumps(receipt, indent=2) + '\n')
    print(json.dumps(receipt))


if __name__ == '__main__':
    main()
