package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestDocumentRunCreationDisabledHasNoAdmissionSideEffects(t *testing.T) {
	f, intent := documentCreationAdmissionFixture(t)
	mustExec(t, f.rig.db, `DELETE FROM journal_entries WHERE trace_id=?`, f.a.RunID)
	mustExec(t, f.rig.db, `DELETE FROM artifact_run_principals WHERE run_id=?`, f.a.RunID)
	writer := journal.NewWriter(f.rig.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	h := NewInternalHandler(f.rig.db, "synthetic-host-admission", newTestLogger())
	h.SetJournal(writer)
	body, err := json.Marshal(map[string]any{"id": f.a.RunID, "agent_id": f.a.AgentID, "workspace_id": f.a.WorkspaceID, "chat_id": f.a.ChatID, "trigger_type": "USER", "human_authority": f.receipt, "trigger_message_id": f.a.MessageID, "document_creation_intent": intent})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/api/v1/internal/runs", bytes.NewReader(body))
	response := httptest.NewRecorder()
	h.CreateRun(response, request)
	if response.Code != 503 {
		t.Errorf("disabled creation admission status %d, want503", response.Code)
	}
	for _, query := range []string{`SELECT COUNT(*) FROM journal_entries WHERE entry_type='run.started'`, `SELECT COUNT(*) FROM artifact_run_principals`, `SELECT COUNT(*) FROM document_creation_consents WHERE run_id IS NOT NULL`} {
		var n int
		if err := f.rig.db.QueryRow(query).Scan(&n); err != nil || n != 0 {
			t.Errorf("disabled creation persisted admission state: count%d err%v", n, err)
		}
	}
}

type documentRunAdmissionRig struct {
	f       *artifactPrincipalFixture
	handler *InternalHandler
	writer  *journal.Writer
	body    map[string]any
	key     []byte
}

func newDocumentRunAdmissionRig(t *testing.T) *documentRunAdmissionRig {
	t.Helper()
	f, intent := documentCreationAdmissionFixture(t)
	mustExec(t, f.rig.db, `DELETE FROM journal_entries WHERE trace_id=?`, f.a.RunID)
	mustExec(t, f.rig.db, `DELETE FROM artifact_run_principals WHERE run_id=?`, f.a.RunID)
	key := []byte("synthetic-document-run-admission-chain")
	writer := journal.NewWriter(f.rig.db, newTestLogger(), journal.WriterOptions{ChainKey: key, FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	h := NewInternalHandler(f.rig.db, "synthetic-host-admission", newTestLogger())
	h.SetJournal(writer)
	h.documentCreationEnabled = true
	return &documentRunAdmissionRig{f: f, handler: h, writer: writer, key: key, body: map[string]any{"id": f.a.RunID, "agent_id": f.a.AgentID, "workspace_id": f.a.WorkspaceID, "chat_id": f.a.ChatID, "trigger_type": "USER", "human_authority": f.receipt, "trigger_message_id": f.a.MessageID, "document_creation_intent": intent}}
}

func (rig *documentRunAdmissionRig) call(t *testing.T, modify func(map[string]any), crew bool) *httptest.ResponseRecorder {
	t.Helper()
	body := make(map[string]any, len(rig.body))
	for k, v := range rig.body {
		body[k] = v
	}
	if modify != nil {
		modify(body)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if crew {
		ctx = context.WithValue(ctx, ctxInternalTokenCrew, rig.f.a.CrewID)
	}
	request := httptest.NewRequest("POST", "/api/v1/internal/runs", bytes.NewReader(raw)).WithContext(ctx)
	response := httptest.NewRecorder()
	rig.handler.CreateRun(response, request)
	return response
}

func (rig *documentRunAdmissionRig) assertNoAdmission(t *testing.T, observed int64, status string) {
	t.Helper()
	for _, query := range []string{`SELECT COUNT(*) FROM journal_entries WHERE entry_type='run.started'`, `SELECT COUNT(*) FROM artifact_run_principals`, `SELECT COUNT(*) FROM document_creation_consents WHERE run_id IS NOT NULL`} {
		var n int
		if err := rig.f.rig.db.QueryRow(query).Scan(&n); err != nil || n != 0 {
			t.Errorf("refused admission persisted state: %d %v", n, err)
		}
	}
	var actual string
	if err := rig.f.rig.db.QueryRow(`SELECT status FROM agents WHERE id=?`, rig.f.a.AgentID).Scan(&actual); err != nil || actual != status || observed != 0 {
		t.Errorf("refusal changed status/observer: %s expected%s observed%d err%v", actual, status, observed, err)
	}
}

func TestDocumentRunCreationAdmissionAtomicRetryAndConfiguredWriter(t *testing.T) {
	rig := newDocumentRunAdmissionRig(t)
	rig.f.rig.db.SetMaxOpenConns(1)
	var observed atomic.Int64
	rig.writer.AddCommitObserver(func(entries []journal.Entry) {
		for _, entry := range entries {
			if entry.Type == journal.EntryRunStarted {
				observed.Add(1)
				var n int
				if err := rig.f.rig.db.QueryRow(`SELECT COUNT(*) FROM document_creation_consents c JOIN artifact_run_principals p ON p.run_id=c.run_id WHERE c.run_id=? AND c.expires_at=p.expires_at`, rig.f.a.RunID).Scan(&n); err != nil || n != 1 {
					t.Errorf("observer ran before complete admission: %d %v", n, err)
				}
			}
		}
	})
	creationExpect(t, rig.call(t, nil, false), 201)
	var expiry string
	if err := rig.f.rig.db.QueryRow(`SELECT expires_at FROM artifact_run_principals WHERE run_id=?`, rig.f.a.RunID).Scan(&expiry); err != nil {
		t.Fatal(err)
	}
	creationExpect(t, rig.call(t, nil, false), 201)
	var after string
	if err := rig.f.rig.db.QueryRow(`SELECT expires_at FROM artifact_run_principals WHERE run_id=?`, rig.f.a.RunID).Scan(&after); err != nil || after != expiry || observed.Load() != 1 {
		t.Fatalf("retry renewed admission or fanout: %q %q %d %v", expiry, after, observed.Load(), err)
	}
	verified, err := journal.VerifyChainWithKey(t.Context(), rig.f.rig.db, rig.f.a.WorkspaceID, rig.key)
	if err != nil || !verified.OK {
		t.Fatalf("configured writer chain changed: %+v %v", verified, err)
	}
	creationExpect(t, rig.call(t, func(body map[string]any) {
		body["document_creation_intent"] = map[string]string{"submission_id": "changed-consent"}
	}, false), 409)
	if observed.Load() != 1 {
		t.Fatal("conflict notified a new start")
	}
}

func TestDocumentRunCreationAdmissionRollsBackEveryWrite(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"journal", `CREATE TRIGGER synthetic_admission_fault BEFORE INSERT ON journal_entries WHEN NEW.entry_type='run.started' BEGIN SELECT RAISE(ABORT,'synthetic journal fault'); END`},
		{"principal", `CREATE TRIGGER synthetic_admission_fault BEFORE INSERT ON artifact_run_principals BEGIN SELECT RAISE(ABORT,'synthetic principal fault'); END`},
		{"consent", `CREATE TRIGGER synthetic_admission_fault BEFORE UPDATE OF run_id ON document_creation_consents WHEN NEW.run_id IS NOT NULL BEGIN SELECT RAISE(ABORT,'synthetic consent fault'); END`},
		{"revoked consent", `UPDATE document_creation_consents SET revoked_at='2026-10-17T00:00:00Z'`},
		{"denied policy", `UPDATE workspace_artifact_creation_policy SET enabled=0`},
		{"selected Document run", `CREATE TABLE document_requests(id TEXT,page_id TEXT,run_id TEXT); INSERT INTO document_requests VALUES('selected','other-page','principal-run')`},
		{"selected Slides run", `CREATE TABLE slides_requests(id TEXT,page_id TEXT,original_run_id TEXT); INSERT INTO slides_requests VALUES('selected','other-page','principal-run')`},
		{"unsupported optional schema", `CREATE TABLE document_requests(run_id TEXT)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newDocumentRunAdmissionRig(t)
			rig.f.rig.db.SetMaxOpenConns(1)
			var status string
			if err := rig.f.rig.db.QueryRow(`SELECT status FROM agents WHERE id=?`, rig.f.a.AgentID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			var observed atomic.Int64
			rig.writer.AddCommitObserver(func([]journal.Entry) { observed.Add(1) })
			mustExec(t, rig.f.rig.db, tc.sql)
			response := rig.call(t, nil, false)
			if response.Code < 400 {
				t.Fatalf("fault admitted run: %d", response.Code)
			}
			rig.assertNoAdmission(t, observed.Load(), status)
		})
	}
}

func TestDocumentRunCreationAdmissionRejectsUntrustedOrUnavailable(t *testing.T) {
	for _, name := range []string{"crew", "no receipt", "no message", "selected origin", "non-user", "no transaction writer"} {
		t.Run(name, func(t *testing.T) {
			rig := newDocumentRunAdmissionRig(t)
			var status string
			if err := rig.f.rig.db.QueryRow(`SELECT status FROM agents WHERE id=?`, rig.f.a.AgentID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			var observed atomic.Int64
			rig.writer.AddCommitObserver(func([]journal.Entry) { observed.Add(1) })
			if name == "no transaction writer" {
				rig.handler.SetJournal(noopEmitter{})
			}
			response := rig.call(t, func(body map[string]any) {
				switch name {
				case "no receipt":
					delete(body, "human_authority")
				case "no message":
					delete(body, "trigger_message_id")
				case "selected origin":
					body["chat_origin_id"] = "untrusted-origin"
				case "non-user":
					body["trigger_type"] = "CRON"
				}
			}, name == "crew")
			want := 403
			if name == "no transaction writer" {
				want = 503
			}
			creationExpect(t, response, want)
			rig.assertNoAdmission(t, observed.Load(), status)
		})
	}
}

func TestDocumentRunCreationAdmissionConcurrentRetryAndTerminal(t *testing.T) {
	rig := newDocumentRunAdmissionRig(t)
	rig.f.rig.db.SetMaxOpenConns(1)
	var observed atomic.Int64
	rig.writer.AddCommitObserver(func([]journal.Entry) { observed.Add(1) })
	responses := make(chan *httptest.ResponseRecorder, 3)
	var group sync.WaitGroup
	for range 3 {
		group.Add(1)
		go func() { defer group.Done(); responses <- rig.call(t, nil, false) }()
	}
	group.Wait()
	close(responses)
	for response := range responses {
		creationExpect(t, response, 201)
	}
	if observed.Load() != 1 {
		t.Fatalf("concurrent retry produced %d start notifications", observed.Load())
	}
	if _, err := rig.writer.EmitSync(t.Context(), journal.Entry{WorkspaceID: rig.f.a.WorkspaceID, CrewID: rig.f.a.CrewID, AgentID: rig.f.a.AgentID, TraceID: rig.f.a.RunID, Type: journal.EntryRunCompleted, ActorType: journal.ActorSystem, Summary: "synthetic completed run"}); err != nil {
		t.Fatal(err)
	}
	creationExpect(t, rig.call(t, nil, false), 409)
}
