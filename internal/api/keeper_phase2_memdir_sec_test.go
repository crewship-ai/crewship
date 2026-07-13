package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
)

// TestSecKeeperPhase2_NegativeLearning_IgnoresBodyMemoryDir is the #1037
// regression: the F4.4 negative-learning ALLOW path used body.agent_memory_dir
// verbatim as the lessons.md write target. An internal caller (a compromised
// or buggy sidecar, or any holder of an internal token) could therefore name
// ANOTHER agent's / tenant's .memory directory and have an attacker-authored
// "lesson" written into that victim's lessons.md — cross-agent/tenant memory
// poisoning that steers the victim's future behaviour.
//
// The fix derives the write target from trusted server state only: the agents
// row for body.agent_id (crew_id + slug), scoped to the token's workspace,
// joined to the configured outputBase. The body path is ignored.
//
// This test drives the ALLOW + self_learning=ON path with the body pointed at
// a victim directory and asserts the lesson lands under the *derived* dir for
// the requesting agent, and that the victim dir is never touched.
//
// Red on parent: WriteLesson(body.AgentMemoryDir) writes into victimDir → the
// victim-dir assertion fails. Green after: WriteLesson(derivedDir).
func TestSecKeeperPhase2_NegativeLearning_IgnoresBodyMemoryDir(t *testing.T) {
	db, pr := kp2DB(t)
	base := t.TempDir()

	// Seed a second, unrelated agent (the victim) in its own crew. Its
	// on-disk memory dir is what the attacker will name in the body.
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, autonomy_level, behavior_mode) VALUES ('cr_victim', 'ws1', 'Victim', 'victim', 'guided', 'warn')`); err != nil {
		t.Fatalf("seed victim crew: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('a_victim', 'cr_victim', 'ws1', 'Victim', 'victim')`); err != nil {
		t.Fatalf("seed victim agent: %v", err)
	}
	victimDir := filepath.Join(base, "crews", "cr_victim", "agents", "victim", ".memory")
	if err := os.MkdirAll(victimDir, 0o755); err != nil {
		t.Fatalf("mkdir victim dir: %v", err)
	}

	// Requesting agent a1 (crew cr1, slug worker) has self_learning ON so the
	// ALLOW auto-applies a lesson — the write path we're guarding.
	if _, err := db.Exec(`UPDATE agents SET self_learning_enabled = 1 WHERE id = 'a1'`); err != nil {
		t.Fatalf("flip self_learning ON: %v", err)
	}

	p := &kp2Provider{content: `{"decision":"ALLOW","reason":"note the failure","risk":3}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", kp2Logger())
	ev := gatekeeper.NewNegativeLearningEvaluator(gk, kp2Logger())
	h := NewKeeperPhase2Handler(db, "tok", pr, nil, nil, nil, ev, kp2Logger()).WithMemoryBase(base)

	body := negativeLearningBody{
		WorkspaceID: "ws1", CrewID: "cr1",
		AgentID: "a1", AgentName: "Worker", CrewName: "Ops",
		// Attacker-controlled: point the write at the victim's memory dir.
		AgentMemoryDir: victimDir,
		Trigger:        "run_failed",
		FailureSnippet: "deploy.sh: missing DATABASE_URL",
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/negative-learning", mustJSON(t, body))
	r = r.WithContext(context.WithValue(r.Context(), ctxWorkspaceID, body.WorkspaceID))
	h.HandleNegativeLearning(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}

	// The victim's lessons.md must NOT have been created/written.
	if _, err := os.Stat(filepath.Join(victimDir, "lessons.md")); !os.IsNotExist(err) {
		raw, _ := os.ReadFile(filepath.Join(victimDir, "lessons.md"))
		t.Fatalf("MEMORY POISONING: lesson written into victim dir %q (err=%v)\ncontent=%s",
			victimDir, err, string(raw))
	}

	// The lesson must land under the requesting agent's own derived dir.
	derivedDir := filepath.Join(base, "crews", "cr1", "agents", "worker", ".memory")
	data, err := os.ReadFile(filepath.Join(derivedDir, "lessons.md"))
	if err != nil {
		t.Fatalf("lesson not written to requesting agent's derived dir %q: %v", derivedDir, err)
	}
	if !bytes.Contains(data, []byte("kind: negative")) {
		t.Errorf("derived lessons.md missing 'kind: negative'\n---\n%s\n---", string(data))
	}
}
