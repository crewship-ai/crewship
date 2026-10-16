package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

func TestChatDecisionWaitpointIdentityNeverGrantsCallbackAuthority(t *testing.T) {
	d := newWaitpointCardRig(t, nil)
	card := d.read(t)
	if d.id == d.token || card.Entity.ID != d.id || card.Decision.Source.ID != d.id {
		t.Fatalf("capability used as card identity %+v", card)
	}
	encoded, _ := json.Marshal(card)
	if strings.Contains(string(encoded), d.token) {
		t.Fatal("serialized card leaks callback capability")
	}
	upper, err := d.r.waitpointDecisionAdapter().Upper(t.Context(), d.a)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := d.r.waitpointDecisionAdapter().Scan(t.Context(), d.a, 0, upper, 100)
	if err != nil || len(rows) != 1 || rows[0].ID != d.id {
		t.Fatalf("source list %v %v", rows, err)
	}
	if d.read(t).Decision.Source.ID != d.id {
		t.Fatal("source identity changed on reload")
	}
	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("token", d.id)
	rr := httptest.NewRecorder()
	d.r.PipelinesHandler.CompleteWaitpointToken(rr, req)
	if rr.Code != 409 || d.read(t).Decision.Status != "pending" {
		t.Fatalf("card ID grants callback authority %d %s", rr.Code, rr.Body.String())
	}
	if _, err := d.r.waitpointDecision(t.Context(), d.a, d.token); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("bearer accepted as E2 ID %v", err)
	}
	// Existing callback holders keep the exact legacy behavior.
	req = httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("token", d.token)
	rr = httptest.NewRecorder()
	d.r.PipelinesHandler.CompleteWaitpointToken(rr, req)
	if rr.Code != 200 {
		t.Fatalf("legacy callback broken %d %s", rr.Code, rr.Body.String())
	}
}

func TestChatDecisionWaitpointKnownCapabilityCannotEchoInCardOrReceipt(t *testing.T) {
	for _, field := range []string{"prompt", "label", "option", "action", "answer", "unknown-key", "key"} {
		t.Run(field, func(t *testing.T) {
			form := waitpointCardForm()
			form.Fields = append(form.Fields, pipeline.InputSpec{Name: "note", Type: "string"})
			d := newWaitpointCardRig(t, form)
			version := d.read(t).Decision.Source.Version
			if field == "prompt" {
				execOrFatal(t, d.f.h.db, `UPDATE pipeline_waitpoints SET prompt=? WHERE token=?`, "https://example.invalid/waitpoint-tokens/"+d.token, d.token)
			} else if field == "label" || field == "option" || field == "action" {
				switch field {
				case "label":
					form.Fields[0].Label = d.token
				case "option":
					form.Fields[0].Options = []string{d.token, "later"}
				case "action":
					form.Actions[0].Label = d.token
				}
				raw, _ := json.Marshal(form)
				execOrFatal(t, d.f.h.db, `UPDATE pipeline_waitpoints SET decision_form_json=? WHERE token=?`, string(raw), d.token)
			} else {
				answer, op := d.answer(version, "safe-key", "ship")
				if field == "key" {
					op.Key = d.token
				} else if field == "unknown-key" {
					answer.Values[d.token] = decisionString("value")
				} else {
					answer.Values["note"] = decisionString(d.token)
				}
				if err := d.resolve(t, answer, op); err == nil {
					t.Fatal("callback capability persisted in new chat intent")
				}
				if d.read(t).Decision.Status != "pending" {
					t.Fatal("credential answer changed source")
				}
				return
			}
			if _, err := d.r.waitpointDecision(t.Context(), d.a, d.id); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("known capability exposed through %s: %v", field, err)
			}
		})
	}
}

func TestChatDecisionWaitpointBundleRestoreAndForkIdentity(t *testing.T) {
	d := newWaitpointCardRig(t, waitpointCardForm())
	answer, op := d.answer(d.read(t).Decision.Source.Version, "restore-once", "ship")
	if err := d.resolve(t, answer, op); err != nil {
		t.Fatal(err)
	}
	tx, err := d.f.h.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := runauthority.CaptureOrigin(t.Context(), tx, d.f.run, d.f.agent, d.f.workspace)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, d.f.h.db, `UPDATE pipeline_runs SET chat_origin_id=? WHERE id='card-pipeline-run'`, origin.ID)
	actor := backup.Actor{UserID: d.f.user, Role: "ADMIN"}
	created, err := backup.CreateBackup(t.Context(), d.f.h.db, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: d.f.workspace, OutputDir: t.TempDir(), Actor: actor, Passphrase: "synthetic-waitpoint-backup"})
	if err != nil {
		t.Fatal(err)
	}
	target := setupTestDB(t)
	for attempt := range 2 {
		_, err := backup.RestoreBackup(t.Context(), target, backup.RestoreOptions{Path: created.Path, Passphrase: "synthetic-waitpoint-backup", Actor: actor})
		if attempt == 0 && err != nil || attempt == 1 && !errors.Is(err, backup.ErrNoOpRestore) {
			t.Fatalf("restore attempt%d: %v", attempt, err)
		}
	}
	h := NewPipelineHandler(target, newTestLogger(), nil, nil)
	store := pipeline.NewSQLWaitpointStore(target)
	t.Cleanup(store.Close)
	h.SetWaitpointStore(store)
	r := &Router{db: target, PipelinesHandler: h, logger: newTestLogger()}
	current, err := chataudience.CaptureTrusted(t.Context(), target, d.f.chat, d.f.user, nil)
	if err != nil || current == nil {
		t.Fatalf("restored current audience %v %v", current, err)
	}
	op.Audience = *current
	req := withWorkspaceUser(httptest.NewRequest("POST", "/", nil), d.f.user, d.f.workspace, "OWNER")
	if err := r.resolveWaitpointDecision(req, op, answer); err != nil {
		t.Fatalf("restored retry %v", err)
	}
	if card, err := r.waitpointDecision(t.Context(), *current, d.id); err != nil || card.Ref.Decision.Status != "approved" {
		t.Fatalf("restored card %v %v", card, err)
	}
	var count int
	if err := target.QueryRow(`SELECT COUNT(*) FROM pipeline_waitpoints WHERE workspace_id=?`, d.f.workspace).Scan(&count); err != nil || count != 1 {
		t.Fatalf("restore duplicates=%d %v", count, err)
	}
	fork, err := backup.RestoreBackup(t.Context(), d.f.h.db, backup.RestoreOptions{Path: created.Path, Passphrase: "synthetic-waitpoint-backup", Actor: actor, AsWorkspace: "waitpoint-fork"})
	if err != nil {
		t.Fatal(err)
	}
	var alias, token, chat, status string
	if err := d.f.h.db.QueryRow(`SELECT w.chat_source_id,w.token,w.status FROM pipeline_waitpoints w JOIN pipeline_runs p ON p.id=w.pipeline_run_id AND p.workspace_id=w.workspace_id WHERE w.workspace_id=?`, fork.RestoredWorkspaceID).Scan(&alias, &token, &status); err != nil {
		t.Fatal(err)
	}
	if alias == d.id || token == d.token || alias == token || status != "approved" {
		t.Fatalf("fork identity alias=%q status=%q", alias, status)
	}
	if err := d.f.h.db.QueryRow(`SELECT id FROM chats WHERE workspace_id=?`, fork.RestoredWorkspaceID).Scan(&chat); err != nil {
		t.Fatal(err)
	}
	live, err := chataudience.CaptureTrusted(t.Context(), d.f.h.db, chat, d.f.user, nil)
	if err != nil || live == nil {
		t.Fatalf("fork audience %v %v", live, err)
	}
	var historical, authority string
	if err := d.f.h.db.QueryRow(`SELECT p.invocation_source_run_id,e.human_authority FROM pipeline_runs p JOIN execution_origins e ON e.id=p.chat_origin_id WHERE p.workspace_id=?`, fork.RestoredWorkspaceID).Scan(&historical, &authority); err != nil || historical != d.f.run || authority != "{}" {
		t.Fatalf("fork manufactured authority: run=%q authority=%q err=%v", historical, authority, err)
	}
	// Historical invocation receipts were not upgraded into fork authority.
	if _, err := d.r.waitpointDecision(t.Context(), *live, alias); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("fork revived original invocation: %v", err)
	}
	if _, err := d.r.waitpointDecision(t.Context(), *live, d.id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("fork accepted source alias: %v", err)
	}
}
