#!/usr/bin/env python3
"""Boot the actual Linux release archive in isolated state before publication."""
import argparse
import os
from pathlib import Path
import secrets
import socket
import subprocess
import tarfile
import tempfile
import time
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument('archive')
parser.add_argument('sha')
args = parser.parse_args()
with tempfile.TemporaryDirectory(prefix='crewship-archive-smoke-') as temp:
    root = Path(temp)
    with tarfile.open(args.archive) as archive:
        archive.extractall(root, filter='data')
    binary = root / 'crewship'
    for name in ('crewship', 'crewship-sidecar', 'entrypoint.sh'):
        if not (root / name).is_file():
            raise SystemExit(f'Archive is missing {name}')
    version = subprocess.check_output([str(binary), 'version'], text=True)
    print(version)
    if args.sha[:7] not in version:
        raise SystemExit('Archive commit identity mismatch')
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
    env = {k: v for k, v in os.environ.items() if not k.startswith('CREWSHIP_')}
    env.update(DATABASE_URL=f'file:{root}/smoke.db', NEXTAUTH_SECRET=secrets.token_hex(32),
               ENCRYPTION_KEY=secrets.token_hex(32), CREWSHIP_PORT=str(port),
               CREWSHIP_STORAGE_BASE_PATH=str(root / 'storage'), CREWSHIP_LOG_PATH=str(root / 'logs'),
               CREWSHIP_BOLT_PATH=str(root / 'state.db'), CREWSHIP_CONFIG=str(root / 'cli.yaml'))
    with (root / 'server.log').open('w') as log:
        server = subprocess.Popen([str(binary), 'start', '--no-docker'], cwd=root, env=env, stdout=log, stderr=subprocess.STDOUT)
        try:
            for _ in range(90):
                if server.poll() is not None:
                    raise RuntimeError('Archive server exited before becoming healthy')
                try:
                    with urllib.request.urlopen(f'http://127.0.0.1:{port}/healthz', timeout=2) as response:
                        assert response.status == 200
                    with urllib.request.urlopen(f'http://127.0.0.1:{port}/', timeout=5) as response:
                        assert response.status == 200
                    print('Archive boot, bundled sidecar and embedded UI verified')
                    break
                except OSError:
                    time.sleep(1)
            else:
                raise RuntimeError('Archive server health deadline exceeded')
        except BaseException:
            print((root / 'server.log').read_text())
            raise
        finally:
            server.terminate()
            try:
                server.wait(timeout=15)
            except subprocess.TimeoutExpired:
                server.kill()
                server.wait()
