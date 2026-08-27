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

cd "$(git rev-parse --show-toplevel)" || exit 2

# ELF · Mach-O 32/64, both endiannesses · Mach-O universal · PE/COFF
MAGIC='^(7f454c46|feedface|cefaedfe|feedfacf|cffaedfe|cafebabe|bebafeca|4d5a)'

offenders=()
while IFS= read -r -d '' path; do
  [ -f "$path" ] || continue
  head -c4 -- "$path" 2>/dev/null | od -An -tx1 -v 2>/dev/null | tr -d ' \n' | grep -qE "$MAGIC" || continue
  size=$(wc -c <"$path" | tr -d ' ')
  offenders+=("$path (${size} bytes)")
done < <(git ls-files -z)

if [ ${#offenders[@]} -gt 0 ]; then
  printf 'no-root-binaries: compiled executable(s) tracked in git:\n'
  printf '  %s\n' "${offenders[@]}"
  cat <<'EOF'

A compiled binary must never be committed. It is usually `go build ./cmd/<x>`
or `go build ./scripts/<x>` with no -o, which drops the binary in the repo
root named after its directory, followed by `git add -A`.

  git rm --cached <path> && rm <path>

and add the name to .gitignore next to the others under "# Go".
EOF
  exit 1
fi

printf 'no-root-binaries: no compiled executables tracked (%s files checked)\n' "$(git ls-files | wc -l | tr -d ' ')"
