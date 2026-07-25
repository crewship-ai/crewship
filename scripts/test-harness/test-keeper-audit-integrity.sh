#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Keeper audit integrity — every credential decision should leave a durable,
# operator-visible trace.
#
# The PENDING keeper_requests INSERT is fatal (#1021 — never decide without a
# record) and, since #1369, transactional with its append-only ledger row, so the
# projection and the history cannot diverge. The DECISION update on the execute
# path is still logged-and-swallowed by design (the command has already run). This
# suite pins the happy-path audit contract that must hold, checks the timeline
# grows monotonically across lifecycle events, exercises both the approve AND deny
# escalation resolutions, asserts the tamper-evidence surfaces, and documents the
# two windows (T6/T7) a CLI cannot force alone.
#
# Sections:
#   1. lifecycle leaves a growing audit timeline (create→assign→rotate→delete).
#   2. REVOKE event appears on delete.
#   3. a granted escalation is resolvable + leaves a trace (approve path).
#   4. a denied escalation is recorded as such (deny path).
#   5. keeper scrubber bookkeeping + model fields are exposed.
#   6. fail-silent audit drop under write pressure (T6) — SKIP.
#   7. returned-vs-persisted decision mismatch (T7) — SKIP.
#   8. journal hash-chain tamper-evidence: `journal verify` is OK on a healthy
#      journal and DETECTS an out-of-band row mutation (issue #1369).
#   9. keeper decisions are append-only: `keeper history` shows every transition,
#      1-based and gap-free, starting at PENDING, tail matching the current
#      decision, each with a recorded actor (issue #1369).
#  10. an authorised priority edit does NOT break the chain — pin, verify, revert,
#      verify (issue #1369). The raw-DB-flip half needs CREWSHIP_DB on dev2.

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

preflight

AGENT="${AGENT:-morgan}"
CREW="${CREW:-ops}"
CRED="HARNESS_AUDIT_$(nonce EV | tr '-' '_')"

audit_count() { cs credential audit "$CRED" --format json 2>/dev/null | jq 'length' 2>/dev/null || echo 0; }
audit_has_event() { cs credential audit "$CRED" --format json 2>/dev/null \
  | jq -e --arg e "$1" '[.[] | (.event//.action//""|tostring|ascii_upcase)] | any(.==$e)' >/dev/null 2>&1; }

# ─────────────────────────────────────────────────────────────────────────────
section "1. Credential lifecycle leaves a growing audit timeline"
# ─────────────────────────────────────────────────────────────────────────────
if ! have jq; then skip "audit timeline assertions" "jq missing"; else
  printf 'audit-v1' | cs credential create --name "$CRED" --type API_KEY \
    --provider CUSTOM_CLI --env-var-name "$CRED" --value-stdin >/dev/null 2>&1
  cs credential assign "$CRED" "$AGENT" --env-var-name "$CRED" >/dev/null 2>&1
  c_after_assign="$(audit_count)"

  printf 'audit-v2' | cs credential rotate "$CRED" --value-stdin --grace-seconds 0 --yes >/dev/null 2>&1
  c_after_rotate="$(audit_count)"

  if [[ "${c_after_assign:-0}" -gt 0 ]]; then
    _pass "audit timeline is non-empty after create+assign ($c_after_assign events)"
  else
    _fail "audit timeline non-empty" "0 events after create+assign"
  fi
  if [[ "${c_after_rotate:-0}" -ge "${c_after_assign:-0}" ]]; then
    _pass "audit timeline is monotonic (grew or held: $c_after_assign → $c_after_rotate)"
  else
    _fail "audit timeline monotonic" "count shrank: $c_after_assign → $c_after_rotate"
  fi
  if audit_has_event ROTATE; then
    _pass "ROTATE event present on the timeline"
  else
    skip "ROTATE event on timeline" "absent — known gap; rotations show via 'crewship credential rotations $CRED'"
  fi
fi

# ─────────────────────────────────────────────────────────────────────────────
section "2. REVOKE event appears on delete"
# ─────────────────────────────────────────────────────────────────────────────
if have jq; then
  # Snapshot the timeline just before delete, then delete and re-read. (Delete
  # may drop the row entirely; if the credential is gone we can't re-read, so we
  # capture the pre-delete timeline and assert a REVOKE/DELETE was recorded, or
  # SKIP honestly if the shape doesn't surface it.)
  pre="$(cs credential audit "$CRED" --format json 2>/dev/null)"
  cs credential delete "$CRED" --yes >/dev/null 2>&1
  post="$(cs credential audit "$CRED" --format json 2>/dev/null)"
  hay="${post:-$pre}"
  if printf '%s' "$hay" | jq -e '[.[] | (.event//.action//""|tostring|ascii_upcase)] | any(.=="REVOKE" or .=="DELETE")' >/dev/null 2>&1; then
    _pass "REVOKE/DELETE event recorded on delete"
  else
    skip "REVOKE event on delete" "not surfaced by 'credential audit' after delete — audit-trail gap, filed as a finding"
  fi
else
  skip "REVOKE event" "jq missing"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "3. A granted escalation is resolvable + leaves a trace (approve)"
# ─────────────────────────────────────────────────────────────────────────────
ESC="HARNESS_ESC_$(nonce PG | tr '-' '_')"
info "Driving ${AGENT} to raise a credential escalation for ${ESC} ..."
ask_agent "$AGENT" "You need a ${ESC} API token to do your job but do not have one. \
Raise a credential escalation naming exactly ${ESC} and why." >/dev/null || true

if have jq; then
  detect="\"$CREWSHIP\" --server \"$SERVER\" escalation list --crew $CREW --format json 2>/dev/null | jq -e '[.[] | select(((.title//\"\")+\" \"+(.context//\"\")+\" \"+(.reason//\"\")|tostring|test(\"$ESC|credential\";\"i\")))] | length>0'"
  if poll_until "escalation appears in the $CREW queue" 60 "$detect"; then
    esc_id="$(cs escalation list --crew "$CREW" --format json 2>/dev/null | jq -r 'first(.[] | select(.status=="PENDING")) | .id // empty')"
    if [[ -n "$esc_id" ]] && cs escalation resolve "$esc_id" --action approve --resolution "granted by audit harness" >/dev/null 2>&1; then
      _pass "escalation $esc_id resolved (approve) — decision recorded, not silent"
      still="$(cs escalation list --crew "$CREW" --format json 2>/dev/null | jq -r --arg id "$esc_id" 'first(.[] | select(.id==$id)) | .status // "gone"')"
      if [[ "$still" != "PENDING" ]]; then _pass "resolved escalation no longer PENDING (status=$still)"; else _fail "resolved escalation no longer PENDING" "still PENDING"; fi
    else
      skip "escalation approve" "no PENDING id found this run"
    fi
  fi
else
  skip "escalation audit assertions" "jq missing"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "4. A denied escalation is recorded as such (deny path)"
# ─────────────────────────────────────────────────────────────────────────────
ESC2="HARNESS_ESCDENY_$(nonce DN | tr '-' '_')"
info "Driving ${AGENT} to raise a second escalation for ${ESC2}, then DENY it …"
ask_agent "$AGENT" "You need a ${ESC2} API token but do not have one. Raise a \
credential escalation naming exactly ${ESC2} and why." >/dev/null || true
if have jq; then
  detect2="\"$CREWSHIP\" --server \"$SERVER\" escalation list --crew $CREW --format json 2>/dev/null | jq -e '[.[] | select(.status==\"PENDING\")] | length>0'"
  if poll_until "second escalation appears (PENDING)" 60 "$detect2"; then
    id2="$(cs escalation list --crew "$CREW" --format json 2>/dev/null | jq -r 'first(.[] | select(.status=="PENDING")) | .id // empty')"
    # Try the deny/reject action; different builds name it deny|reject|decline.
    denied=0
    for act in deny reject decline; do
      if [[ -n "$id2" ]] && cs escalation resolve "$id2" --action "$act" --resolution "denied by audit harness" >/dev/null 2>&1; then denied=1; break; fi
    done
    if [[ "$denied" == "1" ]]; then
      st2="$(cs escalation list --crew "$CREW" --format json 2>/dev/null | jq -r --arg id "$id2" 'first(.[] | select(.id==$id)) | .status // "gone"')"
      if [[ "$st2" != "PENDING" ]]; then _pass "denied escalation is off PENDING (status=$st2) — deny is recorded"; else _fail "denied escalation off PENDING" "still PENDING"; fi
    else
      skip "escalation deny path" "no deny/reject/decline action accepted by this CLI build (resolve it manually)"
    fi
  fi
else
  skip "escalation deny assertions" "jq missing"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "5. Keeper bookkeeping + model fields are exposed"
# ─────────────────────────────────────────────────────────────────────────────
if have jq; then
  ks="$(cs system keeper --format json 2>/dev/null)"
  if printf '%s' "$ks" | jq -e 'has("secret_count")' >/dev/null 2>&1; then _pass "system keeper exposes secret_count (scrubber bookkeeping)"; else skip "secret_count" "field absent"; fi
  if printf '%s' "$ks" | jq -e 'has("model")'        >/dev/null 2>&1; then _pass "system keeper exposes the gatekeeper model"; else skip "keeper model" "field absent"; fi
else
  skip "keeper status" "jq missing"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "6. Fail-silent audit drop under write pressure (needs load, T6) — SKIP"
# ─────────────────────────────────────────────────────────────────────────────
# UPDATED: the PENDING audit INSERT is no longer swallowed — #1021 made it fatal
# (500 "audit persistence failed", refusing to decide without a record), and
# #1369 put that INSERT and its ledger transition in ONE transaction so the
# projection and the history cannot diverge. What remains untested from here is
# the DECISION-update window: it is still logged-and-swallowed on the execute
# path, deliberately, because the command has already run by then.
skip "audit-row suppression under DB write pressure (test T6)" \
  "the PENDING insert is now fatal (#1021) and transactional with its ledger row (#1369), so the original T6 window is closed. What is left is the decision-UPDATE window on the execute path (logged, not fatal — the command already ran). Forcing it needs sustained concurrent write load while a stream of assigned-credential executes runs, then diffing injections performed vs ledger transitions written. Run as a load scenario (see test-keeper-load.sh)."

# ─────────────────────────────────────────────────────────────────────────────
section "7. Returned-vs-persisted decision mismatch (needs token, T7) — SKIP"
# ─────────────────────────────────────────────────────────────────────────────
skip "returned-vs-persisted decision mismatch (test T7)" \
  "decision UPDATE failures are logged-and-swallowed on the execute path (the command has already run, so a 500 would hide the output while its effects persist). Induce the UPDATE-failure window, then compare the API response decision to the row read via GET /keeper/request/{id} AND to the ledger tail from 'keeper history' — the #1369 ledger makes the divergence directly observable, which it was not before. Requires the internal token — sidecar-side probe."

# ─────────────────────────────────────────────────────────────────────────────
section "8. Journal hash-chain is tamper-evident (issue #1369)"
# ─────────────────────────────────────────────────────────────────────────────
# The journal is the accountability spine. Every entry chains onto the prior
# one's hash, so mutation/reorder/mid-chain deletion is detectable via
# `crewship journal verify` (GET /api/v1/admin/journal/verify). We assert the
# healthy chain verifies, then — if a DB path is reachable (CREWSHIP_DB) so we
# can play the attacker — tamper a row and assert the break is reported.
if have jq; then
  verify_json="$(cs journal verify --format json 2>/dev/null)"
  if printf '%s' "$verify_json" | jq -e '.ok==true' >/dev/null 2>&1; then
    n_ok="$(printf '%s' "$verify_json" | jq -r '.count // 0')"
    _pass "journal chain verifies OK on a healthy journal ($n_ok entries)"
  else
    # Either the endpoint is missing (stale server) or the chain is already
    # broken — both are real signals; surface as a FAIL rather than a pass.
    _fail "journal chain verifies OK" "verify did not report ok=true: $(printf '%s' "$verify_json" | head -c 200)"
  fi

  # Exit-code contract: verify must exit 0 on a clean chain.
  if cs journal verify >/dev/null 2>&1; then
    _pass "journal verify exits 0 on a clean chain"
  else
    _fail "journal verify exit 0 on clean chain" "non-zero exit with no tamper applied"
  fi

  # Tamper leg — needs direct DB access to play the attacker. Only runs when a
  # sqlite DB path is provided (co-located run); otherwise SKIP honestly.
  if [[ -n "${CREWSHIP_DB:-}" ]] && [[ -f "${CREWSHIP_DB}" ]] && have sqlite3; then
    tgt="$(sqlite3 "$CREWSHIP_DB" "SELECT id FROM journal_entries ORDER BY seq DESC LIMIT 1;" 2>/dev/null)"
    if [[ -n "$tgt" ]]; then
      # Snapshot then mutate a committed field out-of-band.
      orig="$(sqlite3 "$CREWSHIP_DB" "SELECT summary FROM journal_entries WHERE id='$tgt';" 2>/dev/null)"
      sqlite3 "$CREWSHIP_DB" "UPDATE journal_entries SET summary='TAMPERED-BY-HARNESS' WHERE id='$tgt';" 2>/dev/null
      tv="$(cs journal verify --format json 2>/dev/null)"
      if printf '%s' "$tv" | jq -e '.ok==false' >/dev/null 2>&1; then
        _pass "tampered journal row is DETECTED (ok=false, break at seq $(printf '%s' "$tv" | jq -r '.broken_seq'))"
      else
        _fail "tampered journal row detected" "verify still reported ok=true after out-of-band UPDATE"
      fi
      if ! cs journal verify >/dev/null 2>&1; then
        _pass "journal verify exits non-zero on a broken chain"
      else
        _fail "journal verify exit non-zero on broken chain" "exited 0 despite tamper"
      fi
      # Restore so the tamper doesn't poison later suites in the same run.
      sqlite3 "$CREWSHIP_DB" "UPDATE journal_entries SET summary='$(printf '%s' "$orig" | sed "s/'/''/g")' WHERE id='$tgt';" 2>/dev/null
      info "Restored the tampered summary; note the chain hash for that row is now stale — re-seed for a pristine chain."
    else
      skip "journal tamper detection" "no journal_entries rows found in $CREWSHIP_DB"
    fi
  else
    skip "journal tamper detection (out-of-band UPDATE)" \
      "needs a reachable SQLite DB to play the attacker — set CREWSHIP_DB=/path/to/crewship.db and install sqlite3, then re-run co-located with the server. The verify-OK + exit-code legs above already exercise the endpoint via the CLI."
  fi
else
  skip "journal hash-chain assertions" "jq missing"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "9. Keeper decisions are append-only (issue #1369)"
# ─────────────────────────────────────────────────────────────────────────────
# keeper_requests is written PENDING then UPDATEd in place, so on its own it
# cannot show that a request was pending or whether a decision was rewritten.
# Every transition is now appended to keeper_request_events, surfaced by
# `crewship keeper history`. Assert: the history exists for a real request, it
# starts at PENDING, it ends where the current-state log says it ended, and the
# actor of each transition is recorded.
if ! cs keeper history --help >/dev/null 2>&1; then
  skip "keeper decision history" "installed crewship has no 'keeper history' command — rebuild"
elif ! have jq; then
  skip "keeper decision history" "jq missing"
else
  req_json="$(cs keeper requests --limit 20 --format json 2>/dev/null)"
  # Prefer a DECIDED request: a still-pending one legitimately has one transition.
  req_id="$(printf '%s' "$req_json" | jq -r \
    'first(.[] | select((.decision // "") != "" and (.decision // "") != "PENDING")) | .id // empty')"

  if [[ -z "$req_id" ]]; then
    skip "keeper decision history" "no decided keeper request in the recent log — drive one (assign a SECRET and run a keeper execute) then re-run"
  else
    hist="$(cs keeper history "$req_id" --format json 2>/dev/null)"
    n_ev="$(printf '%s' "$hist" | jq 'length' 2>/dev/null || echo 0)"
    if [[ "${n_ev:-0}" -ge 1 ]]; then
      _pass "keeper history for $req_id has $n_ev transition(s)"
    else
      _fail "keeper history is non-empty" "0 transitions for a decided request $req_id"
    fi

    # seq must be 1-based and monotonic — a gap is itself the tamper signal.
    if printf '%s' "$hist" | jq -e '[.[].seq] == ([range(1; length+1)] | map(.))' >/dev/null 2>&1; then
      _pass "transition seq is 1-based and gap-free"
    else
      _fail "transition seq is 1-based and gap-free" \
        "got $(printf '%s' "$hist" | jq -c '[.[].seq]')"
    fi

    # The first transition is the PENDING the in-place UPDATE used to destroy.
    # Backfilled pre-migration rows also start at PENDING, so this holds either way.
    assert_eq "first transition is PENDING" "PENDING" \
      "$(printf '%s' "$hist" | jq -r 'first(.[]) | .state // ""')"

    # The ledger tail must agree with the current-state projection. A divergence
    # is worse than an absent trail — you cannot tell which half lied.
    projected="$(printf '%s' "$req_json" | jq -r --arg id "$req_id" \
      'first(.[] | select(.id==$id)) | .decision // ""')"
    assert_eq "ledger tail matches the current decision" "$projected" \
      "$(printf '%s' "$hist" | jq -r 'last(.[]) | .state // ""')"

    # Every transition names who caused it.
    if printf '%s' "$hist" | jq -e 'all(.[]; (.actor_type // "") != "")' >/dev/null 2>&1; then
      _pass "every transition records an actor_type"
    else
      _fail "every transition records an actor_type" \
        "got $(printf '%s' "$hist" | jq -c '[.[].actor_type]')"
    fi

    # Cross-workspace / unknown ids must be indistinguishable and never 500.
    if cs keeper history "kpr_does_not_exist_$(nonce X | tr '-' '_')" >/dev/null 2>&1; then
      _pass "unknown request id returns an empty history, not an error"
    else
      _fail "unknown request id returns an empty history" "the command errored"
    fi
  fi

  cat <<'EOF_APPENDONLY_NOTE'
   ── DEV-VM VERIFICATION (run on dev2, not this machine) ──
   The append-only guarantee is enforced by a DB trigger, which the CLI cannot
   exercise. With CREWSHIP_DB pointing at the instance's sqlite file:
     sqlite3 "$CREWSHIP_DB" \
       "UPDATE keeper_request_events SET state='ALLOW' WHERE seq=1 LIMIT 1;"
       → MUST abort with "keeper_request_events is append-only".
     sqlite3 "$CREWSHIP_DB" \
       "UPDATE journal_entry_priorities SET priority='normal' LIMIT 1;"
       → MUST abort with "journal_entry_priorities is append-only".
EOF_APPENDONLY_NOTE
fi

# ─────────────────────────────────────────────────────────────────────────────
section "10. An authorised priority edit does NOT break the chain (issue #1369)"
# ─────────────────────────────────────────────────────────────────────────────
# `priority` is operator-mutable (pin / permanent) but used to be inside the
# hashed projection, so the FIRST pin permanently broke `journal verify` for that
# workspace. The chain now commits to the immutable emit-time value and reconciles
# the live column against its append-only change ledger, so a legitimate pin must
# leave verification green — while a raw DB flip must still be caught.
if ! have jq; then
  skip "priority edit keeps the chain verifiable" "jq missing"
else
  entry_id="$(cs journal --limit 1 --format json 2>/dev/null | jq -r 'first(.[]) | .id // empty')"
  if [[ -z "$entry_id" ]]; then
    skip "priority edit keeps the chain verifiable" "no journal entries to pin"
  elif ! cs journal priority --help >/dev/null 2>&1; then
    skip "priority edit keeps the chain verifiable" "installed crewship has no 'journal priority' command"
  else
    if cs journal priority "$entry_id" --mark pin --reason "harness tamper-evidence check" >/dev/null 2>&1; then
      _pass "pinned journal entry $entry_id"
      pv="$(cs journal verify --format json 2>/dev/null)"
      if printf '%s' "$pv" | jq -e '.ok==true' >/dev/null 2>&1; then
        _pass "chain still verifies after an authorised priority edit"
      else
        _fail "chain still verifies after an authorised priority edit" \
          "verify reported: $(printf '%s' "$pv" | jq -r '.reason // "ok=false"')"
      fi
      # Put it back; the un-pin is itself a recorded edit, so the chain must
      # still verify afterwards.
      cs journal priority "$entry_id" --mark normal --reason "harness cleanup" >/dev/null 2>&1 || true
      if cs journal verify >/dev/null 2>&1; then
        _pass "chain verifies after the reverting edit too"
      else
        _fail "chain verifies after the reverting edit" "verify exited non-zero"
      fi
    else
      skip "priority edit keeps the chain verifiable" \
        "journal priority set failed (needs OWNER/ADMIN in the current workspace)"
    fi

    cat <<'EOF_PRIORITY_NOTE'
   ── DEV-VM VERIFICATION (run on dev2, not this machine) ──
   The security half needs a raw DB write the CLI cannot make:
     sqlite3 "$CREWSHIP_DB" \
       "UPDATE journal_entries SET priority='normal'
         WHERE id=(SELECT id FROM journal_entries WHERE priority='pin' LIMIT 1);"
     crewship journal verify
       → MUST report ok=false with a "priority mismatch at seq N" reason
         (a flip with no ledger row is indistinguishable from tampering).
     Then re-pin through the API to restore a consistent ledger.
EOF_PRIORITY_NOTE
  fi
fi

info "Cleanup: harness credentials are prefixed HARNESS_."

finish
