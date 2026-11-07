// How run.mjs hands a queued job its environment, and how it judges --origin.
// Pure apart from the shell the job runs in; tested in verdict.test.mjs.

/** The only variables an account file may hand to the run. */
export const ACCOUNT_VARS = ["PRD_OWNER_EMAIL", "PRD_OWNER_PASSWORD", "PRD_MEMBER_EMAIL", "PRD_MEMBER_PASSWORD", "PRD_READER_EMAIL", "PRD_READER_PASSWORD", "PRD_WORKSPACE_ID", "PRD_KEEP_WORKSPACES"]

// The account file is sourced in a subshell, so nothing it sets or exports reaches the job.
// That subshell writes only ACCOUNT_VARS, shell-quoted, into a private file the job sources
// and removes; then the run's own variables (argv between the file and "--", none of them
// secret) are exported over everything. No credential appears in any argv.
export const JOB = String.raw`f=$1; shift
if [ -n "$f" ]; then
  d=$(mktemp -d) || exit 2
  ( for k in @VARS@; do unset "$k"; done
    . "$f"
    for k in @VARS@; do
      eval "s=\${$k+x}; v=\${$k-}"
      if [ -n "$s" ]; then printf "%s='" "$k" >&3; printf '%s' "$v" | sed "s/'/'\\\\''/g" >&3; printf "'\n" >&3; fi
    done ) 3>"$d/accounts" 1>&2 || { rm -rf "$d"; echo "native walk: the account file failed" >&2; exit 2; }
  set -a; . "$d/accounts"; set +a
  rm -rf "$d"
fi
while [ "$1" != "--" ]; do export "$1"; shift; done; shift
exec "$@"`.replaceAll("@VARS@", ACCOUNT_VARS.join(" "))

/**
 * argv of the job: `sh -c JOB sh <file> K=V... -- command args...`, to run under
 * crewship-heavy or directly. `env` is the run's own settings; they win over the file.
 */
export function jobArgs(envFile, env, command, commandArgs) {
  return ["sh", "-c", JOB, "sh", envFile ?? "", ...Object.entries(env).map(([k, v]) => `${k}=${v}`), "--", command, ...commandArgs]
}

/** The run's own settings. PRD_TRACE=0: a trace records request bodies, logins included. */
export function runEnv({ path, home, browsers, origin, output, chrome, runId }) {
  return {
    PATH: path, HOME: home, PLAYWRIGHT_BROWSERS_PATH: browsers, CI: "1",
    PRD_MODE: "full", PRD_ORIGIN: origin, PRD_OUTPUT_DIR: output, NATIVE_WALK_CHROME: chrome, PRD_TRACE: "0",
    ...(runId ? { PRD_RUN_ID: runId } : {}),
  }
}

/**
 * Bare http(s) origin, as e2e/acceptance/prd/env.ts accepts it. A refusal names only
 * scheme and host: the value may carry credentials, a token in the query or a private path.
 * @returns {{ origin: string } | { error: string }}
 */
export function checkOrigin(raw) {
  let url
  try { url = new URL(raw) } catch { return { error: "--origin is not a URL" } }
  if (!/^https?:$/.test(url.protocol)) return { error: "--origin must be http(s)" }
  if (url.username || url.password || url.search || url.hash || (url.pathname && url.pathname !== "/")) return { error: `--origin must be a bare http(s) origin, got ${url.protocol}//${url.host} with more` }
  return { origin: url.origin }
}
