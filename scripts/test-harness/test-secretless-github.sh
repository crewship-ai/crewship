#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Secretless GitHub — the runtime proof of PRD-CREDENTIALS-V2 §4.3 (T-H1…T-H9).
#
# One sentence is on trial here:
#
#     assign a secret to a CREW and the crew's agents can use it — `gh`, git
#     over HTTPS and SSH, `docker login` — with no step taken inside the
#     container, and no copy of the secret left on the container's disk.
#
# `test-realworld-github.sh` is the thin ancestor of this file: it proves an
# agent can reach a PUBLIC repo when the seed happened to wire a token. This
# suite proves the product claim end to end: crew-scoped delivery, absence of
# `gh auth login`, zero dried copies on disk, private-repo clone + push, GHCR,
# git-over-SSH, revocation, and cross-crew isolation.
#
# ── How it observes the inside of a container ───────────────────────────────
# Two channels, both CLI-only (CLAUDE.md agent-operations rule #1 — no curl at
# /api/v1, no sqlite3, no ssh to the server):
#
#   1. `crewship ask` — an agent runs a command in its own container. This is
#      the only channel that carries the agent's credential ENV, because
#      credentials are injected per agent exec. LLM-mediated, so every prompt
#      asks for a fixed marker line and we assert on the marker.
#   2. A token-zero `script` routine step (the `test-redteam-insider.sh`
#      pattern): a generated probe is delivered with `crew files save`, run
#      inside the SAME crew container, and reports filesystem facts as
#      `SLPROBE key=value` lines. Deterministic, no model in the loop — this is
#      what the zero-disk and file-absence assertions run on.
#
# ── Secrets hygiene ─────────────────────────────────────────────────────────
#   * Every real credential comes from the environment (.env.local in practice):
#       SEED_GITHUB_TOKEN          fine-grained PAT (Contents: Read+Write)
#       SEED_GITHUB_TOKEN_CLASSIC  classic PAT (repo scope) — optional shape check
#       SEED_GITHUB_SSH_KEY        private key, or a path to one
#       SEED_GITHUB_TEST_REPO      owner/repo of the throwaway PRIVATE repo
#     Nothing is read from, or written to, the repo. Every section SKIPs when
#     its own inputs are absent, so a run with none of them still reports
#     something useful (the crew-fanout and isolation proofs need no accounts).
#   * No assertion is ever handed raw agent output: `scrub` replaces every known
#     secret with «REDACTED» first, so a failure message cannot print one.
#   * The value-grep (T-H3) does not use a real token at all. It uses a CANARY:
#     a credential this suite mints whose value is a random nonce. It is safe to
#     grep for, safe to name in output, and it is never echoed in full — the
#     agent reports only a first3-last3 fingerprint, so the canary cannot enter
#     a transcript that then lands on the very disk we are grepping.
#
# ── Dev slots only ──────────────────────────────────────────────────────────
# It creates + deletes workspace credentials, saves + soft-deletes a routine,
# and (only with SEED_GITHUB_TEST_REPO set) pushes and then deletes a uniquely
# named branch on the throwaway repo. Never point it at prod.
#
# Usage:
#   CREWSHIP_SERVER=<devN url> bash test-secretless-github.sh
# Env:
#   SECRETLESS_CREW_A / SECRETLESS_CREW_B   pin the two crews (default: auto)
#   SECRETLESS_GHCR_IMAGE                   image to `docker pull` after login
#   SECRETLESS_KEEP                         1 = skip cleanup (debugging)

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

NONCE="$(nonce SL)"
SAFE_NONCE="${NONCE//-/_}"                 # env-var-safe form of the nonce
CANARY_CRED="HARNESS_CANARY_${SAFE_NONCE}" # crew-derived delivery uses the NAME
CANARY_VALUE="CANARY${SAFE_NONCE}$(nonce V | tr -d '-')"
CANARY_FP="${CANARY_VALUE:0:3}-${CANARY_VALUE: -3}"
GH_CRED=""            # decided in section 1 (GH_TOKEN on a clean slot)
SSH_CRED="HARNESS_SSH_${SAFE_NONCE}"
# Lowercased on purpose: the server derives the routine slug from --name with
# slugify (lowercase, `_`/`-` preserved), so an uppercase name here would save
# as one slug and `routine run` would ask for another.
ROUTINE_SLUG="secretless-probe-$(printf '%s' "$SAFE_NONCE" | tr '[:upper:]' '[:lower:]')"
PROBE_LOCAL="$(mktemp -t sl-probe.XXXXXX)"
PROBE_DSL="$(mktemp -t sl-dsl.XXXXXX).json"
PROBE_READY=0
PROBE_OUT=""

CREW_A=""; CREW_B=""; AGENT_A=""; AGENT_B=""
GH_REPO="${SEED_GITHUB_TEST_REPO:-}"
GH_OWNER="${GH_REPO%%/*}"

# ── secret hygiene helpers ──────────────────────────────────────────────────
# Everything that must never reach stdout. The canary is deliberately NOT in
# here: it is synthetic, and we compare against it.
_SECRETS=()
[[ -n "${SEED_GITHUB_TOKEN:-}" ]]         && _SECRETS+=("$SEED_GITHUB_TOKEN")
[[ -n "${SEED_GITHUB_TOKEN_CLASSIC:-}" ]] && _SECRETS+=("$SEED_GITHUB_TOKEN_CLASSIC")

# scrub <text> — replace every known secret with «REDACTED». Run EVERY agent
# reply through this before it reaches an assertion: assert_contains prints a
# slice of the haystack on failure, and assert_not_contains prints the needle.
scrub() {
  local s="$1" v
  for v in ${_SECRETS[@]+"${_SECRETS[@]}"}; do
    [[ -n "$v" ]] && s="${s//"$v"/«REDACTED»}"
  done
  printf '%s' "$s"
}

# assert_no_secret_leak <name> <text> — the one assertion that may not name its
# needle. Reports only WHICH secret matched, never the value.
assert_no_secret_leak() {
  local name="$1" hay="$2" v i=0 leaked=""
  # Nothing configured = nothing to leak. Say so rather than banking a
  # vacuous PASS in a suite whose whole point is honest reporting.
  if (( ${#_SECRETS[@]} == 0 )); then
    skip "$name" "no real credential configured in this run"
    return 0
  fi
  for v in ${_SECRETS[@]+"${_SECRETS[@]}"}; do
    i=$((i + 1))
    [[ -z "$v" ]] && continue
    if printf '%s' "$hay" | grep -qF -- "$v"; then leaked="${leaked}${leaked:+,}#${i}"; fi
  done
  if [[ -n "$leaked" ]]; then
    _fail "$name" "a configured secret (slot ${leaked}) appeared verbatim in the output"
  else
    _pass "$name"
  fi
}

# shellcheck disable=SC2329  # invoked via `trap ... EXIT`
_cleanup() {
  rm -f "$PROBE_LOCAL" "$PROBE_DSL" "$PROBE_DSL.b"
  [[ "${SECRETLESS_KEEP:-0}" == "1" ]] && return 0
  cs routine delete "$ROUTINE_SLUG" --yes >/dev/null 2>&1 || true
  cs credential delete "$CANARY_CRED" --yes >/dev/null 2>&1 || true
  [[ -n "$SSH_CRED" ]] && cs credential delete "$SSH_CRED" --yes >/dev/null 2>&1
  [[ -n "$GH_CRED" ]] && cs credential delete "$GH_CRED" --yes >/dev/null 2>&1
  return 0
}
trap _cleanup EXIT

preflight

if ! have jq; then
  skip "secretless GitHub suite" "jq is required — every assertion here reads JSON"
  finish
fi

# ─────────────────────────────────────────────────────────────────────────────
section "0. Setup — two crews, one agent each, and the in-container probe"
# ─────────────────────────────────────────────────────────────────────────────
# Crew A receives the credentials; crew B is the isolation control. Both must
# have at least one agent, because an agent run is the only channel that
# carries credential env.
crews_json="$(cs crew list --format json 2>/dev/null)"
if ! printf '%s' "$crews_json" | jq -e 'type=="array" and length>0' >/dev/null 2>&1; then
  _fail "resolve crews" "crew list returned no array — check CREWSHIP_PROFILE / CREWSHIP_WORKSPACE for $SERVER"
  finish
fi

first_agent_of() { cs agent list --crew "$1" --format json 2>/dev/null | jq -r 'first(.[].slug) // empty'; }

for c in $(printf '%s' "$crews_json" | jq -r '.[].slug // empty'); do
  a="$(first_agent_of "$c")"
  [[ -z "$a" ]] && continue
  if [[ -z "$CREW_A" ]]; then CREW_A="$c"; AGENT_A="$a"
  elif [[ -z "$CREW_B" ]]; then CREW_B="$c"; AGENT_B="$a"; fi
done
# Explicit pins win over the auto-pick.
if [[ -n "${SECRETLESS_CREW_A:-}" ]]; then CREW_A="$SECRETLESS_CREW_A"; AGENT_A="$(first_agent_of "$CREW_A")"; fi
if [[ -n "${SECRETLESS_CREW_B:-}" ]]; then CREW_B="$SECRETLESS_CREW_B"; AGENT_B="$(first_agent_of "$CREW_B")"; fi

if [[ -z "$CREW_A" || -z "$AGENT_A" ]]; then
  _fail "resolve a crew with at least one agent" "none found — seed the workspace first"
  finish
fi
_pass "crew A resolved: $CREW_A (agent: $AGENT_A)"
if [[ -n "$CREW_B" && -n "$AGENT_B" ]]; then
  _pass "crew B resolved for the isolation control: $CREW_B (agent: $AGENT_B)"
else
  skip "crew B for cross-crew isolation" "only one crew has agents — section 8 cannot run"
fi

# The probe. Written at runtime (never committed, never carries a real secret)
# and delivered to crew A's shared dir. HARNESS_CANARY arrives through the step
# env, so the canary value is not baked into the file on disk — otherwise the
# grep below would find its own probe and report a false hit.
cat > "$PROBE_LOCAL" <<'PROBE'
#!/usr/bin/env bash
# Secretless zero-disk probe — generated by test-secretless-github.sh.
# Runs inside the crew container as a token-zero routine `script` step and
# reports filesystem facts as `SLPROBE key=value` lines. Never prints a secret:
# the only value it greps for is the harness canary, and it emits PATHS ONLY.
set -uo pipefail
emit() { printf 'SLPROBE %s=%s\n' "$1" "${2:-none}"; }
exists() { if [ -e "$1" ]; then echo present; else echo absent; fi; }
H="${HOME:-/home/agent}"

emit home "$H"
emit uid "$(id -u 2>/dev/null || echo '?')"
for t in gh git ssh docker; do
  if command -v "$t" >/dev/null 2>&1; then emit "have_$t" yes; else emit "have_$t" no; fi
done

# T-H2 / T-H4 / T-H6 — the three files a "real" login would leave behind.
emit gh_hosts_yml "$(exists "$H/.config/gh/hosts.yml")"
emit git_credentials "$(exists "$H/.git-credentials")"
emit docker_config "$(exists "$H/.docker/config.json")"
# A helper REFERENCE in the docker config is the shape we want (L2); a base64
# `auth` blob is a stored copy of the PAT. Report which one it is, not both.
if [ -f "$H/.docker/config.json" ]; then
  if grep -q '"auth"[[:space:]]*:' "$H/.docker/config.json" 2>/dev/null; then
    emit docker_config_shape stored-auth
  else
    emit docker_config_shape helper-only
  fi
else
  emit docker_config_shape absent
fi

# T-H2 — `gh auth login` must never have been run by anyone.
hist=""
for f in "$H/.bash_history" "$H/.zsh_history" "$H/.sh_history" \
         "$H/.local/share/fish/fish_history" /root/.bash_history; do
  [ -f "$f" ] || continue
  if grep -qE 'gh[[:space:]]+auth[[:space:]]+login' "$f" 2>/dev/null; then
    hist="${hist}${hist:+,}$f"
  fi
done
emit gh_auth_login_history "$hist"

# What Crewship itself materialised. Paths only — never contents.
emit secrets_files "$(find /secrets -maxdepth 4 -type f 2>/dev/null | head -25 | tr '\n' ',')"

# T-H3 — the value grep. -I skips binaries; noisy caches are excluded so a
# multi-GB node_modules cannot turn this into a timeout.
CAN="${HARNESS_CANARY:-}"
if [ -n "$CAN" ]; then
  hits="$(grep -rlIF \
      --exclude-dir=.git --exclude-dir=node_modules --exclude-dir=.cache \
      -- "$CAN" "$H" /home/agent /secrets /tmp 2>/dev/null \
    | sort -u | head -25 | tr '\n' ',')"
  emit canary_hits "$hits"
else
  emit canary_hits no-canary
fi
emit probe_done yes
PROBE

cat > "$PROBE_DSL" <<JSON
{ "name":"$ROUTINE_SLUG","description":"secretless zero-disk probe (harness)","dsl_version":"1.0",
  "steps":[{"id":"probe","type":"script",
    "script":{"path":"scripts/secretless-probe.sh","interpreter":"bash",
              "env":{"HARNESS_CANARY":"$CANARY_VALUE"}}}] }
JSON

if cs crew files save "$CREW_A" shared/scripts/secretless-probe.sh --file "$PROBE_LOCAL" >/dev/null 2>&1 \
   && cs routine save --name "$ROUTINE_SLUG" --description "secretless zero-disk probe (harness)" \
        --definition "$PROBE_DSL" --author-crew "$CREW_A" >/dev/null 2>&1; then
  PROBE_READY=1
  _pass "in-container probe delivered + routine saved ($ROUTINE_SLUG)"
else
  skip "in-container probe" "crew files save / routine save failed — filesystem assertions will SKIP"
fi

# probe_run — run the probe and cache its output in PROBE_OUT. Returns non-zero
# when the probe is unavailable or produced nothing parseable.
probe_run() {
  (( PROBE_READY == 1 )) || return 1
  PROBE_OUT="$(cs routine run "$ROUTINE_SLUG" 2>/dev/null)"
  printf '%s' "$PROBE_OUT" | grep -q 'SLPROBE probe_done=yes'
}
# probe_get <key> — read one value out of the cached probe output.
probe_get() {
  printf '%s\n' "$PROBE_OUT" | sed -n "s/^.*SLPROBE $1=//p" | head -1 | tr -d '\r'
}
# probe_ensure — run the probe only if we do not already hold a fresh report.
# Use it for a read that no intervening step could have invalidated; call
# probe_run directly after anything that may have written to the filesystem.
probe_ensure() {
  printf '%s' "$PROBE_OUT" | grep -q 'SLPROBE probe_done=yes' && return 0
  probe_run
}
# probe_tool <name> — yes | no | unknown (the probe never ran), so a section can
# say "docker is missing" and "we could not look" in different words.
probe_tool() {
  local v; v="$(probe_get "have_$1")"
  case "$v" in yes) echo yes ;; no) echo no ;; *) echo unknown ;; esac
}

# ─────────────────────────────────────────────────────────────────────────────
section "1. T-H1 — assign a credential to a CREW, the crew's agent has it"
# ─────────────────────────────────────────────────────────────────────────────
# Two halves. The CANARY half proves the crew fanout itself and needs no GitHub
# account at all; the GitHub half proves the same delivery makes a real tool
# (`gh`) work. Neither takes a step inside the container.
#
# NOTE on naming: crew-derived delivery uses the credential's NAME as the env
# var (credential_delivery.go: `SELECT c.name AS env_var_name` on the crew
# half), so --env-var-name is ignored for a crew-scoped row. That is the
# not-yet-built PRD P3 decoupling; until it lands the name IS the contract.
if printf '%s' "$CANARY_VALUE" | cs credential create \
      --name "$CANARY_CRED" --type CLI_TOKEN --provider CUSTOM_CLI \
      --crews "$CREW_A" --env-var-name "$CANARY_CRED" --value-stdin >/dev/null 2>&1; then
  _pass "canary credential created and scoped to crew $CREW_A"
else
  _fail "canary credential create --crews $CREW_A" "create failed — the rest of the suite cannot run"
  finish
fi

scope="$(cs credential get "$CANARY_CRED" --format json 2>/dev/null | jq -r '.scope // ""')"
assert_eq "credential scope is CREW (not workspace-wide)" "CREW" "$scope"

# The delivery proof. The agent prints a first3-last3 FINGERPRINT, never the
# value — so the canary cannot land in a transcript on the disk section 3 greps.
fp_cmd="printf '%s-%s\\n' \"\$(printf %s \"\$${CANARY_CRED}\" | cut -c1-3)\" \"\$(printf %s \"\$${CANARY_CRED}\" | rev | cut -c1-3 | rev)\""
canary_reply="$(ask_agent "$AGENT_A" "Run exactly this in your container and paste the raw output on a line \
starting with CANARY=. Do not print the variable itself. If the variable is unset, reply exactly CANARY=UNSET.

  ${fp_cmd}")"
assert_no_secret_leak "canary probe output carries no configured secret" "$canary_reply"
canary_reply="$(scrub "$canary_reply")"
assert_contains "crew-scoped credential reached agent $AGENT_A (fingerprint $CANARY_FP)" \
  "$canary_reply" "$CANARY_FP"

# Was this slot seeded with a GitHub token already assigned DIRECTLY to agent A?
# If so a bare `gh auth status` proves nothing about crew scoping, and we must
# point gh at our own crew-delivered credential to keep the claim honest.
agent_creds="$(cs agent credentials "$AGENT_A" --format json 2>/dev/null)"
PREEXISTING_GH="$(printf '%s' "$agent_creds" \
  | jq -r '[.[] | select((.credential_provider//""|ascii_downcase)=="github"
             or (.env_var_name//"")=="GH_TOKEN" or (.env_var_name//"")=="GITHUB_TOKEN")] | length' 2>/dev/null)"
[[ -z "$PREEXISTING_GH" ]] && PREEXISTING_GH=0

# FINDING (not a failure): `crewship agent credentials` — i.e.
# GET /api/v1/agents/{id}/credentials, internal/api/agent_credentials.go —
# still selects from agent_credentials alone. It is the one credential reader
# the crew-fanout branch did not move onto loadDeliveredCredentials, so a
# crew-scoped credential the agent demonstrably HAS is invisible in the only
# CLI surface that answers "what does this agent get?".
if printf '%s' "$agent_creds" | jq -e --arg n "$CANARY_CRED" 'any(.[]; .credential_name==$n)' >/dev/null 2>&1; then
  _pass "crew-derived credential is visible in 'agent credentials'"
else
  skip "crew-derived credential visible in 'agent credentials'" \
    "FINDING: GET /agents/{id}/credentials reads agent_credentials only — crew fanout is invisible to the CLI"
fi

if [[ -z "${SEED_GITHUB_TOKEN:-}" ]]; then
  skip "gh auth status via a crew-scoped GitHub PAT" "SEED_GITHUB_TOKEN not set"
else
  # Clean slot → our credential IS GH_TOKEN and `gh` picks it up with no
  # in-container step at all. Seeded slot → use a distinct name and point gh at
  # it explicitly, so the pass is attributable to OUR crew scoping.
  if (( PREEXISTING_GH > 0 )); then
    GH_CRED="HARNESS_GH_PAT_${SAFE_NONCE}"
    info "agent $AGENT_A already holds a GitHub grant from the seed — using $GH_CRED so the result stays attributable"
  else
    GH_CRED="GH_TOKEN"
  fi

  if printf '%s' "$SEED_GITHUB_TOKEN" | cs credential create \
        --name "$GH_CRED" --type CLI_TOKEN --provider GITHUB \
        --crews "$CREW_A" --env-var-name "$GH_CRED" --value-stdin >/dev/null 2>&1; then
    _pass "GitHub PAT created and scoped to crew $CREW_A (as $GH_CRED)"
  else
    _fail "GitHub PAT create --crews $CREW_A" "create failed (name already taken? set a different one and re-run)"
    GH_CRED=""
  fi
fi

# A valid PAT and a usable agent are two different things — `gh` only exists in
# the container when the crew's devcontainer declares the feature. Say which it
# is before asserting on `gh auth status`.
GH_TOOL_OK=1
if [[ -n "$GH_CRED" ]]; then
  gaps="$(cs crew credential-readiness "$CREW_A" --format json 2>/dev/null \
          | jq -r '[.gaps[]? | select(.tool=="gh")] | length' 2>/dev/null)"
  if [[ "$gaps" =~ ^[0-9]+$ ]] && (( gaps > 0 )); then
    GH_TOOL_OK=0
    skip "gh is present in crew $CREW_A's container" \
      "crew credential-readiness reports a 'gh' gap — add the github-cli feature with 'crewship crew config'"
  else
    _pass "crew credential-readiness reports no 'gh' gap for $CREW_A"
  fi
fi

# gh_prefix — how a command is pointed at our credential. Empty on a clean slot
# (the credential is already GH_TOKEN); an explicit assignment otherwise.
gh_prefix=""
[[ -n "$GH_CRED" && "$GH_CRED" != "GH_TOKEN" ]] && gh_prefix="GH_TOKEN=\"\$${GH_CRED}\" "

if [[ -n "$GH_CRED" ]] && (( GH_TOOL_OK == 1 )); then
  auth_reply="$(ask_agent "$AGENT_A" "Run exactly '${gh_prefix}gh auth status' in your container and paste the \
raw output. Then, on its own last line, print GH_AUTH=OK if the command exited 0, otherwise GH_AUTH=FAIL. \
Never print the token value.")"
  assert_no_secret_leak "gh auth status output does not echo the PAT" "$auth_reply"
  auth_reply="$(scrub "$auth_reply")"
  assert_contains "agent in crew $CREW_A is authenticated to github.com" "$auth_reply" "github.com"
  assert_contains "gh auth status exited 0 (no step taken inside the container)" "$auth_reply" "GH_AUTH=OK"
else
  skip "gh auth status through the crew-scoped PAT" "no usable GitHub credential / no gh in the container"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "2. T-H2 — \`gh auth login\` never ran"
# ─────────────────────────────────────────────────────────────────────────────
# The whole point of the feature: authentication happens by delivery, not by an
# interactive login. `gh auth login` writes ~/.config/gh/hosts.yml — its absence
# after a successful `gh auth status` is the evidence.
if probe_run; then
  assert_eq "gh hosts.yml (the gh auth login artifact) does not exist" "absent" "$(probe_get gh_hosts_yml)"
  hist="$(probe_get gh_auth_login_history)"
  if [[ "$hist" == "none" || -z "$hist" ]]; then
    _pass "no 'gh auth login' in any shell history in the container"
  else
    _fail "no 'gh auth login' in any shell history" "found in: $hist"
  fi
  info "container: home=$(probe_get home) uid=$(probe_get uid) gh=$(probe_get have_gh) git=$(probe_get have_git) ssh=$(probe_get have_ssh) docker=$(probe_get have_docker)"
else
  skip "T-H2 (no gh auth login)" "the in-container probe did not run"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "3. T-H3 — zero-disk: the value is nowhere on the container filesystem"
# ─────────────────────────────────────────────────────────────────────────────
# Grepped roots: \$HOME, /home/agent, /secrets, /tmp — plus the three named
# files checked by existence (~/.config/gh/hosts.yml, ~/.git-credentials,
# ~/.docker/config.json). The needle is the CANARY, never a real token.
#
# SOFT vs HARD — the honest split, and why:
#   HARD FAIL for a hit in a credential-helper artifact, or anywhere under
#     \$HOME that is not /secrets. Nothing in Crewship's delivery path writes
#     there; a hit means some tool dried a copy out of the environment, which is
#     exactly the regression this suite exists to catch.
#   XFAIL (loud, not red) for hits under /secrets. Delivery mounts file-typed
#     credentials as 0400 files there ON PURPOSE today
#     (orchestrator/exec_sidecar.go buildCredFileScript), and \$HOME sits on a
#     persistent volume — the PRD's ephemeral-HOME work (phase F4, "Zero-disk
#     garance" in §6) is what closes it. A hard failure there would report a
#     KNOWN, decided-for-later gap as a regression on every run, and a gate that
#     is red by design gets muted. So: /secrets = xfail with the F4 reference,
#     everything else = fail.
if ! probe_ensure; then
  skip "T-H3 (zero-disk value grep)" "the in-container probe did not run"
else
  hits="$(probe_get canary_hits)"
  case "$hits" in
    no-canary)
      skip "zero-disk value grep" "probe received no canary (step env not delivered)" ;;
    none|"")
      _pass "canary value found in ZERO files under \$HOME, /home/agent, /secrets and /tmp" ;;
    *)
      outside=""; inside=""
      IFS=',' read -r -a _hit_arr <<< "$hits"
      for p in ${_hit_arr[@]+"${_hit_arr[@]}"}; do
        [[ -z "$p" ]] && continue
        case "$p" in
          /secrets/*) inside="${inside}${inside:+ }$p" ;;
          *)          outside="${outside}${outside:+ }$p" ;;
        esac
      done
      if [[ -n "$outside" ]]; then
        _fail "no dried copy of the credential outside /secrets" "found in: $outside"
      else
        _pass "no dried copy of the credential outside /secrets"
      fi
      if [[ -n "$inside" ]]; then
        xfail "zero-disk holds for /secrets too" \
          "PRD phase F4 — file-typed credentials are mounted 0400 under /secrets by design today, and \$HOME is a persistent volume: $inside"
      fi ;;
  esac

  # The three artifacts, by existence rather than by content: each one is a
  # stored copy even when the value inside it is a different token.
  assert_eq "no .git-credentials file was created" "absent" "$(probe_get git_credentials)"
  assert_eq "no .docker/config.json was created (before any docker login)" "absent" "$(probe_get docker_config)"
  info "materialised under /secrets: $(probe_get secrets_files)"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "4. T-H4/T-H5 — private repo: clone over HTTPS, commit, push"
# ─────────────────────────────────────────────────────────────────────────────
# Uses gh's own credential helper INLINE (git -c credential.helper=...), so git
# authenticates without `gh auth setup-git` writing anything persistent, and
# without a token in a remote URL (which would land in .git/config on disk).
# The branch is uniquely named and deleted again on the way out.
BRANCH="harness/secretless-${SAFE_NONCE}"
if [[ -z "$GH_CRED" || -z "$GH_REPO" ]]; then
  skip "private-repo clone + push" "needs SEED_GITHUB_TOKEN and SEED_GITHUB_TEST_REPO (owner/repo)"
elif (( GH_TOOL_OK == 0 )); then
  skip "private-repo clone + push" "gh is not installed in crew $CREW_A's container"
else
  clone_reply="$(ask_agent "$AGENT_A" "Run these commands exactly, in one shell, in your container, and paste the \
raw output. Do not print any token. Finish with a line CLONE_PUSH=OK if every command exited 0, otherwise \
CLONE_PUSH=FAIL.

  d=\$(mktemp -d) && cd \"\$d\" && \\
  ${gh_prefix}gh repo clone ${GH_REPO} r -- --depth 1 && cd r && \\
  git checkout -b ${BRANCH} && \\
  date > harness-${SAFE_NONCE}.txt && \\
  git -c user.name=crewship-harness -c user.email=harness@crewship.invalid add -A && \\
  git -c user.name=crewship-harness -c user.email=harness@crewship.invalid commit -m 'harness: secretless push probe ${SAFE_NONCE}' && \\
  ${gh_prefix}git -c credential.helper='!gh auth git-credential' push -u origin ${BRANCH} && \\
  ${gh_prefix}git -c credential.helper='!gh auth git-credential' push origin --delete ${BRANCH} && \\
  cd / && rm -rf \"\$d\"")"
  assert_no_secret_leak "git transcript does not echo the PAT" "$clone_reply"
  clone_reply="$(scrub "$clone_reply")"
  assert_contains "clone + commit + push + branch cleanup all succeeded on ${GH_REPO}" "$clone_reply" "CLONE_PUSH=OK"

  if probe_run; then
    assert_eq "no .git-credentials file exists after a push" "absent" "$(probe_get git_credentials)"
  else
    skip "post-push .git-credentials check" "the in-container probe did not run"
  fi
fi

# ─────────────────────────────────────────────────────────────────────────────
section "5. T-H6 — docker login ghcr.io with the same PAT"
# ─────────────────────────────────────────────────────────────────────────────
if [[ -z "$GH_CRED" || -z "$GH_OWNER" ]]; then
  skip "docker login ghcr.io" "needs SEED_GITHUB_TOKEN and SEED_GITHUB_TEST_REPO (for the owner)"
elif [[ "$(probe_tool docker)" != "yes" ]]; then
  skip "docker login ghcr.io" \
    "$([[ "$(probe_tool docker)" == "no" ]] \
        && echo "docker is not installed in crew $CREW_A's container (devcontainer feature not declared)" \
        || echo "the in-container probe never ran, so docker availability is unknown")"
else
  pull_cmd=""
  if [[ -n "${SECRETLESS_GHCR_IMAGE:-}" ]]; then
    pull_cmd=" && docker pull ${SECRETLESS_GHCR_IMAGE}"
  else
    info "SECRETLESS_GHCR_IMAGE not set — asserting login only, no pull"
  fi
  docker_reply="$(ask_agent "$AGENT_A" "Run this exactly in your container and paste the raw output. Do not print \
any token. Finish with a line GHCR=OK if every command exited 0, otherwise GHCR=FAIL.

  printf '%s' \"\$${GH_CRED}\" | docker login ghcr.io -u ${GH_OWNER} --password-stdin${pull_cmd}")"
  assert_no_secret_leak "docker transcript does not echo the PAT" "$docker_reply"
  docker_reply="$(scrub "$docker_reply")"
  assert_contains "docker login ghcr.io succeeded with the crew-delivered PAT" "$docker_reply" "GHCR=OK"

  # After an explicit `docker login` a config.json is expected. What matters is
  # its SHAPE: a credential-helper reference is the L2 target; a base64 "auth"
  # blob is a stored copy of the PAT. The helper shim is not built yet, so a
  # stored blob is the known gap, not a regression → xfail, loud.
  if probe_run; then
    case "$(probe_get docker_config_shape)" in
      helper-only) _pass "docker config.json holds only a credential-helper reference" ;;
      absent)      _pass "docker config.json was not created at all" ;;
      stored-auth) xfail "docker config.json holds no stored credential" \
                     "PRD L2 — the docker credential-helper shim is not built, so docker login writes a base64 auth blob" ;;
      *)           skip "docker config.json shape" "probe did not report it" ;;
    esac
  fi
fi

# ─────────────────────────────────────────────────────────────────────────────
section "6. T-H7 — git over SSH with an SSH_KEY credential"
# ─────────────────────────────────────────────────────────────────────────────
# SSH_KEY is mounted 0600 at \$CREWSHIP_SECRETS_DIR/ssh/<name> and the agent
# gets <name>_PATH pointing at it (buildCredFileScript). Read-only proof:
# `git ls-remote` against the private repo over SSH.
#
# GAP: `crewship credential create --value-stdin` reads ONE line
# (cmd_credential_mutate.go: bufio.Scanner + a single Scan), and there is no
# --value-file. A PEM is multi-line, so there is NO non-echoing CLI path to
# ingest one — --value would place the key in the process argv. We therefore
# SKIP rather than leak; this whole section goes live the moment the CLI can
# take a multi-line value.
ssh_key_value=""
if [[ -n "${SEED_GITHUB_SSH_KEY:-}" ]]; then
  if [[ -f "$SEED_GITHUB_SSH_KEY" ]]; then
    ssh_key_value="$(cat "$SEED_GITHUB_SSH_KEY")"
  else
    ssh_key_value="$SEED_GITHUB_SSH_KEY"
  fi
  # From here on the key is scrubbed out of every transcript like any other
  # configured secret.
  [[ -n "$ssh_key_value" ]] && _SECRETS+=("$ssh_key_value")
fi

if [[ -z "$ssh_key_value" || -z "$GH_REPO" ]]; then
  skip "git over SSH" "needs SEED_GITHUB_SSH_KEY (key or path) and SEED_GITHUB_TEST_REPO"
elif [[ "$(printf '%s' "$ssh_key_value" | wc -l | tr -d ' ')" != "0" ]]; then
  skip "git over SSH" \
    "FINDING: the key is multi-line and 'credential create --value-stdin' reads only the first line; there is no --value-file, and --value would expose the key in the process list"
elif [[ "$(probe_tool ssh)" != "yes" ]]; then
  skip "git over SSH" \
    "$([[ "$(probe_tool ssh)" == "no" ]] \
        && echo "ssh is not installed in crew $CREW_A's container" \
        || echo "the in-container probe never ran, so ssh availability is unknown")"
else
  if printf '%s' "$ssh_key_value" | cs credential create \
        --name "$SSH_CRED" --type SSH_KEY --provider GITHUB \
        --crews "$CREW_A" --env-var-name "$SSH_CRED" --value-stdin >/dev/null 2>&1; then
    _pass "SSH_KEY credential created and scoped to crew $CREW_A"
    ssh_reply="$(ask_agent "$AGENT_A" "Run this exactly in your container and paste the raw output. Never print the \
key. Finish with a line GIT_SSH=OK if the command exited 0, otherwise GIT_SSH=FAIL.

  GIT_SSH_COMMAND=\"ssh -i \\\"\$${SSH_CRED}_PATH\\\" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new\" \\
  git ls-remote git@github.com:${GH_REPO}.git | head -3")"
    assert_no_secret_leak "ssh transcript carries no configured secret" "$ssh_reply"
    ssh_reply="$(scrub "$ssh_reply")"
    assert_contains "git ls-remote over SSH reached the private repo" "$ssh_reply" "GIT_SSH=OK"
    assert_not_contains "the private key body never appears in the transcript" "$ssh_reply" "PRIVATE KEY"
  else
    _fail "SSH_KEY credential create" "create failed — the server rejected type SSH_KEY or the value shape"
  fi
fi

# ─────────────────────────────────────────────────────────────────────────────
section "7. T-H8 — revocation: the next exec fails"
# ─────────────────────────────────────────────────────────────────────────────
# The strongest single assertion in this file. If ANY dried copy existed — in a
# helper store, a config file, a warm container's env — `gh` would keep working
# after the credential is gone. This is a hard failure, not a soft one: a
# revocation that does not revoke is the security claim collapsing, and there
# is no known-gap exemption to hide behind.
if [[ -z "$GH_CRED" ]] || (( GH_TOOL_OK == 0 )); then
  skip "revocation stops the next exec" "no usable GitHub credential in this run"
else
  if cs credential delete "$GH_CRED" --yes >/dev/null 2>&1; then
    _pass "credential delete $GH_CRED (revoke)"
    GH_CRED_DELETED="$GH_CRED"
    still="$(cs credential list --all --format json 2>/dev/null | jq -r --arg n "$GH_CRED_DELETED" '[.[]|select(.name==$n)]|length')"
    assert_eq "revoked credential is gone from the vault listing" "0" "$still"

    # Two attempts: the first right away, the second after a pause, so a slow
    # credstore reap reports as "late" rather than as a phantom pass/fail.
    revoked_ok=0
    for attempt in 1 2; do
      rev_reply="$(ask_agent "$AGENT_A" "Run exactly '${gh_prefix}gh auth status' in your container. Paste the raw \
output, then on its own last line print REVOKED_EXEC=FAIL if the command exited NON-ZERO, or REVOKED_EXEC=OK if it \
exited 0.")"
      assert_no_secret_leak "post-revocation output carries no configured secret" "$rev_reply"
      rev_reply="$(scrub "$rev_reply")"
      if printf '%s' "$rev_reply" | grep -qF 'REVOKED_EXEC=FAIL'; then revoked_ok=1; break; fi
      (( attempt == 1 )) && { info "still authenticating — waiting ${POLL_TIMEOUT}s for the credential reap…"; sleep "$POLL_TIMEOUT"; }
    done
    if (( revoked_ok == 1 )); then
      _pass "after revocation the agent's next gh exec FAILS (no dried copy anywhere)"
    else
      _fail "after revocation the agent's next gh exec fails" \
        "gh still authenticated after the credential was deleted — a copy survived (warm container env, helper store or on-disk file)"
    fi
    GH_CRED=""   # already gone; keep the exit trap from deleting it twice
  else
    _fail "credential delete $GH_CRED" "delete failed — cannot test revocation"
  fi
fi

# ─────────────────────────────────────────────────────────────────────────────
section "8. T-H9 — cross-crew isolation"
# ─────────────────────────────────────────────────────────────────────────────
# The canary is scoped to crew A only. An agent in crew B must not see it in
# its environment, and it must not exist anywhere on crew B's filesystem. This
# section needs no external accounts, so it runs on every invocation.
if [[ -z "$CREW_B" || -z "$AGENT_B" ]]; then
  skip "cross-crew isolation" "no second crew with an agent"
else
  b_reply="$(ask_agent "$AGENT_B" "Run exactly this in your container and paste the raw output on a line starting \
with CANARY=. If the variable is unset, reply exactly CANARY=UNSET.

  ${fp_cmd}")"
  assert_no_secret_leak "crew B output carries no configured secret" "$b_reply"
  b_reply="$(scrub "$b_reply")"
  assert_not_contains "crew B's agent does NOT hold crew A's credential" "$b_reply" "$CANARY_FP"

  # Filesystem half — same probe, crew B's container. Needs its own delivery of
  # the probe + a routine owned by crew B (routine script paths resolve against
  # the AUTHOR crew's shared volume).
  B_SLUG="${ROUTINE_SLUG}-b"
  if cs crew files save "$CREW_B" shared/scripts/secretless-probe.sh --file "$PROBE_LOCAL" >/dev/null 2>&1 \
     && sed "s/\"name\":\"$ROUTINE_SLUG\"/\"name\":\"$B_SLUG\"/" "$PROBE_DSL" > "$PROBE_DSL.b" \
     && cs routine save --name "$B_SLUG" --description "secretless zero-disk probe (harness, crew B)" \
          --definition "$PROBE_DSL.b" --author-crew "$CREW_B" >/dev/null 2>&1; then
    b_out="$(cs routine run "$B_SLUG" 2>/dev/null)"
    b_hits="$(printf '%s\n' "$b_out" | sed -n 's/^.*SLPROBE canary_hits=//p' | head -1 | tr -d '\r')"
    if [[ -z "$b_out" ]] || ! printf '%s' "$b_out" | grep -q 'SLPROBE probe_done=yes'; then
      skip "crew B's filesystem holds no copy of crew A's credential" "the probe did not run in crew $CREW_B"
    elif [[ "$b_hits" == "none" || -z "$b_hits" ]]; then
      _pass "crew B's filesystem holds no copy of crew A's credential"
    else
      _fail "crew B's filesystem holds no copy of crew A's credential" "found in: $b_hits"
    fi
    cs routine delete "$B_SLUG" --yes >/dev/null 2>&1 || true
    rm -f "$PROBE_DSL.b"
  else
    skip "crew B filesystem isolation check" "could not deliver the probe to crew $CREW_B"
  fi
fi

info "Cleanup: harness credentials are prefixed HARNESS_ (plus GH_TOKEN on a clean slot) and are deleted on exit."
info "The probe file stays at /crew/shared/scripts/secretless-probe.sh — 'crew files' has no delete verb; it is inert and overwritten each run."

finish
