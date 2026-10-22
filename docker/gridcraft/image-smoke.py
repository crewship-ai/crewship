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
    contract = json.loads(docker('run', '--rm', '--pull=never', '--network=none', '--read-only', '--cap-drop=ALL',
                                 '--security-opt=no-new-privileges', '--user=' + str(os.getuid()) + ':' + str(os.getgid()),
                                 image, '--contract'))
    assert set(contract) == {'protocol', 'protocol_min', 'protocol_max', 'engine_sha256'}, 'worker contract shape'
    protocol, engine = contract['protocol'], contract['engine_sha256']
    accepted = (contract['protocol_min'], contract['protocol_max'])
    assert all(type(v) is int and 1 <= v <= 9999 for v in (protocol, *accepted)), 'worker protocol bounds'
    assert accepted[0] <= protocol <= accepted[1], 'worker must accept its own protocol'
    assert isinstance(engine, str) and len(engine) == 64 and all(c in '0123456789abcdef' for c in engine), 'engine pin'
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

            def candidate(snapshot, command, bearer=secret, host_protocol=str(protocol), host_engine=engine):
                conn = UnixHTTP(str(socket_path))
                try:
                    headers = {'X-Crewship-Worker-Service': bearer, 'Content-Type': 'application/json'}
                    if host_protocol is not None:
                        headers['X-Crewship-Gridcraft-Protocol'] = host_protocol
                    if host_engine is not None:
                        headers['X-Crewship-Gridcraft-Engine'] = host_engine
                    conn.request('POST', '/v1/candidate', json.dumps({'snapshot': snapshot, 'command': command}), headers)
                    response = conn.getresponse()
                    declared = (response.getheader('X-Crewship-Gridcraft-Protocol'), response.getheader('X-Crewship-Gridcraft-Engine'))
                    body = response.read()
                    if response.status != 401:
                        # The running worker declares the same contract as the receipt.
                        assert declared == (str(protocol), engine), 'worker handshake differs from its declared contract'
                    else:
                        assert declared == (None, None), 'contract disclosed before authentication'
                    return response.status, body
                finally:
                    conn.close()

            assert candidate(native, {'kind': 'import'}, 'invalid')[0] == 401
            # The worker refuses a host without, or outside, its protocol contract.
            assert candidate(native, {'kind': 'import'}, host_protocol=None)[0] == 412
            assert candidate(native, {'kind': 'import'}, host_protocol=str(accepted[1] + 1))[0] == 412
            assert candidate(native, {'kind': 'import'}, host_protocol=str(accepted[0] - 1))[0] == 412
            assert candidate(native, {'kind': 'import'}, host_engine='0' * 64)[0] == 412
            assert candidate(native, {'kind': 'file.open'})[0] == 400
            status, imported = candidate(native, {'kind': 'import'})
            assert status == 200, 'import failed'
            base = json.loads(imported)['result']
            status, edited = candidate(base, {'kind': 'cell.set', 'sheet': 'Sheet1', 'cell': 'B2', 'input': '42'})
            assert status == 200, 'edit failed'
            changed = json.loads(edited)['result']
            assert changed != base and json.loads(edited)['base'] == base
            # The image has no shell, so inspect the container's view from the
            # host through its PID 1 (same unprivileged UID as the worker).
            pid = str(json.loads(docker('inspect', identifier))[0]['State']['Pid'])
            proc = Path('/proc') / pid
            assert (proc / 'cmdline').read_bytes().split(b'\0')[0] == b'/usr/local/bin/crewship-gridcraft-worker'
            # Long-lived service survives multiple fresh CLI processes. Scratch
            # must contain neither completed candidate inputs nor inherited
            # secrets, and no engine process may outlive its candidate.
            assert os.listdir(proc / 'root' / 'scratch') == []
            assert (proc / 'task' / pid / 'children').read_text().split() == []
            assert not os.path.lexists(proc / 'root' / 'var' / 'run' / 'docker.sock')
            assert not os.path.lexists(proc / 'root' / 'data' / 'crewship.db')
            for tool in ('bin/sh', 'bin/bash', 'usr/bin/node', 'usr/bin/env'):
                assert not os.path.lexists(proc / 'root' / tool), tool + ' must not be in the worker image'
            print('PASS: protocol', protocol, 'handshake, private authenticated worker, real import/edit, narrow commands, bounded offline container, '
                  'scratch cleanup, no surviving engine process, no shell or interpreter')
        finally:
            docker('rm', '--force', identifier)


if __name__ == '__main__':
    main()
