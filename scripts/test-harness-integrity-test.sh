#!/usr/bin/env bash
# Negative test for test-harness-integrity.sh. It proves the checker is not a
# report-only script: removing a helper must make it fail and name the suite.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d -t crewship-harness-integrity.XXXXXX)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/scripts/test-harness"
cp "$ROOT/scripts/test-harness"/test-*.sh "$TMP/scripts/test-harness/"
cp "$ROOT/scripts/test-harness/lib.sh" "$TMP/scripts/test-harness/lib.sh"
cp "$ROOT/scripts/test-harness-integrity.sh" "$TMP/scripts/test-harness-integrity.sh"

# Keep the mutation local to the synthetic tree. The current repository does
# not carry the historical test-first-projects.sh suite, so create the smallest
# representative suite here. The contract test must prove the gate catches a
# missing helper, not depend on an unrelated suite remaining in the tree.
cat >"$TMP/scripts/test-harness/test-first-projects.sh" <<'EOF'
#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
assert_ok "synthetic helper call" true
EOF
chmod +x "$TMP/scripts/test-harness/test-first-projects.sh"
sed -i.bak '/^assert_ok()[[:space:]]*{/,/^}/d' "$TMP/scripts/test-harness/lib.sh"
output="$(bash "$TMP/scripts/test-harness-integrity.sh" 2>&1)" || rc=$?
rc="${rc:-0}"

if (( rc == 0 )); then
  echo "FAIL: removing assert_ok did not fail the integrity gate" >&2
  exit 1
fi
case "$output" in
  *"test-first-projects.sh: missing command-position function: assert_ok"*) ;;
  *)
    echo "FAIL: missing-helper diagnostic did not name suite and function" >&2
    printf '%s\n' "$output" >&2
    exit 1
    ;;
esac
echo "ok: missing helper is a hard failure with suite + function name"
