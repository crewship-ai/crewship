#!/usr/bin/env bash
#
# One-shot configuration of `main` branch protection on GitHub via the gh CLI.
# Idempotent: re-running overwrites the existing rule with the same shape.
#
# Required checks below are the names that show up on the PR page after the
# corresponding workflow has run at least once. If a check name changes
# (e.g. job renamed in ci.yml), update it here and re-run the script.
#
#   ./scripts/setup-branch-protection.sh
#
# Requires: gh CLI logged in as a repo admin.

set -euo pipefail

REPO="${REPO:-crewship-ai/crewship}"
BRANCH="${BRANCH:-main}"

if ! command -v gh >/dev/null 2>&1; then
  echo "gh CLI not found. Install: https://cli.github.com/" >&2
  exit 1
fi

# `required_status_checks.contexts` must list every workflow's job name that
# should block merging. Names taken from the `name:` field of each job after
# expansion by GitHub Actions — verify with `gh pr checks <PR>` if unsure.
read -r -d '' PAYLOAD <<'JSON' || true
{
  "required_status_checks": {
    "strict": true,
    "contexts": [
      "Frontend",
      "Backend (Go)",
      "Lint migrations",
      "Security",
      "End-to-end (devcontainer)"
    ]
  },
  "enforce_admins": true,
  "required_pull_request_reviews": {
    "dismiss_stale_reviews": true,
    "require_code_owner_reviews": false,
    "required_approving_review_count": 1
  },
  "restrictions": null,
  "required_linear_history": true,
  "allow_force_pushes": false,
  "allow_deletions": false,
  "required_conversation_resolution": true
}
JSON

echo "Applying branch protection to $REPO@$BRANCH..."
gh api \
  --method PUT \
  -H "Accept: application/vnd.github+json" \
  "/repos/${REPO}/branches/${BRANCH}/protection" \
  --input - <<<"$PAYLOAD"
echo "✓ done."

cat <<EOF

Verify in the GitHub UI:
  https://github.com/${REPO}/settings/branches

If a required check name changed in CI, edit this script's PAYLOAD list and re-run.
EOF
