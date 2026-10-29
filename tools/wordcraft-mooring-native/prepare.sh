#!/usr/bin/env bash
# Copy a fixed public source revision and apply only the inventoried dormant seam.
set -euo pipefail
if [[ $# != 2 ]]; then
  echo "usage: $0 WORDCRAFT_GIT_REPOSITORY NEW_OUTPUT_DIRECTORY" >&2
  exit 2
fi
source_pin=668d7ce3a4b1dcf241a9c50a20754a85647a02c4
bundle_dir=$(cd -- "$(dirname -- "$0")" && pwd -P)
source_repo=$(realpath -- "$1")
output_dir=$(realpath -m -- "$2")
if [[ -e "$output_dir" || -L "$output_dir" ]]; then
  echo "output must be a new directory" >&2
  exit 2
fi
actual=$(git --no-replace-objects -C "$source_repo" rev-parse --verify "$source_pin^{commit}")
[[ "$actual" == "$source_pin" ]]
(cd -- "$bundle_dir" && sha256sum --check patch.sha256)
mkdir -- "$output_dir"
# git archive ignores the caller's working files, branch and index.
git --no-replace-objects -C "$source_repo" archive --format=tar "$source_pin" > "$output_dir/.source.tar"
(cd -- "$output_dir" && sha256sum --check "$bundle_dir/source-archive.sha256")
tar -xf "$output_dir/.source.tar" -C "$output_dir"
rm -- "$output_dir/.source.tar"
(cd -- "$output_dir" && sha256sum --check "$bundle_dir/source-before.sha256")
# Do not discover or use an enclosing repository/index when output is nested.
(
  unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_COMMON_DIR
  export GIT_CEILING_DIRECTORIES
  GIT_CEILING_DIRECTORIES=$(dirname -- "$output_dir")
  git -C "$output_dir" apply --check "$bundle_dir/source.patch"
  git -C "$output_dir" apply "$bundle_dir/source.patch"
)
(cd -- "$output_dir" && sha256sum --check "$bundle_dir/source-after.sha256")
printf 'Prepared dormant native source at %s\n' "$output_dir"
printf 'Run cargo xtask ci with the documented test inputs before building a release.\n'
