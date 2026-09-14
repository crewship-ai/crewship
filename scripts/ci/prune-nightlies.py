#!/usr/bin/env python3
"""Remove only old per-run nightlies, retaining at least twenty complete runs."""
from datetime import datetime, timedelta, timezone
import json
import re
import subprocess
import sys


def expired(releases, now):
    candidates = [r for r in releases if r.get('prerelease') and not r.get('draft')
                  and re.fullmatch(r'nightly-[0-9]{8}-r[0-9]+', r.get('tag_name', ''))
                  and r.get('published_at')]
    candidates.sort(key=lambda r: r['published_at'], reverse=True)
    cutoff = now - timedelta(days=14)
    return [r['tag_name'] for r in candidates[20:]
            if datetime.fromisoformat(r['published_at'].replace('Z', '+00:00')) < cutoff]


if __name__ == '__main__':
    repo = sys.argv[1]
    pages = json.loads(subprocess.check_output(['gh', 'api', '--paginate', '--slurp', f'repos/{repo}/releases?per_page=100']))
    for tag in expired([r for page in pages for r in page], datetime.now(timezone.utc)):
        print(f'Removing expired nightly {tag}', flush=True)
        subprocess.run(['gh', 'release', 'delete', tag, '--repo', repo, '--cleanup-tag', '--yes'], check=True)
