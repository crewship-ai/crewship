#!/usr/bin/env bash
#
# no-root-binaries.sh — fail if a compiled executable is tracked in git.
#
# Why this exists rather than another .gitignore line (#2123):
#
# `go build ./scripts/<tool>` with no -o writes the binary into the repo root,
# named after its package directory. A `git add -A` then sweeps it in. This has
# now happened three times — 8.5 MB of /ping-go, a 3.2 MB Mach-O in #1480, and
# 10.5 MB of /docs-surface-check in #2123 — and each time the remedy was to add
# one more name to .gitignore. The .gitignore comment written after the second
# one says outright that an enumerated list only covers the binaries someone
# remembered. It was right, and the third binary arrived through exactly the
# gap it described.
#
# So the list is kept complete *and* this check exists, because the list cannot
# defend against the tool nobody has written yet.
#
# It costs more than disk. A committed Go binary carries every stdlib CVE
# present at build time, statically linked, and the scanners read it as
# shipped code: the high-severity code-scanning alerts on /ping-go were
# vulnerabilities in a binary nothing executed.
#
# Detection is by magic bytes, not by extension or mode, because the failure
# mode is a file with no extension and an ordinary-looking name. Image and
# font assets are not affected — none of them start with these signatures.
set -uo pipefail

# One Python process reads four bytes per tracked file. Spawning head/od/tr/grep
# for every path exhausted Shell's five-minute CI budget on the full tree.
exec python3 - <<'PYTHON'
import os
import subprocess
import sys

os.chdir(subprocess.check_output(["git", "rev-parse", "--show-toplevel"]).rstrip(b"\n"))
paths = [p for p in subprocess.check_output(["git", "ls-files", "-z"]).split(b"\0") if p]
magic = {bytes.fromhex(value) for value in (
    "7f454c46", "feedface", "cefaedfe", "feedfacf", "cffaedfe", "cafebabe", "bebafeca",
)}
offenders = []
for path in paths:
    if not os.path.isfile(path):
        continue
    try:
        with open(path, "rb") as source:
            prefix = source.read(4)
    except OSError:
        # Match the original head pipeline: a missing/unreadable file has no
        # executable signature. Git tracks paths, not their current existence.
        continue
    if prefix in magic or prefix.startswith(b"MZ"):
        offenders.append((os.fsdecode(path), os.path.getsize(path)))

if offenders:
    print("no-root-binaries: compiled executable(s) tracked in git:")
    for path, size in offenders:
        print(f"  {path} ({size} bytes)")
    print('\nA compiled binary must never be committed. It is usually `go build ./cmd/<x>`\nor `go build ./scripts/<x>` with no -o, which drops the binary in the repo\nroot named after its directory, followed by `git add -A`.\n\n  git rm --cached <path> && rm <path>\n\nand add the name to .gitignore next to the others under "# Go".\n')
    sys.exit(1)
print(f"no-root-binaries: no compiled executables tracked ({len(paths)} files checked)")
PYTHON
