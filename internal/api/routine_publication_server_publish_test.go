//go:build linux

package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/journal"
)

// This fixture is synthetic native storage, not a historical admission or a
// passing paid test. No executor, provider client, launch or credential probe is
// installed. The encrypted value is an inert local test string.
type serverPublicationHTTPFixture struct {
	db                                                                      *sql.DB
	writer                                                                  *journal.Writer
	owner, workspace, path, publisherToken, cipher, description, definition string
	draft                                                                   guestRoutineDraft
	call                                                                    func(string, string, string, any, int) *httptest.ResponseRecorder
	checkForeignKeys                                                        func()
}

func (f *serverPublicationHTTPFixture) request() map[string]any {
	return map[string]any{"draft_id": f.draft.DraftID, "revision": f.draft.Revision, "base_revision": f.draft.BaseRevision}
}

func newServerPublicationHTTPFixture(t *testing.T, active ...bool) *serverPublicationHTTPFixture {
	t.Helper()
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	owner, workspace := seedInviter(t, db)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('publish-crew',?,'Publish','publish-crew')`, workspace)
	seedAgentRow(t, db, "publish-agent", workspace, "publish-crew", "Publish", "publish-agent", "AGENT")
	execOrFatal(t, db, `UPDATE agents SET restricted_execution_profile='responses_text',llm_provider='OPENAI',llm_model='fixture-model' WHERE id='publish-agent'`)
	cipher, err := encryption.Encrypt("synthetic-publication-inert-key")
	if err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, db, `INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,created_by) VALUES('publish-key',?,'Publish fixture',?,'API_KEY','OPENAI',?)`, workspace, cipher, owner)
	execOrFatal(t, db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('publish-key-grant','publish-agent','publish-key','OPENAI_API_KEY')`)
	const original = `{"dsl_version":"1.0","name":"publish-fixture","steps":[{"id":"answer","type":"agent_run","agent_slug":"publish-agent","prompt":"Original fixture"}]}`
	const replacement = `{"dsl_version":"1.0","name":"publish-fixture","steps":[{"id":"answer","type":"agent_run","agent_slug":"publish-agent","prompt":"Reviewed replacement"}]}`
	execOrFatal(t, db, `INSERT INTO pipelines(id,workspace_id,slug,name,description,definition_json,definition_hash,author_crew_id,author_agent_id,author_user_id,authored_via,status,head_version,last_test_run_at,last_test_run_passed) VALUES('publish-routine',?,'publish-routine','Original','Original description',?,?,'publish-crew','publish-agent',?,'user_api','disabled',1,NULL,0)`, workspace, original, definitionHashHex([]byte(original)), owner)
	if len(active) > 0 && active[0] {
		execOrFatal(t, db, `UPDATE pipelines SET status='active' WHERE id='publish-routine'`)
	}
	execOrFatal(t, db, `INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,authority_fingerprint) VALUES('publish-original','publish-routine',1,?,?,'user',?,'legacy-trusted:v1')`, original, definitionHashHex([]byte(original)), owner)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('publish-user','publish-user@example.test')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('publish-member',?,'publish-user','VIEWER','restricted')`, workspace)
	writer := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{})
	t.Cleanup(func() { writer.Close() })
	router, err := NewRouter(db, "publish-http-synthetic-router-secret", newTestLogger(), WithJournal(writer))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	ownerToken := mintTokenFor(t, db, owner, "publish-owner-session")
	publisherToken := mintTokenFor(t, db, "publish-user", "publish-user-session")
	call := func(token, method, path string, body any, want int) *httptest.ResponseRecorder {
		t.Helper()
		var encoded []byte
		if body != nil {
			encoded, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(encoded))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Workspace-ID", workspace)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if want != 0 && w.Code != want {
			t.Fatalf("%s %s: HTTP%d want%d", method, path, w.Code, want)
		}
		return w
	}
	policyPath := "/api/v1/workspaces/" + workspace + "/members/publish-member/access"
	var policy access.Policy
	if err = json.Unmarshal(call(ownerToken, "GET", policyPath, nil, 200).Body.Bytes(), &policy); err != nil {
		t.Fatal(err)
	}
	rights := []access.Right{
		{Kind: "routine", ID: "publish-routine", Operation: "view"},
		{Kind: "routine", ID: "publish-routine", Operation: "edit"},
		{Kind: "routine", ID: "publish-routine", Operation: "publish"},
		{Kind: "agent", ID: "publish-agent", Operation: "run"},
	}
	call(ownerToken, "PUT", policyPath, map[string]any{"membership_id": policy.ID, "revision": policy.Revision, "mode": "restricted", "rights": rights}, 200)
	store := access.Store{DB: db}
	for _, right := range rights {
		if err = store.Check(t.Context(), "publish-user", workspace, right); err != nil {
			t.Fatalf("native current rights calibration: %v", err)
		}
	}
	if err = store.Check(t.Context(), "publish-user", workspace, access.Right{Kind: "routine", ID: "publish-routine", Operation: "run"}); err == nil {
		t.Fatal("publisher accidentally holds root Run authority")
	}
	var fk int
	if err = db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatal("native fixture requires foreign keys", err)
	}
	assertNoForeignKeyFailures := func() {
		t.Helper()
		rows, e := db.Query(`PRAGMA foreign_key_check`)
		if e != nil {
			t.Fatal(e)
		}
		defer rows.Close()
		if rows.Next() {
			t.Fatal("publication left a foreign-key violation")
		}
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
	}
	assertNoForeignKeyFailures()
	path := "/api/v1/workspaces/" + workspace + "/restricted-routine-library/publish-routine/draft"
	read := func() guestRoutineDraft {
		t.Helper()
		var d guestRoutineDraft
		if err = json.Unmarshal(call(publisherToken, "GET", path, nil, 200).Body.Bytes(), &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	draft := read()
	draft.Name, draft.Definition = "Published fixture", json.RawMessage(replacement)
	description := "Reviewed description"
	draft.Description = &description
	call(publisherToken, "PUT", path, draft.guestRoutineDraftContent, 200)
	draft = read()
	if draft.DraftID == "" || draft.Revision != 1 {
		t.Fatal("real draft calibration did not persist")
	}
	return &serverPublicationHTTPFixture{db: db, writer: writer, owner: owner, workspace: workspace, path: path, publisherToken: publisherToken, cipher: cipher, description: description, definition: replacement, draft: draft, call: call, checkForeignKeys: assertNoForeignKeyFailures}
}

// Snapshots retain native SQL values in memory, including NULL and BLOB shape.
func (f *serverPublicationHTTPFixture) snapshot(t *testing.T) string {
	t.Helper()
	all := map[string][]string{}
	for _, table := range []string{"pipelines", "pipeline_versions", "pipeline_drafts", "journal_entries"} {
		rows, err := f.db.Query(`SELECT * FROM ` + table)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values, targets := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				targets[i] = &values[i]
			}
			if err = rows.Scan(targets...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			encoded, err := json.Marshal(values)
			if err != nil {
				rows.Close()
				t.Fatal(err)
			}
			all[table] = append(all[table], string(encoded))
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err = rows.Close(); err != nil {
			t.Fatal(err)
		}
		sort.Strings(all[table])
	}
	encoded, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestRoutineServerPublicationConsumesDraftAndRetainsManifest(t *testing.T) {
	f := newServerPublicationHTTPFixture(t)
	published := f.call(f.publisherToken, "POST", f.path+"/publish", f.request(), 200)
	if published.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("publication response may be cached")
	}
	for _, forbidden := range []string{"authority", "credential", "encrypted_value", f.cipher, "fixture-model"} {
		if strings.Contains(published.Body.String(), forbidden) {
			t.Fatal("publication response leaked private metadata")
		}
	}
	var name, description, raw, hash, status, crew, agent, user, via string
	var head, revision, tested int
	var origin, testedAt any
	err := f.db.QueryRow(`SELECT name,description,definition_json,definition_hash,status,author_crew_id,author_agent_id,author_user_id,authored_via,head_version,publication_revision,last_test_run_at,last_test_run_passed,chat_origin_id FROM pipelines WHERE id='publish-routine'`).Scan(&name, &description, &raw, &hash, &status, &crew, &agent, &user, &via, &head, &revision, &testedAt, &tested, &origin)
	if err != nil {
		t.Fatal(err)
	}
	if name != f.draft.Name || description != f.description || raw != f.definition || hash != definitionHashHex([]byte(f.definition)) || head != 2 || revision != f.draft.BaseRevision+1 {
		t.Fatal("content/head/revision did not commit together")
	}
	if status != "disabled" || crew != "publish-crew" || agent != "publish-agent" || user != f.owner || via != "user_api" || origin != nil || testedAt != nil || tested != 0 {
		t.Fatal("publication changed author/origin/status or fabricated paid test success")
	}
	var manifest any
	var fingerprint, archiveRaw, archiveAuthor, archiveType string
	err = f.db.QueryRow(`SELECT publication_authority_json,authority_fingerprint,definition_json,author_id,author_type FROM pipeline_versions WHERE pipeline_id='publish-routine' AND version=?`, head).Scan(&manifest, &fingerprint, &archiveRaw, &archiveAuthor, &archiveType)
	if err != nil {
		t.Fatal(err)
	}
	text, ok := manifest.(string)
	if !ok || text == "" {
		t.Fatal("manifest is not native nonempty TEXT")
	}
	authority, err := access.DecodePublicationAuthority([]byte(text), fingerprint)
	if err != nil || authority.PublisherID != "publish-user" || authority.WorkspaceID != f.workspace || authority.PipelineID != "publish-routine" || authority.RootCrewID != crew || authority.AuthorAgentID != agent || authority.RootOriginID != "" || len(authority.Authoring) != 3 || len(authority.Execution) != 1 || len(authority.Agents) != 1 || archiveRaw != raw || archiveAuthor != "publish-user" || archiveType != "user" {
		t.Fatal("archive lost canonical host bindings", err)
	}
	if _, err = f.db.Exec(`UPDATE pipeline_versions SET publication_authority_json='{}' WHERE pipeline_id='publish-routine' AND version=?`, head); err == nil {
		t.Fatal("immutable manifest could be changed")
	}
	var remaining, audits int
	if err = f.db.QueryRow(`SELECT COUNT(*) FROM pipeline_drafts WHERE id=?`, f.draft.DraftID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("draft not consumed", err)
	}
	if err = f.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE workspace_id=? AND json_extract(payload,'$.action')='publish' AND json_extract(payload,'$.pipeline_id')='publish-routine'`, f.workspace).Scan(&audits); err != nil || audits != 1 {
		t.Fatal("missing atomic publication audit", err)
	}
	chain, err := journal.VerifyChain(t.Context(), f.db, f.workspace)
	if err != nil || !chain.OK {
		t.Fatal("audit chain invalid", err)
	}
	for _, table := range []string{"pipeline_runs", "chats", "access_attempts", "restricted_workflow_jobs", "pipeline_schedules", "pipeline_webhooks"} {
		var count int
		if err = f.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("publication produced %s effects: %d %v", table, count, err)
		}
	}
	f.checkForeignKeys()
}

func TestRoutineServerPublicationDeduplicatesExactIdentity(t *testing.T) {
	f := newServerPublicationHTTPFixture(t)
	f.call(f.publisherToken, "POST", f.path+"/publish", f.request(), 200)
	// GET after consumption synthesizes a fresh draft at the new published base.
	var fresh guestRoutineDraft
	if err := json.Unmarshal(f.call(f.publisherToken, "GET", f.path, nil, 200).Body.Bytes(), &fresh); err != nil {
		t.Fatal(err)
	}
	f.call(f.publisherToken, "PUT", f.path, fresh.guestRoutineDraftContent, 200)
	if err := json.Unmarshal(f.call(f.publisherToken, "GET", f.path, nil, 200).Body.Bytes(), &fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.DraftID == f.draft.DraftID || fresh.BaseRevision != f.draft.BaseRevision+1 {
		t.Fatal("fresh draft did not capture new base")
	}
	f.draft = fresh
	var before string
	if err := f.db.QueryRow(`SELECT publication_authority_json FROM pipeline_versions WHERE pipeline_id='publish-routine' AND version=2`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	f.call(f.publisherToken, "POST", f.path+"/publish", f.request(), 200)
	var versions, head, revision, audits, remaining int
	var after string
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM pipeline_versions WHERE pipeline_id='publish-routine'`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT head_version,publication_revision FROM pipelines WHERE id='publish-routine'`).Scan(&head, &revision); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT publication_authority_json FROM pipeline_versions WHERE pipeline_id='publish-routine' AND version=2`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE json_extract(payload,'$.action')='publish'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM pipeline_drafts WHERE id=?`, fresh.DraftID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if versions != 2 || head != 2 || revision != fresh.BaseRevision+1 || after != before || audits != 2 || remaining != 0 {
		t.Fatal("dedup changed immutable identity, skipped consume/revision, or lost audit")
	}
	f.checkForeignKeys()
}

func TestRoutineServerPublicationRefusalRollsBack(t *testing.T) {
	for _, scenario := range []string{"stale", "missing-view", "missing-edit", "missing-publish", "missing-agent-run", "invalid-origin", "operational-error", "audit-abort", "audit-ignore", "schedule", "webhook"} {
		t.Run(scenario, func(t *testing.T) {
			f := newServerPublicationHTTPFixture(t)
			want := 404
			switch scenario {
			case "stale":
				f.draft.Revision++
				want = 409
			case "missing-view", "missing-edit", "missing-publish":
				op := strings.TrimPrefix(scenario, "missing-")
				execOrFatal(t, f.db, `DELETE FROM access_grants WHERE member_id='publish-member' AND routine_id='publish-routine' AND operation=?`, op)
			case "missing-agent-run":
				execOrFatal(t, f.db, `DELETE FROM access_grants WHERE member_id='publish-member' AND agent_id='publish-agent' AND operation='run'`)
			case "invalid-origin":
				// A native, FK-valid but deliberately invalid provenance row is
				// negative data, not a fabricated historical positive receipt.
				execOrFatal(t, f.db, `INSERT INTO execution_origins(id,workspace_id,root_run_id,chat_id,trigger_message_id,human_authority,created_at) VALUES('publish-invalid-origin',?,'invalid-root','invalid-chat','invalid-message','{}','2026-10-19T00:00:00Z')`, f.workspace)
				execOrFatal(t, f.db, `UPDATE pipelines SET chat_origin_id='publish-invalid-origin' WHERE id='publish-routine'`)
			case "operational-error":
				// Missing schema is an operational error, not a hidden denial.
				execOrFatal(t, f.db, `DROP VIEW live_access_grants`)
				want = 500
			case "audit-abort", "audit-ignore":
				statement := "RAISE(ABORT,'synthetic publication audit failure')"
				if scenario == "audit-ignore" {
					statement = "RAISE(IGNORE)"
				}
				execOrFatal(t, f.db, `CREATE TRIGGER publication_test_audit BEFORE INSERT ON journal_entries WHEN json_extract(NEW.payload,'$.action')='publish' BEGIN SELECT `+statement+`; END`)
				want = 500
			case "schedule":
				execOrFatal(t, f.db, `INSERT INTO pipeline_schedules(id,workspace_id,name,target_pipeline_id,cron_expr) VALUES('publish-schedule',?,'Existing schedule','publish-routine','0 9 * * *')`, f.workspace)
				want = 422
			case "webhook":
				execOrFatal(t, f.db, `INSERT INTO pipeline_webhooks(id,workspace_id,name,target_pipeline_id,token) VALUES('publish-webhook',?,'Existing webhook','publish-routine','synthetic-inert-token')`, f.workspace)
				want = 422
			}
			var notifications atomic.Int32
			f.writer.AddReadObserver(func(entries []journal.Entry) {
				for _, entry := range entries {
					if entry.Payload["action"] == "publish" {
						notifications.Add(1)
					}
				}
			})
			before := f.snapshot(t)
			f.call(f.publisherToken, "POST", f.path+"/publish", f.request(), want)
			if after := f.snapshot(t); after != before {
				t.Fatal("refused publication changed content/archive/draft/audit")
			}
			if notifications.Load() != 0 {
				t.Fatal("refused publication notified observers before commit")
			}
			f.checkForeignKeys()
		})
	}
}

func TestRoutinePublicationAuditCannotUndoPublication(t *testing.T) {
	f := newServerPublicationHTTPFixture(t)
	execOrFatal(t, f.db, `CREATE TRIGGER publication_test_undo AFTER INSERT ON journal_entries WHEN json_extract(NEW.payload,'$.action')='publish' BEGIN
 UPDATE pipelines SET head_version=1,publication_authority_fingerprint='legacy-trusted:v1',
 definition_json=(SELECT definition_json FROM pipeline_versions WHERE pipeline_id=pipelines.id AND version=1),
 definition_hash=(SELECT definition_hash FROM pipeline_versions WHERE pipeline_id=pipelines.id AND version=1)
 WHERE id=json_extract(NEW.payload,'$.pipeline_id'); END`)
	var notifications atomic.Int32
	f.writer.AddReadObserver(func(entries []journal.Entry) {
		for _, entry := range entries {
			if entry.Payload["action"] == "publish" {
				notifications.Add(1)
			}
		}
	})
	before := f.snapshot(t)
	response := f.call(f.publisherToken, "POST", f.path+"/publish", f.request(), 0)
	if response.Code != 500 {
		t.Errorf("audit undo returned HTTP%d, want500", response.Code)
	}
	if f.snapshot(t) != before {
		t.Error("audit undo did not roll back recipe/archive/draft/audit")
	}
	if notifications.Load() != 0 {
		t.Errorf("audit undo notified %d times", notifications.Load())
	}
	f.checkForeignKeys()
}

func TestPublicationRejectsNullDescription(t *testing.T) {
	f := newServerPublicationHTTPFixture(t)
	before := f.snapshot(t)
	f.call(f.publisherToken, "PUT", f.path, map[string]any{"name": "Invalid", "description": nil, "definition": f.draft.Definition, "revision": f.draft.Revision, "base_revision": f.draft.BaseRevision}, 400)
	if f.snapshot(t) != before {
		t.Fatal("null description mutated stored state")
	}
}
