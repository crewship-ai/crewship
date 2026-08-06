package orchestrator

// Exec-count pins for the collapsed preflight (#1646).
//
// preparePreflightDirs used to issue one container exec per setup step —
// mkdir, manifest, memory dirs, crew memory dirs, migration probe, credential
// files, Claude config, MCP config, OAuth tokens, then FIVE canonical
// system-prompt files and FIVE skill files per assigned skill. Every one of
// those is >=2 daemon round-trips, paid on a warm container, before the agent
// CLI starts.
//
// These tests assert the NUMBER of execs issued for a given request shape,
// not merely that the files end up written. A test that only checked the
// resulting content would stay green if someone reintroduced twenty
// round-trips, which is the entire regression this file exists to prevent.

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// recordedExec is one Exec call as the fake provider saw it, with the stdin
// stream already drained so assertions can read the merged script.
type recordedExec struct {
	cfg   provider.ExecConfig
	stdin string
	id    string
}

// countingContainer is a provider.ContainerProvider that records every Exec
// and answers the two READ probes the preflight path makes (the sidecar
// /health curl and the skill-folder listing) with fixed output, so the exec
// count for a request shape is deterministic.
type countingContainer struct {
	mu    sync.Mutex
	execs []recordedExec
	exits map[string]int
	// stdout, when set, returns the output for a given exec — given its argv
	// and the stdin the merged preflight script rode in on. Default "".
	stdout func(cfg provider.ExecConfig, stdin string) string
	// exitFor, when set, returns the exit code for a given exec.
	exitFor func(cfg provider.ExecConfig) int
}

func (c *countingContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	var stdin string
	if cfg.Stdin != nil {
		b, _ := io.ReadAll(cfg.Stdin)
		stdin = string(b)
	}
	c.mu.Lock()
	id := "exec-" + itoa(len(c.execs))
	c.execs = append(c.execs, recordedExec{cfg: cfg, stdin: stdin, id: id})
	if c.exitFor != nil {
		if c.exits == nil {
			c.exits = map[string]int{}
		}
		c.exits[id] = c.exitFor(cfg)
	}
	c.mu.Unlock()

	out := ""
	if c.stdout != nil {
		out = c.stdout(cfg, stdin)
	}
	// A container that actually runs the merged preflight script ends it by
	// printing the completion marker, and Flush requires that as proof the
	// script was delivered at all (#1779). Appending it here keeps this fake a
	// model of a WORKING container: a fake that stayed silent would now mean
	// "the script never reached the shell", which is a different fixture and
	// belongs to the tests that deliberately model it.
	if len(cfg.Cmd) == 1 && cfg.Cmd[0] == "sh" && stdin != "" &&
		!strings.Contains(out, preflightDoneMarker) {
		out += preflightDoneMarker + "\n"
	}
	return &provider.ExecResult{ExecID: id, Reader: io.NopCloser(strings.NewReader(out))}, nil
}

func (c *countingContainer) ExecInspect(_ context.Context, execID string) (bool, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return false, c.exits[execID], nil
}

func (c *countingContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	return "container-123", nil
}
func (c *countingContainer) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (c *countingContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (c *countingContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{State: "running"}, nil
}
func (c *countingContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (c *countingContainer) CrewContainerName(id, slug string) string {
	return "crew-" + slug + "-" + id
}
func (c *countingContainer) CopyToContainer(_ context.Context, _ string, _ string, _ io.Reader) error {
	return nil
}

func (c *countingContainer) snapshot() []recordedExec {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]recordedExec, len(c.execs))
	copy(out, c.execs)
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// healthySidecarStdout answers the two read probes the preflight makes.
func healthySidecarStdout(cfg provider.ExecConfig, _ string) string {
	if len(cfg.Cmd) == 3 && strings.Contains(cfg.Cmd[2], "9119/health") {
		return `{"status":"ok","network_mode":"free"}`
	}
	// The completion marker is appended centrally by countingContainer.Exec, so
	// this helper stays a pure "is the sidecar healthy" probe — tests that
	// chain onto it rely on it returning "" for everything else.
	return ""
}

// preflightFixture builds an orchestrator wired to a countingContainer plus
// the representative request shape the counts below are pinned against.
func preflightFixture(t *testing.T) (*Orchestrator, *countingContainer, AgentRunRequest) {
	t.Helper()
	c := &countingContainer{stdout: healthySidecarStdout}
	o := New(c, newMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := AgentRunRequest{
		AgentID:       "agent-1",
		AgentSlug:     "scout",
		CrewID:        "crew-1",
		CrewSlug:      "team",
		WorkspaceID:   "ws-1",
		ContainerID:   "container-123",
		CLIAdapter:    "CLAUDE_CODE",
		MemoryEnabled: true,
		SystemPrompt:  "be helpful",
		Credentials: []Credential{
			{ID: "c1", EnvVarName: "GH_TOKEN", PlainValue: "ghp_supersecret_value", Type: "CLI_TOKEN"},
		},
	}
	return o, c, req
}

// preflightExecCountRepresentative is the pinned number of container execs a
// representative warm-container agent run pays before its CLI starts:
//
//	1  /crew/manifest.json pre-create (root — deliberately NOT merged, see
//	   preflight_batch.go; widening the merged script to root would be worse
//	   than paying one extra exec)
//	1  merged preflight script, flushed when the sidecar /health READ probe
//	   needs ordering preserved
//	1  the sidecar /health read probe itself
//	1  merged preflight script, flushed when the skill-folder READ probe
//	   needs ordering preserved
//	1  the skill-folder listing read probe
//
// Before #1646 the same shape cost nineteen. The four non-root execs are two
// merged writes and two reads; the reads are what remains to collapse.
const preflightExecCountRepresentative = 5

func TestPreparePreflightDirs_RepresentativeRequestIssuesFiveExecs(t *testing.T) {
	o, c, req := preflightFixture(t)

	if _, _, err := o.preparePreflightDirs(context.Background(), req, nil, true, false, "run-1"); err != nil {
		t.Fatalf("preparePreflightDirs: %v", err)
	}

	got := c.snapshot()
	if len(got) != preflightExecCountRepresentative {
		t.Errorf("preflight issued %d container execs, want %d — the preflight is a "+
			"per-run latency floor on every agent and every provider; each exec is "+
			">=2 daemon round-trips. If a step was legitimately added, collapse it "+
			"into the merged script rather than raising this number.",
			len(got), preflightExecCountRepresentative)
		for i, e := range got {
			t.Logf("  exec[%d] user=%q privileged=%v stdin=%v cmd=%q",
				i, e.cfg.User, e.cfg.AllowPrivileged, e.cfg.Stdin != nil, truncCmd(e.cfg.Cmd))
		}
	}
}

// TestPreparePreflightDirs_ExecCountDoesNotScaleWithSkills is the strongest
// anti-regression property here: the per-file writes must be merged, so going
// from one assigned skill to nine — forty extra file writes across five
// discovery paths each — must cost ZERO extra execs. Before #1646 it cost
// forty.
//
// The comparison starts at one skill rather than zero on purpose: a
// zero-skill run queues nothing after the prune listing, so its final flush is
// empty and issues no exec at all. That is a real one-exec difference between
// "has skills" and "has none", not a per-skill fan-out, and folding it into
// this assertion would make the test read as scaling when it is not.
func TestPreparePreflightDirs_ExecCountDoesNotScaleWithSkills(t *testing.T) {
	counts := map[int]int{}
	for _, n := range []int{1, 4, 9} {
		o, c, req := preflightFixture(t)
		for i := 0; i < n; i++ {
			req.Skills = append(req.Skills, SkillBundle{
				Slug:    "skill-" + itoa(i),
				Content: "# skill " + itoa(i),
			})
		}
		if _, _, err := o.preparePreflightDirs(context.Background(), req, nil, true, false, "run-1"); err != nil {
			t.Fatalf("preparePreflightDirs (%d skills): %v", n, err)
		}
		counts[n] = len(c.snapshot())
	}
	if counts[1] != counts[4] || counts[1] != counts[9] {
		t.Errorf("preflight exec count scales with assigned skills: 1 skill=%d, 4=%d, 9=%d "+
			"(want all equal). Per-file execs are back: every skill write must ride "+
			"the one merged script, not five round-trips per skill.",
			counts[1], counts[4], counts[9])
	}
}

// TestPreparePreflightDirs_ExecCountDoesNotScaleWithOAuthMCPServers pins the
// other per-N fan-out the issue named: injectMCPOAuthTokens wrote one to two
// execs PER OAuth MCP server, in a loop.
//
// fileCreds is false throughout, which keeps the count about the OAuth fan-out
// alone. It used to be forced: an `_OAUTH_ACCESS_TOKEN:<uuid>` credential name
// failed buildCredFileScript's envVarNameRE and aborted the batch, so a
// fileCreds run could not reach this shape at all. That was #1652, fixed —
// TestPreparePreflightDirs_OAuthMCPBindingDoesNotAbortAFileCredRun covers the
// combination.
func TestPreparePreflightDirs_ExecCountDoesNotScaleWithOAuthMCPServers(t *testing.T) {
	o, c, req := preflightFixture(t)
	req.Credentials = nil
	if _, _, err := o.preparePreflightDirs(context.Background(), req, nil, false, false, "run-1"); err != nil {
		t.Fatalf("preparePreflightDirs (no oauth): %v", err)
	}
	baseline := len(c.snapshot())

	o2, c2, req2 := preflightFixture(t)
	req2.Credentials = nil
	req2.MCPServers = []MCPServerConfig{
		{ID: "m1", Name: "linear", Transport: "http", Endpoint: "https://mcp.linear.app",
			Env: map[string]string{"LINEAR_CLIENT_ID": "${LINEAR_CLIENT_ID}"}},
		{ID: "m2", Name: "notion", Transport: "http", Endpoint: "https://mcp.notion.com",
			Env: map[string]string{"NOTION_CLIENT_ID": "${NOTION_CLIENT_ID}"}},
	}
	req2.Credentials = append(req2.Credentials,
		Credential{ID: "o1", EnvVarName: "LINEAR_CLIENT_ID", PlainValue: "cid", Type: "OAUTH2"},
		Credential{ID: "o1", EnvVarName: "_OAUTH_ACCESS_TOKEN:o1", PlainValue: "linear-access-token", Type: "OAUTH2"},
		Credential{ID: "o2", EnvVarName: "NOTION_CLIENT_ID", PlainValue: "cid2", Type: "OAUTH2"},
		Credential{ID: "o2", EnvVarName: "_OAUTH_ACCESS_TOKEN:o2", PlainValue: "notion-access-token", Type: "OAUTH2"},
	)
	if _, _, err := o2.preparePreflightDirs(context.Background(), req2, nil, false, false, "run-1"); err != nil {
		t.Fatalf("preparePreflightDirs (2 oauth servers): %v", err)
	}
	withOAuth := len(c2.snapshot())

	if withOAuth != baseline {
		t.Errorf("two OAuth MCP servers changed the preflight exec count: %d -> %d "+
			"(want unchanged) — the per-server token writes are round-tripping again",
			baseline, withOAuth)
	}
}

// TestPreparePreflightDirs_MergedScriptRidesStdinNotArgv is the security half.
// buildCredFileScript base64s credential material straight into the `sh -c`
// argument, and /proc/<pid>/cmdline is world-readable regardless of uid — the
// same defect #1629 fixed for the agent's bearer token. The merged script must
// arrive on stdin.
func TestPreparePreflightDirs_MergedScriptRidesStdinNotArgv(t *testing.T) {
	o, c, req := preflightFixture(t)
	secret := "ghp_supersecret_value"
	req.Credentials = []Credential{
		{ID: "c1", EnvVarName: "GH_TOKEN", PlainValue: secret, Type: "CLI_TOKEN"},
	}

	if _, _, err := o.preparePreflightDirs(context.Background(), req, nil, true, false, "run-1"); err != nil {
		t.Fatalf("preparePreflightDirs: %v", err)
	}

	needles := []string{secret, base64.StdEncoding.EncodeToString([]byte(secret))}
	for i, e := range c.snapshot() {
		joined := strings.Join(e.cfg.Cmd, " ")
		for _, n := range needles {
			if strings.Contains(joined, n) {
				t.Errorf("exec[%d] carries credential material in argv (%q...): "+
					"/proc/<pid>/cmdline is mode 0444 and a bare ps prints it, so every "+
					"sibling agent in the crew container can read it. Deliver the script "+
					"on ExecConfig.Stdin.", i, n[:min(12, len(n))])
			}
		}
	}

	// And the credential material must actually have been delivered — a merged
	// script that simply dropped the credential step would pass the scan above.
	var found bool
	for _, e := range c.snapshot() {
		if strings.Contains(e.stdin, base64.StdEncoding.EncodeToString([]byte(secret))) {
			found = true
		}
	}
	if !found {
		t.Error("no exec delivered the credential material on stdin — the write was lost, not moved")
	}
}

// TestPreparePreflightDirs_OnlyManifestStepIsPrivileged pins the root
// boundary. /crew/manifest.json genuinely needs root to chmod 0666 a
// dual-writer file; nothing else does, and the merged script must not be
// widened to root to make the collapse tidy.
func TestPreparePreflightDirs_OnlyManifestStepIsPrivileged(t *testing.T) {
	o, c, req := preflightFixture(t)
	if _, _, err := o.preparePreflightDirs(context.Background(), req, nil, true, false, "run-1"); err != nil {
		t.Fatalf("preparePreflightDirs: %v", err)
	}

	privileged := 0
	for i, e := range c.snapshot() {
		if !e.cfg.AllowPrivileged && !provider.IsPrivilegedExecUser(e.cfg.User) {
			continue
		}
		privileged++
		if !strings.Contains(strings.Join(e.cfg.Cmd, " "), "/crew/manifest.json") {
			t.Errorf("exec[%d] runs privileged (user=%q allow=%v) but is not the manifest "+
				"pre-create: %q", i, e.cfg.User, e.cfg.AllowPrivileged, truncCmd(e.cfg.Cmd))
		}
		if e.cfg.Stdin != nil {
			t.Errorf("exec[%d] is BOTH privileged and carries the merged script on stdin — "+
				"the merged script must never run as root", i)
		}
	}
	if privileged != 1 {
		t.Errorf("privileged execs = %d, want exactly 1 (the manifest pre-create)", privileged)
	}
}

// TestPreparePreflightDirs_ConditionsResolvedInGoNotShell pins the design
// choice from the issue: MemoryEnabled / CrewID decide host-side which steps
// are emitted, so the shipped script contains only the work the request
// actually needs and a log of it reads as the truth.
func TestPreparePreflightDirs_ConditionsResolvedInGoNotShell(t *testing.T) {
	cases := []struct {
		name          string
		memoryEnabled bool
		crewID        string
		wantSteps     []string
		notWantSteps  []string
	}{
		{
			name:          "memory off",
			memoryEnabled: false,
			crewID:        "crew-1",
			wantSteps:     []string{preflightStepAgentDirs},
			notWantSteps:  []string{preflightStepMemoryDirs, preflightStepCrewMemoryDirs, preflightStepMemoryMigrate},
		},
		{
			name:          "memory on, no crew",
			memoryEnabled: true,
			crewID:        "",
			wantSteps:     []string{preflightStepAgentDirs, preflightStepMemoryDirs, preflightStepMemoryMigrate},
			notWantSteps:  []string{preflightStepCrewMemoryDirs},
		},
		{
			name:          "memory on, in a crew",
			memoryEnabled: true,
			crewID:        "crew-1",
			wantSteps: []string{preflightStepAgentDirs, preflightStepMemoryDirs,
				preflightStepCrewMemoryDirs, preflightStepMemoryMigrate},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, c, req := preflightFixture(t)
			req.MemoryEnabled = tc.memoryEnabled
			req.CrewID = tc.crewID
			if _, _, err := o.preparePreflightDirs(context.Background(), req, nil, true, false, "run-1"); err != nil {
				t.Fatalf("preparePreflightDirs: %v", err)
			}
			var scripts strings.Builder
			for _, e := range c.snapshot() {
				scripts.WriteString(e.stdin)
			}
			all := scripts.String()
			for _, s := range tc.wantSteps {
				if !strings.Contains(all, preflightStepMarker+s+"'") {
					t.Errorf("merged script is missing step %q", s)
				}
			}
			for _, s := range tc.notWantSteps {
				if strings.Contains(all, preflightStepMarker+s+"'") {
					t.Errorf("merged script emitted step %q for a request that does not need it — "+
						"the condition belongs in Go, not in a shell `if`", s)
				}
			}
		})
	}
}

// TestPreparePreflightDirs_FailingStepIsNamed guards the reporting regression
// the issue called out: today a failing step is identifiable because it IS its
// own exec. One merged script must still say which part failed.
func TestPreparePreflightDirs_FailingStepIsNamed(t *testing.T) {
	c := &countingContainer{stdout: healthySidecarStdout}
	// Make the merged script report the credential step as failed, exactly as
	// the shipped script does when a step's exit status is non-zero.
	c.stdout = func(cfg provider.ExecConfig, stdin string) string {
		if out := healthySidecarStdout(cfg, stdin); out != "" {
			return out
		}
		if stdin != "" {
			return preflightFailMarker + preflightStepCredentials + "\n"
		}
		return ""
	}
	c.exitFor = func(cfg provider.ExecConfig) int {
		if cfg.Stdin != nil {
			return 1
		}
		return 0
	}
	o := New(c, newMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := AgentRunRequest{
		AgentID: "agent-1", AgentSlug: "scout", CrewID: "crew-1", WorkspaceID: "ws-1",
		ContainerID: "container-123", CLIAdapter: "CLAUDE_CODE", MemoryEnabled: true,
		Credentials: []Credential{{ID: "c1", EnvVarName: "GH_TOKEN", PlainValue: "v", Type: "CLI_TOKEN"}},
	}

	_, _, err := o.preparePreflightDirs(context.Background(), req, nil, true, false, "run-1")
	if err == nil {
		t.Fatal("a failed credential write on a file-mounted-credentials run must abort the run")
	}
	if !strings.Contains(err.Error(), preflightStepCredentials) {
		t.Errorf("error does not name the failing step: %v\n"+
			"one merged script must still identify which part failed, or debugging a "+
			"broken crew gets materially worse than the pre-#1646 one-exec-per-step form", err)
	}
}

// TestPreparePreflightDirs_ReadProbeSeesEarlierWrites pins the ordering
// invariant that makes deferring writes safe at all: a read probe dispatched
// through the batch must be preceded by a flush of everything queued before
// it, so it can never observe a container state the sequential form would not
// have produced.
func TestPreparePreflightDirs_ReadProbeSeesEarlierWrites(t *testing.T) {
	o, c, req := preflightFixture(t)
	if _, _, err := o.preparePreflightDirs(context.Background(), req, nil, true, false, "run-1"); err != nil {
		t.Fatalf("preparePreflightDirs: %v", err)
	}

	got := c.snapshot()
	firstRead := -1
	for i, e := range got {
		if len(e.cfg.Cmd) == 3 && strings.Contains(e.cfg.Cmd[2], "9119/health") {
			firstRead = i
			break
		}
	}
	if firstRead < 0 {
		t.Fatal("sidecar health read probe never dispatched")
	}
	flushedBefore := false
	for _, e := range got[:firstRead] {
		if e.cfg.Stdin != nil && strings.Contains(e.stdin, preflightStepMarker+preflightStepAgentDirs) {
			flushedBefore = true
		}
	}
	if !flushedBefore {
		t.Error("the sidecar health read probe ran before the queued directory/credential " +
			"writes were flushed — a deferred write must never be reordered past a read")
	}
}

// TestPreparePreflightDirs_SecretsLockSpansTheFlush guards the subtlest thing
// the merge changed. The per-agent secrets lock exists so a lagging cleanup rm
// from a previous run cannot delete the credential files right after they land
// (TOCTOU window #2, secrets_cleanup.go). It used to wrap the credential
// WRITE exec; the write is now a step in the merged script, so the lock has to
// wrap the FLUSH instead. Held only around the queueing call, it would guard
// nothing at all.
// Both adapter shapes are exercised because WHICH flush carries the credential
// step depends on whether the adapter's setup path contains a read probe. With
// the Claude adapter the sidecar-health probe forces an early flush; with an
// adapter that has neither an MCP writer nor prompt files there is no probe at
// all and the credential step rides the final flush at the bottom of
// preparePreflightDirs. Only the second shape catches a release taken one line
// too early, and only the first catches a lock scoped to the queueing call.
func TestPreparePreflightDirs_SecretsLockSpansTheFlush(t *testing.T) {
	for _, adapter := range []string{"CLAUDE_CODE", "NO_SUCH_CLI"} {
		t.Run(adapter, func(t *testing.T) {
			o, c, req := preflightFixture(t)
			req.CLIAdapter = adapter
			lk := o.agentSecretsLock(req.ContainerID, req.AgentSlug)

			var checked, heldDuringCredentialFlush bool
			c.stdout = func(cfg provider.ExecConfig, stdin string) string {
				if out := healthySidecarStdout(cfg, stdin); out != "" {
					return out
				}
				if !strings.Contains(stdin, preflightStepMarker+preflightStepCredentials) {
					return ""
				}
				// Runs inside preparePreflightDirs, at the exact moment the
				// credential files land in the container.
				checked = true
				if lk.TryLock() {
					lk.Unlock()
				} else {
					heldDuringCredentialFlush = true
				}
				return ""
			}

			if _, _, err := o.preparePreflightDirs(context.Background(), req, nil, true, false, "run-1"); err != nil {
				t.Fatalf("preparePreflightDirs: %v", err)
			}
			if !checked {
				t.Fatal("the credential step never reached a flush — the probe never ran")
			}
			if !heldDuringCredentialFlush {
				t.Error("the per-agent secrets lock was NOT held while the credential files landed: " +
					"a concurrent cleanup rm can now delete them the instant they are written " +
					"(TOCTOU window #2, secrets_cleanup.go)")
			}
			if !lk.TryLock() {
				t.Error("the secrets lock was still held after preparePreflightDirs returned")
			} else {
				lk.Unlock()
			}
		})
	}
}

// TestPreparePreflightDirs_OrphanSkillRemovalRidesTheMergedScript closes the
// other half of the skill-prune collapse: the listing is a read and keeps its
// own exec, but the rm that follows is a write and must not.
func TestPreparePreflightDirs_OrphanSkillRemovalRidesTheMergedScript(t *testing.T) {
	o, c, req := preflightFixture(t)
	c.stdout = func(cfg provider.ExecConfig, stdin string) string {
		if out := healthySidecarStdout(cfg, stdin); out != "" {
			return out
		}
		if len(cfg.Cmd) == 3 && strings.Contains(cfg.Cmd[2], "ls -1 ") {
			return skillListingMarker + ".claude/skills\nstale-one\nstale-two\n" +
				skillListingMarker + ".cursor/rules\nstale-three.mdc\n"
		}
		return ""
	}
	if _, _, err := o.preparePreflightDirs(context.Background(), req, nil, true, false, "run-1"); err != nil {
		t.Fatalf("preparePreflightDirs: %v", err)
	}

	var inScript bool
	for i, e := range c.snapshot() {
		if len(e.cfg.Cmd) == 3 && strings.Contains(e.cfg.Cmd[2], "rm -rf") {
			t.Errorf("exec[%d] is a bare rm of orphan skill folders: %q — a write with no "+
				"output the caller reads must ride the merged script", i, truncCmd(e.cfg.Cmd))
		}
		if strings.Contains(e.stdin, preflightStepMarker+preflightStepSkillsPrune) &&
			strings.Contains(e.stdin, "stale-one") {
			inScript = true
		}
	}
	if !inScript {
		t.Error("the orphan removal never reached the merged script — the prune was lost, not moved")
	}
}

func truncCmd(cmd []string) string {
	s := strings.Join(cmd, " ")
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}
