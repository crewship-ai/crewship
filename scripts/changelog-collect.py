#!/usr/bin/env python3
"""Collect changelog.d/ fragments into CHANGELOG.md's ## [Unreleased] section.

A PR writes its release note as its own file, changelog.d/<number>-<slug>.md
(#4751), so queued PRs no longer conflict at the top of ## [Unreleased]. At
release time this script moves every fragment's text to the top of that
section, highest number first (the file is newest-first), and deletes the
collected fragment files. See RELEASING.md.

It installs the new CHANGELOG.md (staged, fsynced, then atomically renamed)
before it deletes any fragment, so an interruption never loses a note: a
rerun finds the leftover fragments, sees their notes already in
## [Unreleased], does not add them again, and only deletes them.

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
NEXT_SECTION = re.compile(r'^## ', re.M)
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


def collected(section, note):
    """Whether note already stands, as whole lines, in the section."""
    return re.search(r'^' + re.escape(note) + r'$', section, re.M) is not None


def collect(changelog, notes):
    """Return changelog with the notes not yet in ## [Unreleased] on top.

    A note already present there is from an earlier, interrupted run that
    installed CHANGELOG.md but did not finish deleting the fragments.
    """
    match = HEADING.search(changelog)
    if not match:
        raise ValueError("CHANGELOG.md has no '## [Unreleased]' heading")
    head, rest = changelog[:match.end()], changelog[match.end():]
    end = NEXT_SECTION.search(rest)
    section = rest[:end.start()] if end else rest
    notes = [note for note in notes if not collected(section, note)]
    if not notes:
        return changelog
    rest = rest.lstrip('\n')
    block = '\n'.join(notes)
    return head + '\n\n' + block + '\n\n' + rest


def install(path, text):
    """Atomically replace path with text, durable before it returns."""
    staged = path.with_name(path.name + '.collect-tmp')
    try:
        with open(staged, 'w', encoding='utf-8') as f:
            f.write(text)
            f.flush()
            os.fsync(f.fileno())
        os.replace(staged, path)
    except BaseException:
        try:
            staged.unlink()
        except OSError:
            pass
        raise
    fsync_dir(path.parent)


def fsync_dir(directory):
    fd = os.open(directory, os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


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
        changelog = changelog_path.read_text(encoding='utf-8')
        result = collect(changelog, notes)
    except (ValueError, OSError) as err:
        print(f'error: {err}', file=sys.stderr)
        return 1

    if args.dry_run:
        sys.stdout.write(result)
        return 0

    # Install CHANGELOG.md first, durably, and only then delete the fragments
    # (#4800). Deleting first lost every deleted note when the run stopped
    # before the install: the rerun no longer saw them. Now an interruption at
    # any point leaves each note in CHANGELOG.md, in its fragment, or both,
    # and collect() skips the notes a rerun finds already collected.
    try:
        if result != changelog:
            install(changelog_path, result)
        for path in paths:
            path.unlink()
            print(f'collected {path.relative_to(root)}', file=sys.stderr)
        fsync_dir(root / 'changelog.d')
    except OSError as err:
        print(f'error: {err}; rerun to finish collecting', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
