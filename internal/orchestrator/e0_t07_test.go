package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// T07 — "Start/cleanup/cancel B during A; auth write/remove; attach/logs.
// A pokračuje, credentials/policy/memory attribution A beze změny."
// (docs/prd/WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md §11.)
//
// This is the whole E0 cut in one shape: TWO RUNS OF ONE AGENT in ONE
// container, with B started, then cleaned up, while A is live. Every runtime
// name A owns must be untouched by both halves of that.
//
// What it does NOT do is the real-Claude leg (T06) — no live container, no
// real tmux, no CLI. It asserts on the commands the orchestrator generates and
// on the cleanup scripts it would run, which is the layer where the collisions
// actually were.

// t07Recorder captures every command the orchestrator sends to the container,
// tagged with the run that sent it, including the preflight script that rides
// on stdin (where the mkdir of HOME/output/secrets lives).
type t07Recorder struct {
	mu      sync.Mutex
	byRun   map[string][]string
	current string
}

func newT07Recorder() *t07Recorder {
	return &t07Recorder{byRun: map[string][]string{}}
}

func (rec *t07Recorder) container() *mockContainer {
	return &mockContainer{
		execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
			line := strings.Join(cfg.Cmd, " ")
			if cfg.Stdin != nil {
				if body, err := io.ReadAll(cfg.Stdin); err == nil {
					line += "\n" + string(body)
				}
			}
			if cfg.WorkingDir != "" {
				line += "\nWORKDIR=" + cfg.WorkingDir
			}
			for _, e := range cfg.Env {
				line += "\nENV=" + e
			}
			rec.mu.Lock()
			rec.byRun[rec.current] = append(rec.byRun[rec.current], line)
			rec.mu.Unlock()
			return &provider.ExecResult{ExecID: "e", Reader: io.NopCloser(strings.NewReader(preflightDoneMarker + "\n"))}, nil
		},
	}
}

func (rec *t07Recorder) setRun(runID string) {
	rec.mu.Lock()
	rec.current = runID
	rec.mu.Unlock()
}

func (rec *t07Recorder) textFor(runID string) string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return strings.Join(rec.byRun[runID], "\n")
}

// t07Paths is every runtime name one run of an agent owns. Held as a table so
// a new per-run path added later shows up as a missing row rather than as a
// silent cross-run collision.
func t07Paths(slug, runID string) map[string]string {
	session := TmuxSessionName(slug, runID)
	return map[string]string{
		"tmux session": session,
		"args file":    "/tmp/" + session + ".args",
		"env file":     "/tmp/" + session + ".env",
		"inner script": "/tmp/" + session + ".sh",
		"FIFO":         "/tmp/" + session + ".fifo",
		"exit file":    "/tmp/" + session + ".exit",
		"wait-for":     session + "-done",
		"HOME":         agentHomeDir(slug, runID),
		"output dir":   agentRunOutputDir(slug, runID),
		"secrets dir":  agentSecretsDir(slug, runID),
		"scratch dir":  "/workspace/" + slug + "/" + runID,
	}
}

func t07Request(slug, runID, chatID string) AgentRunRequest {
	return AgentRunRequest{
		AgentID:       "agent-1",
		AgentSlug:     slug,
		RunID:         runID,
		ChatID:        chatID,
		CrewID:        "crew-1",
		WorkspaceID:   "ws-1",
		ContainerID:   "shared-container",
		CLIAdapter:    "CLAUDE_CODE",
		UserMessage:   "work",
		TimeoutSecs:   30,
		MemoryEnabled: true,
		Credentials: []Credential{
			{ID: "c-" + runID, Type: "CLI_TOKEN", EnvVarName: "GH_TOKEN", PlainValue: "tok-" + runID},
		},
	}
}

// TestT07_StartBDuringA_LeavesAUntouched: run B's entire setup must not name a
// single runtime path belonging to run A.
func TestT07_StartBDuringA_LeavesAUntouched(t *testing.T) {
	const slug = "eva"
	rec := newT07Recorder()
	o := New(rec.container(), newMemState(), slog.Default())

	rec.setRun("run-a")
	if err := o.RunAgent(context.Background(), t07Request(slug, "run-a", "chat-a"), nil); err != nil {
		t.Fatalf("run A: %v", err)
	}
	rec.setRun("run-b")
	if err := o.RunAgent(context.Background(), t07Request(slug, "run-b", "chat-b"), nil); err != nil {
		t.Fatalf("run B: %v", err)
	}

	fromB := rec.textFor("run-b")
	for name, aPath := range t07Paths(slug, "run-a") {
		if strings.Contains(fromB, aPath) {
			t.Errorf("starting run B names run A's %s (%q)", name, aPath)
		}
	}
	// Positive half, so the negatives above cannot pass vacuously against a
	// build that dropped run scoping altogether.
	for name, bPath := range t07Paths(slug, "run-b") {
		if !strings.Contains(fromB, bPath) {
			t.Errorf("run B never named its own %s (%q)", name, bPath)
		}
	}
}

// The credential half. Run B writes ITS credentials to ITS directory; before
// per-run secrets both runs wrote to /secrets/<slug> and B's write replaced
// A's files in place, so A carried on using B's credentials.
func TestT07_CredentialsAreNotOverwrittenAcrossRuns(t *testing.T) {
	const slug = "eva"
	rec := newT07Recorder()
	o := New(rec.container(), newMemState(), slog.Default())

	rec.setRun("run-a")
	if err := o.RunAgent(context.Background(), t07Request(slug, "run-a", "chat-a"), nil); err != nil {
		t.Fatalf("run A: %v", err)
	}
	rec.setRun("run-b")
	if err := o.RunAgent(context.Background(), t07Request(slug, "run-b", "chat-b"), nil); err != nil {
		t.Fatalf("run B: %v", err)
	}

	fromA, fromB := rec.textFor("run-a"), rec.textFor("run-b")
	aDir, bDir := agentSecretsDir(slug, "run-a"), agentSecretsDir(slug, "run-b")

	if !strings.Contains(fromA, aDir) || !strings.Contains(fromB, bDir) {
		t.Fatal("each run must write its credentials into its own secrets directory")
	}
	if strings.Contains(fromB, aDir) {
		t.Errorf("run B wrote into run A's secrets directory %q", aDir)
	}
	// CREWSHIP_SECRETS_DIR is what the agent is told to read; the two runs must
	// be pointed at different directories.
	if !strings.Contains(fromA, "ENV=CREWSHIP_SECRETS_DIR="+aDir) {
		t.Errorf("run A's CREWSHIP_SECRETS_DIR is not its own directory")
	}
	if !strings.Contains(fromB, "ENV=CREWSHIP_SECRETS_DIR="+bDir) {
		t.Errorf("run B's CREWSHIP_SECRETS_DIR is not its own directory")
	}
}

// The cleanup half: ending B removes B's directories and NAMES NONE OF A'S.
// This is the case that made the pre-E0 cleanup actively dangerous once the
// directories became per-run — `rm -rf /secrets/<slug>` would have deleted a
// live sibling's credentials.
func TestT07_CleanupOfBLeavesAsDirectoriesAlone(t *testing.T) {
	const slug = "eva"

	secretsB := buildSecretsCleanupScript(slug, "run-b")
	homeB := buildRunHomeCleanupScript(slug, "run-b")
	if secretsB == "" || homeB == "" {
		t.Fatal("cleanup scripts must be buildable for a valid slug and run id")
	}

	for name, script := range map[string]string{"secrets": secretsB, "home": homeB} {
		t.Run(name, func(t *testing.T) {
			// It removes B's own directory...
			var wantB string
			if name == "secrets" {
				wantB = agentSecretsDir(slug, "run-b")
			} else {
				wantB = agentHomeDir(slug, "run-b")
			}
			if !strings.Contains(script, wantB) {
				t.Errorf("B's %s cleanup does not remove %q: %s", name, wantB, script)
			}
			// ...and neither A's...
			if strings.Contains(script, "run-a") {
				t.Errorf("B's %s cleanup names run A: %s", name, script)
			}
			// ...nor the agent-wide parent, which is what would take A with it.
			for _, parent := range []string{"'/secrets/" + slug + "'", "'/crew/runs/" + slug + "'"} {
				if strings.Contains(script, parent) {
					t.Errorf("B's %s cleanup removes the agent-wide directory %s, which would "+
						"delete live run A's files: %s", name, parent, script)
				}
			}
		})
	}
}

// Memory attribution: A and B must resolve $HOME/.memory to the SAME directory
// — the agent's durable memory tree. Per-run HOMEs must not give each run its
// own memory, and the run-home cleanup must remove the symlink, not the tree.
func TestT07_MemoryIsSharedAcrossRunsAndSurvivesCleanup(t *testing.T) {
	const slug = "eva"
	setupA := runHomeSetupScript(slug, "run-a")
	setupB := runHomeSetupScript(slug, "run-b")

	shared := agentSharedMemoryDir(slug)
	if shared != "/crew/agents/"+slug+"/.memory" {
		t.Fatalf("shared memory dir = %q, want the agent's durable /crew/agents/<slug>/.memory", shared)
	}
	// Both runs link the SAME target...
	for name, script := range map[string]string{"A": setupA, "B": setupB} {
		if !strings.Contains(script, shared) {
			t.Errorf("run %s does not link the shared memory tree %q: %s", name, shared, script)
		}
	}
	// ...at their OWN HOMEs.
	if !strings.Contains(setupA, agentHomeDir(slug, "run-a")+"/.memory") {
		t.Error("run A does not place .memory inside its own HOME")
	}
	if strings.Contains(setupB, "run-a") {
		t.Errorf("run B's HOME setup names run A: %s", setupB)
	}

	// The cleanup removes the run's HOME, which contains only a SYMLINK to the
	// memory tree. `rm -rf` never follows a symlink to a directory, so the
	// agent's memory survives every run that borrowed it. If .memory were
	// copied or bind-mounted instead, this would be a data-loss bug.
	cleanup := buildRunHomeCleanupScript(slug, "run-a")
	if strings.Contains(cleanup, shared) {
		t.Errorf("the run-home cleanup names the shared memory tree directly: %s", cleanup)
	}
	if !strings.Contains(cleanup, agentHomeDir(slug, "run-a")) {
		t.Errorf("the run-home cleanup does not remove the run's HOME: %s", cleanup)
	}
}

// Cancelling B targets B's session and never A's. The orchestrator's own
// wrapper is the check: B's opening `tmux kill-session` — still unconditional
// — must name B.
func TestT07_CancellingBDoesNotNameA(t *testing.T) {
	const slug = "eva"
	o, rc := newTmuxExecOrchestrator()

	wrapperA, err := o.setupTmuxExec(context.Background(), "shared-container",
		[]string{"claude", "-p", "A"}, slug, "run-a", []string{"ANTHROPIC_API_KEY=key-A"})
	if err != nil {
		t.Fatalf("setup A: %v", err)
	}
	nA := len(rc.recorded())
	wrapperB, err := o.setupTmuxExec(context.Background(), "shared-container",
		[]string{"claude", "-p", "B"}, slug, "run-b", []string{"ANTHROPIC_API_KEY=key-B"})
	if err != nil {
		t.Fatalf("setup B: %v", err)
	}

	fromB := strings.Join(append(rc.recorded()[nA:], strings.Join(wrapperB, " ")), "\n")
	if strings.Contains(fromB, TmuxSessionName(slug, "run-a")) {
		t.Errorf("run B's setup names run A's session:\n%s", fromB)
	}
	if !strings.Contains(strings.Join(wrapperB, " "), "tmux kill-session -t '"+TmuxSessionName(slug, "run-b")+"'") {
		t.Error("run B's wrapper does not kill its OWN session first")
	}
	// A's wrapper still reads A's own exit file, so A's outcome is its own.
	if !strings.Contains(strings.Join(wrapperA, " "), "/tmp/"+TmuxSessionName(slug, "run-a")+".exit") {
		t.Error("run A's wrapper lost its own exit file")
	}
	// And A's credentials never appear anywhere in B's setup.
	if strings.Contains(fromB, "key-A") {
		t.Error("run A's credential value appears in run B's setup")
	}
}

// The run-end notification: ending B tells the sidecar B is over, using B's own
// token, and names no other run.
func TestT07_RunEndNotificationIsScopedAndSecretSafe(t *testing.T) {
	tokB := "agtv2.dGVzdA.dGVzdA.cnVuLWI.deadbeefcafe"
	script := sidecarRunEndScript(tokB)
	if script == "" {
		t.Fatal("a run with a token must produce a notification")
	}
	if !strings.Contains(script, tokB) {
		t.Error("the notification does not carry the run's token, so the sidecar cannot tell which run ended")
	}
	// The token must ride a heredoc on fd 3, never an argument: this container
	// is shared with every other agent in the crew and /proc/<pid>/cmdline is
	// world-readable. Same rule the agent preamble gives agents.
	if strings.Contains(script, "-H ") || strings.Contains(script, "--header ") {
		t.Errorf("the run's bearer token is passed as a curl argument, which puts it in "+
			"/proc/<pid>/cmdline for every sibling agent to read:\n%s", script)
	}
	if !strings.Contains(script, "-K /dev/fd/3") {
		t.Errorf("the notification does not read its credentials from fd 3:\n%s", script)
	}
	// Best-effort: a missing curl or a stopped sidecar must not fail the exec
	// that also removes the run's HOME.
	if !strings.Contains(script, "|| true") {
		t.Errorf("the notification is not best-effort; a stopped sidecar would fail the cleanup exec:\n%s", script)
	}
	// No token, no notification — rather than an unauthenticated call.
	if sidecarRunEndScript("") != "" {
		t.Error("a run with no token must send no notification rather than an anonymous one")
	}
}

// The cleanup exec that carries it must deliver the script on STDIN, for the
// same reason: argv is public.
func TestT07_CleanupExecDeliversTheTokenOnStdin(t *testing.T) {
	const slug = "eva"
	var mu sync.Mutex
	var argvSeen, stdinSeen []string
	mc := &mockContainer{
		execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
			mu.Lock()
			argvSeen = append(argvSeen, strings.Join(cfg.Cmd, " "))
			if cfg.Stdin != nil {
				if b, err := io.ReadAll(cfg.Stdin); err == nil {
					stdinSeen = append(stdinSeen, string(b))
				}
			}
			mu.Unlock()
			return &provider.ExecResult{ExecID: "e", Reader: io.NopCloser(strings.NewReader(""))}, nil
		},
	}
	o := New(mc, newMemState(), slog.Default())
	const tok = "agtv2.dGVzdA.dGVzdA.cnVuLWI.deadbeefcafe"
	o.cleanupRunHome("shared-container", slug, "run-b", tok)

	mu.Lock()
	defer mu.Unlock()
	argv, stdin := strings.Join(argvSeen, "\n"), strings.Join(stdinSeen, "\n")
	if strings.Contains(argv, tok) {
		t.Errorf("the run token reached the command line: %s", argv)
	}
	if !strings.Contains(stdin, tok) {
		t.Errorf("the run token never reached the script on stdin: %s", stdin)
	}
	if !strings.Contains(stdin, agentHomeDir(slug, "run-b")) {
		t.Error("the cleanup did not remove the run's HOME")
	}
	if strings.Contains(stdin, "run-a") {
		t.Errorf("run B's cleanup names run A: %s", stdin)
	}
	// Notify BEFORE removing the directory a still-draining call may be reading.
	notifyAt := strings.Index(stdin, "/agent/run/end")
	rmAt := strings.Index(stdin, "rm -rf")
	if notifyAt < 0 || rmAt < 0 || notifyAt > rmAt {
		t.Errorf("the run-end notification must precede the rm; got notify@%d rm@%d", notifyAt, rmAt)
	}
}

// The credential-REFRESH half of T07 ("auth write/remove"): a provider login
// rotated while A and B are both live must reach BOTH runs' HOMEs, and must
// not be written anywhere neither of them reads.
//
// This is the regression per-run HOME creates if it is done naively. The
// refresher exists to reach a run that is ALREADY EXECUTING; with one HOME per
// agent it had one address, and with one per run a single write goes nowhere.
// A long Codex or Gemini run would then keep presenting the token that was
// just rotated out from under it until its OAuth failed mid-run.
func TestT07_ProviderLoginRefreshReachesEveryLiveRun(t *testing.T) {
	const slug = "reviewer"
	const container = "shared-container"

	var mu sync.Mutex
	var writes []string
	mc := &mockContainer{
		execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
			mu.Lock()
			writes = append(writes, cfg.WorkingDir+" :: "+strings.Join(cfg.Cmd, " "))
			mu.Unlock()
			return &provider.ExecResult{ExecID: "e", Reader: io.NopCloser(strings.NewReader(""))}, nil
		},
	}
	login := Credential{
		ID: "cred-1", Type: "PROVIDER_LOGIN", Provider: "OPENAI", PlainValue: "access-new",
		Fields: []CredentialField{
			{Key: "id_token", Value: "id.new"},
			{Key: "account_id", Value: "acct-1"},
			{Key: "mode", Value: "subscription"},
		},
	}

	// No live run: a clean no-op. The next run renders its own copy at
	// preflight, so there is genuinely nothing to update.
	if err := DeliverProviderLogin(context.Background(), mc, container, slug, "CODEX_CLI", login, slog.Default()); err != nil {
		t.Fatalf("refresh with no live run should be a no-op, got: %v", err)
	}
	mu.Lock()
	none := len(writes)
	mu.Unlock()
	if none != 0 {
		t.Errorf("refresh wrote %d times with no live run; a credential written where nothing "+
			"reads it is worse than not writing it: %v", none, writes)
	}

	// Two runs of the SAME agent live at once.
	retainRunHome(container, slug, "run-a")
	retainRunHome(container, slug, "run-b")
	t.Cleanup(func() {
		releaseRunHome(container, slug, "run-a")
		releaseRunHome(container, slug, "run-b")
	})

	if err := DeliverProviderLogin(context.Background(), mc, container, slug, "CODEX_CLI", login, slog.Default()); err != nil {
		t.Fatalf("refresh across two live runs: %v", err)
	}

	mu.Lock()
	got := strings.Join(writes, "\n")
	mu.Unlock()
	for _, runID := range []string{"run-a", "run-b"} {
		if !strings.Contains(got, agentHomeDir(slug, runID)) {
			t.Errorf("the refreshed login never reached run %s's HOME (%s); that run keeps "+
				"authenticating with the rotated-out token:\n%s", runID, agentHomeDir(slug, runID), got)
		}
	}
	// And never into the agent's shared directory, which no run reads any more.
	if strings.Contains(got, agentSharedDir(slug)+" ") {
		t.Errorf("the refreshed login was written to the agent's shared directory, where no run "+
			"reads it:\n%s", got)
	}

	// Ending one run stops it receiving refreshes; its sibling keeps them.
	releaseRunHome(container, slug, "run-a")
	mu.Lock()
	writes = nil
	mu.Unlock()
	if err := DeliverProviderLogin(context.Background(), mc, container, slug, "CODEX_CLI", login, slog.Default()); err != nil {
		t.Fatalf("refresh after one run ended: %v", err)
	}
	mu.Lock()
	after := strings.Join(writes, "\n")
	mu.Unlock()
	if strings.Contains(after, agentHomeDir(slug, "run-a")) {
		t.Error("a finished run was handed a freshly rotated credential")
	}
	if !strings.Contains(after, agentHomeDir(slug, "run-b")) {
		t.Errorf("the still-live run stopped receiving refreshes when its sibling ended:\n%s", after)
	}
}

func TestLiveRunHomes_RegistryIsScoped(t *testing.T) {
	const c1, c2, slug = "ctr-1", "ctr-2", "eva"
	t.Cleanup(func() {
		for _, id := range []string{"r1", "r2"} {
			releaseRunHome(c1, slug, id)
			releaseRunHome(c2, slug, id)
			releaseRunHome(c1, "other", id)
		}
	})

	if got := liveRunIDsForAgent(c1, slug); len(got) != 0 {
		t.Fatalf("a fresh registry reported %v", got)
	}
	retainRunHome(c1, slug, "r2")
	retainRunHome(c1, slug, "r1")
	retainRunHome(c2, slug, "r1")
	retainRunHome(c1, "other", "r1")

	// Sorted, so delivery order is deterministic.
	if got := liveRunIDsForAgent(c1, slug); strings.Join(got, ",") != "r1,r2" {
		t.Errorf("liveRunIDsForAgent = %v, want [r1 r2] sorted", got)
	}
	// Scoped by container AND by agent — a refresh for one must not reach
	// another's run home.
	if got := liveRunIDsForAgent(c2, slug); strings.Join(got, ",") != "r1" {
		t.Errorf("container scoping leaked: %v", got)
	}
	if got := liveRunIDsForAgent(c1, "other"); strings.Join(got, ",") != "r1" {
		t.Errorf("agent scoping leaked: %v", got)
	}

	releaseRunHome(c1, slug, "r1")
	if got := liveRunIDsForAgent(c1, slug); strings.Join(got, ",") != "r2" {
		t.Errorf("after releasing r1, got %v, want [r2]", got)
	}
	// Empty input is ignored rather than creating a phantom entry.
	retainRunHome("", slug, "r1")
	retainRunHome(c1, "", "r1")
	retainRunHome(c1, slug, "")
	if got := liveRunIDsForAgent(c1, slug); strings.Join(got, ",") != "r2" {
		t.Errorf("empty-input retains polluted the registry: %v", got)
	}
}
