"""CI-only isolated worker qualification; synthetic inputs, no host credentials."""
import http.client
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import time


def docker(*args):
    return subprocess.check_output(['docker', *args], stderr=subprocess.DEVNULL, timeout=60)


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__('worker')
        self.socket_path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(25)
        self.sock.connect(self.socket_path)


def main():
    image = os.environ['GRIDCRAFT_IMAGE']
    info = json.loads(docker('image', 'inspect', image))[0]
    assert info['Config']['User'] == '1000:1000'
    assert info['Os'] == 'linux' and info['Architecture'] == 'amd64'
    assert os.getuid() != 0, 'qualification must use a non-root worker identity'
    name = 'gridcraft-ci-' + secrets.token_hex(8)
    with tempfile.TemporaryDirectory(prefix='gridcraft-ci-') as root:
        path = Path(root)
        service = path / 'socket'
        service.mkdir(mode=0o700)
        token = path / 'token'
        secret = secrets.token_hex(32)
        token.write_text(secret)
        token.chmod(0o600)
        args = ['run', '--detach', '--pull=never', '--name', name,
                '--network=none', '--read-only', '--cap-drop=ALL',
                '--security-opt=no-new-privileges', '--user=' + str(os.getuid()) + ':' + str(os.getgid()),
                '--memory=256m', '--memory-swap=256m', '--cpus=1', '--pids-limit=32',
                '--tmpfs=/scratch:rw,noexec,nosuid,nodev,size=32m,mode=0700,uid=' + str(os.getuid()) + ',gid=' + str(os.getgid()),
                '--mount=type=bind,src=' + str(service) + ',dst=/run/worker',
                '--mount=type=bind,src=' + str(token) + ',dst=/run/secrets/service-token,readonly', image]
        identifier = docker(*args).decode().strip()
        try:
            socket_path = service / 'service.sock'
            for _ in range(100):
                if socket_path.exists():
                    break
                time.sleep(0.1)
            assert socket_path.exists(), 'worker did not listen'
            cfg = json.loads(docker('inspect', identifier))[0]
            host = cfg['HostConfig']
            assert host['NetworkMode'] == 'none' and host['ReadonlyRootfs']
            assert host['CapDrop'] == ['ALL'] and host['PidsLimit'] == 32
            assert host['Memory'] == host['MemorySwap'] == 256 * 1024 * 1024
            assert host['NanoCpus'] == 1000000000
            assert 'no-new-privileges' in host['SecurityOpt']
            assert all(m['Destination'] in ('/run/worker', '/run/secrets/service-token', '/scratch') for m in cfg['Mounts'])
            binds = [m for m in cfg['Mounts'] if m['Type'] == 'bind']
            assert {m['Destination'] for m in binds} == {'/run/worker', '/run/secrets/service-token'}
            assert all(m['Type'] in ('bind', 'tmpfs') for m in cfg['Mounts'])
            assert next(m for m in binds if m['Destination'] == '/run/secrets/service-token')['RW'] is False
            scratch = host['Tmpfs']['/scratch']
            assert all(option in scratch.split(',') for option in ('noexec', 'nosuid', 'nodev', 'size=32m', 'mode=0700'))
            native = json.loads(Path('internal/appengine/testdata/blank-workbook.json').read_text())

            def candidate(snapshot, command, bearer=secret):
                conn = UnixHTTP(str(socket_path))
                try:
                    conn.request('POST', '/v1/candidate', json.dumps({'snapshot': snapshot, 'command': command}),
                                 {'X-Crewship-Worker-Service': bearer, 'Content-Type': 'application/json'})
                    response = conn.getresponse()
                    return response.status, response.read()
                finally:
                    conn.close()

            assert candidate(native, {'kind': 'import'}, 'invalid')[0] == 401
            assert candidate(native, {'kind': 'file.open'})[0] == 400
            status, imported = candidate(native, {'kind': 'import'})
            assert status == 200, 'import failed'
            base = json.loads(imported)['result']
            status, edited = candidate(base, {'kind': 'cell.set', 'sheet': 'Sheet1', 'cell': 'B2', 'input': '42'})
            assert status == 200, 'edit failed'
            changed = json.loads(edited)['result']
            assert changed != base and json.loads(edited)['base'] == base
            # Long-lived service survives multiple fresh CLI processes. Scratch
            # must contain neither completed candidate inputs nor inherited secrets.
            assert docker('exec', identifier, '/bin/sh', '-c', 'test -z "$(ls -A /scratch)"').decode() == ''
            assert docker('exec', identifier, '/bin/sh', '-c', 'test ! -S /var/run/docker.sock && test ! -e /data/crewship.db').decode() == ''
            print('PASS: private authenticated worker, real import/edit, narrow commands, bounded offline container, scratch cleanup')
        finally:
            docker('rm', '--force', identifier)


if __name__ == '__main__':
    main()
