//go:build integration && linux

package orchestrator_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	dockerprovider "github.com/crewship-ai/crewship/internal/provider/docker"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// This observer delegates every command to the real container provider. It does
// not read, rewrite or deliver credential values and never executes on the host.
type nativeKeeperRuntimeProxy struct {
	provider.ContainerProvider
	calls atomic.Int32
}

func (p *nativeKeeperRuntimeProxy) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	if cfg.User != "1001:1001" {
		return nil, fmt.Errorf("fixture Keeper exec must use agent UID")
	}
	p.calls.Add(1)
	return p.ContainerProvider.Exec(ctx, cfg)
}
func (p *nativeKeeperRuntimeProxy) ContainerUser(ctx context.Context, id string) (string, error) {
	resolver, ok := p.ContainerProvider.(interface {
		ContainerUser(context.Context, string) (string, error)
	})
	if !ok {
		return "", fmt.Errorf("fixture provider cannot resolve UID")
	}
	return resolver.ContainerUser(ctx, id)
}

type nativeCrewKeeperEmptyCache struct{}

func (nativeCrewKeeperEmptyCache) Get(string) (string, bool) { return "", false }

type nativeCrewKeeperJudge struct{}

func (nativeCrewKeeperJudge) Name() string { return "fixture" }
func (nativeCrewKeeperJudge) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	decision := "ALLOW"
	for _, m := range req.Messages {
		if strings.Contains(m.Content, "fixture-deny") {
			decision = "DENY"
		}
		if strings.Contains(m.Content, "fixture-escalate") {
			decision = "ESCALATE"
		}
	}
	return &llm.Response{Content: fmt.Sprintf(`{"decision":%q,"reason":"isolated fixture verdict","risk":1}`, decision)}, nil
}
func (j nativeCrewKeeperJudge) Stream(ctx context.Context, req llm.Request, _ func(llm.StreamEvent) error) (*llm.Response, error) {
	return j.Complete(ctx, req)
}

type nativeCrewKeeperReply struct {
	RequestID          string  `json:"request_id"`
	Decision           string  `json:"decision"`
	ExitCode           *int    `json:"exit_code,omitempty"`
	Reason             string  `json:"reason"`
	Output             string  `json:"output"`
	ApprovalConsumedAt *string `json:"approval_consumed_at"`
}

func nativeKeeperCrewScenario(t *testing.T, ctx context.Context, db *sql.DB, ctr *dockerprovider.Provider, executor *nativeKeeperRuntimeProxy, containerID, crew, ws, agent, peer, master string, require func(string, ...string) []byte, hostPort int, acceptedToken *atomic.Value, authenticated *atomic.Int32) {
	t.Helper()
	// All identities, services and values are isolated synthetic fixtures. The
	// owner profile and its admin token stay on the host native onboarding CLI.
	secret, replacement := strings.Repeat("a", 64), strings.Repeat("b", 64)
	acceptedToken.Store(secret)
	authenticated.Store(0)
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	now := tsformat.Format(time.Now())
	for i, run := range []string{"native-live", "native-peer", "native-ended"} {
		id, state := agent, "running"
		var terminal any
		if i == 1 {
			id = peer
		}
		if i == 2 {
			state, terminal = "succeeded", now
		}
		seed(`INSERT INTO work_items(id,workspace_id,source,source_ref,agent_id,crew_id,session_id,class,input_json,input_sha256,target_revision,state,generation,attempts,priority,eligible_at,created_at,updated_at,terminal_at) VALUES(?,?,'manual','',?,?,'','background','{}','sha','rev',?,1,0,0,?,?,?,?)`, "work-"+run, ws, id, crew, state, now, now, now, terminal)
		seed(`INSERT INTO work_attempts(run_id,work_id,attempt,generation,lease_owner,lease_expires_at,heartbeat_at,started_at,runtime_phase,ended_at) VALUES(?,?,1,1,'fixture',?,?,?,'confirmed',?)`, run, "work-"+run, tsformat.Format(time.Now().Add(time.Hour)), now, now, terminal)
		seed(`INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,payload,trace_id) VALUES(?,?,?,?,'run.started','orchestrator','fixture run','{}',?)`, "journal-"+run, ws, crew, id, run)
		if terminal != nil {
			seed(`INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,payload,trace_id) VALUES(?,?,?,?,'run.completed','orchestrator','fixture end','{}',?)`, "journal-end-"+run, ws, crew, id, run)
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hostURL := fmt.Sprintf("http://%s:%d", ctr.HostAddress(), hostPort)
	runKey := internaltoken.DeriveAgentRunKey(master, ws, crew)
	if err := orchestrator.NativeKeeperSidecarForTest(ctx, ctr, containerID, &orchestrator.SidecarIPCConfig{BaseURL: hostURL, Token: internaltoken.DeriveCrewToken(master, ws, crew), WorkspaceID: ws, CrewID: crew, AgentID: agent, AgentSlug: agent, ContainerID: containerID, AgentRunKey: runKey, RunID: "native-live"}, []orchestrator.SidecarCrewMember{{ID: agent, Slug: agent}, {ID: peer, Slug: peer}}, logger); err != nil {
		t.Fatal("production crew sidecar startup failed", err)
	}
	call := func(capability, input string, args ...string) ([]byte, int) {
		t.Helper()
		// Canonical read-only provisioned SDK, local real crew sidecar, UID1001.
		cfg := provider.ExecConfig{ContainerID: containerID, User: "1001:1001", Cmd: append(append([]string{"/usr/local/bin/crewship"}, args...), "-f", "json"), WorkingDir: "/home/agent", Stdin: strings.NewReader(input), Env: []string{"CREWSHIP_AGENT_TOKEN=" + capability, "CREWSHIP_SIDECAR_URL=http://127.0.0.1:9119", "NO_COLOR=1"}}
		result, err := ctr.Exec(ctx, cfg)
		if err != nil {
			t.Fatal("native crew Keeper SDK did not start", err)
		}
		out, err := readNativeContainerOutput(ctx, result.Reader)
		if err != nil {
			t.Fatal("native crew Keeper output did not settle", err)
		}
		code, err := provider.WaitExecExit(ctx, ctr, result.ExecID, time.Second)
		if err != nil {
			t.Fatal("native crew Keeper exit not observed", err)
		}
		if bytes.Contains(out, []byte(secret)) || bytes.Contains(out, []byte(replacement)) || (capability != "" && bytes.Contains(out, []byte(capability))) {
			t.Fatal("crew Keeper output exposed credential or capability")
		}
		return out, code
	}
	out := require(secret, "credential", "create", "--name", "NATIVE_KEEPER", "--type", "SECRET", "--provider", "NONE", "--env-var-name", "GITLAB_TOKEN", "--security-level", "3", "--value-stdin")
	var created struct {
		ID string `json:"id" yaml:"id"`
	}
	if json.Unmarshal(out, &created) != nil || created.ID == "" {
		t.Fatal("native credential create returned no ID")
	}
	require("", "credential", "assign", created.ID, agent, "--env-var-name", "GITLAB_TOKEN", "--ttl", "30m")
	resolved, err := chatbridge.NewIPCResolver(fmt.Sprintf("http://127.0.0.1:%d", hostPort), master, logger).ResolveAgent(t.Context(), agent, ws)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range resolved.Credentials {
		if c.ID == created.ID && c.PlainValue != "" {
			t.Fatal("Keeper-gated credential escaped into startup resolution")
		}
	}
	capability := internaltoken.DeriveAgentRunToken(runKey, ws, agent, "native-live")
	intent := "Authenticate to the isolated native Keeper GitLab fixture to verify the assigned synthetic account for this regression test, with no production target or data."
	request, code := call(capability, "", "keeper", "request", created.ID, "--id", "--intent", intent)
	var decision nativeCrewKeeperReply
	if code != 0 || decodeNativeKeeperCrewReply(request, code, &decision) != nil || decision.Decision != "ALLOW" || decision.RequestID == "" {
		t.Fatalf("native request was not allowed: exit=%d", code)
	}
	peerCap := internaltoken.DeriveAgentRunToken(runKey, ws, peer, "native-peer")
	require("", "credential", "assign", created.ID, peer, "--env-var-name", "GITLAB_TOKEN", "--ttl", "30m")
	peerRequest, peerCode := call(peerCap, "", "keeper", "request", created.ID, "--id", "--intent", intent)
	var peerDecision nativeCrewKeeperReply
	if peerCode != 0 || decodeNativeKeeperCrewReply(peerRequest, peerCode, &peerDecision) != nil || peerDecision.RequestID == "" || peerDecision.Decision != "ALLOW" {
		t.Fatal("positive peer capability/assignment control failed")
	}
	if _, code := call(peerCap, "", "keeper", "request-status", peerDecision.RequestID); code != 0 {
		t.Fatal("peer could not read its own allowed request")
	}
	if _, code := call(peerCap, "", "keeper", "request-status", decision.RequestID); code == 0 {
		t.Fatal("sibling read another agent's Keeper request")
	}
	if _, code := call(capability, "", "keeper", "request-status", peerDecision.RequestID); code == 0 {
		t.Fatal("boot agent read the peer's Keeper request")
	}
	require("", "credential", "unassign", created.ID, peer)
	for _, command := range []string{"/opt/crew-tools/fixture-glab api user", "printenv GITLAB_TOKEN", "false"} {
		out, code := call(capability, command, "keeper", "execute", created.ID, "--id", "--intent", intent, "--command-stdin")
		want := 0
		if command == "false" {
			want = 1
		}
		if code != want || decodeNativeKeeperCrewReply(out, code, &decision) != nil || decision.Decision != "ALLOW" || decision.ExitCode == nil || *decision.ExitCode != want {
			t.Fatalf("native %s result incorrect: exit=%d", command, code)
		}
		if command == "printenv GITLAB_TOKEN" && !strings.Contains(decision.Output, "REDACTED") {
			t.Fatal("redaction receipt missing from actual standard-tool output")
		}
	}
	if authenticated.Load() != 1 || executor.calls.Load() != 3 {
		t.Fatal("independent standard-tool authentication or execution receipts missing")
	}
	before := executor.calls.Load()
	// Distinct commands keep independent decisions out of the production
	// five-second execute debounce; changing intent alone is not a new operation.
	for i, suffix := range []string{" fixture-deny", " fixture-escalate"} {
		command := "/opt/crew-tools/fixture-glab api user --header X-Native-Scenario:" + []string{"deny", "escalate"}[i]
		out, code := call(capability, command, "keeper", "execute", created.ID, "--id", "--intent", intent+suffix, "--command-stdin")
		want := []string{"DENY", "ESCALATE"}[i]
		decision = nativeCrewKeeperReply{}
		decodeErr := decodeNativeKeeperCrewReply(out, code, &decision)
		if code != []int{4, 5}[i] || decodeErr != nil || decision.Decision != want {
			t.Fatalf("expected Keeper %s decision and matching CLI exit, got exit=%d decision=%s reason=%s decode=%v", want, code, decision.Decision, decision.Reason, decodeErr)
		}
	}
	approval := decision.RequestID
	if executor.calls.Load() != before || authenticated.Load() != 1 {
		t.Fatal("denied or escalated command reached the standard tool")
	}
	require("", "keeper", "resolve", approval, "--decision", "allow", "--reason", "Approve the isolated fixture operation")
	approvedArgs := []string{"keeper", "execute", created.ID, "--id", "--intent", intent + " fixture-escalate", "--command-stdin", "--approval-request-id", approval}
	approvedOut, code := call(capability, "/opt/crew-tools/fixture-glab api user --header X-Native-Scenario:escalate", approvedArgs...)
	var approved nativeCrewKeeperReply
	if code != 0 || decodeNativeKeeperCrewReply(approvedOut, code, &approved) != nil || approved.RequestID == "" || approved.RequestID == approval {
		t.Fatal("native approved execution lacks its own successful request receipt")
	}
	var projection, pendingEvent, allowedEvent, leaked int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM keeper_requests WHERE id=? AND requesting_agent_id=? AND credential_id=? AND request_type='execute' AND decision='ALLOW' AND exit_code=0 AND approval_consumed_at IS NOT NULL`, approved.RequestID, agent, created.ID).Scan(&projection); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"PENDING", "ALLOW"} {
		var count int
		if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM keeper_request_events WHERE request_id=? AND workspace_id=? AND requesting_agent_id=? AND requesting_crew_id=? AND credential_id=? AND request_type='execute' AND state=? AND (state='PENDING' OR exit_code=0)`, approved.RequestID, ws, agent, crew, created.ID, state).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if state == "PENDING" {
			pendingEvent = count
		} else {
			allowedEvent = count
		}
	}
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM keeper_request_events WHERE instr(COALESCE(intent,'')||COALESCE(command,'')||COALESCE(reason,''),?)>0 OR instr(COALESCE(intent,'')||COALESCE(command,'')||COALESCE(reason,''),?)>0`, secret, replacement).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if projection != 1 || pendingEvent != 1 || allowedEvent != 1 || leaked != 0 {
		t.Fatal("native approved execution lacks correlated, value-free projection/transition audit")
	}
	var runBoundRequest, runBoundEvents, vaultDecision int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM keeper_requests WHERE id=? AND requesting_agent_id=? AND requesting_run_id='native-live' AND credential_id=?`, approved.RequestID, agent, created.ID).Scan(&runBoundRequest); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM keeper_request_events WHERE request_id=? AND requesting_agent_id=? AND requesting_run_id='native-live' AND credential_id=?`, approved.RequestID, agent, created.ID).Scan(&runBoundEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM credential_audit WHERE credential_id=? AND agent_id=? AND event_type='KEEPER_DECISION' AND json_extract(metadata_json,'$.run_id')='native-live' AND json_extract(metadata_json,'$.keeper_request_id')=?`, created.ID, agent, approved.RequestID).Scan(&vaultDecision); err != nil {
		t.Fatal(err)
	}
	if runBoundRequest != 1 || runBoundEvents != 2 || vaultDecision != 1 {
		t.Fatal("crew Keeper execution lost credential/agent/run audit correlation")
	}
	approvedCalls := executor.calls.Load()
	// An immediate replay exercises the five-second execute debounce only.
	if _, code := call(capability, "/opt/crew-tools/fixture-glab api user --header X-Native-Scenario:escalate", approvedArgs...); code != 5 {
		t.Fatal("immediate approval replay was not rejected")
	}
	status, code := call(capability, "", "keeper", "request-status", approval)
	var spent nativeCrewKeeperReply
	if code != 0 || decodeNativeKeeperCrewReply(status, code, &spent) != nil || spent.ApprovalConsumedAt == nil || *spent.ApprovalConsumedAt == "" {
		t.Fatal("native approval status lacks its consumption receipt")
	}
	duplicateCount := func() int {
		t.Helper()
		var count int
		if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM keeper_requests WHERE requesting_agent_id=? AND decision='DUPLICATE_SUPPRESSED'`, agent).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	duplicates := duplicateCount()
	if duplicates == 0 {
		t.Fatal("immediate replay did not exercise the debounce control")
	}
	// Pass the production debounce without modifying its clock or policy. A
	// post-window denial, with no new duplicate-suppressed row, exercises the
	// approval's durable single-use guard rather than in-memory deduplication.
	timer := time.NewTimer(5100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.Context().Done():
		t.Fatal("fixture canceled before post-debounce approval replay")
	}
	if _, code := call(capability, "/opt/crew-tools/fixture-glab api user --header X-Native-Scenario:escalate", approvedArgs...); code != 5 {
		t.Fatal("already-consumed approval was not rejected after execute debounce")
	}
	if duplicateCount() != duplicates {
		t.Fatal("post-window replay was still suppressed by debounce; single-use guard was not exercised")
	}
	if executor.calls.Load() != approvedCalls || authenticated.Load() != 2 {
		t.Fatal("approval replay executed the standard tool twice")
	}
	if _, code := call(capability, "/opt/crew-tools/fixture-glab api user --header X-Native-Scenario:healthy", "keeper", "execute", created.ID, "--id", "--intent", intent, "--command-stdin"); code != 0 || authenticated.Load() != 3 {
		t.Fatal("healthy control after approval replay failed")
	}
	require(replacement, "credential", "rotate", created.ID, "--value-stdin", "--yes")
	acceptedToken.Store(replacement)
	if _, code := call(capability, "/opt/crew-tools/fixture-glab api user --header X-Native-Scenario:rotated", "keeper", "execute", created.ID, "--id", "--intent", intent, "--command-stdin"); code != 0 || authenticated.Load() != 4 {
		t.Fatal("rotated native credential did not authenticate")
	}
	before = executor.calls.Load()
	for _, invalid := range []string{capability + "x", internaltoken.DeriveAgentRunToken(runKey, ws, agent, "native-ended")} {
		if _, code := call(invalid, "/opt/crew-tools/fixture-glab api user", "keeper", "execute", created.ID, "--id", "--intent", intent, "--command-stdin"); code != 4 {
			t.Fatal("tampered or ended run executed with the credential")
		}
	}
	require("", "credential", "unassign", created.ID, agent)
	if _, code := call(capability, "/opt/crew-tools/fixture-glab api user", "keeper", "execute", created.ID, "--id", "--intent", intent, "--command-stdin"); code != 3 {
		t.Fatal("ungranted credential executed")
	}
	require("", "credential", "assign", created.ID, agent, "--env-var-name", "GITLAB_TOKEN", "--ttl", "1s")
	time.Sleep(1100 * time.Millisecond)
	if _, code := call(capability, "/opt/crew-tools/fixture-glab api user", "keeper", "execute", created.ID, "--id", "--intent", intent, "--command-stdin"); code != 3 {
		t.Fatal("expired grant executed")
	}
	if executor.calls.Load() != before || authenticated.Load() != 4 {
		t.Fatal("denied, pending, invalid-run or revoked access reached the executor")
	}
	t.Logf("native Keeper crew CLI/sidecar/standard-tool PASS container=%s source_run=native-live; real UID1001 command execution, approvals/rotation/expiry/revoke/redaction/run audit; no model qualification", containerID)
}
