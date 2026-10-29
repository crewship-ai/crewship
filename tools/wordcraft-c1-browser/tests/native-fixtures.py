#!/usr/bin/env python3
"""Synthetic codec/selection fixtures from the ACTUAL pinned C1 helper.
Use crewship-heavy. No services, SQL, credentials, user documents or model runs.
"""
import argparse
import hashlib
import json
from pathlib import Path
import socket
import struct
import subprocess

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--helper', type=Path, required=True)
p.add_argument('--expected-sha256', required=True)
p.add_argument('--output', type=Path, required=True)
a = p.parse_args()
assert hashlib.sha256(a.helper.read_bytes()).hexdigest() == a.expected_sha256
out = a.output.resolve()
out.mkdir(parents=True, exist_ok=False)
s, c = socket.socketpair()
s.settimeout(15)
child = subprocess.Popen([str(a.helper.resolve())], pass_fds=[c.fileno()],
    env={'MOORING_C1_ENGINE_FD': str(c.fileno()), 'LANG': 'C.UTF-8'},
    stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
c.close()


def read(n):
    raw = b''
    while len(raw) < n:
        part = s.recv(n - len(raw))
        if not part:
            raise RuntimeError('helper EOF')
        raw += part
    return raw


def call(command, params, native=None, target=None):
    raw = json.dumps(dict(command=command, params=params, native=native, target=target,
        author='Technical fixture', time='2001-02-03T04:05:06Z')).encode()
    s.sendall(struct.pack('>I', len(raw)) + raw)
    n = struct.unpack('>I', read(4))[0]
    assert 0 < n <= 4 * 1024 * 1024
    reply = json.loads(read(n))
    assert reply['ok'], 'actual engine refused synthetic fixture'
    return reply['value']


try:
    initial = call('init', {})['native']
    native = initial
    fixtures = []
    target = dict(block=0, start=0, end=0, unit='utf8_bytes')
    for name, command, params in [('blank', None, {}), ('unicode', 'text.insert', {'text': 'Žluťoučký kůň'}),
        ('bold', 'format.bold', {'value': True}), ('font', 'format.font', {'name': 'Arial'}),
        ('size', 'format.size', {'size': 14}), ('align', 'para.align', {'value': 'center'}),
        ('enter', 'text.newParagraph', {})]:
        if command:
            native = call(command, params, native, target)['native']
        f = out / (name + '.json')
        f.write_text(json.dumps(native, ensure_ascii=False, separators=(',', ':')))
        fixtures.append(dict(name=name, path=str(f), sha256=hashlib.sha256(f.read_bytes()).hexdigest()))
    native = initial
    block = off = 0
    items = []
    for command, params in [('text.insert', {'text': 'a'}), ('text.insert', {'text': 'b'}),
        ('text.insert', {'text': 'č'}), ('text.newParagraph', {}), ('text.insert', {'text': 'Ž'}), ('text.backspace', {})]:
        target = dict(block=block, start=off, end=off, unit='utf8_bytes')
        value = call(command, params, native, target)
        native = value['native']
        items.append(dict(command=command, params=params, target=target, native=native,
            native_json=json.dumps(native, ensure_ascii=False, separators=(',', ':')), selection=value['result']))
        block, off = value['result']['focus']['path'][0], value['result']['focus']['off']
    (out / 'native-fixtures.json').write_text(json.dumps(dict(helper_sha256=a.expected_sha256, fixtures=fixtures)))
    (out / 'command-projections.json').write_text(json.dumps(dict(initial=initial,
        initial_json=json.dumps(initial, ensure_ascii=False, separators=(',', ':')), items=items), ensure_ascii=False))
finally:
    s.close()
    try:
        code = child.wait(timeout=5)
    except subprocess.TimeoutExpired:
        child.kill()
        child.wait()
        raise
assert code == 0
print(json.dumps({'fixtures': len(fixtures), 'command_projections': len(items), 'actual_helper_sha256': a.expected_sha256}))
