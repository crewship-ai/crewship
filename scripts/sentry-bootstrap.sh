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
#   SENTRY_ORG       (default: your-sentry-org)
#   SENTRY_HOST      (default: sentry.io — set for self-hosted)
#   SKIP_GH          (default: 0; set to 1 to skip GitHub secrets — useful
#                     for a dry-run audit of DSNs + alert rules)

set -euo pipefail

SENTRY_ORG="${SENTRY_ORG:-your-sentry-org}"
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
# The default project set scopes the script to the crewship-ai org —
# touching personal repos by default would be a footgun for anyone
# who clones this script and runs it expecting only Crewship work.
# Set INCLUDE_PERSONAL_REPOS=1 to opt into the personal/non-org rows
# below.
PROJECTS=(
  "crewship-backend    crewship-ai/crewship      SENTRY_DSN"
  "crewship-frontend   crewship-ai/crewship      SENTRY_DSN_FRONTEND"
  "crewship-web        crewship-ai/crewship-web  SENTRY_DSN"
)
if [ "${INCLUDE_PERSONAL_REPOS:-0}" = "1" ]; then
  PROJECTS+=(
    "unify-web           your-org/your-repo          SENTRY_DSN"
  )
fi

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

# curl + jq are always required (we always hit the Sentry API). gh
# only matters for the secrets-set path; SKIP_GH=1 audits run on
# machines without gh installed (e.g. a sandboxed CI runner doing a
# DSN-only check).
for cmd in curl jq; do
  command -v "$cmd" >/dev/null || { echo "missing: $cmd" >&2; exit 1; }
done

if [ "$SKIP_GH" != "1" ]; then
  command -v gh >/dev/null || { echo "missing: gh (or set SKIP_GH=1 for a DSN-only run)" >&2; exit 1; }
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
  #
  # If the environments endpoint itself fails (network blip, permission
  # change), surface the error rather than silently fall through to a
  # rule without environment filtering — silently un-scoping the rule
  # would route prod events into the beta firehose, which is exactly
  # what scoping is meant to prevent.
  local env_json has_beta
  env_json=$(sentry_api "${API}/projects/${SENTRY_ORG}/${project}/environments/")
  has_beta=$(echo "$env_json" | jq -r '[.[] | select(.name == "beta")] | length')
  local beta_line=''
  if [ "$has_beta" = "1" ]; then
    beta_line='"environment": "beta",'
  fi

  # upsert_rule: PUT to update an existing rule by id, POST to create new.
  # The earlier delete-then-POST shape had a brief window where the project
  # had NO alert rule for the managed name — if the POST then failed for
  # any reason (rate limit, transient 5xx), we'd leave the project
  # alerting-blind. Update-in-place keeps the rule live across the call.
  #
  # Stale duplicates (same name but unexpected extras from manual edits)
  # are pruned at the end of this function so the project ends in a
  # known good state without ever being unprotected mid-run.
  local existing
  existing=$(sentry_api "${API}/projects/${SENTRY_ORG}/${project}/rules/")

  upsert_rule() {
    local name=$1
    local payload=$2
    local existing_id
    existing_id=$(echo "$existing" | jq -r --arg n "$name" '[.[] | select(.name == $n)] | .[0].id // empty')
    if [ -n "$existing_id" ]; then
      echo "$payload" | sentry_api -X PUT --data @- \
        "${API}/projects/${SENTRY_ORG}/${project}/rules/${existing_id}/" >/dev/null
      echo "$existing_id"
    else
      echo "$payload" | sentry_api -X POST --data @- \
        "${API}/projects/${SENTRY_ORG}/${project}/rules/" \
        | jq -r '.id'
    fi
  }

  local new_issue_id spike_id
  new_issue_id=$(upsert_rule "New issue (beta) — Crewship" "$(cat <<JSON
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
  )")

  spike_id=$(upsert_rule "Spike — 50+ events / hour" "$(cat <<JSON
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
  )")

  # Prune stale duplicates: any rule with a managed name whose id is not
  # one of the two we just upserted. Runs AFTER both upserts so the
  # project is never alerting-blind mid-call.
  for name in "${RULES_OWNED[@]}"; do
    local ids
    ids=$(echo "$existing" | jq -r --arg n "$name" '.[] | select(.name == $n) | .id')
    while IFS= read -r id; do
      [ -z "$id" ] && continue
      if [ "$id" != "$new_issue_id" ] && [ "$id" != "$spike_id" ]; then
        sentry_api -X DELETE \
          "${API}/projects/${SENTRY_ORG}/${project}/rules/${id}/" >/dev/null
      fi
    done <<< "$ids"
  done

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
  # `printf '%s'` over `echo -n`: `-n` is non-POSIX, and dash (Debian's
  # /bin/sh) prints `-n` literally instead of suppressing the trailing
  # newline. `gh secret set` would then store a value with `-n ` prefix
  # and a trailing newline — both invisible until the secret silently
  # fails to authenticate against the upstream service.
  printf '%s' "$value" | gh secret set "$name" --repo "$repo" --body - >/dev/null
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
