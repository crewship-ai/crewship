#!/usr/bin/env python3
"""Prepare a separate pinned C1 renderer; never changes the legacy SDK recipe.
Use crewship-heavy. Builds and browser tests remain separate explicit commands.
"""
import argparse
import hashlib
import json
import re
from pathlib import Path
import shutil
import subprocess
import sys


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def qualified_commands(path, spec):
    """Same pinned engine qualification as the server; no runtime/user input."""
    raw = path.read_bytes()
    if hashlib.sha256(raw).hexdigest() != spec['capabilities_sha256']:
        raise SystemExit('C1 capabilities identity mismatch')
    catalog = json.loads(raw)
    if catalog.get('schemaVersion') != 1 or catalog.get('engine', {}).get('sha') != spec['revision']:
        raise SystemExit('C1 capabilities engine mismatch')
    names = []
    for row in catalog['commands']:
        qualification = row.get('engineQualification', {})
        if (qualification.get('status') == 'qualified'
                and qualification.get('scope') == 'isolated-c1-fixture'
                and row.get('effect') == 'document-candidate'
                and row.get('schemaStatus') == 'exact'
                and row.get('permanentlyForbidden') is False):
            name = row.get('id', '')
            if qualification.get('engineSha') != spec['revision'] or not re.fullmatch(r'[A-Za-z0-9_.-]{1,200}', name):
                raise SystemExit('C1 qualified command identity mismatch')
            names.append(name)
    if len(names) != spec['qualified_command_count'] or len(set(names)) != len(names):
        raise SystemExit('C1 qualified command count mismatch')
    return ('// Generated from pinned schemas/wordcraft/capabilities.json; do not edit.\n'
            'const C1_QUALIFIED_COMMANDS: &[&str] = &[\n'
            + ''.join('    ' + json.dumps(name) + ',\n' for name in sorted(names))
            + '];\n').encode()


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--output', type=Path, required=True)
    p.add_argument('--capabilities', type=Path,
                   help='Build-time read-only catalog override; the exact pinned hash is mandatory')
    args = p.parse_args()
    here = Path(__file__).resolve().parent
    spec = json.loads((here / 'source.json').read_text())
    engine = here.parent / 'wordcraft-c1-engine'
    if sha(engine / 'source.json') != spec['engine_source_manifest_sha256']:
        raise SystemExit('C1 engine source identity mismatch')
    patch = here / 'source.patch'
    if sha(patch) != spec['patch_sha256']:
        raise SystemExit('C1 renderer patch identity mismatch')
    capabilities = args.capabilities or here.parents[1] / 'schemas/wordcraft/capabilities.json'
    generated_commands = qualified_commands(capabilities, spec)
    output = args.output.resolve()
    subprocess.run([sys.executable, str(engine / 'prepare.py'), '--output', str(output)], check=True)
    source = output / 'source'
    for name, pins in spec['files'].items():
        target = source / name
        if (sha(target) if target.exists() else None) != pins['before_sha256']:
            raise SystemExit('C1 renderer input identity mismatch')
    subprocess.run(['git', '-C', str(source), 'apply', '--check', str(patch)], check=True)
    subprocess.run(['git', '-C', str(source), 'apply', str(patch)], check=True)
    generated = source / 'crates/ui-egui/src/c1_qualified_commands.rs'
    if generated.exists():
        raise SystemExit('C1 generated command output already exists')
    generated.write_bytes(generated_commands)
    for name, pins in spec['files'].items():
        if sha(source / name) != pins['after_sha256']:
            raise SystemExit('C1 renderer output identity mismatch')
    shutil.copytree(here / 'wasm-harness', output / 'wasm-harness')
    (output / 'renderer-source-receipt.json').write_text(json.dumps({**spec, 'prepared': True,
        'scope': 'Prepared source only. No browser, SQL, host Saved or user acceptance claim.'}, indent=2) + '\n')
    print(json.dumps({'renderer_prepared': True, 'revision': spec['revision']}))


if __name__ == '__main__':
    main()
