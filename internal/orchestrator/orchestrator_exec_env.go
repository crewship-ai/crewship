package orchestrator

// Execution-environment scaffolding extracted from orchestrator_lifecycle.go.
// Covers MCP stdio domain resolution (used to build the per-crew network
// egress allowlist) and the tmux-wrapped exec setup (cache + writer of
// args / env / inner-script files inside the crew container).
//

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/managedlaunch"
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
	return o.setupTmuxExecAt(ctx, containerID, cmd, agentSlug, runID, env, tmuxExecDir(agentSlug, runID))
}

// setupTmuxExecAt shares the production bootstrap with isolated host fixtures.
// Production always supplies the canonical per-run HOME child above; no agent
// environment or request field selects an arbitrary cleanup directory.
func (o *Orchestrator) setupTmuxExecAt(ctx context.Context, containerID string, cmd []string, agentSlug, runID string, env []string, privateDir string) ([]string, error) {
	// Fail closed rather than build a path out of an unchecked id. In a real
	// run ensureRunID has already vetted this, so reaching here means a caller
	// built an ExecConfig outside RunAgent — the one case where a bad id would
	// otherwise reach a shell.
	if !ValidRunID(runID) || !validSlugRe.MatchString(agentSlug) {
		return nil, fmt.Errorf("invalid agent slug or run id for tmux session")
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
			Cmd:         []string{"sh", "-c", "command -v tmux >/dev/null 2>&1 && /usr/bin/env -i PATH=" + managedlaunch.SafePath + " /bin/sh -c 'command -v awk >/dev/null 2>&1 && stdbuf -oL setsid --wait /bin/true'"},
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

	// Bootstrap files live inside the HOME this run already owns. Existing
	// run-home settlement therefore covers failure/cancellation without guessing
	// ownership of a separate predictable /tmp directory.
	session := TmuxSessionName(agentSlug, runID)
	argsFile := privateDir + "/args"
	scriptFile := privateDir + "/run.sh"
	fifo := privateDir + "/output.fifo"
	exitFile := privateDir + "/exit"
	envFile := privateDir + "/env"

	// One setup exec writes the args, fresh environment and inner script.

	// Args: null-separated command args.
	var argsBuf []byte
	for _, arg := range cmd {
		argsBuf = append(argsBuf, []byte(arg)...)
		argsBuf = append(argsBuf, 0)
	}
	argsEncoded := base64.StdEncoding.EncodeToString(argsBuf)

	// Snapshot the fresh container exec environment, including image PATH/mise
	// and this run's overrides. Export output from the same POSIX shell can be
	// sourced without serializing a secret into process arguments. An existing
	// tmux server's environment is deliberately excluded from the target CLI.

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
	// Hold the FIFO writer for the whole shell lifetime, including bootstrap
	// failure and the exit-receipt trap. EOF therefore means the receipt has
	// been written. Direct fallback streams to its caller without a FIFO.
	scriptContent := fmt.Sprintf(`#!/bin/sh
umask 077
trap 'EC=$?; /bin/rm -f "%s" "%s"; printf "%%s\n" "$EC" > "%s.tmp"; /bin/mv -f "%s.tmp" "%s"' 0
trap 'exit 143' TERM
trap 'exit 130' INT
trap 'exit 129' HUP
if [ "${1:-}" != direct ]; then exec > '%s' 2>&1; fi
. '%s'
rm -f '%s'
EX=0
xargs -0 stdbuf -oL < '%s' || EX=$?
rm -f '%s'
exit "$EX"
`, envFile, argsFile, exitFile, exitFile, exitFile, fifo, envFile, envFile, argsFile, argsFile)
	scriptEncoded := base64.StdEncoding.EncodeToString([]byte(scriptContent))

	// Refuse an existing directory instead of reusing a file or following a
	// symlink. The trap owns only the fresh directory created by this script.
	// Every payload travels on stdin; credential values never enter argv.
	writeCmd := fmt.Sprintf(
		"set -eu\numask 077\nmkdir -m 700 '%s'\n"+
			"trap \"rm -rf -- '%s'\" 0\ntrap 'exit 1' HUP INT TERM\nset -C\n"+
			"printf '%%s' '%s' | base64 -d > '%s'\n"+
			"export -p > '%s'\n"+
			"printf '%%s' '%s' | base64 -d > '%s'\nchmod 700 '%s'\n"+
			"trap - 0 HUP INT TERM\n",
		privateDir, privateDir, argsEncoded, argsFile, envFile,
		scriptEncoded, scriptFile, scriptFile)
	writeResult, err := o.container.Exec(ctx, provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"/bin/sh", "-s"},
		Stdin:       strings.NewReader(writeCmd),
		Env:         env,
		User:        "1001:1001",
	})
	if err != nil {
		return nil, fmt.Errorf("write tmux exec files: %w", err)
	}
	io.Copy(io.Discard, writeResult.Reader)
	writeResult.Reader.Close()
	if code, err := provider.WaitExecExit(ctx, o.container, writeResult.ExecID, execProbeTimeout); err != nil || code != 0 {
		return nil, fmt.Errorf("write tmux exec files did not complete successfully")
	}

	// Multiargv tmux direct-exec avoids the server's inherited default-shell.
	// Both launch paths start the target shell with a clean environment before
	// loading the fresh snapshot. Only this run's session may be stopped.
	readerPIDFile := privateDir + "/reader.pid"
	readerReady := privateDir + "/reader.ready"
	readerCommand := append([]string{"/usr/bin/env", "-i", "PATH=" + managedlaunch.SafePath}, directRunCommandWithPIDFile(readerPIDFile, []string{"/bin/sh", "-c", `exec 0< "$1"; : > "$2"; exec /bin/cat`, "crewship-tmux-reader", fifo, readerReady})...)
	quotedReader := make([]string, len(readerCommand))
	for i, arg := range readerCommand {
		quotedReader[i] = "'" + shellEscape(arg) + "'"
	}
	// The one reader uses Crewship's existing isolated-group/kernel-starttime
	// protocol, with a distinct identity inside this run's HOME. $! is wait-only.
	// Parent-owned Linux RDWR keeps the FIFO open until the reader has opened a
	// read-only descriptor and the producer has ended. No child inherits fd3.
	// An unavailable client (127) can fall back directly; an ambiguous client
	// error must not start a duplicate producer. UNKNOWN keeps the wrapper live
	// for the existing lifecycle stop/hold authority instead of cleaning HOME.
	wrapper := fmt.Sprintf(`umask 077
tmux kill-session -t '=%[1]s' 2>/dev/null
rm -f '%[4]s' '%[3]s'
mkfifo '%[4]s' || exit 1
exec 3<> '%[4]s'
NEW_STATUS=0
tmux new-session -d -s '%[1]s' -x 200 -y 50 /usr/bin/env -i /bin/sh '%[2]s' 3>&- || NEW_STATUS=$?
READER_STATUS=0
if [ "$NEW_STATUS" -eq 127 ]; then
 exec 3>&-
 /usr/bin/env -i /bin/sh '%[2]s' direct
else
 %[7]s 3>&- &
 reader=$!
 crewship_stop_reader() { ( %[8]s ) 3>&-; }
 trap 'exec 3>&-; crewship_stop_reader >/dev/null 2>&1; wait "$reader" 2>/dev/null || true' 0
 trap 'exit 143' TERM
 trap 'exit 130' INT
 trap 'exit 129' HUP
 while [ ! -f '%[9]s' ]; do
  reader_state=$( ( %[10]s ) 3>&-)
  if [ "$reader_state" = ABSENT ]; then READER_STATUS=1; break; fi
  /bin/sleep 0.05 3>&-
 done
 if [ "$READER_STATUS" -ne 0 ]; then exec 3>&-; fi
 unknown=0
 while [ ! -f '%[3]s' ]; do
  state=$({ %[6]s; } 3>&-)
  if [ -f '%[3]s' ] || [ "$state" = ABSENT ]; then break; fi
  if [ "$state" != PRESENT ] && [ "$unknown" -eq 0 ]; then
   printf 'tmux state unknown; retaining runtime ownership\n' >&2
   unknown=1
  fi
  /bin/sleep 0.05 3>&-
 done
 # The atomic receipt and a closed parent fd release/drain the established
 # reader even when the producer never executed its bootstrap script.
 exec 3>&-
 wait "$reader" 2>/dev/null || READER_STATUS=$?
 trap - 0 TERM INT HUP
fi
EC=1
if [ "$READER_STATUS" -eq 0 ] && [ -f '%[3]s' ]; then EC=$(cat '%[3]s'); fi
case "$EC" in ''|*[!0-9]*) EC=1;; esac
[ "$EC" -le 255 ] 2>/dev/null || EC=1
rm -rf -- '%[5]s'
exit "$EC"
`, session, scriptFile, exitFile, fifo, privateDir, tmuxSessionProbe(session), strings.Join(quotedReader, " "), managedDirectRunProbeAt(readerPIDFile, true), readerReady, managedDirectRunProbeAt(readerPIDFile, false))
	return []string{"sh", "-c", wrapper}, nil
}

// tmuxExecDir is inside one run's canonical HOME and shares its retained cleanup.
func tmuxExecDir(agentSlug, runID string) string {
	return agentHomeDir(agentSlug, runID) + "/.crewship-tmux"
}

// A failed client query is not evidence of absence. A live server's roster
// must confirm that this exact session is missing; otherwise retain UNKNOWN.
// Callers supply only names built from validated agent slugs and run IDs.
// The leading = disables tmux target prefix matching against neighboring runs.
//
// One failure is answerable (#5510): tmux exits its server with the last
// session, so after a run's own session ends the client reports that no
// server exists. That alone is still not absence — a removed socket file
// gives the same message while the server lives — so ABSENT additionally
// requires the container's complete process table to hold no tmux process
// at all. No server process means no session can exist on any socket.
func tmuxSessionProbe(session string) string {
	return tmuxSessionProbeAt(session, "/proc")
}

// tmuxSessionProbeAt takes the process-table root so tests can supply one.
func tmuxSessionProbeAt(session, procRoot string) string {
	return fmt.Sprintf(`if ! command -v tmux >/dev/null 2>&1; then echo NOTMUX;
elif tmux has-session -t '=%[1]s' 2>/dev/null; then echo PRESENT;
elif crewship_sessions=$(tmux list-sessions -F '#{session_name}' 2>/dev/null); then
 case "
$crewship_sessions
" in *'
%[1]s
'*) echo PRESENT;; *) echo ABSENT;; esac
else %[2]s
fi`, session, tmuxNoServerProbe(procRoot))
}

// tmuxNoServerProbe prints ABSENT only when the client found no server AND
// no tmux process is visible in a process table that hides nothing.
func tmuxNoServerProbe(procRoot string) string {
	return fmt.Sprintf(`crewship_tmux_err=$(LC_ALL=C tmux list-sessions 2>&1 >/dev/null) && crewship_tmux_err=''
 case "$crewship_tmux_err" in
 'no server running on '*|'error connecting to '*' (No such file or directory)'|'error connecting to '*' (Connection refused)')
  crewship_tmux_state=ABSENT
  if ! [ -r '%[1]s/self/mountinfo' ] || ! [ -r '%[1]s/1/comm' ]; then crewship_tmux_state=UNKNOWN; else
   while IFS= read -r crewship_line; do
    case "$crewship_line" in *' - proc '*hidepid=[!0]*|*' - proc '*subset=*) crewship_tmux_state=UNKNOWN;; esac
   done < '%[1]s/self/mountinfo'
  fi
  if [ "$crewship_tmux_state" = ABSENT ]; then
   for crewship_comm in '%[1]s'/[0-9]*/comm; do
    [ -e "$crewship_comm" ] || continue
    if IFS= read -r crewship_name < "$crewship_comm"; then
     case "$crewship_name" in tmux*) crewship_tmux_state=UNKNOWN; break;; esac
    elif [ -e "$crewship_comm" ]; then crewship_tmux_state=UNKNOWN; break; fi
   done 2>/dev/null
  fi
  echo "$crewship_tmux_state";;
 *) echo UNKNOWN;;
 esac`, procRoot)
}
