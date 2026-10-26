#!/usr/bin/env python3
"""Prepare a separate pinned C1 renderer; never changes the legacy SDK recipe.
Use crewship-heavy. Builds and browser tests remain separate explicit commands.
"""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--output', type=Path, required=True)
    args = p.parse_args()
    here = Path(__file__).resolve().parent
    spec = json.loads((here / 'source.json').read_text())
    engine = here.parent / 'wordcraft-c1-engine'
    if sha(engine / 'source.json') != spec['engine_source_manifest_sha256']:
        raise SystemExit('C1 engine source identity mismatch')
    patch = here / 'source.patch'
    if sha(patch) != spec['patch_sha256']:
        raise SystemExit('C1 renderer patch identity mismatch')
    output = args.output.resolve()
    subprocess.run([sys.executable, str(engine / 'prepare.py'), '--output', str(output)], check=True)
    source = output / 'source'
    for name, pins in spec['files'].items():
        target = source / name
        if (sha(target) if target.exists() else None) != pins['before_sha256']:
            raise SystemExit('C1 renderer input identity mismatch')
    subprocess.run(['git', '-C', str(source), 'apply', '--check', str(patch)], check=True)
    subprocess.run(['git', '-C', str(source), 'apply', str(patch)], check=True)
    for name, pins in spec['files'].items():
        if sha(source / name) != pins['after_sha256']:
            raise SystemExit('C1 renderer output identity mismatch')
    shutil.copytree(here / 'wasm-harness', output / 'wasm-harness')
    (output / 'renderer-source-receipt.json').write_text(json.dumps({**spec, 'prepared': True,
        'scope': 'Prepared source only. No browser, SQL, host Saved or user acceptance claim.'}, indent=2) + '\n')
    print(json.dumps({'renderer_prepared': True, 'revision': spec['revision']}))


if __name__ == '__main__':
    main()
