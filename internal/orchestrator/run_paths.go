package orchestrator

import (
	"context"
	"fmt"
	"github.com/crewship-ai/crewship/internal/provider"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
)

// Per-run container paths — E0 §4 (docs/prd/WEBHOOKS-AGENT-PARALLELISM-MEMORY-1-0-2026-09-10.md).
//
// run_identity.go gave a run its own NAME. This file gives it its own PLACES:
// a writable HOME, an output directory and a secrets directory that belong to
// one attempt and no other. Before it, all three were keyed by agent slug
// alone, so two runs of one agent shared a config file, an auth file, an
// output namespace and a credential set — B's setup overwrote A's.
//
// # What is per-run and what is deliberately NOT
//
// Per-run: HOME (and with it .claude.json, .mcp.json, MCP OAuth token files,
// skills, CLI system-prompt files, .codex/, XDG_DATA_HOME), the output
// subdirectory, and the secrets directory.
//
// SHARED, on purpose: /crew/agents/<slug>/.memory. An agent's memory is the
// one thing that must accumulate ACROSS its runs — splitting it would give
// each run amnesia and then silently diverge two copies. Concurrent writes to
// it are made safe by the memory mutation contract (internal/memory), not by
// path separation: a CAS revision check and operation-ID dedup under one lock.
// That is a real distinction and it is why this file symlinks .memory into
// each run's HOME instead of copying it — two runs resolve $HOME/.memory to
// the SAME directory, and the ledger arbitrates.
//
// Also shared: /crew/shared, /output/<slug> as a tree (the per-run directory
// is a subdirectory of it, not a replacement — four read surfaces parse that
// layout), and /secrets/shared.
//
// # Why HOME lives under /crew/runs rather than inside /crew/agents/<slug>
//
// Nesting run homes inside the agent's own directory would push them into
// every tree that already walks it: the backup collector, the memory index,
// the stranded-crew sweeper, and internal/api/credential_reconcile.go, none of
// which are asking for a run's worth of CLI config. A sibling root keeps
// /crew/agents/<slug> meaning exactly what it has always meant — the agent's
// durable state — and makes a run's cleanup a single self-contained rm.

// containerRunHomeRoot is the parent of every per-run HOME. One directory per
// agent slug beneath it, one per run beneath that.
const containerRunHomeRoot = "/crew/runs"

// agentSharedDir is the agent's DURABLE directory: /crew/agents/<slug>.
//
// This is no longer HOME (see agentHomeDir) but it is still the agent's own
// persistent state, and .memory lives here. Named separately from HOME so the
// two cannot be confused again: everything that must survive a run goes here,
// everything that must not goes in the run home.
func agentSharedDir(agentSlug string) string {
	return "/crew/agents/" + agentSlug
}

// agentSharedMemoryDir is the shared, cross-run memory tree —
// /crew/agents/<slug>/.memory. Symlinked into each run's HOME so $HOME/.memory
// resolves here for every run of this agent, concurrent ones included.
func agentSharedMemoryDir(agentSlug string) string {
	return path.Join(agentSharedDir(agentSlug), ".memory")
}

// agentHomeDir is the HOME baseAgentEnv gives one RUN of an agent:
// /crew/runs/<slug>/<runID>.
//
// It was /crew/agents/<slug> — shared by every run of that agent, on a
// persistent volume. Two concurrent runs therefore shared one .claude.json,
// one .mcp.json, one set of MCP OAuth token files and one CLI login file, and
// whichever started second overwrote the first's. A run that then refreshed
// its login wrote a token the other run's CLI would pick up.
//
// runID is assumed valid (ValidRunID); every path built here is interpolated
// into a shell command, and ensureRunID vets it at the RunAgent boundary
// before anything reaches this file.
func agentHomeDir(agentSlug, runID string) string {
	return path.Join(containerRunHomeRoot, agentSlug, runID)
}

// ContainerRunOutputSegment is the fixed directory name that separates an
// agent's own output tree from its per-run subdirectories:
// /output/<slug>/runs/<runID>.
//
// Exported because internal/server/file_journal.go matches on it to decide
// whether a watched write belongs to a run.
//
// The literal segment is what makes that decision EXACT rather than a guess.
// Without it the layout is /output/<slug>/<runID>/..., and the only way to
// tell a run directory from any other directory an agent created is to ask
// whether the segment looks like a run id — which "attachments" does. Chat
// attachments live at /output/<slug>/attachments/<chatID>/<id>/<file>
// (internal/api/proxy_attachments.go), so every uploaded file would have been
// attributed to a run called "attachments". A reserved name costs one path
// segment and removes the whole class.
const ContainerRunOutputSegment = "runs"

// agentRunOutputDir is where THIS run's artifacts land:
// /output/<slug>/runs/<runID>.
//
// Deliberately a subdirectory of the agent's existing /output/<slug> tree
// rather than a namespace of its own, because four read surfaces already
// parse that layout — the Files panel (internal/server/routes_files.go), the
// file-watcher journal attribution (internal/server/file_journal.go), chat
// attachments (internal/api/proxy_attachments.go) and the produced-files list
// (internal/api/proxy_run_files.go). A subdirectory keeps every one of them
// working unchanged AND lets file_journal.go attribute a write to the run that
// made it rather than only to the agent.
func agentRunOutputDir(agentSlug, runID string) string {
	return path.Join("/output", agentSlug, ContainerRunOutputSegment, runID)
}

// agentSecretsDir is THIS run's credential directory: /secrets/<slug>/<runID>.
//
// The refcounted retain/release in secrets_cleanup.go already handled
// same-agent overlap correctly — it is the one piece of this tree that did —
// but it was protecting the wrong thing: two overlapping runs shared the FILE
// SET, so run B's writeCredentialFiles overwrote run A's credentials in place
// and A carried on using B's. The refcount stopped the files being deleted too
// early; it could not stop them being replaced. A per-run directory does.
func agentSecretsDir(agentSlug, runID string) string {
	return path.Join("/secrets", agentSlug, runID)
}

// runHomeSetupScript builds the shell that materialises one run's HOME: make
// it, then point .memory at the agent's shared, durable memory tree.
//
// `ln -sfn` rather than a bind mount: a bind needs root and the CAP_SYS_ADMIN
// this container drops, while a symlink is created by the agent's own UID and
// resolves for every process that opens $HOME/.memory — which is the property
// the test asserts. `-n` matters: without it, a re-run against an existing
// symlinked directory would create .memory/.memory instead of replacing it.
//
// The link is created before the memory directories are (they are a later step
// in the same preflight batch, and are skipped entirely when the run has memory
// disabled), so it can be briefly or permanently DANGLING. That is deliberate
// and matches the old behaviour rather than changing it: with memory off,
// $HOME/.memory did not exist before and does not resolve now, so a read gets
// ENOENT either way. Creating the target here instead would hand a
// memory-disabled run a memory directory it is not supposed to have.
func runHomeSetupScript(agentSlug, runID string) string {
	home := agentHomeDir(agentSlug, runID)
	return fmt.Sprintf("mkdir -p %s && ln -sfn %s %s",
		shellJoin(home), shellJoin(agentSharedMemoryDir(agentSlug)), shellJoin(path.Join(home, ".memory")))
}

// ---------------------------------------------------------------------------
// Live run homes
//
// Per-run HOMEs created a problem the shared HOME did not have: a file that
// must be UPDATED mid-run no longer has one address. The provider-login
// refresher (internal/api/provider_login_refresh.go) re-renders a refreshed
// OAuth token into a running container, and before E0 there was exactly one
// place to put it. Now there is one per live run, and writing to the agent's
// shared directory would put it where nothing reads — a long Codex or Gemini
// run would keep authenticating with the token that was refreshed out from
// under it until its OAuth failed mid-run.
//
// So the orchestrator records which run homes currently exist. Package-level
// rather than a field on Orchestrator because DeliverProviderLogin is a
// package-level function called from internal/api with no Orchestrator in
// hand — and because the fact recorded is process-global anyway: which
// directories exist inside which container.
// ---------------------------------------------------------------------------

var liveRunHomes = struct {
	mu sync.Mutex
	m  map[string]map[string]struct{} // containerID|agentSlug -> set of runIDs
}{m: make(map[string]map[string]struct{})}

func runHomeKey(containerID, agentSlug string) string {
	return containerID + "|" + agentSlug
}

// retainRunHome records that a run's HOME exists. Paired with releaseRunHome,
// and called from the same place the run home is created.
func retainRunHome(containerID, agentSlug, runID string) {
	if containerID == "" || agentSlug == "" || runID == "" {
		return
	}
	liveRunHomes.mu.Lock()
	defer liveRunHomes.mu.Unlock()
	key := runHomeKey(containerID, agentSlug)
	if liveRunHomes.m[key] == nil {
		liveRunHomes.m[key] = make(map[string]struct{})
	}
	liveRunHomes.m[key][runID] = struct{}{}
}

// releaseRunHome forgets a run's HOME. Called wherever the directory is
// removed — and ALSO on the path that deliberately does not remove it (a
// detached CLI still running), because the entry's purpose is "may a refreshed
// credential still be written here", and for a run this process has finished
// accounting for, the answer is no either way.
func releaseRunHome(containerID, agentSlug, runID string) {
	if containerID == "" || agentSlug == "" || runID == "" {
		return
	}
	liveRunHomes.mu.Lock()
	defer liveRunHomes.mu.Unlock()
	key := runHomeKey(containerID, agentSlug)
	set := liveRunHomes.m[key]
	if set == nil {
		return
	}
	delete(set, runID)
	if len(set) == 0 {
		delete(liveRunHomes.m, key)
	}
}

// liveRunIDsForAgent returns the runs of agentSlug whose HOMEs exist in
// containerID, sorted for deterministic delivery order. Empty is a normal
// answer, not a failure: with no live run there is nothing to update, and the
// next run renders its own login file at preflight from the current credential.
func liveRunIDsForAgent(containerID, agentSlug string) []string {
	liveRunHomes.mu.Lock()
	defer liveRunHomes.mu.Unlock()
	set := liveRunHomes.m[runHomeKey(containerID, agentSlug)]
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// runHomeLocation finds which container and agent a live run belongs to.
//
// Present only for runs THIS process started: the registry is process-global,
// not durable. That limitation is the point of the (found bool) return — see
// RunIsAlive, where "this process has no record" has to stay distinguishable
// from "there is no such process".
func runHomeLocation(runID string) (containerID, agentSlug string, found bool) {
	if runID == "" {
		return "", "", false
	}
	liveRunHomes.mu.Lock()
	defer liveRunHomes.mu.Unlock()
	for key, set := range liveRunHomes.m {
		if _, ok := set[runID]; !ok {
			continue
		}
		i := strings.LastIndex(key, "|")
		if i < 0 {
			continue
		}
		return key[:i], key[i+1:], true
	}
	return "", "", false
}

// RunIsAlive reports whether a runtime for runID still exists.
//
// Two different "no"s have to stay apart here, and the first version of this
// collapsed them. `tmux has-session … && echo yes || echo no` prints "no" when
// the session is absent AND when tmux is missing, the container is gone, or the
// exec itself failed — so an unreachable container read as a stopped process,
// which is the licence to start a second one.
//
// The probe therefore reports tmux's own exit status, and anything that is not
// a clean present/absent answer is an error. An unknown run is an error too:
// the registry only knows runs THIS process started, so after a restart it
// knows nothing, and answering "not alive" there would let a dispatcher
// conclude that a process it cannot see is a process that does not exist. A
// caller that gets an error parks the work for reconciliation, which is true.
func (o *Orchestrator) RunIsAlive(ctx context.Context, runID string) (bool, error) {
	containerID, agentSlug, found := runHomeLocation(runID)
	if !found {
		return false, fmt.Errorf("this process has no record of run %s; whether a runtime exists for it "+
			"cannot be answered from here", runID)
	}
	session := TmuxSessionName(agentSlug, runID)
	// PRESENT / ABSENT are distinct tokens, and anything else — including an
	// empty read — is not an answer.
	probe := "if ! command -v tmux >/dev/null 2>&1; then echo NOTMUX; " +
		"elif tmux has-session -t '" + session + "' 2>/dev/null; then echo PRESENT; " +
		"else echo ABSENT; fi"
	out, err := o.probeExec(ctx, containerID, probe)
	if err != nil {
		return false, fmt.Errorf("probe run %s: %w", runID, err)
	}
	switch {
	case strings.Contains(out, "PRESENT"):
		return true, nil
	case strings.Contains(out, "ABSENT"):
		return false, nil
	case strings.Contains(out, "NOTMUX"):
		// The tmux path is the only runtime identity this profile can search
		// for. Without tmux a run may exist under the direct-exec fallback and
		// nothing here can see it, so this is a refusal rather than an absence.
		return false, fmt.Errorf("run %s: the container has no tmux, so a direct-exec runtime cannot be "+
			"located; this profile cannot answer for it", runID)
	default:
		return false, fmt.Errorf("run %s: the probe returned %q, which is neither present nor absent",
			runID, strings.TrimSpace(out))
	}
}

// StopRun signals the runtime for runID and reports whether it is now gone.
//
// It targets that run's own tmux session, never the agent: before E0 the
// session name carried only the slug, so stopping one run of an agent stopped
// every run of it. Returning (false, nil) means "asked, still there" — not a
// failure, and not something a caller may report as a stop. As with RunIsAlive,
// an unreadable probe is an error rather than a convenient "gone".
func (o *Orchestrator) StopRun(ctx context.Context, runID string) (bool, error) {
	containerID, agentSlug, found := runHomeLocation(runID)
	if !found {
		return false, fmt.Errorf("this process has no record of run %s; it cannot signal a runtime "+
			"it does not know the location of", runID)
	}
	session := TmuxSessionName(agentSlug, runID)
	probe := "if ! command -v tmux >/dev/null 2>&1; then echo NOTMUX; else " +
		"tmux kill-session -t '" + session + "' >/dev/null 2>&1; " +
		"if tmux has-session -t '" + session + "' 2>/dev/null; then echo PRESENT; else echo ABSENT; fi; fi"
	out, err := o.probeExec(ctx, containerID, probe)
	if err != nil {
		return false, fmt.Errorf("stop run %s: %w", runID, err)
	}
	switch {
	case strings.Contains(out, "ABSENT"):
		return true, nil
	case strings.Contains(out, "PRESENT"):
		return false, nil
	case strings.Contains(out, "NOTMUX"):
		return false, fmt.Errorf("run %s: the container has no tmux, so its runtime cannot be signalled "+
			"by session name", runID)
	default:
		return false, fmt.Errorf("run %s: the stop probe returned %q, which says nothing about whether "+
			"the runtime is gone", runID, strings.TrimSpace(out))
	}
}

// probeExec runs a short shell probe in a container and returns its output.
func (o *Orchestrator) probeExec(ctx context.Context, containerID, script string) (string, error) {
	res, err := o.container.Exec(ctx, provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", script},
		User:        "1001:1001",
	})
	if err != nil {
		return "", err
	}
	defer res.Reader.Close()
	out, err := io.ReadAll(res.Reader)
	if err != nil {
		return "", fmt.Errorf("read probe output: %w", err)
	}
	return string(out), nil
}
