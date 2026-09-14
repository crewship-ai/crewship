#!/usr/bin/env python3
"""Push one real host snapshot to the custom Operations demo; no scheduler.

Run on the dev3 host. This samples the host, not a crew container. Authentication
uses the operator's existing CLI profile; credentials never enter the Page.
"""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import time
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--cli', required=True)
    parser.add_argument('--server', required=True)
    parser.add_argument('--slug', default='custom-operations')
    args = parser.parse_args()
    # This example deliberately probes only its local dev3 listener.
    if args.server != 'http://localhost:8083':
        parser.error('this dev3 host example requires --server http://localhost:8083')
    samples = []
    for _ in range(8):
        memory = dict(line.split(':', 1) for line in Path('/proc/meminfo').read_text().splitlines())
        samples.append(round(int(memory['MemAvailable'].split()[0]) / 1024))
        time.sleep(0.2)
    start = time.monotonic()
    with urllib.request.urlopen(args.server + '/api/health', timeout=5) as response:
        if response.status != 200:
            raise RuntimeError('dev3 health probe failed')
    latency = round((time.monotonic() - start) * 1000)
    disk = shutil.disk_usage('/')
    cpus = os.cpu_count() or 1
    load = os.getloadavg()[0]
    status = {'items': [
        {'name': 'Crewship API', 'state': 'ok', 'label': f'200 OK · {latency} ms'},
        {'name': 'Disk', 'state': 'ok' if disk.used / disk.total < .85 else 'warning', 'label': f'{round(disk.used / disk.total * 100)} % využito'},
        {'name': 'Paměť', 'state': 'ok' if samples[-1] > 1024 else 'warning', 'label': f'{samples[-1]} MB volných'},
        {'name': 'Zátěž CPU', 'state': 'ok' if load < cpus else 'warning', 'label': f'{load:.2f} / {cpus} CPU'},
    ]}
    metric = {'value': samples[-1], 'unit': 'MB', 'sparkline': samples}
    for panel, payload in [('services', status), ('memory', metric)]:
        subprocess.run([args.cli, '--server', args.server, 'page', 'set', f'{args.slug}/{panel}', '--data', '-'], input=json.dumps(payload), text=True, check=True, timeout=15)


if __name__ == '__main__':
    main()
