package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
)

// This exercises the real producer, dispatcher, assignment runtime, RunAgent,
// memory authorization and durable projection. Only container operations and
// CLI bytes are substituted; it is not real-container acceptance evidence.
func TestAssignmentWorkActualOrchestratorColdWarm(t *testing.T) {
	rig := newVerticalRig(t)
	actor := seedAgentRow(t, rig.db, "assignment-work-actor", rig.wsID, rig.crewID, "Lead", "assignment-work-lead", "LEAD")
	const chat = "assignment-work-chat"
	execOrFatal(t, rig.db, `INSERT INTO chats (id,agent_id,workspace_id,title,mode,status,started_at,created_at,updated_at) VALUES (?,?,?,'work','MISSION','ACTIVE',datetime('now'),datetime('now'),datetime('now'))`, chat, actor, rig.wsID)
	var user string
	if err := rig.db.QueryRow(`SELECT user_id FROM workspace_members WHERE workspace_id=? LIMIT 1`, rig.wsID).Scan(&user); err != nil {
		t.Fatal(err)
	}
	seedCredentialEncTyped(t, rig.db, rig.wsID, user, "assignment-work-key", "ANTHROPIC_API_KEY", "synthetic-assignment-key", "API_KEY")
	execOrFatal(t, rig.db, `UPDATE credentials SET provider='ANTHROPIC' WHERE id='assignment-work-key'`)
	execOrFatal(t, rig.db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name,priority) VALUES ('assignment-work-grant',?,'assignment-work-key','ANTHROPIC_API_KEY',0)`, rig.agentID)
	p := &assignmentWorkContainer{db: rig.db, ws: rig.wsID, agent: rig.agentID}
	o := orchestrator.New(p, newInbandAsgState(), quietLogger())
	o.SetSidecarEnabled(true)
	o.SetIPCConfig(rig.baseURL, "vertical-internal-token")
	h := rig.router.assignmentHandler
	if h == nil {
		t.Fatal("production assignment handler missing")
	}
	h.orch = o
	// Provisioning images is outside this mock provider's boundary.
	h.provisioner = nil
	rig.router.webhookHandler.orch = o
	stop, hint, err := rig.router.StartAgentWorkDispatcher(context.Background(), quietLogger(), nil, 4096, 2)
	if err != nil {
		t.Fatal(err)
	}
	rig.stopDispatcher = stop
	var runIDs []string
	for i := 0; i < 3; i++ {
		p.mu.Lock()
		p.failCLI = i == 2
		p.mu.Unlock()
		body, _ := json.Marshal(map[string]string{"target_slug": "s-" + rig.agentID, "task": fmt.Sprintf("task %d", i), "crew_id": rig.crewID, "workspace_id": rig.wsID, "chat_id": chat})
		rr := covAsgPost(t, h, string(body))
		if rr.Code != http.StatusCreated {
			t.Fatalf("producer: %d %s", rr.Code, rr.Body.String())
		}
		var id string
		if err := rig.db.QueryRow(`SELECT id FROM assignments WHERE task=?`, fmt.Sprintf("task %d", i)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		hint("")
		deadline := time.Now().Add(15 * time.Second)
		var status, result, message, runID string
		for time.Now().Before(deadline) {
			err = rig.db.QueryRow(`SELECT a.status,COALESCE(a.result_summary,''),COALESCE(a.error_message,''),COALESCE(wa.run_id,'') FROM assignments a LEFT JOIN work_items w ON w.domain_id=a.id AND w.source='assignment' LEFT JOIN work_attempts wa ON wa.work_id=w.id AND wa.generation=w.generation WHERE a.id=?`, id).Scan(&status, &result, &message, &runID)
			if err != nil {
				t.Fatal(err)
			}
			if status == "COMPLETED" || status == "FAILED" || status == "CANCELLED" {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		expectedStatus, expectedWorkState := "COMPLETED", "succeeded"
		if i == 2 {
			expectedStatus, expectedWorkState = "FAILED", "failed"
		}
		if status != expectedStatus || !strings.Contains(result, "assignment integration done") {
			t.Fatalf("projection status=%s result=%q error=%q", status, result, message)
		}
		var n int
		if err := rig.db.QueryRow(`SELECT count(*) FROM work_attempts wa JOIN work_items w ON w.id=wa.work_id WHERE w.domain_id=? AND w.state=? AND wa.ended_at IS NOT NULL`, id, expectedWorkState).Scan(&n); err != nil || n != 1 {
			t.Fatalf("settled attempt count=%d err=%v", n, err)
		}

		if i == 2 {
			var actualCode int
			if err := rig.db.QueryRow(`SELECT json_extract(wa.run_result_json,'$.exit_code') FROM work_attempts wa JOIN work_items w ON w.id=wa.work_id WHERE w.domain_id=?`, id).Scan(&actualCode); err != nil || actualCode != 7 {
				t.Fatalf("observed failed exit=%d err=%v", actualCode, err)
			}
		}
		var generation int64
		if err := rig.db.QueryRow(`SELECT generation FROM work_attempts WHERE run_id=?`, runID).Scan(&generation); err != nil {
			t.Fatal(err)
		}
		if err := (&MemoryMutationHandler{db: rig.db}).authorizeRun(rig.wsID, rig.agentID, runID, generation)(context.Background()); err == nil {
			t.Fatal("settled attempt still authorized memory mutation")
		}
		runIDs = append(runIDs, runID)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.launches != 1 || len(p.runs) != 3 || p.healthChecks < 2 {
		t.Fatalf("sidecar launches=%d probes=%d CLI runs=%d", p.launches, p.healthChecks, len(p.runs))
	}
	for i, r := range p.runs {
		if r["run_id"] != runIDs[i] {
			t.Errorf("CLI identity differs from claimed attempt")
		}
		if r["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:9119" || r["ANTHROPIC_API_KEY"] == "" || r["ANTHROPIC_API_KEY"] == "synthetic-assignment-key" {
			t.Errorf("CLI provider auth did not route through sidecar")
		}
	}
	if runIDs[0] == runIDs[1] || p.runs[0]["HOME"] == p.runs[1]["HOME"] {
		t.Error("warm run reused run identity or HOME")
	}
}

type assignmentWorkContainer struct {
	covAsg2Provider
	mu                     sync.Mutex
	db                     *sql.DB
	ws, agent              string
	failCLI                bool
	health                 string
	launches, healthChecks int
	runs                   []map[string]string
}

func (p *assignmentWorkContainer) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	script := strings.Join(cfg.Cmd, " ")
	if cfg.Stdin != nil {
		b, err := io.ReadAll(cfg.Stdin)
		if err != nil {
			return nil, err
		}
		script += "\n" + string(b)
	}
	output := ""
	execID := "assignment-integration-exec"
	switch {
	case len(cfg.Env) > 0:
		env := map[string]string{}
		for _, v := range cfg.Env {
			k, v, ok := strings.Cut(v, "=")
			if ok {
				env[k] = v
			}
		}
		runID := path.Base(env["HOME"])
		var generation int64
		if err := p.db.QueryRowContext(ctx, `SELECT wa.generation FROM work_attempts wa JOIN work_items w ON w.id=wa.work_id WHERE wa.run_id=? AND w.source='assignment' AND w.domain_kind='assignment' AND wa.runtime_phase='requested'`, runID).Scan(&generation); err != nil {
			return nil, fmt.Errorf("CLI before durable creation request: %w", err)
		}
		if err := (&MemoryMutationHandler{db: p.db}).authorizeRun(p.ws, p.agent, runID, generation)(ctx); err != nil {
			return nil, err
		}
		execID = "assignment-integration-cli-" + runID
		env["run_id"] = runID
		p.mu.Lock()
		p.runs = append(p.runs, env)
		p.mu.Unlock()
		text := "---HANDOFF---\nsummary: assignment integration done\nconfidence: high\noutcome: SUCCEEDED\n---END HANDOFF---"
		delta, _ := json.Marshal(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_delta", "delta": map[string]string{"type": "text_delta", "text": text}}})
		output = string(delta) + "\n" + `{"type":"result","subtype":"success","result":"done"}` + "\n"
	case strings.Contains(script, "crewship-sidecar --addr"):
		if cfg.User != "1002:1002" {
			return nil, fmt.Errorf("sidecar UID %q", cfg.User)
		}
		_, tail, ok := strings.Cut(script, "echo '")
		if !ok {
			return nil, fmt.Errorf("missing sidecar payload")
		}
		encoded, _, ok := strings.Cut(tail, "'")
		if !ok {
			return nil, fmt.Errorf("unterminated payload")
		}
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, err
		}
		var payload struct {
			Fingerprint string                             `json:"config_fingerprint"`
			Policy      *orchestrator.SidecarNetworkPolicy `json:"network_policy"`
		}
		if err = json.Unmarshal(raw, &payload); err != nil {
			return nil, err
		}
		mode := "free"
		var domains []string
		if payload.Policy != nil && payload.Policy.Mode != "" {
			mode = payload.Policy.Mode
			if mode == "restricted" {
				domains = payload.Policy.AllowedDomains
			}
		}
		b, _ := json.Marshal(map[string]any{"status": "ok", "network_mode": mode, "domains_hash": orchestrator.DomainsHash(domains), "config_fingerprint": payload.Fingerprint, "provider_creds": map[string]int{"ANTHROPIC": 1}})
		p.mu.Lock()
		p.health = string(b)
		p.launches++
		p.mu.Unlock()
	case strings.Contains(script, "127.0.0.1:9119/health"):
		p.mu.Lock()
		output = p.health
		p.healthChecks++
		p.mu.Unlock()
	case strings.Contains(script, "crewship-preflight-done"):
		output = "crewship-preflight-done\n"
	}
	return &provider.ExecResult{ExecID: execID, Reader: io.NopCloser(strings.NewReader(output))}, nil
}
func (p *assignmentWorkContainer) ExecInspect(_ context.Context, id string) (bool, int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failCLI && strings.HasPrefix(id, "assignment-integration-cli-") {
		return false, 7, nil
	}
	return false, 0, nil
}
