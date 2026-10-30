package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/mooring"
)

func seedMooringErasureShapes(t *testing.T, r *pagesIdentityRig, workspace, tag string) {
	t.Helper()
	generic, collab := "pid-mooring-"+tag, "pid-collab-"+tag
	for _, page := range []string{generic, collab} {
		pidExec(t, r.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,private_audience,spec_json) VALUES(?,?,?,?,?,1,'{}')`, page, workspace, page, "Identity history", r.other)
	}
	pidExec(t, r.db, `INSERT INTO mooring_documents(page_id,workspace_id,app_id,rev,generation,snapshot) VALUES(?,?,'hello',1,1,'{}')`, generic, workspace)
	pidExec(t, r.db, `INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64) VALUES(?,?,'hello','2','')`, collab, workspace)
	for i, user := range []string{r.userID, r.other} {
		key := fmt.Sprint(i + 1)
		pidExec(t, r.db, `INSERT INTO mooring_intents(page_id,mandate_id,run_id,human_user_id,page_version,policy_version,generation,base_rev,command,fingerprint) VALUES(?,?,?, ?,1,1,1,1,'set','fingerprint')`, generic, key, "run-"+tag, user)
		pidExec(t, r.db, `INSERT INTO mooring_collab_artifact_edits(page_id,workspace_id,application_id,change_id,membership_hash,first_revision,last_revision,host_session_id,actor_kind,human_user_id) VALUES(?,?,'hello',?,'membership',?,?,'session','human',?)`, collab, workspace, key, key, key, user)

		// Cover both revoked mandates and agent-authored edits on behalf of a human.
		extraKey := fmt.Sprint(i + 3)
		pidExec(t, r.db, `INSERT INTO mooring_intents(page_id,mandate_id,run_id,human_user_id,page_version,policy_version,generation,base_rev,command,fingerprint,revoked_at) VALUES(?,?,?, ?,1,1,1,1,'set','fingerprint','2026-01-01T00:00:00Z')`, generic, extraKey, "revoked-run-"+tag, user)
		pidExec(t, r.db, `INSERT INTO mooring_collab_artifact_edits(page_id,workspace_id,application_id,change_id,membership_hash,first_revision,last_revision,host_session_id,actor_kind,human_user_id,agent_id,crew_id,chat_id,message_id,work_id,run_id,attempt_number,attempt_generation) VALUES(?,?,'hello',?,'membership',?,?,'session','agent',?,'agent','crew','chat','message','work','run',1,2)`, collab, workspace, extraKey, extraKey, extraKey, user)
	}
}

func mooringIdentityRow(t *testing.T, db *sql.DB, table, page, key string) map[string]any {
	t.Helper()
	keyColumn := "mandate_id"
	if table == "mooring_collab_artifact_edits" {
		keyColumn = "change_id"
	}
	rows, err := db.Query(fmt.Sprintf(`SELECT * FROM %q WHERE page_id=? AND %q=?`, table, keyColumn), page, key)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		t.Fatalf("missing %s/%s/%s", table, page, key)
	}
	values := make([]any, len(columns))
	dest := make([]any, len(columns))
	for i := range dest {
		dest[i] = &values[i]
	}
	if err := rows.Scan(dest...); err != nil {
		t.Fatal(err)
	}
	result := map[string]any{}
	for i, column := range columns {
		result[column] = values[i]
	}
	return result
}

func TestGDPRErasure_MooringRowsKeepContentAndLoseOnlySubjectIdentity(t *testing.T) {
	r := pagesIdentitySetup(t)
	type row struct {
		table, page, key string
		before           map[string]any
		erase            bool
	}
	var rows []row
	for _, tag := range []string{"a", "b"} {
		for _, table := range []string{"mooring_intents", "mooring_collab_artifact_edits"} {
			page := "pid-mooring-" + tag
			if table == "mooring_collab_artifact_edits" {
				page = "pid-collab-" + tag
			}
			for _, key := range []string{"1", "2", "3", "4"} {
				rows = append(rows, row{table, page, key, mooringIdentityRow(t, r.db, table, page, key), tag == "a" && (key == "1" || key == "3")})
			}
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		result := r.erase(t, pidWSA)
		scope := result["scope"].(map[string]any)
		for _, key := range []string{"mooring_intents_anonymised", "mooring_collab_artifact_edits_anonymised"} {
			want := float64(2 * (1 - attempt))
			if scope[key] != want {
				t.Errorf("attempt %d scope[%s]=%v want %v", attempt, key, scope[key], want)
			}
		}
		for _, row := range rows {
			want := map[string]any{}
			for column, value := range row.before {
				want[column] = value
			}
			if row.erase {
				want["human_user_id"] = "erased"
			}
			got := mooringIdentityRow(t, r.db, row.table, row.page, row.key)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("attempt %d %s/%s/%s changed beyond subject attribution\ngot=%v\nwant=%v", attempt, row.table, row.page, row.key, got, want)
			}
		}
	}
}

func TestMooringIdentityErasureTriggerPreservesEveryOtherColumn(t *testing.T) {
	r := pagesIdentitySetup(t)
	for _, table := range []string{"mooring_intents", "mooring_collab_artifact_edits"} {
		t.Run(table, func(t *testing.T) {
			page, keyColumn := "pid-mooring-a", "mandate_id"
			if table == "mooring_collab_artifact_edits" {
				page, keyColumn = "pid-collab-a", "change_id"
			}
			before := mooringIdentityRow(t, r.db, table, page, "1")
			for column, value := range before {
				if column == "human_user_id" {
					continue
				}
				t.Run(column, func(t *testing.T) {
					changed := any("tampered")
					if n, ok := value.(int64); ok {
						changed = n + 1
					}
					_, err := r.db.Exec(fmt.Sprintf(`UPDATE %q SET human_user_id='erased',%q=? WHERE page_id=? AND %q='1'`, table, column, keyColumn), changed, page)
					if err == nil || !strings.Contains(err.Error(), "immutable") {
						t.Fatalf("nonidentity mutation did not hit immutable trigger: %v", err)
					}
				})
			}
			if _, err := r.db.Exec(fmt.Sprintf(`UPDATE %q SET human_user_id=? WHERE page_id=? AND %q='1'`, table, keyColumn), r.other, page); err == nil {
				t.Fatal("attribution transferred to another person")
			}
			if _, err := r.db.Exec(fmt.Sprintf(`UPDATE %q SET human_user_id='erased' WHERE page_id=? AND %q='1'`, table, keyColumn), page); err != nil {
				t.Fatalf("identity-only erasure: %v", err)
			}
			if _, err := r.db.Exec(fmt.Sprintf(`UPDATE %q SET human_user_id=? WHERE page_id=? AND %q='1'`, table, keyColumn), r.userID, page); err == nil {
				t.Fatal("erased identity restored")
			}
		})
	}
	// Revocation remains one-way after anonymisation, independently of identity.
	pidExec(t, r.db, `UPDATE mooring_intents SET revoked_at='2026-10-30T00:00:00Z' WHERE page_id='pid-mooring-a' AND mandate_id='1'`)
	if _, err := r.db.Exec(`UPDATE mooring_intents SET revoked_at=NULL WHERE page_id='pid-mooring-a' AND mandate_id='1'`); err == nil {
		t.Fatal("anonymisation allowed mandate renewal")
	}
}

func TestMooringErasedMandateIdentityCannotAuthorizeOrReplay(t *testing.T) {
	rig := pagesTransferSetupWithDB(t, gridcraftFullDB(t))
	f, _ := documentCreationAdmissionWithPrincipal(t, newArtifactPrincipalFixtureWithRig(t, rig))
	f.rig.db.SetMaxOpenConns(1)
	wmSeedPage(t, f.rig.db, f.a.WorkspaceID, "mooring-agent-page", "mooring-agent", "Generic", f.a.HumanUserID, "")
	mustExec(t, f.rig.db, `UPDATE pages SET private_audience=1 WHERE id='mooring-agent-page'`)
	mustExec(t, f.rig.db, `INSERT INTO mooring_documents(workspace_id,page_id,app_id,rev,generation,snapshot) VALUES(?,'mooring-agent-page','hello',1,1,'{"text":"initial"}')`, f.a.WorkspaceID)
	f.pages.artifactMasterToken = "synthetic-mooring-master"
	h := &mooringHTTP{workbook: &gridcraftHTTP{pages: f.pages}, storage: mooring.JSONAdapter{}}
	humanToken := mintTokenFor(t, f.rig.db, f.a.HumanUserID, "mooring-owner-cli")
	humanRequest := func(body string) *http.Request {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+humanToken)
		ctx := context.WithValue(r.Context(), ctxUser, &AuthUser{ID: f.a.HumanUserID})
		ctx = context.WithValue(ctx, ctxWorkspaceID, f.a.WorkspaceID)
		ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
		r = r.WithContext(ctx)
		r.SetPathValue("slug", "@mooring-agent-page")
		return r
	}
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	packet := `{"action":"command","artifact_id":"mooring-agent-page","mandate_id":"set-text","request":{"mutation_id":"agent-one","base_rev":1,"generation":1,"command":"set","arguments":{"text":"agent confirmed"}}}`
	call := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.agent(w, documentDraftRequest(f, token, "POST", body))
		return w
	}
	wantCode(t, call(packet), 404, "no implicit generic agent mandate")
	grant := httptest.NewRecorder()
	h.mandate(grant, humanRequest(fmt.Sprintf(`{"mandate_id":"set-text","run_id":%q,"base_rev":1,"generation":1,"command":"set"}`, f.a.RunID)))
	wantCode(t, grant, 200, "retained manifest command mandate")
	for i := 0; i < 2; i++ {
		w := call(packet)
		wantCode(t, w, 200, "agent commit retry")
		var receipt mooring.Receipt
		if json.Unmarshal(w.Body.Bytes(), &receipt) != nil || receipt.Rev != 2 {
			t.Fatal(w.Body.String())
		}
	}
	read := call(`{"action":"read","artifact_id":"mooring-agent-page","mandate_id":"set-text"}`)
	wantCode(t, read, 200, "agent current read")
	if !strings.Contains(read.Body.String(), "agent confirmed") {
		t.Fatal(read.Body.String())
	}
	mustExec(t, f.rig.db, `UPDATE mooring_intents SET human_user_id='erased' WHERE page_id='mooring-agent-page' AND mandate_id='set-text'`)
	wantCode(t, call(packet), 404, "erased mandate cannot replay an earlier receipt")
	wantCode(t, call(`{"action":"read","artifact_id":"mooring-agent-page","mandate_id":"set-text"}`), 404, "erased mandate cannot authorize agent reads")
}

func TestGDPRErasure_MooringSecondUpdateFailureRollsBackIdentityStep(t *testing.T) {
	r := pagesIdentitySetup(t)
	// This refusal is reached only after the preceding collaboration update has
	// written its sentinel inside the same transaction.
	pidExec(t, r.db, `CREATE TRIGGER fail_mooring_erasure BEFORE UPDATE OF human_user_id ON mooring_intents
 WHEN NEW.human_user_id='erased' AND EXISTS(SELECT 1 FROM mooring_collab_artifact_edits WHERE workspace_id='pid-ws-a' AND human_user_id='erased')
 BEGIN SELECT RAISE(ABORT,'injected Mooring erasure rollback'); END`)
	out := r.eraseAs(t, pidWSA, r.adminID, http.StatusMultiStatus)
	if msg, _ := out["error"].(string); !strings.Contains(msg, "injected Mooring erasure rollback") {
		t.Fatalf("did not reach second-update failure after collaboration write: %v", out)
	}
	scope := out["scope"].(map[string]any)
	for _, key := range []string{"mooring_collab_artifact_edits_anonymised", "mooring_intents_anonymised"} {
		if value, ok := scope[key].(float64); !ok || value != 0 {
			t.Errorf("rolled-back receipt %s=%v", key, scope[key])
		}
	}
	for _, table := range []string{"mooring_intents", "mooring_collab_artifact_edits"} {
		page := "pid-mooring-a"
		if table == "mooring_collab_artifact_edits" {
			page = "pid-collab-a"
		}
		for _, key := range []string{"1", "3"} {
			if got := mooringIdentityRow(t, r.db, table, page, key)["human_user_id"]; got != r.userID {
				t.Errorf("%s/%s identity changed despite rollback: %v", table, key, got)
			}
		}
	}
}
