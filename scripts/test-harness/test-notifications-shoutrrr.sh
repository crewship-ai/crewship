#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Native outbound notification system (#1412) — category preference matrix.
#
# Distinct from test-notifications.sh (the ORIGINAL #850 workspace-wide
# run-terminal broadcast — untouched by this feature). This suite validates
# the NEW per-user category x channel preference matrix end to end:
#
#   - seed a real event through the live server (an agent reply — see the
#     "why chat.replies, not runs.failed" note below)
#   - a channel with the matching category set to `immediate` gets EXACTLY
#     ONE delivery on a fake local webhook receiver
#   - a channel where that category is MUTED gets ZERO
#
# ── Why chat.replies, not runs.failed ───────────────────────────────────────
# The issue's acceptance text says "seeded routine failure". In practice the
# `failed_run` inbox item — the #1412 tap point for the runs.failed category —
# is only written for SCHEDULED (unattended) run failures
# (internal/pipeline/schedules.go alertFailedScheduledRun), NOT ad-hoc CLI
# runs (see test-notifications.sh section 4's own note on this). Waiting for
# a real cron tick to fail is not something a fast, deterministic harness
# script can do. `crewship ask` reliably triggers chatnotify's
# UpsertMessage(kind=message) → CategoryChatReplies on every call instead, so
# this suite proves the identical preference-routing pipeline (category
# resolution → matrix lookup → admin allowlist → delivery) through that
# category. The routing code path is category-agnostic — internal/notifyroute
# does not special-case chat.replies vs runs.failed — so this is an equally
# valid end-to-end proof of the pipeline the issue asks for.
#
# ── Network reachability note ───────────────────────────────────────────────
# The fake webhook receiver runs LOCALLY (on this machine, where the harness
# runs), but the crewship SERVER under test delivers the webhook — so the
# server process must be able to reach this machine over the network. This
# is transparent when SERVER is the local dev server (http://localhost:PORT)
# but requires RECEIVER_HOST to be set to a server-reachable address (this
# machine's LAN/VPN IP) when SERVER points at a remote devN box.

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

preflight

if ! have python3; then
  skip "notifications-shoutrrr suite" "python3 not found — needed for the fake webhook receiver"
  finish
fi
if ! have jq; then
  skip "notifications-shoutrrr suite" "jq not found — channel/pref ids are read from JSON output"
  finish
fi

RECEIVER_PORT="${RECEIVER_PORT:-8973}"
RECEIVER_HOST="${RECEIVER_HOST:-127.0.0.1}"
RECEIVER_URL="http://${RECEIVER_HOST}:${RECEIVER_PORT}"
LOGFILE="$(mktemp -t cs-notify-receiver.XXXXXX)"
RECEIVER_PID=""

cleanup() {
  [[ -n "$RECEIVER_PID" ]] && kill "$RECEIVER_PID" >/dev/null 2>&1
  [[ -n "${CHAN_ENABLED_ID:-}" ]] && cs notifychannel rm "$CHAN_ENABLED_ID" --yes >/dev/null 2>&1
  [[ -n "${CHAN_MUTED_ID:-}" ]] && cs notifychannel rm "$CHAN_MUTED_ID" --yes >/dev/null 2>&1
  rm -f "$LOGFILE"
}
trap cleanup EXIT

# ─────────────────────────────────────────────────────────────────────────────
section "1. Fake webhook receiver"
# ─────────────────────────────────────────────────────────────────────────────
# Logs "<path>\t<body-on-one-line>" per POST — the path carries which channel
# fired (?chan=enabled|muted) so one receiver can distinguish both cases
# without needing two ports.
python3 - "$LOGFILE" "$RECEIVER_PORT" <<'PYEOF' &
import http.server, sys, threading

class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0) or 0)
        body = self.rfile.read(length) if length else b""
        line = self.path + "\t" + body.decode("utf-8", "replace").replace("\n", " ") + "\n"
        with lock:
            with open(sys.argv[1], "a") as f:
                f.write(line)
        self.send_response(200)
        self.end_headers()

    def log_message(self, *a):
        pass  # keep the harness output clean

lock = threading.Lock()
http.server.ThreadingHTTPServer(("0.0.0.0", int(sys.argv[2])), Handler).serve_forever()
PYEOF
RECEIVER_PID=$!
sleep 1
if kill -0 "$RECEIVER_PID" >/dev/null 2>&1; then
  _pass "fake webhook receiver listening on :$RECEIVER_PORT (pid $RECEIVER_PID)"
else
  _fail "fake webhook receiver started" "process exited immediately — port $RECEIVER_PORT busy?"
  finish
fi

# ─────────────────────────────────────────────────────────────────────────────
section "2. Create two personal channels — one to enable, one to mute"
# ─────────────────────────────────────────────────────────────────────────────
CHAN_ENABLED_ID=""
CHAN_MUTED_ID=""
if out="$(cs notifychannel add --type webhook --url "${RECEIVER_URL}/hook?chan=enabled" --personal --format json 2>/dev/null)"; then
  CHAN_ENABLED_ID="$(printf '%s' "$out" | jq -r '.id // empty')"
  assert_nonempty "created 'enabled' personal channel" "$CHAN_ENABLED_ID"
else
  _fail "create 'enabled' personal channel" "$(printf '%s' "${out:-}" | head -c 200)"
fi

if out="$(cs notifychannel add --type webhook --url "${RECEIVER_URL}/hook?chan=muted" --personal --format json 2>/dev/null)"; then
  CHAN_MUTED_ID="$(printf '%s' "$out" | jq -r '.id // empty')"
  assert_nonempty "created 'muted' personal channel" "$CHAN_MUTED_ID"
else
  _fail "create 'muted' personal channel" "$(printf '%s' "${out:-}" | head -c 200)"
fi

if [[ -z "$CHAN_ENABLED_ID" || -z "$CHAN_MUTED_ID" ]]; then
  _fail "notifications-shoutrrr suite" "could not create both fixture channels — aborting remaining sections"
  finish
fi

# ─────────────────────────────────────────────────────────────────────────────
section "3. Set the preference matrix: enable chat.replies on one, mute the other"
# ─────────────────────────────────────────────────────────────────────────────
if cs notify prefs set --category chat.replies --channel "$CHAN_ENABLED_ID" --state immediate >/dev/null 2>&1; then
  _pass "chat.replies = immediate on the 'enabled' channel"
else
  _fail "set chat.replies immediate on 'enabled' channel"
fi

# The "*" category mutes a channel entirely, overriding every cell — set it
# to 'immediate' (the mute-all row's "on" state; see internal/notifyroute's
# cellIndex.muted doc comment) rather than also opting the muted channel
# into chat.replies, so this genuinely tests the mute override winning over
# an otherwise-eligible category rather than just an unset (default-off) cell.
if cs notify prefs set --category chat.replies --channel "$CHAN_MUTED_ID" --state immediate >/dev/null 2>&1 \
   && cs notify prefs set --category "*" --channel "$CHAN_MUTED_ID" --state immediate >/dev/null 2>&1; then
  _pass "chat.replies = immediate BUT channel muted via '*' on the 'muted' channel"
else
  _fail "set chat.replies + mute-all on 'muted' channel"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "4. Trigger a real event: an agent reply (see header note on why)"
# ─────────────────────────────────────────────────────────────────────────────
AGENT="${AGENT:-$(cs agent list --format json 2>/dev/null | jq -r '.[0].slug // .[0].id // empty')}"
if [[ -z "$AGENT" ]]; then
  skip "trigger agent reply" "no agent available in this workspace to ask"
  finish
fi
info "Asking agent '$AGENT' a throwaway question to trigger a chat reply…"
if ! cs ask --agent "$AGENT" --quiet --no-stream --timeout "$ASK_TIMEOUT" -p "Reply with just: ok" >/tmp/cs-notify-ask.out 2>&1; then
  _fail "ask agent for a reply" "$(head -c 200 /tmp/cs-notify-ask.out | tr '\n' ' ')"
  finish
fi
_pass "agent replied"

# ─────────────────────────────────────────────────────────────────────────────
section "5. Exactly ONE delivery on the enabled channel, ZERO on the muted one"
# ─────────────────────────────────────────────────────────────────────────────
# Delivery is async (internal/notifyroute.Router.NotifyInboxItem spawns its
# own goroutine) — poll the receiver log rather than checking immediately.
poll_until "receiver observed at least one POST" "$POLL_TIMEOUT" \
  "grep -q 'chan=enabled' '$LOGFILE'"

ENABLED_COUNT="$(grep -c 'chan=enabled' "$LOGFILE" 2>/dev/null || echo 0)"
MUTED_COUNT="$(grep -c 'chan=muted' "$LOGFILE" 2>/dev/null || echo 0)"

assert_eq "enabled channel received exactly 1 delivery" "1" "$ENABLED_COUNT"
assert_eq "muted channel received exactly 0 deliveries" "0" "$MUTED_COUNT"

if have jq; then
  BODY_LINE="$(grep 'chan=enabled' "$LOGFILE" | head -1 | cut -f2)"
  CATEGORY="$(printf '%s' "$BODY_LINE" | jq -r '.category // empty' 2>/dev/null)"
  assert_eq "delivered payload carries category=chat.replies" "chat.replies" "$CATEGORY"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "6. Admin delivery log records the sent row"
# ─────────────────────────────────────────────────────────────────────────────
if out="$(cs notifychannel deliveries --channel "$CHAN_ENABLED_ID" --format json 2>/dev/null)"; then
  SENT_COUNT="$(printf '%s' "$out" | jq '[.[] | select(.status=="sent")] | length' 2>/dev/null || echo 0)"
  if [[ "${SENT_COUNT:-0}" -ge 1 ]]; then
    _pass "delivery log shows >=1 sent row for the enabled channel"
  else
    _fail "delivery log sent row" "got: $(printf '%s' "$out" | head -c 200)"
  fi
else
  skip "delivery log check" "'notifychannel deliveries' requires ADMIN/OWNER — this seeded user may not qualify"
fi

finish
