package orchestrator

// Lifecycle, container management, tmux, and small helpers extracted from
// orchestrator.go for readability. All public function signatures are
// unchanged; this is a pure file move.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

func (o *Orchestrator) GetOrCreateContainer(ctx context.Context, crewSlug, crewID, workspaceID string) (string, error) {
	if o.container == nil {
		return "", fmt.Errorf("container provider not configured")
	}
	containerID, err := o.container.EnsureCrewRuntime(ctx, provider.CrewConfig{
		ID:   crewID,
		Slug: crewSlug,
	})
	if err != nil {
		return "", fmt.Errorf("ensure crew runtime for crew %s (workspace %s): %w", crewID, workspaceID, err)
	}
	// Register for stats streaming. Without this, the direct-run path (server
	// routes.go handleAgentStart) is the only thing that registers containers,
	// which means mission-driven runs (the overwhelming majority) produce no
	// container.stats WS events and the dashboard tile stays empty.
	o.mu.RLock()
	reg := o.statsRegister
	o.mu.RUnlock()
	if reg != nil && workspaceID != "" {
		reg(containerID, crewID, workspaceID)
	}
	return containerID, nil
}

// RunAgentForAssignment runs a sub-agent as part of a mission assignment.
// It skips conversation history injection (each task gets a clean context via the mission brief).
// SkipSidecar is respected from the caller — regular AGENT tasks skip sidecar,
// while LEAD planning tasks need sidecar for mission management API access.

func (o *Orchestrator) StopAccepting() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.accepting = false
}

// RecoverFromCrash inspects all persisted run states and marks stale runs

func (o *Orchestrator) RecoverFromCrash(ctx context.Context) error {
	runs, err := o.state.List(ctx, "agent_runs")
	if err != nil {
		return fmt.Errorf("list runs: %w", err)
	}

	for key, data := range runs {
		var run RunState
		if err := json.Unmarshal(data, &run); err != nil {
			o.logger.Warn("corrupt run state", "key", key, "error", err)
			continue
		}
		if run.Status != "running" {
			continue
		}

		if run.ExecID == "" {
			o.updateRunStatus(ctx, run.ID, "error")
			continue
		}

		running, _, err := o.container.ExecInspect(ctx, run.ExecID)
		if err != nil {
			// Transient inspect failures (Docker daemon briefly unreachable
			// during startup, container being restarted by an external
			// process, etc.) must not be collapsed with "exec finished".
			// Leave the run state alone so the next recovery pass — or the
			// run's own exec loop — can reconcile it.
			o.logger.Warn("inspect failed during crash recovery; leaving run state untouched",
				"run_id", run.ID, "exec_id", run.ExecID, "error", err)
			continue
		}
		if !running {
			o.updateRunStatus(ctx, run.ID, "completed")
			o.logger.Info("recovered stale run", "run_id", run.ID, "agent_id", run.AgentID)
		}
	}
	return nil
}

// wrapScrubHandler returns a handler that scrubs credential patterns from
// event content before forwarding to the real handler.
// When a credential pattern is detected and redacted, a system event is emitted
// so the user can see that the scrubber is active and protecting their secrets.

func (o *Orchestrator) Start(ctx context.Context) error {
	o.logger.Info("starting orchestrator container TTL manager")
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			o.checkTTLs(ctx)
		}
	}
}

func (o *Orchestrator) refreshActivity(crewID, containerID string, ttlHours int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	cs := o.crews[crewID]
	if cs == nil {
		cs = &crewState{}
		o.crews[crewID] = cs
	}
	cs.lastActivity = time.Now()
	cs.containerID = containerID
	if ttlHours > 0 {
		cs.ttl = time.Duration(ttlHours) * time.Hour
	} else {
		cs.ttl = 0
	}
}

func (o *Orchestrator) checkTTLs(ctx context.Context) {
	o.mu.Lock()
	var toStop []struct {
		crewID      string
		containerID string
	}
	now := time.Now()
	for crewID, cs := range o.crews {
		if cs.ttl <= 0 {
			continue
		}
		if now.Sub(cs.lastActivity) > cs.ttl {
			toStop = append(toStop, struct {
				crewID      string
				containerID string
			}{crewID: crewID, containerID: cs.containerID})
			delete(o.crews, crewID)
		}
	}
	o.mu.Unlock()

	for _, stop := range toStop {
		if stop.containerID == "" {
			continue
		}
		o.logger.Info("stopping idle crew container (TTL expired)", "crew_id", stop.crewID, "container_id", stop.containerID)
		if err := o.container.StopCrewRuntime(ctx, stop.containerID); err != nil {
			o.logger.Error("failed to stop idle crew container", "crew_id", stop.crewID, "error", err)
		}
	}
}

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

// TmuxSessionName returns the tmux session name for a given agent slug.

func TmuxSessionName(agentSlug string) string {
	return "agent-" + agentSlug
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

// setupTmuxExec prepares a tmux-wrapped execution environment for an agent.
// It writes command args, env vars, and a script to files in the container
// (avoiding shell quoting issues), then returns a wrapper command that starts
// tmux and streams output via FIFO. Falls back gracefully if setup fails.
func (o *Orchestrator) setupTmuxExec(ctx context.Context, containerID string, cmd []string, agentSlug string, env []string) ([]string, error) {
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
		_, tmuxExitCode, inspectErr := o.container.ExecInspect(ctx, checkResult.ExecID)
		if inspectErr != nil {
			return nil, fmt.Errorf("tmux check inspect: %w", inspectErr)
		}
		has := tmuxExitCode == 0
		o.tmuxCacheStore(containerID, has)
		if !has {
			return nil, fmt.Errorf("tmux not installed in container")
		}
	}

	session := TmuxSessionName(agentSlug)
	argsFile := fmt.Sprintf("/tmp/%s.args", session)
	scriptFile := fmt.Sprintf("/tmp/%s.sh", session)
	fifo := fmt.Sprintf("/tmp/%s.fifo", session)
	exitFile := fmt.Sprintf("/tmp/%s.exit", session)
	doneSignal := session + "-done"
	envFile := fmt.Sprintf("/tmp/%s.env", session)

	// Step 1: Write null-separated command args to file via base64.
	var argsBuf []byte
	for _, arg := range cmd {
		argsBuf = append(argsBuf, []byte(arg)...)
		argsBuf = append(argsBuf, 0)
	}
	argsEncoded := base64.StdEncoding.EncodeToString(argsBuf)
	writeArgsResult, err := o.container.Exec(ctx, provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", fmt.Sprintf("printf '%%s' '%s' | base64 -d > '%s'", argsEncoded, argsFile)},
		User:        "1001:1001",
	})
	if err != nil {
		return nil, fmt.Errorf("write args file: %w", err)
	}
	io.Copy(io.Discard, writeArgsResult.Reader)
	writeArgsResult.Reader.Close()

	// Step 2: Write env vars as sourceable shell script.
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
	envWriteResult, err := o.container.Exec(ctx, provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", fmt.Sprintf("printf '%%s' '%s' | base64 -d > '%s'", envEncoded, envFile)},
		User:        "1001:1001",
	})
	if err != nil {
		return nil, fmt.Errorf("write env file: %w", err)
	}
	io.Copy(io.Discard, envWriteResult.Reader)
	envWriteResult.Reader.Close()

	// Step 3: Write inner script (sources env, runs command via xargs).
	scriptContent := fmt.Sprintf("#!/bin/sh\n. '%s'\n"+
		"EX=0\nxargs -0 stdbuf -oL < '%s' > '%s' 2>&1 || EX=$?\necho $EX > '%s'\nrm -f '%s'\ntmux wait-for -S '%s'\n",
		envFile, argsFile, fifo, exitFile, fifo, doneSignal)
	scriptEncoded := base64.StdEncoding.EncodeToString([]byte(scriptContent))
	writeScriptResult, err := o.container.Exec(ctx, provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", fmt.Sprintf("printf '%%s' '%s' | base64 -d > '%s' && chmod +x '%s'", scriptEncoded, scriptFile, scriptFile)},
		User:        "1001:1001",
	})
	if err != nil {
		return nil, fmt.Errorf("write script file: %w", err)
	}
	io.Copy(io.Discard, writeScriptResult.Reader)
	writeScriptResult.Reader.Close()

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
