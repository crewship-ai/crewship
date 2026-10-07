#!/usr/bin/env bash
# dev-sh-test.sh — unit tests for dev.sh helper functions.
#
# dev.sh runs under `set -euo pipefail`, which makes an innocuous-looking
# `[[ cond ]] && cmd` as a function's LAST statement fatal: when the test
# is false the list returns 1, the function returns 1, and `set -e` aborts
# the whole script at the call site. That is not theoretical — it wedged
# the dev1 slot on crewship-dev from 2026-07-16 to 2026-07-20 (see
# preserve_crash_log below), and through it the slot reconciler, which
# `set -e`s out of its own loop when a slot's dev.sh exits non-zero.
#
# Functions are extracted from dev.sh by name and eval'd rather than
# sourcing the file, because sourcing dev.sh executes its command
# dispatcher.
#
# Usage: bash scripts/dev-sh-test.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEV_SH="$SCRIPT_DIR/../dev.sh"

FAILURES=0
pass() { printf '  ok   %s\n' "$1"; }
fail() { printf '  FAIL %s\n' "$1"; FAILURES=$((FAILURES + 1)); }

# Print the source of a single `name() { ... }` block from dev.sh.
extract_fn() {
  awk -v fn="$1" '
    $0 ~ "^" fn "\\(\\) \\{" { inside = 1 }
    inside { print }
    inside && /^}/ { exit }
  ' "$DEV_SH"
}

# Run a snippet with the named dev.sh functions in scope, under the same
# shell options dev.sh itself uses. Echoes the snippet's exit status.
run_with_fn() {
  local fn="$1" snippet="$2" body
  body="$(extract_fn "$fn")"
  if [[ -z "$body" ]]; then
    echo "127"
    return
  fi
  # Fed through stdin rather than `bash -c "...$body...$snippet"`: the
  # function body comes out of dev.sh verbatim, quotes and all, and
  # interpolating it into a double-quoted command string is one stray
  # quote away from a syntax error that would look like a test failure.
  printf '%s\n%s\n%s\n' 'set -euo pipefail' "$body" "$snippet" | bash >/dev/null 2>&1
  echo "$?"
}

echo "dev.sh: preserve_crash_log"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# The wedge: on a slot whose previous run already rotated its log away,
# GO_LOG does not exist on the next start. preserve_crash_log must be a
# no-op that returns 0, not an `set -e` abort that stops the slot from
# ever starting again.
status=$(run_with_fn preserve_crash_log "preserve_crash_log '$TMP/missing.log'; echo reached")
if [[ "$status" == "0" ]]; then
  pass "missing log file: returns 0 (caller under set -e survives)"
else
  fail "missing log file: exited $status, expected 0 — set -e would abort dev.sh start"
fi

: > "$TMP/empty.log"
status=$(run_with_fn preserve_crash_log "preserve_crash_log '$TMP/empty.log'; echo reached")
if [[ "$status" == "0" ]]; then
  pass "empty log file: returns 0"
else
  fail "empty log file: exited $status, expected 0"
fi
if [[ ! -e "$TMP/empty.log.prev" ]]; then
  pass "empty log file: not rotated"
else
  fail "empty log file: rotated to .prev, expected no rotation"
fi

echo "crash reason" > "$TMP/full.log"
status=$(run_with_fn preserve_crash_log "preserve_crash_log '$TMP/full.log'")
if [[ "$status" == "0" ]]; then
  pass "non-empty log file: returns 0"
else
  fail "non-empty log file: exited $status, expected 0"
fi
if [[ -f "$TMP/full.log.prev" ]] && [[ "$(cat "$TMP/full.log.prev")" == "crash reason" ]]; then
  pass "non-empty log file: rotated to .prev with contents intact"
else
  fail "non-empty log file: not rotated to .prev"
fi
if [[ ! -e "$TMP/full.log" ]]; then
  pass "non-empty log file: original moved, not copied"
else
  fail "non-empty log file: original still present, expected mv"
fi

# guard_log_size backgrounds a watcher that outlives the call and runs for
# as long as the service does. If that watcher inherits the caller's
# stdout, it holds the write end of whatever pipe dev.sh was invoked
# through — so `ssh host ./dev.sh start` never returns, because ssh waits
# for EOF that a 30-second-sleep loop will not deliver until the server
# dies. That is exactly why scripts/deploy-dev.sh appeared to hang for
# 37 minutes after printing "Deploy complete", and why the reconciler's
# own log accumulated a pair of long-lived writers on every slot restart.
echo "dev.sh: guard_log_size does not hold the caller's stdout"

FIFO="$TMP/fifo"
mkfifo "$FIFO"

# A long-lived owner so the watch loop stays alive, mimicking a running server.
sleep 60 &
OWNER=$!

GUARD_BODY="$(extract_fn guard_log_size)"
# Body and paths go in as positional arguments, not interpolated into the
# -c string: the body is dev.sh source verbatim, quotes and backticks
# included, and one stray quote would turn a passing test into a syntax
# error that reads like a failure. Same reasoning as run_with_fn above.
(
  bash -c 'set -euo pipefail
S=""
warn() { echo "[warn] $*"; }
eval "$1"
guard_log_size "$2" "$3"
exit 0' _ "$GUARD_BODY" "$TMP/guarded.log" "$OWNER" >"$FIFO" 2>&1
) &
WRITER=$!

# cat returns only when EVERY writer has closed the pipe. If the watcher
# inherited stdout, the fd stays open and this blocks until the timeout —
# which is the deploy hang, reproduced.
if timeout 6 cat "$FIFO" >/dev/null 2>&1; then
  pass "pipe closes when dev.sh returns (ssh would exit)"
else
  fail "pipe still held after dev.sh returned — ssh/deploy-dev.sh would hang"
fi

kill "$OWNER" 2>/dev/null || true
kill "$WRITER" 2>/dev/null || true
wait "$OWNER" 2>/dev/null || true
wait "$WRITER" 2>/dev/null || true

# generate_env_local rewrites .env.local on every start. Every key it emits
# must also be one it strips, or each start appends another copy: clone 3
# reached 100+ CREWSHIP_ALLOWED_ORIGINS lines and as many banners by
# 2026-09-12. Run it twice over the same file and count.
echo "dev.sh: generate_env_local is idempotent"
ENV_DIR="$(mktemp -d)"
printf 'NEXTAUTH_SECRET=keep-me\n' > "$ENV_DIR/.env.local"
env_snippet="
PROJECT_DIR='$ENV_DIR'; S='-9'; GO_PORT=8089; NEXT_PORT=3019
SOCKET_PATH=/tmp/x.sock; CONTAINER_NETWORK=net; DATA_DIR=/tmp/d; LOG_PATH=/tmp/l; STATE_DIR=/tmp/s
NC=''; CYAN=''; YELLOW=''; GREEN=''; RED=''
log() { :; }; ok() { :; }; warn() { :; }
generate_env_local; generate_env_local; generate_env_local
"
run_with_fn generate_env_local "$env_snippet" >/dev/null
origins=$(grep -c '^CREWSHIP_ALLOWED_ORIGINS=' "$ENV_DIR/.env.local" || true)
banners=$(grep -c '^# --- Auto-generated by dev.sh' "$ENV_DIR/.env.local" || true)
if [[ "$origins" == 1 && "$banners" == 1 ]] && grep -q '^NEXTAUTH_SECRET=keep-me$' "$ENV_DIR/.env.local"; then
  pass "three runs leave one CREWSHIP_ALLOWED_ORIGINS line, one banner, and the user's own keys"
else
  fail "after three runs: $origins origins line(s), $banners banner(s) — every emitted key must be a managed key"
fi
rm -rf "$ENV_DIR"

echo "dev.sh: environment precedence and installation paths"
PATH_TEST_DIR="$(mktemp -d)"
cat > "$PATH_TEST_DIR/defaults.env" <<'ENV'
export CREWSHIP_HOME="/should-not-win"
CREWSHIP_PORT=9000
EMPTY_OVERRIDE=from-file
SHELL_DEFAULT="quoted value"
DERIVED_DEFAULT="${SHELL_DEFAULT}/suffix"
ENV
loader_snippet="
export CREWSHIP_HOME='$PATH_TEST_DIR/installation' CREWSHIP_PORT=8082 EMPTY_OVERRIDE=''
load_env_local '$PATH_TEST_DIR/defaults.env'
[[ \"\$CREWSHIP_HOME\" == '$PATH_TEST_DIR/installation' ]]
[[ \"\$CREWSHIP_PORT\" == 8082 && \"\$EMPTY_OVERRIDE\" == '' ]]
[[ \"\$SHELL_DEFAULT\" == 'quoted value' && \"\$DERIVED_DEFAULT\" == 'quoted value/suffix' ]]
"
status=$(run_with_fn load_env_local "$loader_snippet")
if [[ "$status" == 0 ]]; then pass "exported values and empty values win; shell quoting and expansion work"; else fail "environment loader exited $status"; fi
paths_snippet="
unset CREWSHIP_HOME CREWSHIP_DATA_DIR CREWSHIP_STORAGE_BASE_PATH CREWSHIP_LOG_PATH CREWSHIP_BOLT_PATH CREWSHIP_PAGE_PROJECTS_PATH CREWSHIP_SOCKET_PATH DATABASE_URL XDG_RUNTIME_DIR CREWSHIP_PORT XDG_DATA_HOME
HOME='$PATH_TEST_DIR/user'; PROJECT_DIR='$PATH_TEST_DIR/checkout-a'; GO_PORT=8082
resolve_dev_paths
first=\$DEV_HOME
[[ \"\$DATA_DIR\" == \"\$first/data\" && \"\$DATABASE_URL\" == \"file:\$first/db/crewship.db\" ]]
[[ \"\$GO_PID_FILE\" == \"\$first/run/go.pid\" && \"\$DEV_BINARY\" == \"\$first/run/bin/crewship\" ]]
unset CREWSHIP_HOME CREWSHIP_DATA_DIR CREWSHIP_STORAGE_BASE_PATH CREWSHIP_LOG_PATH CREWSHIP_BOLT_PATH CREWSHIP_PAGE_PROJECTS_PATH CREWSHIP_SOCKET_PATH DATABASE_URL
PROJECT_DIR='$PATH_TEST_DIR/checkout-b'
resolve_dev_paths
[[ \"\$DEV_HOME\" != \"\$first\" ]]
export CREWSHIP_HOME='$PATH_TEST_DIR/explicit' CREWSHIP_DATA_DIR='$PATH_TEST_DIR/explicit'
export CREWSHIP_STORAGE_BASE_PATH='$PATH_TEST_DIR/override' XDG_RUNTIME_DIR='$PATH_TEST_DIR/runtime'
resolve_dev_paths
[[ \"\$DATA_DIR\" == '$PATH_TEST_DIR/override' && \"\$GO_PID_FILE\" == '$PATH_TEST_DIR/runtime/'* ]]
cd /
resolve_dev_paths
[[ \"\$DEV_HOME\" == '$PATH_TEST_DIR/explicit' ]]
"
status=$(run_with_fn resolve_dev_paths "$paths_snippet")
if [[ "$status" == 0 ]]; then pass "durable checkout isolation, explicit override, runtime dir and cwd independence"; else fail "path resolver exited $status"; fi
mkdir -p "$PATH_TEST_DIR/checkout/child" "$PATH_TEST_DIR/data"
ln -s "$PATH_TEST_DIR/checkout" "$PATH_TEST_DIR/link"
cleanup_snippet="
PROJECT_DIR='$PATH_TEST_DIR/checkout'
! validate_dev_cleanup_dir '$PATH_TEST_DIR'
! validate_dev_cleanup_dir '$PATH_TEST_DIR/checkout/child'
! validate_dev_cleanup_dir '$PATH_TEST_DIR/link'
validate_dev_cleanup_dir '$PATH_TEST_DIR/data'
"
status=$(run_with_fn validate_dev_cleanup_dir "$cleanup_snippet")
if [[ "$status" == 0 ]]; then pass "cleanup rejects checkout, parent and symlink paths"; else fail "cleanup guards exited $status"; fi
mkdir -p "$PATH_TEST_DIR/legacy/crewship-test-data"
printf 'keep' > "$PATH_TEST_DIR/legacy/crewship-test-data/output"
legacy_snippet="
S=-test; DATA_DIR='$PATH_TEST_DIR/new/data'; STATE_DIR='$PATH_TEST_DIR/new/state'; PAGE_PROJECTS_DIR='$PATH_TEST_DIR/new/pages'
! check_legacy_dev_paths '$PATH_TEST_DIR/legacy'
[[ -f '$PATH_TEST_DIR/legacy/crewship-test-data/output' ]]
DATA_DIR='$PATH_TEST_DIR/legacy/crewship-test-data'
check_legacy_dev_paths '$PATH_TEST_DIR/legacy'
"
status=$(run_with_fn check_legacy_dev_paths "$legacy_snippet")
if [[ "$status" == 0 ]]; then pass "legacy data blocks silent path change and remains untouched"; else fail "legacy migration guard exited $status"; fi
mkdir -p "$PATH_TEST_DIR/proc/$$"
printf '%s' "$$" > "$PATH_TEST_DIR/old.pid"
ln -s /tmp/crewship-test-dev "$PATH_TEST_DIR/proc/$$/exe"
ln -s "$PATH_TEST_DIR/checkout" "$PATH_TEST_DIR/proc/$$/cwd"
pid_snippet="
S=-test; PROJECT_DIR='$PATH_TEST_DIR/checkout'
legacy_dev_server_running '$PATH_TEST_DIR/proc' '$PATH_TEST_DIR/old.pid'
rm '$PATH_TEST_DIR/proc/$$/exe'
ln -s /usr/bin/unrelated '$PATH_TEST_DIR/proc/$$/exe'
! legacy_dev_server_running '$PATH_TEST_DIR/proc' '$PATH_TEST_DIR/old.pid'
"
status=$(run_with_fn legacy_dev_server_running "$pid_snippet")
if [[ "$status" == 0 ]]; then pass "legacy PID must match executable and checkout; reused PIDs ignored"; else fail "legacy PID checks exited $status"; fi
invalid_paths_snippet="
PROJECT_DIR='$PATH_TEST_DIR/checkout'; GO_PORT=8082
export CREWSHIP_HOME='$PATH_TEST_DIR/explicit/' CREWSHIP_DATA_DIR='$PATH_TEST_DIR/explicit'
unset CREWSHIP_SOCKET_PATH CREWSHIP_STORAGE_BASE_PATH CREWSHIP_LOG_PATH CREWSHIP_BOLT_PATH CREWSHIP_PAGE_PROJECTS_PATH DATABASE_URL
export XDG_RUNTIME_DIR='$PATH_TEST_DIR/runtime'
resolve_dev_paths
[[ \"\$DEV_HOME\" == '$PATH_TEST_DIR/explicit' ]]
export XDG_RUNTIME_DIR=relative
! resolve_dev_paths
export XDG_RUNTIME_DIR='$PATH_TEST_DIR/runtime'
export CREWSHIP_SOCKET_PATH='/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.sock'
! resolve_dev_paths
"
status=$(run_with_fn resolve_dev_paths "$invalid_paths_snippet")
if [[ "$status" == 0 ]]; then pass "root aliases normalize trailing slash; relative runtime and long sockets rejected"; else fail "runtime validation exited $status"; fi
docker_paths_snippet="
PROJECT_DIR='$PATH_TEST_DIR/checkout'; GO_PORT=8082
unset CREWSHIP_DATA_DIR CREWSHIP_CONTAINER_PREFIX CREWSHIP_CONTAINER_NETWORK XDG_RUNTIME_DIR CREWSHIP_SOCKET_PATH CREWSHIP_STORAGE_BASE_PATH CREWSHIP_LOG_PATH CREWSHIP_BOLT_PATH CREWSHIP_PAGE_PROJECTS_PATH DATABASE_URL
export CREWSHIP_HOME='$PATH_TEST_DIR/installation-a'
resolve_dev_paths
prefix_a=\$CREWSHIP_CONTAINER_PREFIX; network_a=\$CONTAINER_NETWORK
unset CREWSHIP_DATA_DIR CREWSHIP_CONTAINER_PREFIX CREWSHIP_CONTAINER_NETWORK
export CREWSHIP_HOME='$PATH_TEST_DIR/installation-b'
resolve_dev_paths
[[ \"\$CREWSHIP_CONTAINER_PREFIX\" != \"\$prefix_a\" && \"\$CONTAINER_NETWORK\" != \"\$network_a\" ]]
unset CREWSHIP_DATA_DIR CREWSHIP_CONTAINER_PREFIX CREWSHIP_CONTAINER_NETWORK
export CREWSHIP_HOME='$PATH_TEST_DIR/installation-a'
resolve_dev_paths
[[ \"\$CREWSHIP_CONTAINER_PREFIX\" == \"\$prefix_a\" && \"\$CONTAINER_NETWORK\" == \"\$network_a\" ]]
export CREWSHIP_CONTAINER_PREFIX=explicit-prefix CREWSHIP_CONTAINER_NETWORK=explicit-network
resolve_dev_paths
[[ \"\$CREWSHIP_CONTAINER_PREFIX\" == explicit-prefix && \"\$CONTAINER_NETWORK\" == explicit-network ]]
export CREWSHIP_CONTAINER_PREFIX=''
! resolve_dev_paths
"
status=$(run_with_fn resolve_dev_paths "$docker_paths_snippet")
if [[ "$status" == 0 ]]; then pass "Docker prefixes and networks isolate installation roots, remain stable and honor explicit values"; else fail "Docker resource isolation exited $status"; fi
runtime_isolation_snippet="
PROJECT_DIR='$PATH_TEST_DIR/checkout'; GO_PORT=8082
unset CREWSHIP_DATA_DIR CREWSHIP_CONTAINER_PREFIX CREWSHIP_CONTAINER_NETWORK CREWSHIP_SOCKET_PATH CREWSHIP_STORAGE_BASE_PATH CREWSHIP_LOG_PATH CREWSHIP_BOLT_PATH CREWSHIP_PAGE_PROJECTS_PATH DATABASE_URL
export XDG_RUNTIME_DIR='$PATH_TEST_DIR/runtime' CREWSHIP_HOME='$PATH_TEST_DIR/a'
resolve_dev_paths
pid_a=\$GO_PID_FILE; socket_a=\$SOCKET_PATH
unset CREWSHIP_DATA_DIR CREWSHIP_SOCKET_PATH CREWSHIP_CONTAINER_PREFIX CREWSHIP_CONTAINER_NETWORK
export CREWSHIP_HOME='$PATH_TEST_DIR/b'
resolve_dev_paths
[[ \"\$GO_PID_FILE\" != \"\$pid_a\" && \"\$SOCKET_PATH\" != \"\$socket_a\" ]]
export CREWSHIP_STORAGE_BASE_PATH=relative-data CREWSHIP_LOG_PATH=relative-logs CREWSHIP_BOLT_PATH=relative-state/state.db CREWSHIP_PAGE_PROJECTS_PATH=relative-pages CREWSHIP_SOCKET_PATH=relative.sock CREWSHIP_STORAGE_MEMORY_ROOT=relative-memory
cd /
resolve_dev_paths
[[ \"\$DATA_DIR\" == '$PATH_TEST_DIR/checkout/relative-data' && \"\$LOG_PATH\" == '$PATH_TEST_DIR/checkout/relative-logs' ]]
[[ \"\$BOLT_PATH\" == '$PATH_TEST_DIR/checkout/relative-state/state.db' && \"\$PAGE_PROJECTS_DIR\" == '$PATH_TEST_DIR/checkout/relative-pages' ]]
[[ \"\$SOCKET_PATH\" == '$PATH_TEST_DIR/checkout/relative.sock' && \"\$CREWSHIP_STORAGE_MEMORY_ROOT\" == '$PATH_TEST_DIR/checkout/relative-memory' ]]
"
status=$(run_with_fn resolve_dev_paths "$runtime_isolation_snippet")
if [[ "$status" == 0 ]]; then pass "XDG runtime isolates homes; relative overrides anchor to startup checkout from any cwd"; else fail "runtime isolation and relative cleanup paths exited $status"; fi
rm -rf "$PATH_TEST_DIR"

# deploy_staleness is the sentence behind `status`'s STALE line. It has to
# fire for both ways a slot serves old code and stay quiet otherwise.
echo "dev.sh: deploy_staleness"
STALE_DIR="$(mktemp -d)"
git -C "$STALE_DIR" init -q && git -C "$STALE_DIR" -c user.email=t@t -c user.name=t commit -q --allow-empty -m one
HEAD1=$(git -C "$STALE_DIR" rev-parse HEAD)
stale_snippet="deploy_staleness '$STALE_DIR' '$STALE_DIR/bin'"
# Extracted once and required non-empty: these cases pipe the body straight
# into bash rather than through run_with_fn, so a renamed function would
# define nothing, print nothing, and let the first case (which asserts
# silence) pass for a function that no longer exists.
STALE_FN="$(extract_fn deploy_staleness)"
if [[ -z "$STALE_FN" ]]; then fail "deploy_staleness could not be extracted from dev.sh"; fi
# Fresh: marker at HEAD, binary newer than the commit.
printf '%s\n' "$HEAD1" > "$STALE_DIR/.web-build-marker"
touch -d '+1 minute' "$STALE_DIR/bin"
out=$(printf '%s\n%s\n%s\n' 'set -euo pipefail' "$STALE_FN" "$stale_snippet" | bash 2>/dev/null)
if [[ -z "$out" ]]; then pass "quiet when web/out and the binary match HEAD"; else fail "fresh slot reported: $out"; fi
# Binary older than the commit it should be running.
touch -d '-1 hour' "$STALE_DIR/bin"
out=$(printf '%s\n%s\n%s\n' 'set -euo pipefail' "$STALE_FN" "$stale_snippet" | bash 2>/dev/null)
if [[ "$out" == *"built before HEAD was committed"* ]]; then pass "names a binary older than HEAD's commit"; else fail "old binary not reported: '$out'"; fi
# Marker behind HEAD after a new commit.
touch -d '+1 minute' "$STALE_DIR/bin"
git -C "$STALE_DIR" -c user.email=t@t -c user.name=t commit -q --allow-empty -m two
out=$(printf '%s\n%s\n%s\n' 'set -euo pipefail' "$STALE_FN" "$stale_snippet" | bash 2>/dev/null)
if [[ "$out" == *"web/out/ was built from ${HEAD1:0:8}"* ]]; then pass "names the HEAD web/out was built from when the repo moved on"; else fail "stale marker not reported: '$out'"; fi
# Validation 2026-09-13: after the file on disk is replaced the process keeps
# running the old inode; /proc/<pid>/exe then reads "<path> (deleted)" and a
# `-f` test on that string finds nothing. The check has to look at the link.
printf '%s\n' "$(git -C "$STALE_DIR" rev-parse HEAD)" > "$STALE_DIR/.web-build-marker"
# rm first: the earlier steps left a non-executable stand-in at this path,
# and cp onto it would keep that mode and the exec would fail silently.
rm -f "$STALE_DIR/bin"
cp "$(command -v sleep)" "$STALE_DIR/bin"
"$STALE_DIR/bin" 30 &
RUNNER=$!
sleep 0.2
cp "$(command -v sleep)" "$STALE_DIR/bin.new" && mv -f "$STALE_DIR/bin.new" "$STALE_DIR/bin"
out=$(printf '%s\n%s\n%s\n' 'set -euo pipefail' "$STALE_FN" "deploy_staleness '$STALE_DIR' '/proc/$RUNNER/exe'" | bash 2>/dev/null)
if [[ "$out" == *"replaced on disk"* ]]; then pass "names a running binary whose file was replaced (proc exe reads deleted)"; else fail "replaced binary not reported: '$out'"; fi
kill "$RUNNER" 2>/dev/null || true
wait "$RUNNER" 2>/dev/null || true
rm -rf "$STALE_DIR"

# Guard the whole class of bug, not just the one instance of it.
echo "dev.sh: no function ends in a bare && list"
offenders="$(awk '
  /^[a-zA-Z_][a-zA-Z0-9_]*\(\) \{/ { fn = $1; last = ""; next }
  /^}/ { if (fn != "" && last ~ /&&/ && last !~ /\|\|/) print fn " -> " last; fn = ""; next }
  fn != "" && $0 !~ /^[[:space:]]*(#|$)/ { last = $0 }
' "$DEV_SH")"
if [[ -z "$offenders" ]]; then
  pass "no function returns the status of a trailing && list"
else
  fail "function(s) end in a bare && list — non-zero return aborts callers under set -e:"
  printf '       %s\n' "$offenders"
fi

echo ""
if [[ "$FAILURES" -eq 0 ]]; then
  echo "dev.sh tests passed"
else
  echo "dev.sh tests: $FAILURES failure(s)"
  exit 1
fi
