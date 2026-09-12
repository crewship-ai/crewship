package orchestrator

// Execution-environment scaffolding extracted from orchestrator_lifecycle.go.
// Covers MCP stdio domain resolution (used to build the per-crew network
// egress allowlist) and the tmux-wrapped exec setup (cache + writer of
// args / env / inner-script files inside the crew container).
//
// All function signatures and receivers are unchanged; this is a pure
// file move.

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// execProbeTimeout bounds waiting for a short probe exec to report its status.
// Generous for a `command -v`, short enough that a wedged runtime cannot hold a
// run open.
const execProbeTimeout = 30 * time.Second

func mcpStdioDomains(servers []MCPServerConfig) []string {
	seen := make(map[string]bool)
	for _, s := range servers {
		if s.Transport != "stdio" || !knownPackageLaunchers[s.Command] {
			continue
		}
		// Find the first non-flag arg — that's the package name.
		for _, arg := range s.Args {
			if strings.HasPrefix(arg, "-") {
				continue // skip flags like -y, --quiet, dlx
			}
			pkg := normalizeNPMPackage(arg)
			if domains, ok := mcpPackageDomains[pkg]; ok {
				for _, d := range domains {
					seen[d] = true
				}
			}
			break // only the first non-flag arg is the package
		}
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// npmSpecRe strips @version suffix from scoped and unscoped npm packages.
// "@scope/pkg@1.0.0" → "@scope/pkg", "pkg@latest" → "pkg", "-y" → "-y"
var npmSpecRe = regexp.MustCompile(`^(@[^/]+/[^@]+|[^@]+)(?:@.+)?$`)

func normalizeNPMPackage(arg string) string {
	m := npmSpecRe.FindStringSubmatch(arg)
	if len(m) > 1 {
		return m[1]
	}
	return arg
}

// TmuxSessionPrefix returns the prefix every tmux session of one agent shares:
// "agent-<slug>-". Two concurrent runs of the same agent share this and differ
// only in the run id that follows it, so a caller holding just a slug can list
// that agent's live sessions (`tmux list-sessions -F '#{session_name}'`, then
// filter on this prefix) and see how many runs are actually up.
//
// The trailing "-" is load-bearing: without it "agent-eva" would also prefix
// "agent-eva2-<run>", and a hard stop or an attach aimed at one agent could
// name another agent's session.
func TmuxSessionPrefix(agentSlug string) string {
	return "agent-" + agentSlug + "-"
}

// TmuxSessionName returns the tmux session name for ONE RUN of an agent:
// "agent-<slug>-<runID>".
//
// It was "agent-<slug>" before E0, which made the session — and every /tmp
// file setupTmuxExec derives from it — a mutable key shared by every run of
// that agent. Starting run B then killed run A outright (setupTmuxExec's
// unconditional opening `tmux kill-session`), and before that it overwrote A's
// args and env files, so A's own wrapper could exec B's argv with B's
// credentials. See docs/prd/WEBHOOKS-AGENT-PARALLELISM-MEMORY-1-0-2026-09-10.md
// §4: "Opětovné použití agent slug jako mutable runtime klíče je chyba."
//
// runID must satisfy ValidRunID (run_identity.go) — the result is single-quoted
// into `sh -c` scripts and interpolated into /tmp paths. Callers inside a run
// get that for free: ensureRunID checks it before anything derives a name.
// An EMPTY runID yields the obviously-wrong "agent-<slug>-", which still cannot
// collide with any real run's session; it is not a supported input.
func TmuxSessionName(agentSlug, runID string) string {
	return TmuxSessionPrefix(agentSlug) + runID
}

// tmuxCacheLookup returns the cached tmux-present value for containerID and
// whether the cache held an entry.
func (o *Orchestrator) tmuxCacheLookup(containerID string) (bool, bool) {
	o.tmuxCacheMu.RLock()
	defer o.tmuxCacheMu.RUnlock()
	v, ok := o.tmuxCache[containerID]
	return v, ok
}

// tmuxCacheStore records whether containerID has tmux installed. A size cap
// (tmuxCacheMaxEntries) prevents unbounded growth on long-running crewshipd
// processes that churn containers (recreate on config change, TTL cycle,
// etc.). On overflow the entire cache is flushed — cheaper than tracking
// liveness against provider state, and the worst case is a one-time re-
// probe of `command -v tmux` for each active crew (~50 ms per crew).
func (o *Orchestrator) tmuxCacheStore(containerID string, has bool) {
	o.tmuxCacheMu.Lock()
	defer o.tmuxCacheMu.Unlock()
	if len(o.tmuxCache) >= tmuxCacheMaxEntries {
		// Reset rather than evict-oldest: we do not track access time and
		// bulk clear costs nothing in Go.
		o.tmuxCache = make(map[string]bool, tmuxCacheMaxEntries)
	}
	o.tmuxCache[containerID] = has
}

// tmuxCacheMaxEntries caps the number of remembered container IDs. A busy
// workspace rarely exceeds a few dozen live containers; this cap is a safety
// net against container-ID churn leaking into long-running server memory.
const tmuxCacheMaxEntries = 1024

// InvalidateTmuxCache removes a container's cached tmux-presence entry. Called
// when a container is removed so the map does not grow unbounded across the
// lifetime of the crewshipd process (container IDs are 64 hex chars each and
// a busy workspace churns them). Safe to call for unknown IDs.
func (o *Orchestrator) InvalidateTmuxCache(containerID string) {
	o.tmuxCacheMu.Lock()
	defer o.tmuxCacheMu.Unlock()
	delete(o.tmuxCache, containerID)
}

// setupTmuxExec prepares a tmux-wrapped execution environment for one RUN of
// an agent. It writes command args, env vars, and a script to files in the
// container (avoiding shell quoting issues), then returns a wrapper command
// that starts tmux and streams output via FIFO. Falls back gracefully if setup
// fails.
//
// EVERY name it derives — session, args, env, script, FIFO, exit file and the
// tmux wait-for channel — is scoped by (agentSlug, runID), not by agentSlug
// alone. That is the E0 fix: before it, two runs of one agent shared all seven,
// so B's setup overwrote A's argv and A's exported credentials, A's wrapper
// read B's exit code, and B's opening `tmux kill-session` ended A's session
// outright. The kill-session is still here and still unconditional — it just
// names this run's own session now, which no other run can be using.
func (o *Orchestrator) setupTmuxExec(ctx context.Context, containerID string, cmd []string, agentSlug, runID string, env []string) ([]string, error) {
	// Fail closed rather than build a path out of an unchecked id. In a real
	// run ensureRunID has already vetted this, so reaching here means a caller
	// built an ExecConfig outside RunAgent — the one case where a bad id would
	// otherwise reach a shell.
	if !ValidRunID(runID) {
		return nil, fmt.Errorf("invalid run id for tmux session: %q", runID)
	}
	// Pre-check: fail fast if tmux is not installed in the container. Custom
	// base images (debian:bookworm-slim, ubuntu:24.04) don't ship with tmux.
	// Without this check, the outer wrapper runs anyway and produces noisy
	// stderr output before falling back, which confuses users.
	//
	// Result is cached per container — tmux presence is fixed once the image
	// is built, so repeating the probe on every run (every agent message) was
	// a 50 ms tax for no information. Cache is invalidated naturally when the
	// container is recreated with a new ID.
	if has, ok := o.tmuxCacheLookup(containerID); ok {
		if !has {
			return nil, fmt.Errorf("tmux not installed in container")
		}
	} else {
		checkResult, checkErr := o.container.Exec(ctx, provider.ExecConfig{
			ContainerID: containerID,
			Cmd:         []string{"sh", "-c", "command -v tmux >/dev/null 2>&1"},
			User:        "1001:1001",
		})
		if checkErr != nil {
			return nil, fmt.Errorf("tmux check: %w", checkErr)
		}
		io.Copy(io.Discard, checkResult.Reader)
		checkResult.Reader.Close()
		// Wait for the probe to actually exit before reading its status: the
		// result is CACHED for the container's lifetime, so one inspect/EOF
		// race would mark tmux missing forever (#1779).
		tmuxExitCode, inspectErr := provider.WaitExecExit(ctx, o.container, checkResult.ExecID, execProbeTimeout)
		if inspectErr != nil {
			return nil, fmt.Errorf("tmux check inspect: %w", inspectErr)
		}
		has := tmuxExitCode == 0
		o.tmuxCacheStore(containerID, has)
		if !has {
			return nil, fmt.Errorf("tmux not installed in container")
		}
	}

	// One run-scoped stem for all six file names and the wait-for channel, so
	// none of them can drift back to being slug-keyed one at a time.
	session := TmuxSessionName(agentSlug, runID)
	argsFile := fmt.Sprintf("/tmp/%s.args", session)
	scriptFile := fmt.Sprintf("/tmp/%s.sh", session)
	fifo := fmt.Sprintf("/tmp/%s.fifo", session)
	exitFile := fmt.Sprintf("/tmp/%s.exit", session)
	doneSignal := session + "-done"
	envFile := fmt.Sprintf("/tmp/%s.env", session)

	// Compute the three file payloads up front. They are independent, so a
	// single batched exec (below) writes all of them in one round-trip rather
	// than three sequential ExecCreate+ExecStart+attach+drain cycles — this is
	// the hottest path (every agent message). Mirrors the batched-write idiom
	// in exec_sidecar.go's prepMemoryDirs.

	// Args: null-separated command args.
	var argsBuf []byte
	for _, arg := range cmd {
		argsBuf = append(argsBuf, []byte(arg)...)
		argsBuf = append(argsBuf, 0)
	}
	argsEncoded := base64.StdEncoding.EncodeToString(argsBuf)

	// Env: sourceable shell script of `export KEY='VALUE'` lines.
	var envScript strings.Builder
	for _, e := range env {
		if idx := strings.IndexByte(e, '='); idx > 0 {
			key := e[:idx]
			// Only allow safe env var names ([A-Za-z_][A-Za-z0-9_]*) to prevent
			// shell injection via crafted key names in the sourced export script.
			safe := true
			for i, c := range key {
				if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_' || (i > 0 && c >= '0' && c <= '9')) {
					safe = false
					break
				}
			}
			if !safe || len(key) == 0 {
				continue
			}
			val := e[idx+1:]
			escaped := strings.ReplaceAll(val, "'", "'\\''")
			envScript.WriteString(fmt.Sprintf("export %s='%s'\n", key, escaped))
		}
	}
	envEncoded := base64.StdEncoding.EncodeToString([]byte(envScript.String()))

	// Inner script: source env, DELETE the env file, then run the command via
	// xargs and delete the args file once xargs has consumed it.
	//
	// The env file holds this run's credentials (`export ANTHROPIC_API_KEY=…`),
	// so the delete is on the line immediately after the source, with nothing
	// between them — every command in that gap is a window in which an
	// abandoned run leaves credentials on disk.
	//
	// Why the file exists at all, rather than ExecConfig.Env: a crew container
	// may already be running a tmux SERVER started by a neighbouring agent,
	// and a session started on an existing server does not inherit the
	// client's environment. Dropping the file would therefore risk a run
	// picking up a neighbour's environment — the exact bleed this whole change
	// exists to stop.
	//
	// Why the delete has to be explicit NOW, when it never was before: with
	// slug-keyed names the residue was invisible, because the next run of the
	// same agent overwrote the abandoned file — "cleaning up" by handing one
	// run's credentials to the next. Run-scoped names removed that accident
	// along with the bug, so nothing reclaims the file implicitly any more.
	//
	// And why here rather than only in the wrapper's exit path: the wrapper's
	// trailing `rm -f` does not run when the exec is killed (a hard stop, a
	// container stop, an OOM). This line runs before the agent's first
	// instruction, so the credentials are already gone by the time anything
	// can kill it. The wrapper's own cleanup stays as a belt-and-braces pass —
	// `rm -f` on an already-unlinked file is a no-op that exits 0.
	scriptContent := fmt.Sprintf("#!/bin/sh\n. '%s'\nrm -f '%s'\n"+
		"EX=0\nxargs -0 stdbuf -oL < '%s' > '%s' 2>&1 || EX=$?\nrm -f '%s'\necho $EX > '%s'\nrm -f '%s'\ntmux wait-for -S '%s'\n",
		envFile, envFile, argsFile, fifo, argsFile, exitFile, fifo, doneSignal)
	scriptEncoded := base64.StdEncoding.EncodeToString([]byte(scriptContent))

	// Single batched write: decode all three files and chmod the script in one
	// exec. Base64 output is alphanumerics plus '+', '/' and '=' — never a
	// single quote — so single-quoting each payload is safe. `&&` chains the
	// steps so any decode failure aborts the rest, and the returned exec error
	// surfaces the same kind of wrapped error any single write would have.
	writeCmd := fmt.Sprintf(
		"printf '%%s' '%s' | base64 -d > '%s' && "+
			"printf '%%s' '%s' | base64 -d > '%s' && "+
			"printf '%%s' '%s' | base64 -d > '%s' && chmod +x '%s'",
		argsEncoded, argsFile,
		envEncoded, envFile,
		scriptEncoded, scriptFile, scriptFile)
	writeResult, err := o.container.Exec(ctx, provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", writeCmd},
		User:        "1001:1001",
	})
	if err != nil {
		return nil, fmt.Errorf("write tmux exec files: %w", err)
	}
	io.Copy(io.Discard, writeResult.Reader)
	writeResult.Reader.Close()

	// Step 4: Return outer wrapper. Uses session-scoped kill (not kill-server)
	// to avoid disrupting other agent sessions in the same crew container.
	// If tmux new-session fails, falls back to direct exec via sh.
	wrapper := fmt.Sprintf(
		"tmux kill-session -t '%s' 2>/dev/null; rm -f '%s' '%s'; mkfifo '%s'; "+
			"if tmux new-session -d -s '%s' -x 200 -y 50 'sh %s'; then "+
			"cat '%s' 2>/dev/null; "+
			"tmux wait-for '%s' 2>/dev/null || true; "+
			"else sh '%s'; fi; "+
			"EC=0; [ -f '%s' ] && EC=$(cat '%s') && rm -f '%s'; "+
			"rm -f '%s' '%s' '%s'; exit $EC",
		session, fifo, exitFile, fifo,
		session, scriptFile,
		fifo,
		doneSignal,
		scriptFile,
		exitFile, exitFile, exitFile,
		scriptFile, argsFile, envFile,
	)
	return []string{"sh", "-c", wrapper}, nil
}
