#!/usr/bin/env python3
"""Build an engine-scoped qualification declaration from a complete actual C1 run.
Raw rows/logs remain outside the product repository. The emitted declaration is
capability configuration, not a captured run report or a claim about UI/SQL/RBAC.
"""
import argparse
import hashlib
import json
import os
import subprocess
import tempfile
from pathlib import Path

C1_SHA = '9f1cda0e6609a0b7ae1c3398f846a520ca9f6481'
ENGINE_SHA = '7ef2633a7e84956522d1ca47b99fe6feef16c1db'
CHECKS = {'execution', 'staleTarget', 'restoreP', 'ownUndo', 'oracleIntegrity'}


def closed(properties, required=()):
    return {'type': 'object', 'properties': properties, 'required': list(required), 'additionalProperties': False}


# Only these commands have meaningful effect oracles at the pinned C1 commit.
SUPPORTED = set(json.loads(Path(__file__).with_name('fixtures.json').read_text()))


def tested_schema(params):
    properties = {}
    for key, value in params.items():
        kind = 'boolean' if type(value) is bool else 'number' if type(value) in (int, float) else 'string' if type(value) is str else 'object' if type(value) is dict else 'array' if type(value) is list else 'null' if value is None else None
        if kind is None:
            raise ValueError('no reviewed scalar fixture representation')
        properties[key] = {'type': kind, 'enum': [value]}
    return closed(properties, sorted(params.keys()))


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def checked_rows(cases, rows, execution_identity, invocation_id):
    expected = {case['id']: case for case in cases}
    actual = {row['id']: row for row in rows}
    if len(expected) != len(cases) or len(actual) != len(rows) or set(expected) != set(actual):
        raise ValueError('qualification inventory missing, duplicated or unexpected')
    passed = {}
    for command_id, case in expected.items():
        row = actual[command_id]
        if row.get('invocationId') != invocation_id or row.get('executionIdentity') != execution_identity or row.get('actualEngineBinarySha256') != execution_identity['engineBinarySha256']:
            raise ValueError('qualification execution identity mismatch')
        if row['group'] != case['group'] or row['params'] != case['params'] or row['parameterSchemaSha256'] != case['parameterSchemaSha256']:
            raise ValueError('qualification fixture/schema mismatch')
        if row['test'] != 'ce_' + command_id.replace('.', '_').lower() or set(row['checks']) != CHECKS:
            raise ValueError('qualification test or invariant mismatch')
        if type(row['passed']) is not bool or any(type(value) is not bool for value in row['checks'].values()):
            raise ValueError('qualification outcomes must be booleans')
        if row['passed']:
            if not all(row['checks'].values()) or row['reason'] is not None or case['effectOracle'] != command_id:
                raise ValueError('incomplete qualification cannot enable a command')
            if command_id not in SUPPORTED:
                raise ValueError('no reviewed C1 admission domain for qualified command')
            passed[command_id] = row
    return passed


def verify_runtime(prepared, source, preparation):
    manifest_path = prepared / 'source-manifest.json'
    if digest(manifest_path) != preparation['runtimeSourceManifestSha256']:
        raise ValueError('runtime source manifest drift')
    manifest = json.loads(manifest_path.read_text())
    command = ['git', '--no-replace-objects', '-C', str(source)]
    if subprocess.check_output(command + ['rev-parse', 'HEAD'], text=True).strip() != C1_SHA:
        raise ValueError('C1 source commit mismatch')
    entries = subprocess.check_output(command + ['ls-tree', '-r', '-z', '--format=%(objectname) %(path)', C1_SHA, '--', 'mooring', 'tools/wordcraft-c1-engine']).decode().split('\0')
    expected = dict(entry.split(' ', 1)[::-1] for entry in entries if entry)
    if set(expected) != set(manifest):
        raise ValueError('runtime source inventory mismatch')
    for relative, blob in expected.items():
        path = prepared / relative
        raw = os.readlink(path).encode() if path.is_symlink() else path.read_bytes()
        git_blob = hashlib.sha1(f'blob {len(raw)}\0'.encode() + raw).hexdigest()
        if git_blob != blob or digest(path) != manifest[relative]:
            raise ValueError(f'runtime source drift: {relative}')


def atomic_write(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(dir=path.parent, prefix='.' + path.name + '.', delete=False) as stream:
        temporary = Path(stream.name)
        stream.write(value)
    try:
        temporary.replace(path)
    finally:
        temporary.unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--prepared', required=True, type=Path)
    parser.add_argument('--c1-source', required=True, type=Path)
    parser.add_argument('--rows', required=True, type=Path)
    parser.add_argument('--execution', required=True, type=Path)
    parser.add_argument('--profiles', required=True, type=Path)
    parser.add_argument('--engine-binary', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--receipt', required=True, type=Path)
    args = parser.parse_args()
    if args.output.resolve() == args.receipt.resolve():
        raise SystemExit('policy and raw receipt paths must differ')
    binary_sha = digest(args.engine_binary)
    here = Path(__file__).resolve().parent
    preparation = json.loads((args.prepared / 'preparation.json').read_text())
    if preparation['engineSha'] != ENGINE_SHA or preparation['c1Sha'] != C1_SHA:
        raise SystemExit('qualification source identity mismatch')
    execution = json.loads(args.execution.read_text())
    if execution.get('completed') is not True or execution.get('exitCode') not in (0, 101) or not execution.get('invocationId') or execution.get('preparationSha256') != digest(args.prepared / 'preparation.json') or execution.get('engineBinarySha256') != binary_sha:
        raise SystemExit('incomplete or mismatched execution receipt')
    checks = [('profileSha256', args.profiles), ('caseSourceSha256', here / 'qualification.rs'),
              ('prepareSourceSha256', here / 'prepare.py'), ('fixturesSha256', here / 'fixtures.json'),
              ('generatedTestsSha256', args.prepared / 'mooring/crates/mooring-collab/tests/ce_catalog.rs'),
              ('casesSha256', args.prepared / 'mooring/crates/mooring-collab/tests/ce_catalog_cases.json')]
    for field, path in checks:
        if digest(path) != preparation[field]:
            raise SystemExit(f'qualification evidence drift: {field}')
    if preparation['engineBinarySha256'] != binary_sha:
        raise SystemExit('qualification engine binary drift')
    verify_runtime(args.prepared, args.c1_source, preparation)
    cases = json.loads(checks[-1][1].read_text())
    if len(cases) != 163 or preparation['commands'] != 163:
        raise SystemExit('full C-E qualification inventory required')
    paths = list(args.rows.glob('*.json'))
    if {path.name for path in paths} != {case['id'] + '.json' for case in cases}:
        raise SystemExit('qualification row filename inventory mismatch')
    rows = [json.loads(path.read_text()) for path in paths]
    passed = checked_rows(cases, rows, preparation, execution['invocationId'])
    source = json.loads((args.prepared / 'tools/wordcraft-c1-engine/source.json').read_text())
    policy = {'schemaVersion': 1, 'catalogVersion': 5, 'engineSha': ENGINE_SHA, 'c1HostSha': C1_SHA,
              'helperPatchSha256': source['patch_sha256'], 'registryDigest': preparation['registryDigest'],
              'profileSha256': preparation['profileSha256'], 'scope': 'isolated-c1-fixture',
              'fixturesSource': {'path': 'tools/wordcraft-capabilities/c1-qualification/fixtures.json', 'sha256': preparation['fixturesSha256']},
              'testSource': {'path': 'tools/wordcraft-capabilities/c1-qualification/qualification.rs',
                             'sha256': preparation['caseSourceSha256']}, 'commands': {}}
    for command_id in sorted(passed):
        row = passed[command_id]
        case = next(case for case in cases if case['id'] == command_id)
        policy['commands'][command_id] = {'test': row['test'], 'testedParameters': row['params'],
                                         'testedTarget': {'unit': 'utf8_bytes', 'start': case['start'], 'end': case['end']},
                                         'testedSetup': case['setup'], 'initialParagraphs': ['Alpha', 'Peer'],
                                         'coverage': 'tested-fixture-only', 'invariants': sorted(CHECKS), 'testedParameterSchema': tested_schema(row['params']),
                                         'targetProfile': 'exact-current-revision/body/utf8-grapheme-boundaries',
                                         'undoCommand': 'participant.undo'}
    policy_bytes = (json.dumps(policy, indent=2) + '\n').encode()
    receipt = {**preparation, 'execution': execution, 'matrixStatus': 'passed' if len(passed) == len(cases) else 'failed',
               'passed': len(passed), 'failed': len(cases) - len(passed), 'skipped': 0,
               'engineBinarySha256': binary_sha, 'commands': rows,
               'qualificationPolicySha256': hashlib.sha256(policy_bytes).hexdigest()}
    atomic_write(args.receipt, (json.dumps(receipt, indent=2) + '\n').encode())
    atomic_write(args.output, policy_bytes)
    print(json.dumps({'qualified': sorted(passed), 'failed': len(cases) - len(passed), 'skipped': 0}))


if __name__ == '__main__':
    main()
