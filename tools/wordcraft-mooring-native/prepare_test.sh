#!/usr/bin/env bash
# Opt-in source-only regression checks. No compilation, downloads or service.
set -euo pipefail
if [[ $# != 1 ]]; then
  echo "usage: $0 WORDCRAFT_GIT_REPOSITORY" >&2
  exit 2
fi
source_repo=$(realpath -- "$1")
bundle_dir=$(cd -- "$(dirname -- "$0")" && pwd -P)
fixture=$(mktemp -d)
trap 'rm -rf -- "$fixture"' EXIT
before_head=$(git -C "$source_repo" rev-parse HEAD)
before_status=$(git -C "$source_repo" status --porcelain=v1 --untracked-files=all)
# A surrounding Git repository must not capture the nested apply operation.
git init --quiet --template= "$fixture/parent"
printf 'preserve parent work\n' > "$fixture/parent/sentinel"
bash "$bundle_dir/prepare.sh" "$source_repo" "$fixture/parent/output"
test "$(cat "$fixture/parent/sentinel")" = 'preserve parent work'
test ! -e "$fixture/parent/.git/index"
if bash "$bundle_dir/prepare.sh" "$source_repo" "$fixture/parent/output"; then
  echo 'existing output was accepted' >&2
  exit 1
fi
(cd -- "$fixture/parent/output" && sha256sum --check "$bundle_dir/source-after.sha256")
# Tampered source must be rejected before even creating the requested output.
mkdir "$fixture/tampered"
cp -- "$bundle_dir/prepare.sh" "$bundle_dir/patch.sha256" "$bundle_dir/source.patch" "$bundle_dir/source-archive.sha256" "$fixture/tampered/"
printf '\n' >> "$fixture/tampered/source.patch"
if bash "$fixture/tampered/prepare.sh" "$source_repo" "$fixture/rejected"; then
  echo 'tampered patch was accepted' >&2
  exit 1
fi
test ! -e "$fixture/rejected"
# Whole-source verification covers unchanged files, not only patched paths.
cp -- "$bundle_dir/source.patch" "$fixture/tampered/source.patch"
printf '%064d  .source.tar\n' 0 > "$fixture/tampered/source-archive.sha256"
if bash "$fixture/tampered/prepare.sh" "$source_repo" "$fixture/rejected-archive"; then
  echo 'wrong whole-source archive was accepted' >&2
  exit 1
fi
test ! -e "$fixture/rejected-archive/crates"
test -f "$fixture/rejected-archive/.source.tar"
test "$(git -C "$source_repo" rev-parse HEAD)" = "$before_head"
test "$(git -C "$source_repo" status --porcelain=v1 --untracked-files=all)" = "$before_status"
printf 'PASS: exact source copy, nested Git isolation, existing-output refusal, patch/archive tamper refusal, caller preservation\n'
