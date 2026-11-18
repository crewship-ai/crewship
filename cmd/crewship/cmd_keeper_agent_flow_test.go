package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/sidecar"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Exercise the production Gatekeeper prompt/response parser with deterministic
// model replies. No model network, paid credential or real system is involved.
type nativeKeeperJudge struct{}

func (nativeKeeperJudge) Name() string { return "fixture" }
func (nativeKeeperJudge) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
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
func (j nativeKeeperJudge) Stream(ctx context.Context, req llm.Request, _ func(llm.StreamEvent) error) (*llm.Response, error) {
	return j.Complete(ctx, req)
}

type nativeKeeperEmptyCache struct{}

func (nativeKeeperEmptyCache) Get(string) (string, bool) { return "", false }

// A host executor fixture uses the REAL standard tool with the exact ExecConfig
// produced by Keeper. This does not qualify Docker execution, UID confinement,
// container lifecycle or file cleanup. Unexpected host commands never run.
type nativeKeeperToolExecutor struct {
	provider.ContainerProvider
	home, host string
	calls      atomic.Int32
	lastCode   atomic.Int32
}

func (*nativeKeeperToolExecutor) CrewContainerName(string, string) string {
	return "native-fixture-owned"
}
func (*nativeKeeperToolExecutor) ContainerUser(context.Context, string) (string, error) {
	return "1001:1001", nil
}
func (*nativeKeeperToolExecutor) ContainerStatus(context.Context, string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{State: "stopped"}, nil // no actual container to clean
}
func (e *nativeKeeperToolExecutor) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	if cfg.ContainerID != "native-fixture-owned" || cfg.User != "1001:1001" || len(cfg.Cmd) != 3 || cfg.Cmd[0] != "sh" || cfg.Cmd[1] != "-c" {
		return nil, errors.New("unexpected fixture execution target")
	}
	switch cfg.Cmd[2] {
	case "glab api user", "printenv GITLAB_TOKEN", "false",
		"glab api user --header X-Native-Scenario:deny",
		"glab api user --header X-Native-Scenario:escalate",
		"glab api user --header X-Native-Scenario:healthy",
		"glab api user --header X-Native-Scenario:rotated":
	default:
		return nil, errors.New("unexpected fixture command")
	}
	e.calls.Add(1)
	cmd := exec.CommandContext(ctx, cfg.Cmd[0], cfg.Cmd[1:]...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + e.home,
		"GITLAB_HOST=" + e.host, "GLAB_API_PROTOCOL=http", "GLAB_CONFIG_DIR=" + filepath.Join(e.home, "glab"),
		"GLAB_CHECK_UPDATE=false", "GLAB_SEND_TELEMETRY=false", "GLAB_NO_PROMPT=true"}, cfg.Env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return nil, errors.New("fixture tool could not start")
		}
		code = exit.ExitCode()
	}
	e.lastCode.Store(int32(code))
	return &provider.ExecResult{ExecID: "native-exec", Reader: io.NopCloser(bytes.NewReader(out))}, nil
}
func (e *nativeKeeperToolExecutor) ExecInspect(context.Context, string) (bool, int, error) {
	return false, int(e.lastCode.Load()), nil
}

func TestKeeperAgentCLIVaultSidecarStandardTool(t *testing.T) {
	if _, err := exec.LookPath("glab"); err != nil {
		t.Fatal("standard glab is required for native Keeper acceptance")
	}
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("ab", 32))
	t.Setenv("CREWSHIP_SIDECAR_STATE_DIR", t.TempDir())
	t.Setenv("CREWSHIP_SIDECAR_RUN_AUTHORITY", "1")
	const ws, crew, agent, peer = "cnativews00000000000001", "native-crew", "cnativeagent00000000001", "cnativepeer000000000001"
	const master = "synthetic-native-keeper-master"
	humanToken := "crewship_cli_" + strings.Repeat("0", 48)
	secret, replacement := strings.Repeat("a", 64), strings.Repeat("b", 64)
	db := testutil.MigratedDB(t).DB
	seed := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), q, args...); err != nil {
			t.Fatal(err)
		}
	}
	sum := sha256.Sum256([]byte(humanToken))
	seed(`INSERT INTO users(id,email,full_name) VALUES('native-owner','keeper@native.invalid','Fixture')`)
	seed(`INSERT INTO workspaces(id,name,slug) VALUES(?,'Native Keeper','native-keeper')`, ws)
	seed(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('native-member',?,'native-owner','OWNER')`, ws)
	seed(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('native-token','native-owner','fixture',?,datetime('now'))`, hex.EncodeToString(sum[:]))
	seed(`INSERT INTO crews(id,workspace_id,name,slug,network_mode,container_memory_mb,container_cpus,created_at) VALUES(?,?,'Native','native','free',512,1,datetime('now'))`, crew, ws)
	for _, id := range []string{agent, peer} {
		seed(`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,status,cli_adapter,llm_provider,llm_model,tool_profile,timeout_seconds,memory_enabled,created_at) VALUES(?,?,?,?,?,'AGENT','IDLE','CLAUDE_CODE','ANTHROPIC','fixture','CODING',60,0,datetime('now'))`, id, ws, crew, id, id)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
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
		seed(`INSERT INTO work_attempts(run_id,work_id,attempt,generation,lease_owner,lease_expires_at,heartbeat_at,started_at,runtime_phase,ended_at) VALUES(?,?,1,1,'fixture',?,?,?,'confirmed',?)`, run, "work-"+run, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), now, now, terminal)
	}
	var authenticated atomic.Int32
	var acceptedToken atomic.Value
	acceptedToken.Store(secret)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := acceptedToken.Load().(string)
		if r.URL.Path != "/api/v4/user" || (r.Header.Get("PRIVATE-TOKEN") != expected && r.Header.Get("Authorization") != "Bearer "+expected) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		authenticated.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":1,"username":"native-keeper-fixture"}`)
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)
	executor := &nativeKeeperToolExecutor{home: t.TempDir(), host: u.Host}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	judge := gatekeeper.New(nativeKeeperJudge{}, "fixture", logger)
	router, err := api.NewRouter(db, strings.Repeat("keeper-fixture-jwt-", 3), logger,
		api.WithInternalToken(master), api.WithKeeperConfig(&config.KeeperConfig{Enabled: true}),
		api.WithKeeperGatekeeper(judge), api.WithKeeperSecrets(nativeKeeperEmptyCache{}), api.WithKeeperContainer(executor))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	host := httptest.NewServer(router)
	t.Cleanup(host.Close)
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := reservation.Addr().String()
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	runKey := internaltoken.DeriveAgentRunKey(master, ws, crew)
	s := sidecar.NewServer(sidecar.ServerConfig{Addr: addr, Logger: logger, IPC: &sidecar.IPCConfig{
		BaseURL: host.URL, Token: internaltoken.DeriveCrewToken(master, ws, crew), WorkspaceID: ws, CrewID: crew,
		AgentID: agent, AgentSlug: agent, ContainerID: "native-fixture-owned", AgentRunKey: runKey, RunID: "native-live",
	}})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(7 * time.Second):
			t.Error("owned sidecar did not stop")
		}
	})
	select {
	case <-s.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("owned sidecar did not start")
	}
	binary := buildCrewshipBinary(t)
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+host.URL+"\nworkspace: "+ws+"\ntoken: "+humanToken+"\nformat: json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	call := func(capability, input string, args ...string) ([]byte, int) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), binary, args...)
		for _, e := range os.Environ() {
			if !strings.HasPrefix(e, "CREWSHIP_") && !strings.HasPrefix(e, "HOME=") {
				cmd.Env = append(cmd.Env, e)
			}
		}
		cmd.Env = append(cmd.Env, "HOME="+t.TempDir(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_NO_SLUG_CACHE=1", "CREWSHIP_SIDECAR_URL=http://"+addr)
		if capability != "" {
			cmd.Env = append(cmd.Env, "CREWSHIP_AGENT_TOKEN="+capability)
		}
		cmd.Stdin = strings.NewReader(input)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if strings.Contains(string(out), secret) || strings.Contains(stderr.String(), secret) || strings.Contains(string(out), replacement) || strings.Contains(stderr.String(), replacement) || (capability != "" && (strings.Contains(string(out), capability) || strings.Contains(stderr.String(), capability))) {
			t.Fatal("native Keeper CLI disclosed credential material or a run capability")
		}
		code := 0
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatal("native fixture CLI could not start")
			}
			code = exit.ExitCode()
		}
		return out, code
	}
	require := func(input string, args ...string) []byte {
		t.Helper()
		out, code := call("", input, args...)
		if code != 0 {
			t.Fatalf("native onboarding command %s failed with code %d", args[0], code)
		}
		return out
	}
	out := require(secret, "credential", "create", "--name", "NATIVE_KEEPER", "--type", "SECRET", "--provider", "NONE", "--env-var-name", "GITLAB_TOKEN", "--security-level", "3", "--value-stdin")
	var created struct {
		ID string `json:"id" yaml:"id"`
	}
	if json.Unmarshal(out, &created) != nil || created.ID == "" {
		t.Fatal("native credential create returned no ID")
	}
	require("", "credential", "assign", created.ID, agent, "--env-var-name", "GITLAB_TOKEN", "--ttl", "30m")
	resolved, err := chatbridge.NewIPCResolver(host.URL, master, logger).ResolveAgent(t.Context(), agent, ws)
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
	var decision keeperAgentReply
	if code != 0 || json.Unmarshal(request, &decision) != nil || decision.Decision != "ALLOW" || decision.RequestID == "" {
		t.Fatalf("native request was not allowed: exit=%d", code)
	}
	peerCap := internaltoken.DeriveAgentRunToken(runKey, ws, peer, "native-peer")
	require("", "credential", "assign", created.ID, peer, "--env-var-name", "GITLAB_TOKEN", "--ttl", "30m")
	peerRequest, peerCode := call(peerCap, "", "keeper", "request", created.ID, "--id", "--intent", intent)
	var peerDecision keeperAgentReply
	if peerCode != 0 || json.Unmarshal(peerRequest, &peerDecision) != nil || peerDecision.RequestID == "" || peerDecision.Decision != "ALLOW" {
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
	for _, command := range []string{"glab api user", "printenv GITLAB_TOKEN", "false"} {
		out, code := call(capability, command, "keeper", "execute", created.ID, "--id", "--intent", intent, "--command-stdin")
		want := 0
		if command == "false" {
			want = 1
		}
		if code != want || json.Unmarshal(out, &decision) != nil || decision.Decision != "ALLOW" {
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
		command := "glab api user --header X-Native-Scenario:" + []string{"deny", "escalate"}[i]
		out, code := call(capability, command, "keeper", "execute", created.ID, "--id", "--intent", intent+suffix, "--command-stdin")
		want := []string{"DENY", "ESCALATE"}[i]
		decision = keeperAgentReply{}
		decodeErr := json.Unmarshal(out, &decision)
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
	if _, code := call(capability, "glab api user --header X-Native-Scenario:escalate", approvedArgs...); code != 0 {
		t.Fatal("native approved execution failed")
	}
	approvedCalls := executor.calls.Load()
	_, _ = call(capability, "glab api user --header X-Native-Scenario:escalate", approvedArgs...)
	if executor.calls.Load() != approvedCalls || authenticated.Load() != 2 {
		t.Fatal("approval replay executed the standard tool twice")
	}
	if _, code := call(capability, "glab api user --header X-Native-Scenario:healthy", "keeper", "execute", created.ID, "--id", "--intent", intent, "--command-stdin"); code != 0 || authenticated.Load() != 3 {
		t.Fatal("healthy control after approval replay failed")
	}
	require(replacement, "credential", "rotate", created.ID, "--value-stdin", "--yes")
	acceptedToken.Store(replacement)
	if _, code := call(capability, "glab api user --header X-Native-Scenario:rotated", "keeper", "execute", created.ID, "--id", "--intent", intent, "--command-stdin"); code != 0 || authenticated.Load() != 4 {
		t.Fatal("rotated native credential did not authenticate")
	}
	before = executor.calls.Load()
	for _, invalid := range []string{capability + "x", internaltoken.DeriveAgentRunToken(runKey, ws, agent, "native-ended")} {
		if _, code := call(invalid, "glab api user", "keeper", "execute", created.ID, "--id", "--intent", intent, "--command-stdin"); code != 4 {
			t.Fatal("tampered or ended run executed with the credential")
		}
	}
	require("", "credential", "unassign", created.ID, agent)
	if _, code := call(capability, "glab api user", "keeper", "execute", created.ID, "--id", "--intent", intent, "--command-stdin"); code != 3 {
		t.Fatal("ungranted credential executed")
	}
	require("", "credential", "assign", created.ID, agent, "--env-var-name", "GITLAB_TOKEN", "--ttl", "1s")
	time.Sleep(1100 * time.Millisecond)
	if _, code := call(capability, "glab api user", "keeper", "execute", created.ID, "--id", "--intent", intent, "--command-stdin"); code != 3 {
		t.Fatal("expired grant executed")
	}
	if executor.calls.Load() != before || authenticated.Load() != 4 {
		t.Fatal("denied, pending, invalid-run or revoked access reached the executor")
	}
}
