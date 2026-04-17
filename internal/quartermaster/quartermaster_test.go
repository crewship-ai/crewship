package quartermaster

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"

	_ "modernc.org/sqlite"
)

// schemaSQL mirrors the subset of migration 52 the quartermaster needs.
// We don't require the full migrate package because we only read from
// journal_entries — the other tables are irrelevant to this unit test.
const schemaSQL = `
CREATE TABLE workspaces (id TEXT PRIMARY KEY);
INSERT INTO workspaces (id) VALUES ('ws_test');

CREATE TABLE journal_entries (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    crew_id TEXT,
    agent_id TEXT,
    mission_id TEXT,
    ts TEXT NOT NULL,
    entry_type TEXT NOT NULL,
    severity TEXT NOT NULL DEFAULT 'info',
    actor_type TEXT NOT NULL,
    actor_id TEXT,
    summary TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '{}',
    refs TEXT NOT NULL DEFAULT '{}',
    trace_id TEXT,
    span_id TEXT,
    expires_at TEXT
);
CREATE INDEX idx_journal_ws_ts ON journal_entries(workspace_id, ts DESC);
`

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recordingEmitter is a synchronous in-memory Emitter. Unlike the
// batched journal.Writer it persists on every Emit so tests don't need
// a sleep-wait-flush dance.
type recordingEmitter struct {
	db       *sql.DB
	inner    *journal.Writer
	mu       sync.Mutex
	captured []journal.Entry
}

func newRecordingEmitter(db *sql.DB) *recordingEmitter {
	return &recordingEmitter{
		db:    db,
		inner: journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1, FlushInterval: 5 * time.Millisecond}),
	}
}

func (r *recordingEmitter) Emit(ctx context.Context, e journal.Entry) (string, error) {
	id, err := r.inner.Emit(ctx, e)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	e.ID = id
	r.captured = append(r.captured, e)
	r.mu.Unlock()
	return id, nil
}

func (r *recordingEmitter) Flush(ctx context.Context) error { return r.inner.Flush(ctx) }
func (r *recordingEmitter) Close()                          { _ = r.inner.Close() }

func (r *recordingEmitter) captureByType(t journal.EntryType) []journal.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []journal.Entry
	for _, e := range r.captured {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// seed inserts a journal entry synchronously via raw SQL so tests can
// deterministically build a trajectory without racing the batcher.
func seed(t *testing.T, db *sql.DB, e journal.Entry) {
	t.Helper()
	if e.ID == "" {
		e.ID = "j_" + randID(t)
	}
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	if e.Severity == "" {
		e.Severity = journal.SeverityInfo
	}
	payload := "{}"
	refs := "{}"
	if len(e.Payload) > 0 {
		b, err := jsonMarshal(e.Payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		payload = b
	}
	if len(e.Refs) > 0 {
		b, err := jsonMarshal(e.Refs)
		if err != nil {
			t.Fatalf("marshal refs: %v", err)
		}
		refs = b
	}
	_, err := db.Exec(`INSERT INTO journal_entries
		(id, workspace_id, crew_id, agent_id, mission_id, ts, entry_type, severity, actor_type, actor_id, summary, payload, refs, trace_id, span_id, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID,
		e.WorkspaceID,
		nullIfEmpty(e.CrewID),
		nullIfEmpty(e.AgentID),
		nullIfEmpty(e.MissionID),
		e.TS.UTC().Format("2006-01-02T15:04:05.000Z"),
		string(e.Type),
		string(e.Severity),
		string(e.ActorType),
		nullIfEmpty(e.ActorID),
		e.Summary,
		payload,
		refs,
		nullIfEmpty(e.TraceID),
		nullIfEmpty(e.SpanID),
		nil,
	)
	if err != nil {
		t.Fatalf("seed insert: %v", err)
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ---------- tests ----------

func TestExtractProjectsSteps(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	now := time.Now().UTC()
	base := journal.Entry{WorkspaceID: "ws_test", MissionID: "m1", ActorType: journal.ActorAgent, ActorID: "a1", Summary: "s"}

	// Mix of kept and dropped types. Extract must drop output_chunk,
	// container.metrics, and network.port_opened while keeping the rest.
	kept := []journal.EntryType{
		journal.EntryAssignmentCreate,
		journal.EntryExecCommand,
		journal.EntryLLMCall,
		journal.EntryKeeperDecision,
		journal.EntryMissionStatus,
		journal.EntryGuardrailOutput,
	}
	dropped := []journal.EntryType{
		journal.EntryExecOutputChunk,
		journal.EntryContainerMetrics,
		journal.EntryNetworkPortOpen,
	}

	for i, k := range kept {
		e := base
		e.Type = k
		e.TS = now.Add(time.Duration(i) * time.Second)
		seed(t, db, e)
	}
	for i, k := range dropped {
		e := base
		e.Type = k
		e.TS = now.Add(time.Duration(10+i) * time.Second)
		seed(t, db, e)
	}

	steps, err := Extract(context.Background(), db, "ws_test", "m1")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(steps) != len(kept) {
		t.Fatalf("expected %d steps, got %d", len(kept), len(steps))
	}
	// Steps must be in time order (oldest first).
	for i := 1; i < len(steps); i++ {
		if steps[i].Index <= steps[i-1].Index {
			t.Errorf("step index not monotonic at %d", i)
		}
	}
	// Kept types must appear in the extracted set.
	seen := map[string]bool{}
	for _, s := range steps {
		seen[s.EntryType] = true
	}
	for _, k := range kept {
		if !seen[string(k)] {
			t.Errorf("missing projected step for type %s", k)
		}
	}
	for _, d := range dropped {
		if seen[string(d)] {
			t.Errorf("unexpected projection for dropped type %s", d)
		}
	}
}

func TestComputeToolSuccessRate(t *testing.T) {
	// 3 exec.command: 2 pass, 1 fail. 2 keeper.decision: 1 allow, 1 deny.
	// Expected success rate = (2 + 1) / (3 + 2) = 0.6.
	steps := []TrajectoryStep{
		{Index: 0, EntryType: "exec.command", ToolName: "ls", Success: true},
		{Index: 1, EntryType: "exec.command", ToolName: "grep", Success: true},
		{Index: 2, EntryType: "exec.command", ToolName: "rm", Success: false},
		{Index: 3, EntryType: "keeper.decision", ToolName: "cred_a", Success: true},
		{Index: 4, EntryType: "keeper.decision", ToolName: "cred_b", Success: false},
	}
	m := Compute(steps)
	if got := roundTo(m.ToolSuccessRate, 2); got != 0.60 {
		t.Errorf("tool success rate: got %v want 0.60", got)
	}
	if m.ToolCallCount != 3 {
		t.Errorf("tool call count: got %d want 3", m.ToolCallCount)
	}
}

func TestComputeDetectsToolLoop(t *testing.T) {
	// 6 rapid repeats of the same exec.command within a 2-minute budget →
	// tool_loop should fire.
	steps := []TrajectoryStep{}
	for i := 0; i < 6; i++ {
		steps = append(steps, TrajectoryStep{
			Index:     i,
			EntryType: "exec.command",
			ToolName:  "curl",
			Success:   true,
			ElapsedMs: 5000, // 5s between; 6 * 5s = 30s, well under 2min
		})
	}
	m := Compute(steps)
	if !contains(m.FailureModes, "tool_loop") {
		t.Errorf("expected tool_loop in FailureModes, got %v", m.FailureModes)
	}
}

func TestComputeDetectsBudgetRunaway(t *testing.T) {
	steps := []TrajectoryStep{
		{Index: 0, EntryType: "exec.command", ToolName: "a", Success: true},
		{Index: 1, EntryType: "budget.exceeded", Success: false},
	}
	m := Compute(steps)
	if !contains(m.FailureModes, "budget_runaway") {
		t.Errorf("expected budget_runaway, got %v", m.FailureModes)
	}
}

func TestComputeDetectsEscalationLoop(t *testing.T) {
	steps := []TrajectoryStep{
		{Index: 0, EntryType: "peer.escalation", Success: true},
		{Index: 1, EntryType: "peer.escalation", Success: true},
		{Index: 2, EntryType: "peer.escalation", Success: true},
	}
	m := Compute(steps)
	if !contains(m.FailureModes, "escalation_loop") {
		t.Errorf("expected escalation_loop, got %v", m.FailureModes)
	}
}

func TestReplayEmitsEvents(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	em := newRecordingEmitter(db)
	defer em.Close()

	now := time.Now().UTC()
	base := journal.Entry{WorkspaceID: "ws_test", MissionID: "m1", ActorType: journal.ActorAgent, ActorID: "a1", Summary: "s"}
	for i, kind := range []journal.EntryType{
		journal.EntryAssignmentCreate,
		journal.EntryExecCommand,
		journal.EntryLLMCall,
		journal.EntryMissionStatus,
	} {
		e := base
		e.Type = kind
		e.TS = now.Add(time.Duration(i) * time.Second)
		if kind == journal.EntryMissionStatus {
			e.Payload = map[string]any{"to_status": "completed"}
		}
		if kind == journal.EntryLLMCall {
			e.Payload = map[string]any{"total_tokens": 1234}
		}
		seed(t, db, e)
	}

	run, err := Replay(context.Background(), db, em, "ws_test", "m1", 42)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if run.Status != "completed" {
		t.Errorf("status: got %q want completed", run.Status)
	}
	if run.SeedSignature == "" {
		t.Error("expected non-empty seed signature")
	}
	if run.Metrics.TotalTokens != 1234 {
		t.Errorf("total tokens: got %d want 1234", run.Metrics.TotalTokens)
	}

	// Flush the emitter so we can inspect the durable record.
	_ = em.Flush(context.Background())
	started := em.captureByType(journal.EntryEvalRunStarted)
	metrics := em.captureByType(journal.EntryEvalMetric)
	if len(started) != 1 {
		t.Errorf("expected 1 eval.run_started, got %d", len(started))
	}
	if len(metrics) < 4 {
		t.Errorf("expected at least 4 eval.metric entries, got %d", len(metrics))
	}
}

func TestCompareRegressionThresholds(t *testing.T) {
	baseline := EvalMetrics{
		ToolSuccessRate: 0.95,
		StepsToGoal:     10,
		TotalCostUSD:    1.00,
		Hallucinations:  0,
	}

	tests := []struct {
		name      string
		candidate EvalMetrics
		wantReg   bool
		wantName  string
	}{
		{
			"tool success drop > 5%",
			EvalMetrics{ToolSuccessRate: 0.85, StepsToGoal: 10, TotalCostUSD: 1.00},
			true, "tool_success_rate",
		},
		{
			"steps rise > 20%",
			EvalMetrics{ToolSuccessRate: 0.95, StepsToGoal: 13, TotalCostUSD: 1.00},
			true, "steps_to_goal",
		},
		{
			"cost rise > 15%",
			EvalMetrics{ToolSuccessRate: 0.95, StepsToGoal: 10, TotalCostUSD: 1.20},
			true, "total_cost_usd",
		},
		{
			"hallucinations any rise",
			EvalMetrics{ToolSuccessRate: 0.95, StepsToGoal: 10, TotalCostUSD: 1.00, Hallucinations: 1},
			true, "hallucinations",
		},
		{
			"within thresholds — no regression",
			EvalMetrics{ToolSuccessRate: 0.94, StepsToGoal: 11, TotalCostUSD: 1.10, Hallucinations: 0},
			false, "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			report := Compare(baseline, tc.candidate)
			if report.Regressed != tc.wantReg {
				t.Fatalf("regressed: got %v want %v (summary=%q)", report.Regressed, tc.wantReg, report.DeltaSummary)
			}
			if tc.wantReg {
				found := false
				for _, d := range report.Deltas {
					if d.Name == tc.wantName && d.Regressed {
						found = true
					}
				}
				if !found {
					t.Errorf("expected metric %q to be flagged regressed", tc.wantName)
				}
			}
		})
	}
}

// stubJudge returns a fixed verdict — lets tests prove the ensemble
// aggregation math.
type stubJudge struct {
	score      float64
	confidence float64
	reasoning  string
	err        error
}

func (s *stubJudge) Judge(ctx context.Context, prompt string, rubric []string) (JudgeVerdict, error) {
	if s.err != nil {
		return JudgeVerdict{}, s.err
	}
	return JudgeVerdict{
		Score:      s.score,
		Confidence: s.confidence,
		Reasoning:  s.reasoning,
		Rubric:     append([]string(nil), rubric...),
	}, nil
}

func TestEnsembleJudgeAgreement(t *testing.T) {
	// Three identical judges → median == score, high confidence → no
	// escalation, no disagreement warning.
	js := []JudgeInterface{
		&stubJudge{score: 0.9, confidence: 0.95, reasoning: "clean"},
		&stubJudge{score: 0.9, confidence: 0.95, reasoning: "clean"},
		&stubJudge{score: 0.9, confidence: 0.95, reasoning: "clean"},
	}
	v, err := EnsembleJudge(context.Background(), js, "prompt", []string{"r1", "r2"}, 3, 7)
	if err != nil {
		t.Fatalf("ensemble: %v", err)
	}
	if roundTo(v.Score, 2) != 0.90 {
		t.Errorf("score: got %v want 0.90", v.Score)
	}
	if v.HumanEscalate {
		t.Errorf("should not escalate at confidence 0.95")
	}
	if strings.Contains(v.Reasoning, "high judge disagreement") {
		t.Errorf("unexpected disagreement warning: %q", v.Reasoning)
	}
}

func TestEnsembleJudgeDisagreement(t *testing.T) {
	// Wide spread: 0.1, 0.5, 0.9 → stddev ~0.327 → disagreement warning.
	// Also low confidence → HumanEscalate.
	js := []JudgeInterface{
		&stubJudge{score: 0.1, confidence: 0.5, reasoning: "bad"},
		&stubJudge{score: 0.5, confidence: 0.5, reasoning: "mid"},
		&stubJudge{score: 0.9, confidence: 0.5, reasoning: "good"},
	}
	v, err := EnsembleJudge(context.Background(), js, "prompt", []string{"r1"}, 3, 7)
	if err != nil {
		t.Fatalf("ensemble: %v", err)
	}
	if !strings.Contains(v.Reasoning, "high judge disagreement") {
		t.Errorf("expected disagreement warning, got %q", v.Reasoning)
	}
	if !v.HumanEscalate {
		t.Errorf("expected HumanEscalate=true at confidence 0.5")
	}
	if roundTo(v.Score, 2) != 0.50 {
		t.Errorf("median: got %v want 0.50", v.Score)
	}
}

func TestEnsembleJudgePropagatesError(t *testing.T) {
	js := []JudgeInterface{
		&stubJudge{score: 0.5, confidence: 0.95},
		&stubJudge{err: errors.New("boom")},
	}
	_, err := EnsembleJudge(context.Background(), js, "prompt", []string{"r1"}, 2, 1)
	if err == nil {
		t.Error("expected error from failing judge")
	}
}

// ---------- helpers ----------

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func roundTo(f float64, digits int) float64 {
	mul := 1.0
	for i := 0; i < digits; i++ {
		mul *= 10
	}
	return float64(int(f*mul+0.5)) / mul
}

// randID / jsonMarshal are kept local to avoid reaching into internal/journal.
func randID(t *testing.T) string {
	t.Helper()
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func jsonMarshal(v map[string]any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
