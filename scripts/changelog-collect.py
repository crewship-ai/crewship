#!/usr/bin/env python3
"""Collect changelog.d/ fragments into CHANGELOG.md's ## [Unreleased] section.

A PR writes its release note as its own file, changelog.d/<number>-<slug>.md
(#4751), so queued PRs no longer conflict at the top of ## [Unreleased]. At
release time this script moves every fragment's text to the top of that
section, highest number first (the file is newest-first), and deletes the
collected fragment files. See RELEASING.md.

Fragments are the *.md files directly inside changelog.d/, except README.md.
The script refuses, changing nothing, when CHANGELOG.md has no
## [Unreleased] heading, or a fragment is empty, not valid UTF-8, or not a
regular file (a symlink, dangling or not, is never followed).

Usage: python3 scripts/changelog-collect.py [--root DIR] [--dry-run]
"""
import argparse
import os
from pathlib import Path
import re
import sys

HEADING = re.compile(r'^## \[Unreleased\][ \t]*$', re.M)
NUMBER = re.compile(r'^(\d+)')


def fragment_key(path):
    match = NUMBER.match(path.name)
    return (-int(match[1]) if match else 0, path.name)


def fragments(root):
    """Return the fragment paths; raise ValueError on a non-regular one."""
    directory = root / 'changelog.d'
    if not directory.is_dir():
        return []
    found = []
    for path in directory.iterdir():
        if path.suffix != '.md' or path.name == 'README.md':
            continue
        # is_symlink first: is_file() follows links and is False for a
        # dangling one, so either check alone would follow or skip it.
        if path.is_symlink() or not path.is_file():
            if path.is_dir() and not path.is_symlink():
                continue
            raise ValueError(f'{path.relative_to(root)} is not a regular file; '
                             'replace it with the release note itself')
        found.append(path)
    return sorted(found, key=fragment_key)


def collect(changelog, notes):
    match = HEADING.search(changelog)
    if not match:
        raise ValueError("CHANGELOG.md has no '## [Unreleased]' heading")
    head, rest = changelog[:match.end()], changelog[match.end():]
    rest = rest.lstrip('\n')
    block = '\n'.join(notes)
    return head + '\n\n' + block + '\n\n' + rest


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split('\n', 1)[0])
    parser.add_argument('--root', default=str(Path(__file__).resolve().parents[1]),
                        help='repository root (default: this checkout)')
    parser.add_argument('--dry-run', action='store_true',
                        help='print the resulting CHANGELOG.md, change nothing')
    args = parser.parse_args(argv)
    root = Path(args.root)
    changelog_path = root / 'CHANGELOG.md'

    try:
        paths = fragments(root)
        if not paths:
            print('No changelog fragments to collect.', file=sys.stderr)
            return 0
        notes = []
        for path in paths:
            try:
                text = path.read_text(encoding='utf-8').strip()
            except UnicodeDecodeError:
                raise ValueError(f'{path.relative_to(root)} is not valid UTF-8') from None
            if not text:
                raise ValueError(f'{path.relative_to(root)} is empty; write its release note or delete it')
            notes.append(text)
        result = collect(changelog_path.read_text(encoding='utf-8'), notes)
    except (ValueError, OSError) as err:
        print(f'error: {err}', file=sys.stderr)
        return 1

    if args.dry_run:
        sys.stdout.write(result)
        return 0

    # Stage the new file first, then remove the fragments, then swap it in
    # atomically: a failure before the swap leaves CHANGELOG.md untouched, and
    # no state has the notes both in CHANGELOG.md and still in changelog.d/,
    # so a rerun cannot duplicate entries. git still holds any removed file.
    staged = changelog_path.with_name(changelog_path.name + '.collect-tmp')
    staged.write_text(result, encoding='utf-8')
    for path in paths:
        path.unlink()
        print(f'collected {path.relative_to(root)}', file=sys.stderr)
    os.replace(staged, changelog_path)
    return 0


if __name__ == '__main__':
    sys.exit(main())
