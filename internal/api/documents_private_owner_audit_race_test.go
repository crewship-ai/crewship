package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/documents"
)

// The private Go authority and Store are real. The Node transport/decode is a
// bounded adapter below; opaque snapshots are not Yjs or model acceptance.
func TestDocumentPrivateOwnerAuditCustodyFence(t *testing.T) {
	for _, operation := range []string{"state", "checkpoints", "checkpoint-read"} {
		for _, rejectAudit := range []bool{false, true} {
			name := operation + "/audit-durable"
			if rejectAudit {
				name = operation + "/audit-unavailable"
			}
			t.Run(name, func(t *testing.T) {
				rig := pagesTransferSetup(t)
				const page = "custody-document"
				const pin = "private-pin"
				rig.seedPage(t, page, page, "Private custody fixture", rig.adminID, "")
				execOrFatal(t, rig.db, `UPDATE pages SET private_audience=1 WHERE id=?`, page)
				pages := NewPageHandler(rig.db, nil, newTestLogger())
				store := documents.New(rig.db, documents.Options{})
				key := documents.Key{WorkspaceID: rig.wsID, PageID: page}
				allow := func(context.Context, *sql.Tx) (string, error) { return "user:" + rig.adminID, nil }
				retained := []byte("opaque-private-custody-fixture")
				if _, err := store.Init(t.Context(), key, 2, retained, json.RawMessage(`[]`), allow); err != nil {
					t.Fatal(err)
				}
				lease, err := store.Acquire(t.Context(), key, "fixture-engine", allow)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.Checkpoint(t.Context(), key, lease, pin, retained, 0, allow); err != nil {
					t.Fatal(err)
				}
				// Test-only schedule: outer preflight still sees caller as custodian;
				// the actual Store writer fence transfers custody before authorization.
				execOrFatal(t, rig.db, `CREATE TRIGGER fixture_document_custody_fence AFTER UPDATE ON document_writer_fence BEGIN UPDATE pages SET owner_user_id='ptdeparting' WHERE id='custody-document' AND owner_user_id='ptadmin'; END`)
				if rejectAudit {
					execOrFatal(t, rig.db, `CREATE TRIGGER fixture_private_audit_unavailable BEFORE INSERT ON audit_logs WHEN NEW.action='artifact.private_owner_accessed' BEGIN SELECT RAISE(ABORT,'fixture audit unavailable'); END`)
				}
				request := ownershipRequest(t, rig, "GET", "/api/v1/pages/"+page+"/document", "")
				request.SetPathValue("slug", page)
				request.SetPathValue("checkpoint", pin)
				ctx, cancel := context.WithTimeout(request.Context(), 3*time.Second)
				defer cancel()
				request = request.WithContext(ctx)
				rig.db.SetMaxOpenConns(1) // A helper accidentally invoked in tx must not pass.
				authority := &documentAuthority{pages: pages, store: store}
				privateSuccess, auditBeforeBytes := false, false
				pages.documentCall = func(ctx context.Context, workspace, _ string, pageID, publicOperation string, _ any) ([]byte, int, error) {
					expected := map[string]string{"state": "read", "checkpoints": "checkpoints", "checkpoint-read": "checkpoint-read"}[operation]
					if workspace != rig.wsID || pageID != page || publicOperation != expected {
						t.Fatalf("transport scope changed: %s %s %s", workspace, pageID, publicOperation)
					}
					body := `{}`
					if operation == "checkpoint-read" {
						body = `{"id":"` + pin + `"}`
					}
					inner := request.Clone(ctx)
					inner.Method = "POST"
					inner.Body = io.NopCloser(strings.NewReader(body))
					inner.SetPathValue("page", pageID)
					inner.SetPathValue("operation", operation)
					recorder := httptest.NewRecorder()
					gate := &documentAuditGateWriter{ResponseRecorder: recorder, beforeHeader: func(code int) {
						if code != http.StatusOK {
							return
						}
						privateSuccess = true
						var count int
						if err := rig.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_logs WHERE workspace_id=? AND user_id=? AND entity_id=? AND action='artifact.private_owner_accessed' AND json_extract(metadata,'$.owner_user_id')=?`, rig.wsID, rig.adminID, page, rig.userID).Scan(&count); err != nil {
							t.Fatal(err)
						}
						auditBeforeBytes = count > 0
					}}
					authority.operation(gate, inner)
					raw, code := recorder.Body.Bytes(), recorder.Code
					if code != http.StatusOK || operation == "state" {
						return raw, code, nil
					}
					if operation == "checkpoints" {
						wrapped, err := json.Marshal(map[string]any{"checkpoints": json.RawMessage(raw)})
						return wrapped, code, err
					}
					// Stand in only for Node decoding, after real checkpoint authority read.
					var value map[string]any
					if err := json.Unmarshal(raw, &value); err != nil {
						t.Fatal(err)
					}
					value["content"] = map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph", "attrs": map[string]string{"id": "private-text"}, "content": []any{map[string]string{"type": "text", "text": "Private checkpoint marker"}}}}}
					encoded, err := json.Marshal(value)
					return encoded, code, err
				}
				response := httptest.NewRecorder()
				switch operation {
				case "state":
					pages.GetDocument(response, request)
				case "checkpoints":
					pages.ListDocumentCheckpoints(response, request)
				case "checkpoint-read":
					pages.ReadDocumentCheckpoint(response, request)
				}
				if ctx.Err() != nil {
					t.Fatalf("read/audit needed a second writer connection: %v", ctx.Err())
				}
				var currentOwner string
				if err := rig.db.QueryRow(`SELECT owner_user_id FROM pages WHERE id=?`, page).Scan(&currentOwner); err != nil || currentOwner != rig.userID {
					t.Fatalf("custody schedule failed: owner=%s err=%v", currentOwner, err)
				}
				if rejectAudit {
					if response.Code != http.StatusInternalServerError || privateSuccess || strings.Contains(response.Body.String(), "Private checkpoint marker") || strings.Contains(response.Body.String(), "snapshot") || strings.Contains(response.Body.String(), pin) {
						t.Fatalf("required audit rejection released private history: status=%d private200=%v response=%s", response.Code, privateSuccess, response.Body)
					}
				} else if response.Code != http.StatusOK || !privateSuccess || !auditBeforeBytes {
					t.Fatalf("foreign OWNER read lacked fenced-custodian audit before private bytes: status=%d private200=%v audited=%v", response.Code, privateSuccess, auditBeforeBytes)
				}
			})
		}
	}
}

type documentAuditGateWriter struct {
	*httptest.ResponseRecorder
	beforeHeader func(int)
	wroteHeader  bool
}

func (w *documentAuditGateWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		w.beforeHeader(code)
	}
	w.ResponseRecorder.WriteHeader(code)
}
func (w *documentAuditGateWriter) Write(value []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseRecorder.Write(value)
}
