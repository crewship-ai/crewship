#!/usr/bin/env python3
"""Run the complete isolated matrix once, retaining its real failing exit status."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import uuid


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--prepared', required=True, type=Path)
    parser.add_argument('--engine-binary', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    prepared, binary, output = args.prepared.resolve(), args.engine_binary.resolve(), args.output.resolve()
    preparation = json.loads((prepared / 'preparation.json').read_text())
    if digest(binary) != preparation['engineBinarySha256']:
        raise SystemExit('engine binary drift')
    output.mkdir(parents=True, exist_ok=False)
    (output / 'rows').mkdir()
    execution = {'invocationId': str(uuid.uuid4()), 'preparationSha256': digest(prepared / 'preparation.json'),
                 'engineBinarySha256': digest(binary), 'completed': False, 'exitCode': None}
    receipt = output / 'execution.json'
    receipt.write_text(json.dumps(execution, indent=2) + '\n')
    env = {**os.environ, 'MOORING_C1_ENGINE_BINARY': str(binary),
           'WORDCRAFT_CE_QUALIFICATION_OUTPUT': str(output / 'rows'),
           'WORDCRAFT_CE_INVOCATION_ID': execution['invocationId']}
    with (output / 'cargo.log').open('w') as log:
        result = subprocess.run(['cargo', 'test', '--locked', '-p', 'mooring-collab', '--features',
                                 'wordcraft-command-experiment', '--test', 'ce_catalog', '--', '--test-threads=1'],
                                cwd=prepared / 'mooring', env=env, stdout=log, stderr=subprocess.STDOUT)
    execution.update(completed=True, exitCode=result.returncode)
    receipt.write_text(json.dumps(execution, indent=2) + '\n')
    print(json.dumps(execution))
    raise SystemExit(result.returncode)


if __name__ == '__main__':
    main()
