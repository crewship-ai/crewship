#!/usr/bin/env python3
"""Collect changelog.d/ fragments into CHANGELOG.md's ## [Unreleased] section.

A PR writes its release note as its own file, changelog.d/<number>-<slug>.md
(#4751), so queued PRs no longer conflict at the top of ## [Unreleased]. At
release time this script moves every fragment's text to the top of that
section, highest number first (the file is newest-first), and deletes the
collected fragment files. See RELEASING.md.

A run is crash-safe (#4800). Before changing anything it records, in
CHANGELOG.md.collect-journal, the fragments it collects and the SHA-256 of
CHANGELOG.md before and after. It then installs the new CHANGELOG.md
(staged, fsynced, atomically renamed), makes that rename durable, and only
then deletes the fragments and the journal. A rerun that finds a journal
finishes the interrupted run: CHANGELOG.md matching the "after" hash means
the listed fragments are already in it and are only deleted; matching the
"before" hash means nothing was installed and the run starts over. Any
other CHANGELOG.md makes it refuse, changing nothing. No note is ever
recognised by its text, so a new note is never mistaken for a collected one.

Fragments are the *.md files directly inside changelog.d/, except README.md.
The script refuses, changing nothing, when CHANGELOG.md has no
## [Unreleased] heading, or a fragment is empty, not valid UTF-8, or not a
regular file (a symlink, dangling or not, is never followed).

Usage: python3 scripts/changelog-collect.py [--root DIR] [--dry-run]
"""
import argparse
import hashlib
import json
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


def digest(text):
    return hashlib.sha256(text.encode('utf-8')).hexdigest()


def fsync_path(path):
    fd = os.open(path, os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def write_durably(path, text):
    """Atomically replace path with text; the file and its directory entry
    are on disk before it returns."""
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
    make_durable(path)


def make_durable(path):
    """fsync path and its directory: a rename onto path, by this run or by an
    interrupted one, survives a power loss from here on."""
    fsync_path(path)
    fsync_path(path.parent)


def read_journal(root, journal_path, changelog):
    """Return the fragment paths an interrupted run already put into
    CHANGELOG.md ([] when it installed nothing), or None without a journal."""
    try:
        text = journal_path.read_text(encoding='utf-8')
    except FileNotFoundError:
        return None
    name = journal_path.relative_to(root)
    try:
        journal = json.loads(text)
        before, after = journal['before'], journal['after']
        names = journal['fragments']
        if not all(isinstance(n, str) and '/' not in n and n.endswith('.md') for n in names):
            raise ValueError
    except (ValueError, KeyError, TypeError):
        raise ValueError(f'{name} is unreadable; restore CHANGELOG.md and changelog.d/ '
                         f'from git, delete {name} and collect again') from None
    current = digest(changelog)
    if current == after:
        return [root / 'changelog.d' / n for n in names]
    if current == before:
        return []
    raise ValueError(f'CHANGELOG.md changed after the interrupted collect recorded in {name}; '
                     f'check which of its fragments ({", ".join(names)}) are in '
                     f'## [Unreleased], delete those and {name}, and collect again')


def read_notes(root, paths):
    notes = []
    for path in paths:
        try:
            text = path.read_text(encoding='utf-8').strip()
        except UnicodeDecodeError:
            raise ValueError(f'{path.relative_to(root)} is not valid UTF-8') from None
        if not text:
            raise ValueError(f'{path.relative_to(root)} is empty; write its release note or delete it')
        notes.append(text)
    return notes


def delete_fragments(root, changelog_path, journal_path, paths):
    """Delete collected fragments, then the journal naming them. The
    CHANGELOG.md holding their notes is made durable first, on every path:
    the rename may be an interrupted run's, not yet on disk."""
    make_durable(changelog_path)
    for path in paths:
        try:
            path.unlink()
        except FileNotFoundError:
            pass
        print(f'collected {path.relative_to(root)}', file=sys.stderr)
    fsync_path(root / 'changelog.d')
    discard_journal(journal_path)


def discard_journal(journal_path):
    journal_path.unlink()
    fsync_path(journal_path.parent)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split('\n', 1)[0])
    parser.add_argument('--root', default=str(Path(__file__).resolve().parents[1]),
                        help='repository root (default: this checkout)')
    parser.add_argument('--dry-run', action='store_true',
                        help='print the resulting CHANGELOG.md, change nothing')
    args = parser.parse_args(argv)
    root = Path(args.root)
    changelog_path = root / 'CHANGELOG.md'
    journal_path = root / 'CHANGELOG.md.collect-journal'

    # Validate everything before changing anything: a refusal changes nothing.
    try:
        changelog = changelog_path.read_text(encoding='utf-8')
        done = read_journal(root, journal_path, changelog)
        done_names = {path.name for path in done or []}
        paths = [path for path in fragments(root) if path.name not in done_names]
        if not paths and done is None:
            print('No changelog fragments to collect.', file=sys.stderr)
            return 0
        result = collect(changelog, read_notes(root, paths)) if paths else changelog
    except (ValueError, OSError) as err:
        print(f'error: {err}', file=sys.stderr)
        return 1

    if args.dry_run:
        sys.stdout.write(result)
        return 0

    try:
        # Finish an interrupted run first: delete what it already collected,
        # or drop its journal when it installed nothing.
        if done:
            delete_fragments(root, changelog_path, journal_path, done)
        elif done is not None:
            discard_journal(journal_path)
        if paths:
            # Journal, then CHANGELOG.md, then the fragments (#4800): every
            # interruption leaves each note in its fragment, in CHANGELOG.md
            # with the journal naming its fragment, or both.
            write_durably(journal_path, json.dumps({
                'before': digest(changelog),
                'after': digest(result),
                'fragments': [path.name for path in paths],
            }, indent=2) + '\n')
            write_durably(changelog_path, result)
            delete_fragments(root, changelog_path, journal_path, paths)
    except OSError as err:
        print(f'error: {err}; rerun to finish collecting', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
