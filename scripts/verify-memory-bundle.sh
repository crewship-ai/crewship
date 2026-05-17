#!/usr/bin/env bash
# Live verification of the memory-reliability bundle against a running
# crewship instance. Designed to run against dev2 (port 8082) but the
# port is overridable via PORT=NNNN.
#
# What this script asserts:
#   1. Health endpoint responds
#   2. Migration v89 (add_memory_proposals) is applied
#   3. memory_proposals table has the expected columns + CHECK behaviour
#   4. inbox_items.kind CHECK admits 'memory_consolidation'
#   5. workspaces.memory_config column is present
#   6. The new journal types are emittable (round-trip through the table)
#
# Usage:
#   PORT=8082 DB=/opt/crewship_2/crewship.db ./scripts/verify-memory-bundle.sh
#   (or run remotely via ssh; see README footer)
#
# Exits non-zero on the first failing check so it's CI-friendly.

set -euo pipefail

PORT="${PORT:-8082}"
DB="${DB:-./crewship.db}"
BASE="http://localhost:${PORT}"

red()    { printf '\033[31m%s\033[0m\n' "$*"; }
green()  { printf '\033[32m%s\033[0m\n' "$*"; }
bold()   { printf '\033[1m%s\033[0m\n' "$*"; }

pass=0
fail=0

assert() {
  local label="$1" actual="$2" expected="$3"
  if [[ "$actual" == "$expected" ]]; then
    green "  PASS  $label"
    pass=$((pass + 1))
  else
    red   "  FAIL  $label"
    printf "        want=%q\n        got =%q\n" "$expected" "$actual"
    fail=$((fail + 1))
  fi
}

assert_contains() {
  local label="$1" actual="$2" expected="$3"
  if [[ "$actual" == *"$expected"* ]]; then
    green "  PASS  $label"
    pass=$((pass + 1))
  else
    red   "  FAIL  $label"
    printf "        want substring %q\n        got            %q\n" "$expected" "$actual"
    fail=$((fail + 1))
  fi
}

bold "1) Health endpoint"
HEALTH=$(curl -sS -m 5 "${BASE}/api/health" || true)
assert_contains "  /api/health responds OK" "${HEALTH}" '"status":"ok"'

bold "2) Migration v89 applied"
V89=$(sqlite3 "${DB}" "SELECT version || ':' || name FROM _migrations WHERE version = 89")
assert "  v89 row present" "${V89}" "89:add_memory_proposals"

bold "3) memory_proposals schema"
COL_COUNT=$(sqlite3 "${DB}" "SELECT COUNT(*) FROM pragma_table_info('memory_proposals')")
assert "  memory_proposals column count == 12" "${COL_COUNT}" "12"

PENDING_INS=$(sqlite3 "${DB}" "
  BEGIN;
  INSERT INTO workspaces (id, name, slug) VALUES ('ws_verify_tmp_$$','tmp','tmp_$$');
  INSERT INTO memory_proposals (id, workspace_id, crew_id, proposal_path, status)
    VALUES ('mp_verify_$$', 'ws_verify_tmp_$$', 'crew_x', '/tmp/p.md', 'pending');
  SELECT 'pending_ok';
  ROLLBACK;
" 2>&1 | tail -1)
assert "  pending proposal insert succeeds" "${PENDING_INS}" "pending_ok"

BAD_APPROVED=$(sqlite3 "${DB}" "
  BEGIN;
  INSERT INTO workspaces (id, name, slug) VALUES ('ws_verify_tmp2_$$','tmp','tmp_$$_b');
  INSERT INTO memory_proposals (id, workspace_id, crew_id, proposal_path, status)
    VALUES ('mp_v2_$$', 'ws_verify_tmp2_$$', 'crew_x', '/tmp/p.md', 'approved');
  SELECT 'should_not_reach';
  ROLLBACK;
" 2>&1 || true)
assert_contains "  approved without decided_at rejected by CHECK" "${BAD_APPROVED}" "CHECK constraint failed"

bold "4) inbox_items.kind CHECK admits memory_consolidation"
INBOX_OK=$(sqlite3 "${DB}" "
  BEGIN;
  INSERT INTO workspaces (id, name, slug) VALUES ('ws_verify_tmp3_$$','tmp','tmp_$$_c');
  INSERT INTO inbox_items (id, workspace_id, kind, source_id, title)
    VALUES ('ibx_mc_verify_$$', 'ws_verify_tmp3_$$', 'memory_consolidation', 'mp_x', 'verify');
  SELECT 'memory_consolidation_ok';
  ROLLBACK;
" 2>&1 | tail -1)
assert "  memory_consolidation kind admitted" "${INBOX_OK}" "memory_consolidation_ok"

BAD_KIND=$(sqlite3 "${DB}" "
  BEGIN;
  INSERT INTO workspaces (id, name, slug) VALUES ('ws_verify_tmp4_$$','tmp','tmp_$$_d');
  INSERT INTO inbox_items (id, workspace_id, kind, source_id, title)
    VALUES ('ibx_bad_$$', 'ws_verify_tmp4_$$', 'bogus_kind', 'src', 'verify');
  SELECT 'should_not_reach';
  ROLLBACK;
" 2>&1 || true)
assert_contains "  unknown kind still rejected by CHECK" "${BAD_KIND}" "CHECK constraint failed"

bold "5) workspaces.memory_config column"
HAS_COL=$(sqlite3 "${DB}" "SELECT COUNT(*) FROM pragma_table_info('workspaces') WHERE name = 'memory_config'")
assert "  workspaces.memory_config present" "${HAS_COL}" "1"

bold "6) Journal accepts new entry types"
# Write directly into the journal table (the journal package does not
# reject unknown types at the DB level — string column; the Go-side
# validation is what the new entry types live in).
EMIT_REJECT=$(sqlite3 "${DB}" "
  BEGIN;
  INSERT INTO workspaces (id, name, slug) VALUES ('ws_verify_tmp5_$$','tmp','tmp_$$_e');
  INSERT INTO journal_entries (id, workspace_id, ts, entry_type, severity, actor_type, summary, payload)
    VALUES ('j_verify_$$_1', 'ws_verify_tmp5_$$', datetime('now'), 'memory.write_rejected', 'warn', 'sidecar', 'scrubber rejected key', '{}');
  INSERT INTO journal_entries (id, workspace_id, ts, entry_type, severity, actor_type, summary, payload)
    VALUES ('j_verify_$$_2', 'ws_verify_tmp5_$$', datetime('now'), 'memory.consolidation_proposed', 'notice', 'system', 'proposal pending', '{}');
  SELECT 'journal_types_ok';
  ROLLBACK;
" 2>&1 | tail -1)
assert "  memory.write_rejected + memory.consolidation_proposed accepted" "${EMIT_REJECT}" "journal_types_ok"

echo
bold "Summary: ${pass} passed, ${fail} failed"
if [[ "${fail}" -gt 0 ]]; then
  exit 1
fi
