#!/usr/bin/env bash
# Pages — the acceptance suite, driving the REAL CLI against a running server.
#
# Issue #1937 (Pages slice 2: HTTP + CLI write path), epic #1935.
# Spec: docs/prd/pages.md §11 (API and CLI surface), §7 (permissions),
# §4 (the freshness contract), §10 / §10b.3 (caps).
#
# ─── Registration ───────────────────────────────────────────────────────────
#
# This suite is GREEN and registered: run-all.sh runs it behind WITH_PAGES, and
# pr-subset.sh runs it on every PR — it is credential-free and fast, with no
# provider call anywhere below.
#
# The guard at the top still checks that `crewship page` is registered at all,
# and reports a single loud FAIL naming that reason rather than cascading twenty
# confusing ones. It dates from when the command did not yet exist; it is worth
# keeping, because "the subcommand vanished" and "the write path regressed" are
# different diagnoses and should not look alike.
#
# It covers ONE of the four producer doors — the CLI/host one. The routine,
# sidecar and webhook doors are unit-tested in Go and have no coverage here.
#
# ─── Why this exists alongside cmd/crewship/cmd_page_test.go ────────────────
#
# The Go acceptance test drives the same command tree, but against a stub
# server. It can prove the CLI never *sends* provenance, and that it renders
# what the server returns. It cannot prove the server *attaches* provenance, or
# that the 64 KiB cap is real, or that a panel actually goes stale when its SLA
# elapses — those are runtime properties of the handler, and this is where they
# get tested. Per CLAUDE.md the acceptance test drives the CLI binary; that is
# this file.
#
# Usage:
#   export CREWSHIP_SERVER=<devN url>
#   ./scripts/test-harness/test-pages.sh
#
# shellcheck source=./lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

preflight

if ! have jq; then
  skip "pages assertions need jq for JSON field access"
  finish
fi

# ── The red guard ───────────────────────────────────────────────────────────
#
# One check, one failure, no cascade.
#
# The probe is `crewship commands` — the local command manifest, no endpoint
# touched, so a miss here means "not registered", not "the server is unwell"
# (preflight already proved the server answers). It is NOT `page --help`: an
# unknown subcommand plus --help makes Cobra print the ROOT help and exit 0, so
# that probe would pass while the command does not exist. Verified against the
# built binary before this line was written.
# Captured first, then matched — NOT `crewship commands | grep -q`.
#
# lib.sh sets `pipefail`, and `grep -q` exits at its first match, closing the
# pipe. If the writer has not finished, it takes SIGPIPE (141) and the pipeline
# reports failure even though the match SUCCEEDED — so the probe would say "not
# registered" about a command that is registered.
#
# Whether that fires is a race against the 64 KiB pipe buffer: while the whole
# listing fitted, the writer finished before grep closed the pipe and nobody
# noticed. `crewship commands` is now 58 KiB and `page` sits at line 491 of 778,
# so the margin is under 2 KiB and any new command can push it over. It did, in
# CI, on every run — while passing on every developer machine, which is the
# worst possible shape for a guard.
_page_commands="$("$CREWSHIP" commands 2>/dev/null || true)"
if ! grep -qE '^page( |$)' <<<"$_page_commands"; then
  _fail "crewship page is registered" \
    "RED BY DESIGN (issue #1937): the CLI has no 'page' command yet. PRD §11 requires one command per endpoint: page list|get|create|update|delete|set. GREEN = cmd/crewship/cmd_page.go registers them and every assertion below runs."
  finish
fi

# ── Fixture ─────────────────────────────────────────────────────────────────

SLUG="pages-$(nonce ACC | tr '[:upper:]' '[:lower:]')"
PANEL="sluzby"
PRODUCER="script/watch-services.sh"
# 5s so the staleness assertion can wait it out inside a test run. Production
# specs use 30s (PRD §6); the contract under test is "past SLA ⇒ stale", and
# the number is the knob, not the contract.
SLA="5s"
SPEC_FILE="$(mktemp -t "crewship-page-XXXXXX.yaml")"
PAGE_CREATED=0

cleanup() {
  rm -f "$SPEC_FILE"
  if (( PAGE_CREATED == 1 )); then
    cs page delete "$SLUG" --yes >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

# The owning crew has to EXIST before the page can name it. That is not a
# fixture convenience: page_panels.owner_crew_id is NOT NULL and a foreign key
# (§10), because the panel's owner is its ACL and "no crew" would read as
# "visible to everyone" — so the authoring gate (§10b.1) refuses a spec whose
# owner does not resolve, and a slot that has never seen `lookout` would fail
# at create with a message about the crew rather than about anything this suite
# is testing.
#
# Created rather than discovered so the assertions below can stay literal
# ("crew/lookout"), and left behind on purpose: a crew is a shared workspace
# object and deleting one a later run (or a human) is using is worse than one
# spare row.
if ! cs crew list --format json 2>/dev/null | jq -e '.[] | select(.slug == "lookout")' >/dev/null 2>&1; then
  cs crew create --name "Lookout" --slug lookout >/dev/null 2>&1 || true
fi

# PRD §6 layer 1 — the page definition a human authors, in YAML.
cat > "$SPEC_FILE" <<YAML
apiVersion: crewship/v1
kind: Page
metadata:
  name: Harness ${SLUG}
  slug: ${SLUG}
spec:
  panels:
    - id: ${PANEL}
      schema: status.v1
      title: Jede to?
      owner: crew/lookout
      producer: ${PRODUCER}
      sla: ${SLA}
      span: 8
YAML

# page_json — the page as JSON, or "{}" when the CLI cannot produce it. Like
# test-inbox.sh, a transport failure is recorded as a FAIL rather than being
# laundered into an empty document that every later assertion would skip on.
page_json() {
  local out rc
  out="$(cs page get "$SLUG" -f json 2>&1)"; rc=$?
  if (( rc != 0 )); then
    _fail "page get $SLUG" "CLI exited $rc: $(printf '%s' "$out" | head -c 200)"
    printf '{}'
    return 1
  fi
  printf '%s' "$out"
}

# panel_field <jq-path> — one field off the panel under test. The jq walk is
# shape-tolerant on purpose: PRD §10 fixes the COLUMNS (producer_run_id,
# produced_at, state) but §11 does not fix the JSON envelope, so the panel may
# carry provenance flat or nested under .provenance.
panel_field() {
  page_json | jq -r --arg id "$PANEL" --arg path "$1" '
    [.. | objects | select((.id? // .panel_id?) == $id)] | first as $p
    | ($p | getpath($path | split("."))? ) // empty
  ' 2>/dev/null
}

# ── 1. create → get round-trips the spec (PRD §11) ──────────────────────────

section "create → get round-trips the spec"

assert_ok "page create --file" cs page create --file "$SPEC_FILE"
PAGE_CREATED=1

assert_eq "get returns the slug it was created with" "$SLUG" \
  "$(page_json | jq -r '.. | objects | .slug? // empty' | head -1)"

# PRD §3: the schema vocabulary is CLOSED — a schema the round-trip drops or
# rewrites is a schema the renderer cannot dispatch on.
assert_eq "panel schema survives the round-trip" "status.v1" "$(panel_field 'schema')"

# PRD §7.1 rule 2: "panel.owner_crew_id is not decoration; it is the ACL."
assert_eq "panel owner survives the round-trip" "crew/lookout" "$(panel_field 'owner')"

# PRD §7.1 rule 4: producer authority is separate from viewer authority.
assert_eq "panel producer survives the round-trip" "$PRODUCER" "$(panel_field 'producer')"

# PRD §4 rule 1: "Every panel declares sla. A panel without one does not
# validate. There is no default that means 'never mind'." The wire may carry
# "5s" or sla_seconds=5 — §11 does not say which — so accept either shape, but
# never its absence.
SLA_SEEN="$(panel_field 'sla')"
[[ -z "$SLA_SEEN" ]] && SLA_SEEN="$(panel_field 'sla_seconds')"
assert_nonempty "panel carries an SLA (§4 rule 1: mandatory)" "$SLA_SEEN"

# ── 2. list shows it (PRD §11, GET /api/v1/pages) ───────────────────────────

section "list"

assert_contains "page list shows the created page" "$(cs page list 2>&1)" "$SLUG"

# ── 3. set reads JSON on stdin (PRD §11, the single write path) ─────────────

section "set — stdin is the write path"

PAYLOAD='{"items":[{"name":"api","state":"ok","label":"200 OK"}]}'
SET_OUT="$(printf '%s' "$PAYLOAD" | cs page set "$SLUG/$PANEL" --data - 2>&1)"; SET_RC=$?
if (( SET_RC == 0 )); then
  _pass "page set <slug>/<panel> --data - accepts JSON on stdin"
else
  _fail "page set <slug>/<panel> --data - accepts JSON on stdin" \
    "exit $SET_RC: $(printf '%s' "$SET_OUT" | head -c 200)"
fi

assert_contains "the payload read from stdin is what the panel stores" \
  "$(page_json)" '200 OK'

# ── 4. provenance is the SERVER's (PRD §4 rule 5, §7.1b) ────────────────────
#
# "Every panel footer carries provenance: producer, run id, timestamp.
#  Server-attached, not producer-claimed."
#
# Nothing in the push above supplied a run id or a timestamp — the payload is
# four fields of status.v1 data and no more. So anything the read-back shows
# for those was written by the server, which is precisely the assertion.

section "provenance — attached server-side, never claimed"

PROV_PRODUCER="$(panel_field 'provenance.producer')"
[[ -z "$PROV_PRODUCER" ]] && PROV_PRODUCER="$(panel_field 'producer')"
PROV_RUN="$(panel_field 'provenance.run_id')"
[[ -z "$PROV_RUN" ]] && PROV_RUN="$(panel_field 'producer_run_id')"
PROV_AT="$(panel_field 'provenance.produced_at')"
[[ -z "$PROV_AT" ]] && PROV_AT="$(panel_field 'produced_at')"

assert_eq "provenance names the declared producer" "$PRODUCER" "$PROV_PRODUCER"
assert_nonempty "provenance carries a run id the client never sent" "$PROV_RUN"
assert_nonempty "provenance carries a timestamp the client never sent" "$PROV_AT"

# And a human sees them: a footer nobody can read is not provenance.
assert_contains "page get shows the producer in the panel footer" \
  "$(cs page get "$SLUG" 2>&1)" "$PRODUCER"

# ── 5. the 64 KiB cap (PRD §10b.3, §10) ─────────────────────────────────────
#
# "Payload size | 64 KiB | enforced in Go at the handler" — and issue #1937:
# "a payload over the cap returns 422 with a rejection envelope, not a 500".
# 422 maps to CLI exit code 2 (ExitValidation, internal/cli/errors.go:9-22);
# a 5xx would map to 7 (ExitServer), which is the failure this assertion is
# hunting for.

section "cap — 64 KiB, refused not exploded"

BIG_BLOB="$(head -c 70000 /dev/zero | tr '\0' 'x')"
BIG_OUT="$(printf '{"blob":"%s"}' "$BIG_BLOB" | cs page set "$SLUG/$PANEL" --data - 2>&1)"; BIG_RC=$?

if (( BIG_RC == 0 )); then
  _fail "an over-cap payload is refused" "the CLI accepted ~70 KiB; §10b.3 caps a panel payload at 64 KiB"
else
  _pass "an over-cap payload is refused"
fi
assert_eq "over-cap is a validation failure (exit 2), not a server error (exit 7)" "2" "$BIG_RC"
assert_not_contains "over-cap never reports as a 500" "$BIG_OUT" "500"
assert_not_contains "over-cap does not dump the raw rejection envelope" "$BIG_OUT" '"rejected"'
assert_contains "over-cap says what the limit is" "$BIG_OUT" "64 KiB"

# The refused push must not have replaced the good one.
assert_contains "a refused push leaves the last good payload in place" "$(page_json)" '200 OK'

# ── 6. freshness (PRD §4) ───────────────────────────────────────────────────
#
# rule 2: three states, "computed server-side, never by the producer".
# rule 3: "Stale renders degraded … age shown in absolute terms … It never
#          renders as if it were current."

section "freshness — past the SLA is stale, and the CLI says so"

assert_eq "a just-pushed panel is fresh" "fresh" "$(panel_field 'state')"

info "waiting out the ${SLA} SLA…"
sleep 8

assert_eq "a panel past its SLA is stale (§4 rule 2)" "stale" "$(panel_field 'state')"
STALE_OUT="$(cs page get "$SLUG" 2>&1)"
assert_contains "page get says 'stale' in words (§4 rule 3)" "$STALE_OUT" "stale"
assert_not_contains "staleness is absolute, not 'a while ago' (§4 rule 3)" "$STALE_OUT" "a while ago"

# ── 7. delete, and a clean miss afterwards (PRD §11) ────────────────────────

section "delete"

assert_ok "page delete <slug>" cs page delete "$SLUG" --yes
PAGE_CREATED=0

GONE_OUT="$(cs page get "$SLUG" 2>&1)"; GONE_RC=$?
if (( GONE_RC == 0 )); then
  _fail "page get after delete fails" "the CLI still returned the page"
else
  _pass "page get after delete fails"
fi
assert_eq "a deleted page is exit 3 (ExitNotFound), not a generic error" "3" "$GONE_RC"
assert_contains "the not-found error names the page" "$GONE_OUT" "$SLUG"
assert_not_contains "a deleted page renders nothing at all" "$GONE_OUT" "Jede to?"

finish
