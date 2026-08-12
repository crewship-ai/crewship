package api

// Session provenance on the run record, for the run drivers that live in this
// package (#1934).
//
// The chat bridge and the scheduler already merge the CLI's session-init
// metadata into the terminal run entry; assignment, peer-query and webhook runs
// did not, so `crewship run get` rendered them as clean no matter what the CLI
// reported at startup — including an MCP server it refused to load, which costs
// the agent a capability and still exits 0. These tests drive each of the three
// paths through a scripted CLI stream and assert the provenance lands on the
// terminal record, on the FAILED run as much as the COMPLETED one.

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/webhook"
)

// A trimmed real session-init line (see the parser's own fixtures). The
// mcp_server_errors entry is the silent capability loss the run record exists
// to surface — the CLI dropped a server and the run still exits 0.
const provenanceInitLine = `{"type":"system","subtype":"init","cwd":"/output/x",` +
	`"session_id":"e0e80a31-cceb-4df9-929d-6a07e7984399","claude_code_version":"2.1.226",` +
	`"apiKeySource":"ANTHROPIC_API_KEY","permissionMode":"bypassPermissions",` +
	`"model":"claude-opus-5","tools":["Read"],` +
	`"mcp_server_errors":[{"name":"crewship-memory","type":"connection_failed","message":"connect: refused"}]}` + "\n"

const provenanceTextLine = `{"type":"stream_event","event":{"type":"content_block_delta",` +
	`"delta":{"type":"text_delta","text":"done"}}}` + "\n"

// The CLI exits 0 and reports its own turn failed — the in-band failure shape.
// It is the interesting one: a failed run is when someone asks which binary and
// which credential answered.
const provenanceFailLine = `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"refused",` +
	`"total_cost_usd":0.03,"permission_denials":[{"tool_name":"Bash","tool_input":{"command":"curl http://x"}}]}` + "\n"

const provenanceOKLine = `{"type":"result","subtype":"success","is_error":false,"result":"done",` +
	`"total_cost_usd":0.03,"permission_denials":[{"tool_name":"Bash","tool_input":{"command":"curl http://x"}}]}` + "\n"

// The same terminal envelope with nothing on it. Used by the no-init cases so
// "records nothing" stays literally true: a run that reports usage SHOULD
// record usage even without an init event, and that is pinned separately.
const provenanceBareOKLine = `{"type":"result","subtype":"success","is_error":false,"result":"done"}` + "\n"

// assertProvenance checks the four run-scoped fields plus the dropped-server
// list, i.e. everything MergeSessionInitMeta is supposed to have persisted.
//
// It also checks the half that these drivers used to throw away: they set
// CaptureResultMeta and then merged only the session-init keys, so a delegated
// run recorded no denials, no cost and no resolved model while the same work
// through chat or the scheduler recorded all three (#1949).
func assertProvenance(t *testing.T, run *journal.RunAggregated) {
	t.Helper()
	if run.Model != "claude-opus-5" {
		t.Errorf("model = %q, want claude-opus-5 — the driver captured the resolved model and dropped it", run.Model)
	}
	if len(run.PermissionDenials) != 1 || run.PermissionDenials[0].ToolName != "Bash" {
		t.Errorf("permission_denials = %v, want [Bash] — a delegated run that was permission-blocked reads as one that chose not to act",
			run.PermissionDenials)
	}
	if run.CLIVersion != "2.1.226" {
		t.Errorf("cli_version = %q, want 2.1.226 — the run record cannot say which binary answered", run.CLIVersion)
	}
	if run.APIKeySource != "ANTHROPIC_API_KEY" {
		t.Errorf("api_key_source = %q, want ANTHROPIC_API_KEY", run.APIKeySource)
	}
	if run.PermissionMode != "bypassPermissions" {
		t.Errorf("permission_mode = %q, want bypassPermissions", run.PermissionMode)
	}
	if run.SessionID != "e0e80a31-cceb-4df9-929d-6a07e7984399" {
		t.Errorf("session_id = %q, want the CLI's own correlation key", run.SessionID)
	}
	if len(run.MCPServerErrors) != 1 || run.MCPServerErrors[0].Name != "crewship-memory" {
		t.Errorf("mcp_server_errors = %+v, want the one skipped server — a lost capability the exit code does not report",
			run.MCPServerErrors)
	}
}

// assertNoTerminalMetadata reads the raw terminal run.* payload and fails if it
// carries any PROVENANCE key. Going through the SQL rather than the derived
// record is the point: the derived record cannot tell an absent key from an
// empty one, and absence is the contract — the mcp-skip gate can only read a
// missing mcp_server_errors as "nothing was skipped" if absence keeps meaning
// that.
//
// It does not require the metadata map itself to be absent. The Claude adapter
// stamps total_cost_usd and num_turns on every result envelope, so a run whose
// CLI reported usage but no init event legitimately records usage — recording
// it is correct, and inventing provenance to sit beside it is not.
func assertNoTerminalMetadata(t *testing.T, db *sql.DB, traceID string) {
	t.Helper()
	var raw string
	if err := db.QueryRow(`
		SELECT COALESCE(payload,'{}') FROM journal_entries
		 WHERE trace_id = ? AND entry_type IN ('run.completed','run.failed')`, traceID).Scan(&raw); err != nil {
		t.Fatalf("read terminal payload: %v", err)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("decode terminal payload %q: %v", raw, err)
	}
	md, _ := p["metadata"].(map[string]any)
	for _, k := range []string{"cli_version", "api_key_source", "permission_mode", "session_id", "mcp_server_errors", "model"} {
		if v, ok := md[k]; ok {
			t.Errorf("terminal payload carries %s=%v with no init event; want the key absent", k, v)
		}
	}
}

// ---------------------------------------------------------------------------
// assignment / mission dispatch
// ---------------------------------------------------------------------------

// runProvenanceAssignment drives runAssignment against a sub-agent whose CLI
// streams `stream`, then returns the run record the journal derives from it.
func runProvenanceAssignment(t *testing.T, asgID, stream string) (*sql.DB, journal.RunAggregated) {
	t.Helper()
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	jw := journal.NewWriter(h.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = jw.Close() })
	h.SetJournal(jw)
	h.orch = orchestrator.New(inbandAsgProvider{stream: stream}, newInbandAsgState(), newTestLogger())
	insertAssignment(t, h.db, asgID, wsID, chatID, leadID, workerID, "PENDING")

	h.runAssignment(context.Background(), asgID, createAssignmentBody{
		TargetSlug: "asg-worker", Task: "t", CrewID: crewID, WorkspaceID: wsID, ChatID: chatID,
	}, targetAgentInfo{ID: workerID, Slug: "asg-worker", Name: "Worker", CrewSlug: "asg"})

	if err := jw.Flush(context.Background()); err != nil {
		t.Fatalf("flush journal: %v", err)
	}
	runs, _, err := journal.ListRuns(context.Background(), h.db, journal.RunsQuery{WorkspaceID: wsID})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want exactly 1", len(runs))
	}
	return h.db, runs[0]
}

func TestAssignmentRun_CompletedCarriesSessionProvenance(t *testing.T) {
	_, run := runProvenanceAssignment(t, "asg-prov-ok",
		provenanceInitLine+provenanceTextLine+provenanceOKLine)
	if run.Status != journal.RunStatusCompleted {
		t.Fatalf("run status = %q, want COMPLETED", run.Status)
	}
	assertProvenance(t, &run)
}

func TestAssignmentRun_FailedCarriesSessionProvenance(t *testing.T) {
	_, run := runProvenanceAssignment(t, "asg-prov-fail",
		provenanceInitLine+provenanceTextLine+provenanceFailLine)
	if run.Status != journal.RunStatusFailed {
		t.Fatalf("run status = %q, want FAILED", run.Status)
	}
	assertProvenance(t, &run)
}

// A run whose CLI never reported an init event (a non-Claude adapter, or a
// dispatch that died before the stream) must record NOTHING, not empty
// strings: absence is what lets a reader tell "never reported" from "reported
// nothing", and the mcp-skip gate keys off exactly that absence.
func TestAssignmentRun_NoInitEventRecordsNothing(t *testing.T) {
	db, run := runProvenanceAssignment(t, "asg-prov-none",
		provenanceTextLine+provenanceBareOKLine)
	if run.CLIVersion != "" || run.APIKeySource != "" || run.SessionID != "" {
		t.Errorf("provenance invented from a stream with no init event: %+v", run)
	}
	if len(run.MCPServerErrors) != 0 {
		t.Errorf("mcp_server_errors = %+v, want none", run.MCPServerErrors)
	}
	assertNoTerminalMetadata(t, db, run.ID)
}

// ---------------------------------------------------------------------------
// peer query
// ---------------------------------------------------------------------------

func runProvenanceQuery(t *testing.T, stream string) (*sql.DB, journal.RunAggregated) {
	t.Helper()
	h, wsID, crewID, _, _, chatID := covQH3Rig(t)
	jw := journal.NewWriter(h.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = jw.Close() })
	h.SetJournal(jw)
	h.orch = orchestrator.New(inbandAsgProvider{stream: stream}, newInbandAsgState(), newTestLogger())

	covQH3Post(t, h, covQH3Body(wsID, crewID, chatID), "")

	if err := jw.Flush(context.Background()); err != nil {
		t.Fatalf("flush journal: %v", err)
	}
	runs, _, err := journal.ListRuns(context.Background(), h.db, journal.RunsQuery{WorkspaceID: wsID})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want exactly 1", len(runs))
	}
	return h.db, runs[0]
}

func TestPeerQueryRun_CompletedCarriesSessionProvenance(t *testing.T) {
	_, run := runProvenanceQuery(t, provenanceInitLine+provenanceTextLine+provenanceOKLine)
	if run.Status != journal.RunStatusCompleted {
		t.Fatalf("run status = %q, want COMPLETED", run.Status)
	}
	assertProvenance(t, &run)
}

func TestPeerQueryRun_FailedCarriesSessionProvenance(t *testing.T) {
	_, run := runProvenanceQuery(t, provenanceInitLine+provenanceTextLine+provenanceFailLine)
	if run.Status != journal.RunStatusFailed {
		t.Fatalf("run status = %q, want FAILED", run.Status)
	}
	assertProvenance(t, &run)
}

func TestPeerQueryRun_NoInitEventRecordsNothing(t *testing.T) {
	db, run := runProvenanceQuery(t, provenanceTextLine+provenanceBareOKLine)
	if run.CLIVersion != "" || run.APIKeySource != "" || run.SessionID != "" {
		t.Errorf("provenance invented from a stream with no init event: %+v", run)
	}
	assertNoTerminalMetadata(t, db, run.ID)
}

// ---------------------------------------------------------------------------
// webhook
// ---------------------------------------------------------------------------

// provenanceWebhookResolver captures the terminal UpdateRun call — the webhook
// path finalizes its run over the resolver rather than emitting the journal
// entry itself, so the metadata map handed to UpdateRun IS the run record's.
type provenanceWebhookResolver struct {
	fakeChatResolver
	mu     sync.Mutex
	status string
	meta   map[string]interface{}
}

func (r *provenanceWebhookResolver) UpdateRun(_ context.Context, _, status string, _ *int, _ *string, meta map[string]interface{}) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status, r.meta = status, meta
	return nil
}

func runProvenanceWebhook(t *testing.T, stream string) (status string, meta map[string]interface{}) {
	t.Helper()
	resolver := &provenanceWebhookResolver{}
	resolver.resolveReturnInfo = &chatbridge.ChatInfo{
		AgentID: "agent-wh", AgentSlug: "ag", AgentRole: "AGENT",
		CrewID: "crew-wh", CrewSlug: "c", WorkspaceID: "ws-prov", CLIAdapter: "CLAUDE_CODE",
	}
	prov := inbandAsgProvider{stream: stream}
	// A real log writer: the dispatch goroutine hands every event to an
	// OutputBuffer unconditionally, and that buffer dereferences its writer.
	h := NewWebhookHandler(setupTestDB(t), newTestLogger(), resolver,
		orchestrator.New(prov, newInbandAsgState(), newTestLogger()), nil, prov,
		logcollector.NewWriter(t.TempDir(), newTestLogger()))

	if err := h.trigger(context.Background(), "crew-wh", "agent-wh",
		webhook.WebhookPayload{Event: "deploy", Source: "gh"}); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if !waitForBackgroundWork(60 * time.Second) {
		t.Fatal("webhook dispatch goroutine did not finish")
	}
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if resolver.meta == nil {
		t.Fatal("UpdateRun never called with metadata")
	}
	return resolver.status, resolver.meta
}

func TestWebhookRun_CompletedCarriesSessionProvenance(t *testing.T) {
	status, meta := runProvenanceWebhook(t, provenanceInitLine+provenanceTextLine+provenanceOKLine)
	if status != "COMPLETED" {
		t.Fatalf("status = %q, want COMPLETED", status)
	}
	if meta["cli_version"] != "2.1.226" {
		t.Errorf("cli_version = %v, want 2.1.226", meta["cli_version"])
	}
	if meta["api_key_source"] != "ANTHROPIC_API_KEY" {
		t.Errorf("api_key_source = %v", meta["api_key_source"])
	}
	if meta["mcp_server_errors"] == nil {
		t.Error("mcp_server_errors dropped — the skipped server is invisible on the run record")
	}
}

func TestWebhookRun_FailedCarriesSessionProvenance(t *testing.T) {
	status, meta := runProvenanceWebhook(t, provenanceInitLine+provenanceTextLine+provenanceFailLine)
	if status != "FAILED" {
		t.Fatalf("status = %q, want FAILED", status)
	}
	if meta["cli_version"] != "2.1.226" {
		t.Errorf("cli_version = %v, want 2.1.226 on a FAILED run — the one you need it for", meta["cli_version"])
	}
}

func TestWebhookRun_NoInitEventRecordsNothing(t *testing.T) {
	_, meta := runProvenanceWebhook(t, provenanceTextLine+provenanceBareOKLine)
	for _, k := range []string{"cli_version", "api_key_source", "permission_mode", "session_id", "mcp_server_errors"} {
		if _, ok := meta[k]; ok {
			t.Errorf("metadata[%q] present without an init event — absence is what marks 'never reported'", k)
		}
	}
}
