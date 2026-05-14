#!/usr/bin/env bash
#
# One-shot bootstrap for the Crewship Sentry projects.
#
# For every project in the table below this script:
#   1. fetches its public DSN via the Sentry REST API,
#   2. provisions our two standard alert rules ("New issue" firehose +
#      "Spike 50+/h" runaway detector) via the same API,
#   3. writes the DSN to a named GitHub Actions secret in the target
#      repo via the `gh` CLI, so the build pipeline picks it up on the
#      next release/nightly run.
#
# Idempotent: re-runs overwrite the alert rules (the alert rule
# provisioner already does named-overwrite) and overwrite the secret
# (`gh secret set` is upsert). Re-running is the recommended way to
# repair drift — never edit secrets by hand if the source is here.
#
# Requirements:
#   - SENTRY_AUTH_TOKEN env var. Create at
#     https://sentry.io/settings/account/api/auth-tokens/
#     Scopes: project:read, project:write, alerts:write
#   - gh CLI authenticated against github.com (`gh auth status`)
#   - curl, jq
#
# Usage:
#   export SENTRY_AUTH_TOKEN=sntryu_...
#   ./scripts/sentry-bootstrap.sh
#
# Override defaults via env:
#   SENTRY_ORG       (default: unify-7b)
#   SENTRY_HOST      (default: sentry.io — set for self-hosted)
#   SKIP_GH          (default: 0; set to 1 to skip GitHub secrets — useful
#                     for a dry-run audit of DSNs + alert rules)

set -euo pipefail

SENTRY_ORG="${SENTRY_ORG:-unify-7b}"
SENTRY_HOST="${SENTRY_HOST:-sentry.io}"
API="https://${SENTRY_HOST}/api/0"
SKIP_GH="${SKIP_GH:-0}"

# ---------- the table ----------
# Format per row (space-separated): sentry_project  github_repo  secret_name
#
# Adding a project? Append a row. Removing a project? Delete the row
# AND manually run `gh secret delete` on the lingering secret — this
# script intentionally doesn't delete secrets (operator safety).
#
# crewship-backend → main repo, SENTRY_DSN (Go binary, baked in via
#                    ldflag -X .../crashreport.DSN at release time)
# crewship-frontend → main repo, SENTRY_DSN_FRONTEND (embedded Next.js
#                    UI in `crewship start`, baked via
#                    NEXT_PUBLIC_SENTRY_DSN at build time)
# crewship-web     → web repo, SENTRY_DSN (crewship.ai marketing site,
#                    Next.js static export, same NEXT_PUBLIC_* mechanism)
PROJECTS=(
  "crewship-backend    crewship-ai/crewship      SENTRY_DSN"
  "crewship-frontend   crewship-ai/crewship      SENTRY_DSN_FRONTEND"
  "crewship-web        crewship-ai/crewship-web  SENTRY_DSN"
  "unify-web           Srbino/unify-web          SENTRY_DSN"
)

# ---------- preflight ----------
if [ -z "${SENTRY_AUTH_TOKEN:-}" ] && [ -f "${HOME}/.sentryclirc" ]; then
  SENTRY_AUTH_TOKEN=$(awk -F'=' '
    /^[[:space:]]*token[[:space:]]*=/ {
      sub(/^[[:space:]]*token[[:space:]]*=[[:space:]]*/, "")
      print; exit
    }' "${HOME}/.sentryclirc")
fi

if [ -z "${SENTRY_AUTH_TOKEN:-}" ]; then
  cat >&2 <<EOF
SENTRY_AUTH_TOKEN is not set and ~/.sentryclirc has no token.

Create one at https://${SENTRY_HOST}/settings/account/api/auth-tokens/
with scopes: project:read project:write alerts:write
EOF
  exit 1
fi

for cmd in curl jq gh; do
  command -v "$cmd" >/dev/null || { echo "missing: $cmd" >&2; exit 1; }
done

if [ "$SKIP_GH" != "1" ]; then
  # `gh auth status` exits non-zero when ANY known account has stale
  # credentials, even if the active one is healthy. We need only the
  # active account to work — probe it directly via a cheap API call
  # that exercises the same auth path `gh secret set` will use.
  if ! gh api user --jq '.login' >/dev/null 2>&1; then
    echo "gh CLI active account is not authenticated. Run 'gh auth login' first." >&2
    exit 1
  fi
fi

# Unified curl wrapper — captures status + body so 4xx/5xx surfaces the
# Sentry JSON error payload rather than an opaque exit code.
sentry_api() {
  local tmp status
  tmp=$(mktemp)
  status=$(curl --silent --show-error \
    -o "$tmp" -w '%{http_code}' \
    -H "Authorization: Bearer ${SENTRY_AUTH_TOKEN}" \
    -H "Content-Type: application/json" \
    "$@")
  if [ "$status" -ge 400 ]; then
    echo "sentry API ${status}:" >&2
    cat "$tmp" >&2; echo >&2
    rm -f "$tmp"
    return 1
  fi
  cat "$tmp"
  rm -f "$tmp"
}

# ---------- alert-rule provisioning per project ----------
# Names this script owns and overwrites on every run. Anything else in
# the project is left untouched.
RULES_OWNED=(
  "New issue (beta) — Crewship"
  "Spike — 50+ events / hour"
  "Send a notification for new issues"            # Sentry onboarding default
  "Send a notification for high priority issues"  # Sentry onboarding default (alt)
  "Alert me on high priority issues"              # Sentry onboarding default (alt)
)

provision_rules() {
  local project=$1
  echo "  → provisioning alert rules for ${project}..."

  # Check beta env existence to decide whether to scope the new-issue rule.
  # If the project has never received an event in environment=beta, the
  # rules API rejects the rule with "This environment has not been
  # created." — same chicken-and-egg as the original alerts script.
  local env_json has_beta
  env_json=$(sentry_api "${API}/projects/${SENTRY_ORG}/${project}/environments/" 2>/dev/null || echo '[]')
  has_beta=$(echo "$env_json" | jq -r '[.[] | select(.name == "beta")] | length')
  local beta_line=''
  if [ "$has_beta" = "1" ]; then
    beta_line='"environment": "beta",'
  fi

  # Delete any rule whose name matches one we manage. The named-overwrite
  # pattern is what makes re-runs idempotent.
  local existing
  existing=$(sentry_api "${API}/projects/${SENTRY_ORG}/${project}/rules/")
  for name in "${RULES_OWNED[@]}"; do
    local ids
    ids=$(echo "$existing" | jq -r --arg n "$name" '.[] | select(.name == $n) | .id')
    while IFS= read -r id; do
      [ -z "$id" ] && continue
      sentry_api -X DELETE \
        "${API}/projects/${SENTRY_ORG}/${project}/rules/${id}/" >/dev/null
    done <<< "$ids"
  done

  # Create "New issue (beta)" — first-seen event filter.
  cat <<JSON | sentry_api -X POST --data @- \
    "${API}/projects/${SENTRY_ORG}/${project}/rules/" >/dev/null
{
  "name": "New issue (beta) — Crewship",
  "actionMatch": "all",
  "filterMatch": "all",
  "frequency": 5,
  ${beta_line}
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

  # Create "Spike 50+/h" — runaway-loop detector.
  cat <<JSON | sentry_api -X POST --data @- \
    "${API}/projects/${SENTRY_ORG}/${project}/rules/" >/dev/null
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

  echo "    ✓ rules provisioned (env-scope: $([ "$has_beta" = "1" ] && echo "beta" || echo "all"))"
}

# ---------- DSN fetch ----------
fetch_dsn() {
  local project=$1
  # /keys/ returns array of client keys; we want the active one's public
  # DSN. New projects get a single auto-generated key; this picks the
  # first active one which is correct in 99% of cases.
  local keys_json dsn
  keys_json=$(sentry_api "${API}/projects/${SENTRY_ORG}/${project}/keys/")
  dsn=$(echo "$keys_json" | jq -r '.[] | select(.isActive == true) | .dsn.public' | head -n1)
  if [ -z "$dsn" ] || [ "$dsn" = "null" ]; then
    echo "    ✗ no active DSN found for ${project}" >&2
    return 1
  fi
  echo "$dsn"
}

# ---------- GitHub secret set ----------
set_github_secret() {
  local repo=$1
  local name=$2
  local value=$3
  echo -n "$value" | gh secret set "$name" --repo "$repo" --body - >/dev/null
}

# ---------- main loop ----------
echo "Target: ${SENTRY_ORG} on ${SENTRY_HOST}"
echo "Projects: ${#PROJECTS[@]}"
echo

for row in "${PROJECTS[@]}"; do
  # shellcheck disable=SC2086
  read -r project repo secret <<< "$row"

  echo "▸ ${project}"

  dsn=$(fetch_dsn "$project")
  # Print the host portion of the DSN so the operator can eyeball-confirm
  # without splattering the secret into terminal scrollback.
  echo "    DSN endpoint: $(echo "$dsn" | sed -E 's|https?://[^@]+@([^/]+).*|\1|')"

  provision_rules "$project"

  if [ "$SKIP_GH" = "1" ]; then
    echo "    (skipping gh secret set; SKIP_GH=1)"
  else
    set_github_secret "$repo" "$secret" "$dsn"
    echo "    ✓ ${secret} → ${repo}"
  fi
  echo
done

# ---------- summary ----------
echo "─── Summary ───────────────────────────────────────"
for row in "${PROJECTS[@]}"; do
  read -r project repo secret <<< "$row"
  printf "  %-22s → %-30s as %s\n" "$project" "$repo" "$secret"
done
echo
echo "Next steps:"
cat <<EOF
  1. Open the Sentry org-level Security & Privacy → Data Scrubbing page
     and verify the 5 regex rules (email, Bearer, sk-*, ghp_, xox*-) are
     still in place. These are org-level and cover all 4 projects.
  2. Create a Sentry Internal Integration with Project R/W +
     Releases Admin scopes; the resulting token goes into
     SENTRY_AUTH_TOKEN secret in both repos so @sentry/nextjs
     source-map upload works on release builds.
  3. Trigger a CI re-run on the open PRs so the build picks up the
     new secrets:
        gh workflow run ci.yml --repo crewship-ai/crewship
        gh workflow run ci.yml --repo crewship-ai/crewship-web
EOF
