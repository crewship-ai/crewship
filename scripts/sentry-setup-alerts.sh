#!/usr/bin/env bash
#
# Idempotent setup of the two Sentry issue-alert rules we want for the
# Crewship beta:
#
#   1. "New issue (beta)"      — fires on every newly-grouped event in the
#                                 `beta` environment. Used as the firehose
#                                 for first ~2 weeks of beta — we want to
#                                 see everything testers hit.
#   2. "Spike — 50+/hour"      — fires when one issue is seen >50 times in
#                                 a 1-hour window. Catches runaway loops
#                                 that would otherwise eat the free-tier
#                                 quota in minutes.
#
# Both deliver via email to the project's issue owners (= you, until you
# add teammates). The script first DELETES any rule that matches our two
# names so re-running this overwrites cleanly instead of stacking
# duplicates.
#
# The default rule Sentry creates during onboarding ("Send a notification
# for new issues") is also removed so you don't get double-notifications.
#
# Requirements:
#   - SENTRY_AUTH_TOKEN env var, OR a populated ~/.sentryclirc (sentry-cli's
#     own config — auto-read as a fallback). Create the token at:
#     https://sentry.io/settings/account/api/auth-tokens/
#     Scopes needed: project:read, project:write, alerts:write
#     (org:read NOT required — actions target IssueOwners so no
#     /members/me/ lookup is performed)
#   - jq, curl
#
# Usage:
#   export SENTRY_AUTH_TOKEN=sntrys_xxx...
#   ./scripts/sentry-setup-alerts.sh
#
# Override defaults via env:
#   SENTRY_ORG=...        (default: unify-7b)
#   SENTRY_PROJECT=...    (default: crewship-backend)
#   SENTRY_HOST=...       (default: sentry.io — set for self-hosted)

set -euo pipefail

# ---------- config ----------
SENTRY_ORG="${SENTRY_ORG:-unify-7b}"
SENTRY_PROJECT="${SENTRY_PROJECT:-crewship-backend}"
SENTRY_HOST="${SENTRY_HOST:-sentry.io}"
API="https://${SENTRY_HOST}/api/0"

# Names of rules this script manages. Anything matching is wiped before
# re-create — that's what makes the script idempotent. Don't put a rule
# name here that you maintain by hand.
RULES_OWNED=(
  "New issue (beta) — Crewship"
  "Spike — 50+ events / hour"
  "Send a notification for new issues"   # Sentry's onboarding default
  "Send a notification for high priority issues"  # alt onboarding default
)

# ---------- preflight ----------
# Fallback: pull the token from ~/.sentryclirc if SENTRY_AUTH_TOKEN isn't
# already set. sentry-cli stores tokens in INI form under [auth] → token=...
# so awk reads the first `token = ...` line, strips whitespace, and exports.
# Skipped silently when no rc file exists; the empty-token check below still
# catches the no-credentials case.
if [ -z "${SENTRY_AUTH_TOKEN:-}" ] && [ -f "${HOME}/.sentryclirc" ]; then
  SENTRY_AUTH_TOKEN=$(awk -F'=' '
    /^[[:space:]]*token[[:space:]]*=/ {
      sub(/^[[:space:]]*token[[:space:]]*=[[:space:]]*/, "")
      print
      exit
    }' "${HOME}/.sentryclirc")
  if [ -n "$SENTRY_AUTH_TOKEN" ]; then
    echo "==> using token from ~/.sentryclirc"
  fi
fi

if [ -z "${SENTRY_AUTH_TOKEN:-}" ]; then
  cat >&2 <<EOF
SENTRY_AUTH_TOKEN is not set and ~/.sentryclirc has no token.

Create one at https://${SENTRY_HOST}/settings/account/api/auth-tokens/
with scopes: project:read project:write alerts:write

Then:
  export SENTRY_AUTH_TOKEN=sntrys_...
  $0
EOF
  exit 1
fi

for cmd in curl jq; do
  command -v "$cmd" >/dev/null || { echo "missing: $cmd" >&2; exit 1; }
done

# Single curl wrapper — auth header, JSON content type, captures status
# code so we can surface the response body on 4xx/5xx. Plain
# --fail-with-body suppresses the response on success which we want,
# but on error it eats the stderr trail; this wrapper unifies both.
sentry_api() {
  local tmp
  tmp=$(mktemp)
  local status
  status=$(curl --silent --show-error \
    -o "$tmp" \
    -w '%{http_code}' \
    -H "Authorization: Bearer ${SENTRY_AUTH_TOKEN}" \
    -H "Content-Type: application/json" \
    "$@")
  if [ "$status" -ge 400 ]; then
    echo "sentry API ${status}:" >&2
    cat "$tmp" >&2
    echo >&2
    rm -f "$tmp"
    return 1
  fi
  cat "$tmp"
  rm -f "$tmp"
}

echo "==> target: ${SENTRY_ORG}/${SENTRY_PROJECT} on ${SENTRY_HOST}"

# ---------- check environment availability ----------
# Sentry environments are created implicitly on the first event tagged
# with that environment — there is no explicit "create environment"
# API call. If we put environment:"beta" on a rule before any beta
# event has ever been ingested, the API rejects with
# "This environment has not been created."
#
# So: peek at the live env list and use the filter only when the env
# exists. Once the first beta release fires a real event, re-running
# this script will start scoping the rule.
echo "==> checking which environments exist..."
ENV_JSON=$(sentry_api "${API}/projects/${SENTRY_ORG}/${SENTRY_PROJECT}/environments/" || echo '[]')
HAS_BETA=$(echo "$ENV_JSON" | jq -r '[.[] | select(.name == "beta")] | length')
if [ "$HAS_BETA" = "1" ]; then
  echo "    'beta' env exists — New-issue rule will be scoped to it"
  BETA_ENV_LINE='"environment": "beta",'
else
  echo "    'beta' env does not exist yet (first beta event creates it) — rule will fire on all envs"
  BETA_ENV_LINE=''
fi

# ---------- 1. list + delete any rules we own ----------
# We target NotifyEmailAction at "IssueOwners" rather than a specific
# Member id. Why: explicit-member targeting needs an org-member lookup
# (/members/me/), which requires org:read scope on the auth token. By
# routing through IssueOwners + fallthroughType=AllMembers we deliver
# to every org member (= just you for a solo setup, and the natural
# behaviour for a team later). Same delivery semantics, one fewer scope
# the operator has to grant.
echo "==> listing existing rules..."
EXISTING=$(sentry_api "${API}/projects/${SENTRY_ORG}/${SENTRY_PROJECT}/rules/")
echo "    found $(echo "$EXISTING" | jq 'length') rule(s)"

for name in "${RULES_OWNED[@]}"; do
  # jq's --arg avoids shell-quote injection on rule names with special chars.
  IDS=$(echo "$EXISTING" | jq -r --arg n "$name" '.[] | select(.name == $n) | .id')
  if [ -z "$IDS" ]; then
    continue
  fi
  while IFS= read -r id; do
    [ -z "$id" ] && continue
    echo "    deleting existing rule \"$name\" (id=$id)"
    sentry_api -X DELETE \
      "${API}/projects/${SENTRY_ORG}/${SENTRY_PROJECT}/rules/${id}/" \
      >/dev/null
  done <<< "$IDS"
done

# ---------- 3. create the two rules we want ----------

# Rule 1: every new issue in the `beta` environment.
# - FirstSeenEventCondition fires only when a new issue group is created
#   (i.e. first occurrence), not every event in that group.
# - Filtered to environment=beta so production crashes won't double-notify
#   once we start cutting non-beta tags.
# - frequency: 5 means "don't send the same action more than once per 5
#   minutes per issue" — protects against a single noisy issue self-spamming.
echo "==> creating: New issue (beta)"
cat <<JSON | sentry_api -X POST --data @- \
  "${API}/projects/${SENTRY_ORG}/${SENTRY_PROJECT}/rules/" >/dev/null
{
  "name": "New issue (beta) — Crewship",
  "actionMatch": "all",
  "filterMatch": "all",
  "frequency": 5,
  ${BETA_ENV_LINE}
  "conditions": [
    {"id": "sentry.rules.conditions.first_seen_event.FirstSeenEventCondition"}
  ],
  "filters": [],
  "actions": [
    {
      "id": "sentry.mail.actions.NotifyEmailAction",
      "targetType": "IssueOwners",
      "fallthroughType": "AllMembers"
    }
  ]
}
JSON

# Rule 2: spike detector — one issue seen >50 times in 1 hour.
# - EventFrequencyCondition with interval "1h" and value 50.
# - No environment filter — we want this signal everywhere, including
#   production. Runaway loops are equally catastrophic regardless of env.
# - frequency: 60 so the spike alert itself doesn't re-fire every 5min
#   while the underlying loop is still running.
echo "==> creating: Spike — 50+ events / hour"
cat <<JSON | sentry_api -X POST --data @- \
  "${API}/projects/${SENTRY_ORG}/${SENTRY_PROJECT}/rules/" >/dev/null
{
  "name": "Spike — 50+ events / hour",
  "actionMatch": "all",
  "filterMatch": "all",
  "frequency": 60,
  "conditions": [
    {
      "id": "sentry.rules.conditions.event_frequency.EventFrequencyCondition",
      "value": 50,
      "interval": "1h",
      "comparisonType": "count"
    }
  ],
  "filters": [],
  "actions": [
    {
      "id": "sentry.mail.actions.NotifyEmailAction",
      "targetType": "IssueOwners",
      "fallthroughType": "AllMembers"
    }
  ]
}
JSON

# ---------- 4. confirm ----------
echo "==> verifying..."
FINAL=$(sentry_api "${API}/projects/${SENTRY_ORG}/${SENTRY_PROJECT}/rules/")
echo "$FINAL" | jq -r '.[] | "    [\(.id)] \(.name)"'

cat <<EOF

Done. Two rules now live for ${SENTRY_ORG}/${SENTRY_PROJECT}.

Verify in the UI:
  https://${SENTRY_HOST}/organizations/${SENTRY_ORG}/alerts/rules/?project=

To remove everything this script created and start over:
  for id in \$(curl -sS -H "Authorization: Bearer \$SENTRY_AUTH_TOKEN" \\
       "${API}/projects/${SENTRY_ORG}/${SENTRY_PROJECT}/rules/" \\
       | jq -r '.[] | select(.name | test("New issue \\\\(beta\\\\) — Crewship|Spike — 50\\\\+")) | .id'); do
    curl -X DELETE -H "Authorization: Bearer \$SENTRY_AUTH_TOKEN" \\
      "${API}/projects/${SENTRY_ORG}/${SENTRY_PROJECT}/rules/\$id/"
  done
EOF
