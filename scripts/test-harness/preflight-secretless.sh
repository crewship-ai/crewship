#!/usr/bin/env bash
# Preflight for test-secretless-github.sh — answers "would running the suite
# right now tell us anything?" before it spends twenty minutes telling us no.
#
# The suite drives the real CLI against a real server, so a green run only
# means something when THREE things line up, and each of them has failed
# quietly at least once:
#
#   1. The CLI binary understands the commands the suite calls. A CLI older
#      than the branch does not have `credential bind` / `field` / `reveal`,
#      and cobra's answer to an unknown subcommand is a usage error the suite
#      reads as a failed step.
#   2. The SERVER runs the branch. This is the one that wastes the most time:
#      an older server answers the new literal routes with 404 "Credential not
#      found", because /credentials/{credentialId} swallows /credentials/
#      bindings. Nothing about that 404 says "wrong version", and the suite
#      reports it as a product failure.
#   3. The GitHub fixture is real. A token that is valid but scoped to no
#      repository — the default for a fine-grained PAT created before its
#      repo existed — passes every check except the one that matters.
#
# Exit 0 = the suite is worth running. Exit 1 = it is not, and the output says
# which of the three is missing.
#
# Usage:
#   scripts/test-harness/preflight-secretless.sh [--cli /path/to/crewship]
#
# Reads SEED_GITHUB_TOKEN / SEED_GITHUB_TEST_REPO from the environment or,
# failing that, from .env.local at the repo root. Never prints a secret: a
# token is reported by length and prefix only.

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/../.." && pwd)"

CLI="${SECRETLESS_CLI:-crewship}"
while [ $# -gt 0 ]; do
  case "$1" in
    --cli) CLI="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

RED=$'\033[31m'; GREEN=$'\033[32m'; YELLOW=$'\033[33m'; DIM=$'\033[2m'; RESET=$'\033[0m'
fails=0
warns=0

ok()   { printf '  %s✓%s %s\n' "$GREEN" "$RESET" "$1"; }
bad()  { printf '  %s✗%s %s\n' "$RED" "$RESET" "$1"; fails=$((fails+1)); }
warn() { printf '  %s!%s %s\n' "$YELLOW" "$RESET" "$1"; warns=$((warns+1)); }
note() { printf '    %s%s%s\n' "$DIM" "$1" "$RESET"; }
section() { printf '\n%s\n' "$1"; }

# ── fixture ────────────────────────────────────────────────────────────────
# Pull from .env.local only for names the environment has not already set, so
# an explicit export always wins over a stale file.
if [ -f "$REPO_ROOT/.env.local" ]; then
  for name in SEED_GITHUB_TOKEN SEED_GITHUB_TEST_REPO SEED_GITHUB_TOKEN_CLASSIC SEED_GITHUB_SSH_KEY; do
    if [ -z "${!name:-}" ]; then
      line="$(grep -m1 "^${name}=" "$REPO_ROOT/.env.local" 2>/dev/null || true)"
      [ -n "$line" ] && export "$name=${line#*=}"
    fi
  done
fi

section "1. CLI"
if ! command -v "$CLI" >/dev/null 2>&1 && [ ! -x "$CLI" ]; then
  bad "no crewship binary at '$CLI'"
  note "build one from this branch: go build -o /tmp/crewship ./cmd/crewship"
else
  ok "binary: $CLI"
  missing=()
  # One representative command per phase. `--help` is enough: it fails on an
  # unknown subcommand without touching the network or the workspace.
  for cmd in "credential bind" "credential field" "credential reveal-policy" "crew credential-readiness"; do
    # shellcheck disable=SC2086 # deliberate word splitting: "credential bind" is two argv entries
    if ! "$CLI" $cmd --help >/dev/null 2>&1; then
      missing+=("$cmd")
    fi
  done
  if [ ${#missing[@]} -eq 0 ]; then
    ok "understands the credential commands this branch adds"
  else
    bad "CLI predates the branch — missing: ${missing[*]}"
    note "go build -o /tmp/crewship ./cmd/crewship && $0 --cli /tmp/crewship"
  fi
fi

section "2. Server"
if whoami_out="$("$CLI" whoami 2>&1)" && printf '%s' "$whoami_out" | grep -q "Server:"; then
  : # fall through to the reporting below
else
  bad "not authenticated, or the server is unreachable"
  note "${whoami_out%%$'\n'*}"
fi
if printf '%s' "$whoami_out" | grep -q "Server:"; then
  server="$(printf '%s' "$whoami_out" | awk '/Server:/{print $2}')"
  role="$(printf '%s' "$whoami_out" | awk '/Role:/{print $2}')"
  ok "reachable: $server (role $role)"

  # The decisive check. reveal-policy is a LITERAL path registered next to
  # /credentials/{credentialId}; a server without it matches the wildcard and
  # answers 404 "Credential not found" — which is why that exact string, and
  # not a generic 404, is the fingerprint of an out-of-date server.
  policy_out="$("$CLI" credential reveal-policy 2>&1)"
  if printf '%s' "$policy_out" | grep -q "Credential not found"; then
    bad "server does NOT run this branch"
    note "reveal-policy hit the /credentials/{id} wildcard instead of the literal route"
    note "pin the slot: infra-crewship environments/slots.yaml → dev3 → branch: <this branch>"
    note "then: ssh crewship-dev sudo systemctl start crewship-sync.service"
  elif printf '%s' "$policy_out" | grep -qiE "reveal|enabled|disabled|forbidden"; then
    ok "server runs a build with the credential-reveal surface"
  else
    warn "could not classify the server's reveal-policy answer"
    note "${policy_out%%$'\n'*}"
  fi

  bindings_out="$("$CLI" credential bindings 2>&1)"
  if printf '%s' "$bindings_out" | grep -q "Credential not found"; then
    bad "server does NOT have the credential-binding routes"
  else
    ok "server answers the credential-binding routes"
  fi
fi

section "3. GitHub fixture"
if [ -z "${SEED_GITHUB_TOKEN:-}" ]; then
  bad "SEED_GITHUB_TOKEN is not set — every GitHub section would skip"
  note "put it in .env.local; the suite reads it from there"
else
  ok "SEED_GITHUB_TOKEN present (${#SEED_GITHUB_TOKEN} chars, ${SEED_GITHUB_TOKEN:0:11}…)"
  login="$(curl -fsS -H "Authorization: Bearer $SEED_GITHUB_TOKEN" \
      https://api.github.com/user 2>/dev/null | sed -n 's/.*"login": *"\([^"]*\)".*/\1/p' | head -1)"
  if [ -z "$login" ]; then
    bad "the token does not authenticate against api.github.com"
  else
    ok "token belongs to $login"
  fi
fi

if [ -z "${SEED_GITHUB_TEST_REPO:-}" ]; then
  bad "SEED_GITHUB_TEST_REPO is not set — clone/push and ghcr sections would skip"
elif [ -n "${SEED_GITHUB_TOKEN:-}" ]; then
  repo_json="$(curl -fsS -H "Authorization: Bearer $SEED_GITHUB_TOKEN" \
      "https://api.github.com/repos/$SEED_GITHUB_TEST_REPO" 2>/dev/null)"
  if [ -z "$repo_json" ]; then
    bad "$SEED_GITHUB_TEST_REPO is unreachable with this token"
    note "either the repo does not exist, or the fine-grained PAT has no access to it"
    note "a PAT created before its repository existed grants nothing — re-scope it"
  else
    # grep -o, not sed: BSD sed's BRE has no \| alternation, so a
    # "true\|false" pattern matches nothing and every writable repo reads as
    # read-only. A preflight that invents a blocker is worse than none.
    private="$(printf '%s' "$repo_json" | tr ',' '\n' | grep -o '"private": *[a-z]*' | head -1 | awk '{print $2}')"
    push="$(printf '%s' "$repo_json" | tr ',' '\n' | grep -o '"push": *[a-z]*' | head -1 | awk '{print $2}')"
    if [ "$push" = "true" ]; then
      ok "$SEED_GITHUB_TEST_REPO reachable, write access, private=$private"
    else
      bad "$SEED_GITHUB_TEST_REPO is readable but NOT writable — the push section would fail"
      note "grant Contents: Read and write on the token"
    fi
    [ "$private" = "false" ] && warn "the repo is public; the suite is written for a private one"
  fi
fi

for opt in SEED_GITHUB_TOKEN_CLASSIC SEED_GITHUB_SSH_KEY SECRETLESS_GHCR_IMAGE; do
  [ -z "${!opt:-}" ] && note "$opt unset — that section will skip (not a failure)"
done

# ── verdict ────────────────────────────────────────────────────────────────
section "Verdict"
if [ "$fails" -gt 0 ]; then
  printf '  %s%d blocking problem(s)%s — running the suite now would not tell us anything.\n' \
    "$RED" "$fails" "$RESET"
  exit 1
fi
printf '  %sready%s — %d optional section(s) will skip.\n' "$GREEN" "$RESET" "$warns"
printf '  run: %sscripts/test-harness/test-secretless-github.sh%s\n' "$DIM" "$RESET"
exit 0
