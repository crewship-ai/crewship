#!/usr/bin/env bash
# gen-frontend-licenses-test.sh — regression tests for the frontend license
# generator against a synthetic .pnpm store (S1 review):
#   - scoped (@scope/name) and unscoped packages both collected
#   - peer-variant store entries deduplicated
#   - unreadable package.json FAILS (not a silent layout skip)
#   - missing license without an exception FAILS
#   - the real-repo run inventory is sanity-checked for known scoped deps
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GEN="$REPO_ROOT/scripts/gen-frontend-licenses.mjs"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

write_stub_pnpm() { # write_stub_pnpm <expected-json-body-file>
  mkdir -p "$TMP/bin"
  {
    printf '%s\n' '#!/usr/bin/env bash'
    printf '%s\n' '[ "$1 $2 $3" = "licenses list --prod" ] || { echo "stub pnpm: unexpected args" >&2; exit 1; }'
    printf '%s\n' "cat \"$1\""
  } > "$TMP/bin/pnpm"
  chmod +x "$TMP/bin/pnpm"
}

JSON_OK="$TMP/expected-ok.json"
cat > "$JSON_OK" <<'J'
{"MIT": [
  {"name": "plain-demo", "versions": ["1.0.0"]},
  {"name": "@example/scoped-demo", "versions": ["2.0.0"]},
  {"name": "peer-demo", "versions": ["3.0.0"]}
]}
J
JSON_BROKEN="$TMP/expected-broken.json"
cat > "$JSON_BROKEN" <<'J'
{"MIT": [{"name": "broken", "versions": ["1.0.0"]}]}
J
JSON_NOLIC="$TMP/expected-nolic.json"
cat > "$JSON_NOLIC" <<'J'
{"MIT": [{"name": "nolic", "versions": ["1.0.0"]}]}
J

mkpkg() { # mkpkg <store-entry> <pkg-path> <name> <version> <license-content|->
  local dir="$TMP/node_modules/.pnpm/$1/node_modules/$2"
  mkdir -p "$dir"
  if [ "$5" != "-" ]; then printf '%s' "$5" > "$dir/LICENSE"; fi
  printf '{"name": "%s", "version": "%s"}' "$3" "$4" > "$dir/package.json"
}

# ── case 1: scoped + unscoped + peer dedup, with manifest ──────────────────
mkpkg "plain-demo@1.0.0" "plain-demo" "plain-demo" "1.0.0" "plain license"
mkpkg "@example+scoped-demo@2.0.0" "@example/scoped-demo" "@example/scoped-demo" "2.0.0" "scoped license"
mkpkg "peer-demo@3.0.0" "peer-demo" "peer-demo" "3.0.0" "peer license"
mkpkg "peer-demo@3.0.0_react@19" "peer-demo" "peer-demo" "3.0.0" "peer license"
write_stub_pnpm "$JSON_OK"
OUT="$TMP/out1"
( cd "$TMP" && PATH="$TMP/bin:$PATH" node "$GEN" "$OUT" ) > "$TMP/log1" 2>&1

for want in "plain-demo" "@example/scoped-demo"; do
  grep -q -F "\"name\": \"$want\"" "$OUT/npm-licenses.json" || {
    echo "TEST FAIL: $want missing from inventory (S1 scoped walk)" >&2; cat "$TMP/log1" >&2; exit 1; }
done
n="$(grep -c -F '"name": "peer-demo"' "$OUT/npm-licenses.json")"
[ "$n" -eq 1 ] || { echo "TEST FAIL: peer-demo appears $n times (dedup)" >&2; exit 1; }
[ -f "$OUT/texts/@example/scoped-demo@2.0.0/LICENSE" ] || {
  echo "TEST FAIL: scoped text not written" >&2; exit 1; }
[ -s "$OUT/manifest.tsv" ] || { echo "TEST FAIL: manifest empty" >&2; exit 1; }
grep -q -F '@example/scoped-demo' "$OUT/manifest.tsv" || {
  echo "TEST FAIL: scoped package missing from manifest" >&2; exit 1; }
echo "ok  scoped + unscoped + peer-dedup collection with manifest"

# ── case 2: unreadable package.json fails ──────────────────────────────────
mkpkg "broken@1.0.0" "broken" "broken" "1.0.0" "x"
echo '{ not json' > "$TMP/node_modules/.pnpm/broken@1.0.0/node_modules/broken/package.json"
write_stub_pnpm "$JSON_BROKEN"
if ( cd "$TMP" && PATH="$TMP/bin:$PATH" node "$GEN" "$TMP/out2" ) > "$TMP/log2" 2>&1; then
  echo "TEST FAIL: unreadable package.json was silently accepted" >&2; exit 1
fi
echo "ok  unreadable package.json fails"

# ── case 3: missing license without an exception fails ─────────────────────
rm -rf "$TMP/node_modules"
mkpkg "nolic@1.0.0" "nolic" "nolic" "1.0.0" "-"
write_stub_pnpm "$JSON_NOLIC"
if ( cd "$TMP" && PATH="$TMP/bin:$PATH" node "$GEN" "$TMP/out3" ) > "$TMP/log3" 2>&1; then
  echo "TEST FAIL: missing license text without exception passed" >&2; exit 1
fi
echo "ok  missing license text without exception fails"

# ── case 4: real-repo sanity for the actually installed scoped deps ────────
if [ -d "$REPO_ROOT/node_modules/.pnpm" ] && command -v pnpm >/dev/null 2>&1; then
  OUT="$TMP/real"
  ( cd "$REPO_ROOT" && node "$GEN" "$OUT" ) > "$TMP/log4" 2>&1
  for want in "@radix-ui/" "@sentry/" "@tanstack/"; do
    grep -q -F "\"name\": \"$want" "$OUT/npm-licenses.json" || {
      echo "TEST FAIL: real inventory missing $want* packages" >&2; exit 1; }
  done
  echo "ok  real repo inventory contains @radix-ui/@sentry/@tanstack packages"
else
  echo "ok  real-repo inventory check skipped (no node_modules)"
fi

echo "gen-frontend-licenses-test: all regressions pass"
