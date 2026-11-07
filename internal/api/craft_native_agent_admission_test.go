package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/mooring"
)

func TestCraftNativeAgentExplicitWholeDocumentRequest(t *testing.T) {
	if _, err := parseCraftNativeAgentTask([]byte(`{"chat_id":"chat","message_id":"message","whole_document":true}`)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"chat_id":"chat","message_id":"message"}`,
		`{"chat_id":"chat","message_id":"message","whole_document":false}`,
		`{"chat_id":"chat","message_id":"message","whole_document":true,"run_id":"claimed"}`,
		`{"chat_id":"chat","message_id":"message","whole_document":true,"agent_id":"claimed"}`,
		`{"chat_id":"chat","message_id":"message","whole_document":true,"whole_document":true}`,
	} {
		if _, err := parseCraftNativeAgentTask([]byte(raw)); err == nil {
			t.Fatalf("unqualified consent accepted: %s", raw)
		}
	}
}

func nativeAgentAdmissionFixture(t *testing.T) (*artifactPrincipalFixture, *craftNativeAgentManager, context.Context) {
	t.Helper()
	f, source := originalSessionFixture(t)
	f.rig.seedPage(t, "native-agent-page", "native-agent-page", "Native", source.UserID, "")
	mustExec(t, f.rig.db, `INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES('native-agent-page',?,'wordcraft',?,?,?)`, f.rig.wsID, strings.Repeat("a", 40), strings.Repeat("b", 64), strings.Repeat("c", 64))
	// The workspace agent artifact policy admits the agent's crew (#4681).
	mustExec(t, f.rig.db, `INSERT INTO workspace_artifact_creation_policy(workspace_id,enabled,version,updated_at) VALUES(?,1,1,'2026-10-17T00:00:00Z')`, f.a.WorkspaceID)
	mustExec(t, f.rig.db, `INSERT INTO workspace_artifact_creation_crews(workspace_id,crew_id) VALUES(?,?)`, f.a.WorkspaceID, f.a.CrewID)
	// The bridge's instruction-only send: a saved mark in the author's private chat.
	mustExec(t, f.rig.db, `UPDATE chats SET visibility='private' WHERE id=?`, f.a.ChatID)
	mustExec(t, f.rig.db, `UPDATE conversation_messages SET native_craft_instruction=1 WHERE id=?`, f.a.MessageID)
	u := &AuthUser{ID: source.UserID, SessionID: source.ID, OriginalCredential: source}
	ctx := auth.WithOriginalCredential(context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxUser, u), ctxAuthKind, AuthKindSession), ctxWorkspaceID, f.rig.wsID), *source)
	h := newCraftNativeHTTP(f.pages, true, &collaborationAdmissionTestClock{boot: "native-agent-admission", now: time.Second})
	d := newCraftNativeDocumentsHTTP(h, map[string]craftNativeApplication{"wordcraft": {Store: mooring.NativeStoreConfig{Application: "wordcraft"}}})
	m := newCraftNativeAgentManager(h, d, CraftNativeHTTPConfig{AgentsEnabled: true})
	return f, m, ctx
}

// The workspace agent artifact policy is the ceiling for native Craft agent
// tasks: an excluded crew or a disabled policy refuses the task before anything
// is accepted (#4681).
func TestCraftNativeAgentTaskRefusedOutsideWorkspaceArtifactPolicy(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"crew excluded", `DELETE FROM workspace_artifact_creation_crews`},
		{"policy disabled", `UPDATE workspace_artifact_creation_policy SET enabled=0`},
		{"no policy", `DELETE FROM workspace_artifact_creation_crews; DELETE FROM workspace_artifact_creation_policy`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, m, ctx := nativeAgentAdmissionFixture(t)
			mustExec(t, f.rig.db, tc.sql)
			accepted, err := m.produceTask(ctx, "@native-agent-page", craftNativeAgentTaskRequest{ChatID: f.a.ChatID, MessageID: f.a.MessageID, WholeDocument: true})
			if !errors.Is(err, errCraftNativeAgentDenied) || accepted.WorkID != "" {
				t.Fatalf("task admitted outside the artifact policy: receipt=%+v err=%v", accepted, err)
			}
			var items int
			if err = f.rig.db.QueryRow(`SELECT count(*) FROM work_items WHERE domain_kind=?`, craftNativeAgentDomain).Scan(&items); err != nil || items != 0 {
				t.Fatal("refused task left a work item", items, err)
			}
		})
	}
}

// Excluding the crew after admission stops the running task at its next
// recheck (#4681).
func TestCraftNativeAgentRecheckRefusesCrewExcludedAfterAdmission(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"crew excluded", `DELETE FROM workspace_artifact_creation_crews`},
		{"policy disabled", `UPDATE workspace_artifact_creation_policy SET enabled=0`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, m, ctx := nativeAgentAdmissionFixture(t)
			accepted, err := m.produceTask(ctx, "@native-agent-page", craftNativeAgentTaskRequest{ChatID: f.a.ChatID, MessageID: f.a.MessageID, WholeDocument: true})
			if err != nil {
				t.Fatal("admission inside the policy", err)
			}
			m.mu.Lock()
			e := m.entries[accepted.WorkID]
			m.mu.Unlock()
			check := func() error {
				tx, err := f.rig.db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				return m.checkOriginalGrant(t.Context(), tx, e.grant)
			}
			if err = check(); err != nil {
				t.Fatal("recheck inside the policy", err)
			}
			mustExec(t, f.rig.db, tc.sql)
			if err = check(); !errors.Is(err, errCraftNativeAgentDenied) {
				t.Fatalf("recheck kept a task outside the artifact policy: %v", err)
			}
		})
	}
}

func TestCraftNativeAgentTaskCapturesNativeConsentWithoutD1Subject(t *testing.T) {
	f, m, ctx := nativeAgentAdmissionFixture(t)
	input := craftNativeAgentTaskRequest{ChatID: f.a.ChatID, MessageID: f.a.MessageID, WholeDocument: true}
	accepted, err := m.produceTask(ctx, "@native-agent-page", input)
	if err != nil || accepted.WorkID == "" {
		t.Fatalf("actual original human native consent not accepted: receipt=%+v err=%v", accepted, err)
	}
	var domain, app, scope string
	if err = f.rig.db.QueryRow(`SELECT domain_kind,domain_id,authorized_scope FROM work_items WHERE id=?`, accepted.WorkID).Scan(&domain, &app, &scope); err != nil || domain != craftNativeAgentDomain || app != "native-agent-page" || scope != "pages:write" {
		t.Fatal("native work binding", domain, app, scope, err)
	}
	var collab int
	if err = f.rig.db.QueryRow(`SELECT count(*) FROM mooring_collab_documents WHERE page_id='native-agent-page'`).Scan(&collab); err != nil || collab != 0 {
		t.Fatal("native task incorrectly depends on D1", collab, err)
	}
	if _, err = m.produceTask(ctx, "@native-agent-page", input); err == nil {
		t.Fatal("same message/document silently launched twice")
	}
}

// The browser control offers a start on a document a person holds: admission
// does not consult the lease, and the run's own acquire then routes to a
// proposal (TestCraftNativeAgentToolHumanBusyThenRealAgentReceiptAndExactRetry).
func TestCraftNativeAgentTaskAdmittedWhileHumanHoldsLease(t *testing.T) {
	f, m, human := nativeAgentAdmissionFixture(t)
	mustExec(t, f.rig.db, `PRAGMA synchronous=FULL`)
	m.human.agents = m
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{}`)).WithContext(human)
	r.SetPathValue("slug", "@native-agent-page")
	w := httptest.NewRecorder()
	m.human.sessions(w, r)
	var session struct {
		Token string `json:"session_token"`
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &session) != nil {
		t.Fatal("human admission", w.Code, w.Body)
	}
	if held := craftNativeLeaseCall(m.human, human, session.Token, "@native-agent-page", "acquire", `{}`); held.Code != 200 {
		t.Fatal("human acquire", held.Code, held.Body)
	}
	accepted, err := m.produceTask(human, "@native-agent-page", craftNativeAgentTaskRequest{f.a.ChatID, f.a.MessageID, true})
	if err != nil || accepted.WorkID == "" {
		t.Fatalf("task refused on a held document: receipt=%+v err=%v", accepted, err)
	}
}

// The browser persists the instruction without a chat turn; a task still binds
// only this human's own user message in this agent's chat.
func TestCraftNativeAgentTaskRefusesMessageItsCallerDidNotAuthor(t *testing.T) {
	for name, sql := range map[string]string{
		"another author":  `UPDATE conversation_messages SET author_user_id='other-member' WHERE id=?`,
		"not a user turn": `UPDATE conversation_messages SET role='assistant' WHERE id=?`,
	} {
		t.Run(name, func(t *testing.T) {
			f, m, ctx := nativeAgentAdmissionFixture(t)
			mustExec(t, f.rig.db, sql, f.a.MessageID)
			accepted, err := m.produceTask(ctx, "@native-agent-page", craftNativeAgentTaskRequest{f.a.ChatID, f.a.MessageID, true})
			if !errors.Is(err, errCraftNativeAgentDenied) || accepted.WorkID != "" {
				t.Fatalf("task bound a message the caller did not author: receipt=%+v err=%v", accepted, err)
			}
		})
	}
}

// #5009: an ordinary turn has already reached the agent log and the journal,
// and a group chat shows the text to people without Page access. No start
// path may bind either, and a refused start leaves nothing queued.
func TestCraftNativeAgentTaskBindsOnlyAPrivateInstruction(t *testing.T) {
	for name, sql := range map[string]string{
		"ordinary turn": `UPDATE conversation_messages SET native_craft_instruction=0 WHERE id=?`,
		"group chat":    `UPDATE chats SET visibility='group' WHERE id=(SELECT session_id FROM conversation_messages WHERE id=?)`,
	} {
		t.Run(name, func(t *testing.T) {
			f, m, ctx := nativeAgentAdmissionFixture(t)
			mustExec(t, f.rig.db, sql, f.a.MessageID)
			accepted, err := m.produceTask(ctx, "@native-agent-page", craftNativeAgentTaskRequest{f.a.ChatID, f.a.MessageID, true})
			if !errors.Is(err, errCraftNativeAgentDenied) || accepted.WorkID != "" {
				t.Fatalf("task bound a message outside a private instruction: receipt=%+v err=%v", accepted, err)
			}
			var items int
			if err = f.rig.db.QueryRow(`SELECT count(*) FROM work_items WHERE domain_kind=?`, craftNativeAgentDomain).Scan(&items); err != nil || items != 0 {
				t.Fatal("refused task left a work item", items, err)
			}
		})
	}
}

func TestCraftNativeAgentCapturedGrantNeverRefreshesChangedEpochOrRegistry(t *testing.T) {
	f, m, ctx := nativeAgentAdmissionFixture(t)
	accepted, err := m.produceTask(ctx, "@native-agent-page", craftNativeAgentTaskRequest{f.a.ChatID, f.a.MessageID, true})
	if err != nil {
		t.Fatal(err)
	}
	e := m.entries[accepted.WorkID]
	check := func(store *craftNativeAgentManager, want bool) {
		t.Helper()
		tx, err := f.rig.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		err = store.checkGrant(ctx, tx, e)
		if (err == nil) != want {
			t.Fatalf("grant allowed=%v want=%v err=%v", err == nil, want, err)
		}
	}
	check(m, true)
	restarted := newCraftNativeAgentManager(m.human, m.documents, m.config)
	// Even copying database/lookup DATA cannot reconstruct the original seal.
	restarted.entries[accepted.WorkID] = e
	check(restarted, false)
	mustExec(t, f.rig.db, `UPDATE pages SET owner_user_id=? WHERE id='native-agent-page'`, f.rig.adminID)
	mustExec(t, f.rig.db, `UPDATE pages SET owner_user_id=? WHERE id='native-agent-page'`, f.rig.userID)
	check(m, false)
}

func TestCraftNativeAgentTaskRejectsSourceProjectionAndPartialConsent(t *testing.T) {
	f, m, ctx := nativeAgentAdmissionFixture(t)
	input := craftNativeAgentTaskRequest{f.a.ChatID, f.a.MessageID, true}
	projected := context.WithValue(ctx, artifactRunKey{}, &artifactRunAuthority{RunID: "not-human"})
	if _, err := m.produceTask(projected, "@native-agent-page", input); err == nil {
		t.Fatal("projected agent became human consent")
	}
	input.WholeDocument = false
	if _, err := m.produceTask(ctx, "@native-agent-page", input); err == nil {
		t.Fatal("partial/implicit consent accepted")
	}
	var count int
	if err := f.rig.db.QueryRow(`SELECT count(*) FROM work_items WHERE domain_kind=?`, craftNativeAgentDomain).Scan(&count); err != nil || count != 0 {
		t.Fatal("denial published work", count, err)
	}
}

func TestCraftNativeAgentTaskRejectsReadOnlyOriginalCredentialBeforeAccept(t *testing.T) {
	f, m, human := nativeAgentAdmissionFixture(t)
	bearer := mintIngressScopedToken(t, f.rig.db, f.a.HumanUserID, "native-task-read-only", "pages:read")
	user := &AuthUser{ID: f.a.HumanUserID, TokenID: "clt-ingress-scoped-native-task-read-only"}
	source, err := captureOriginalHumanCredential(t.Context(), f.rig.db, user, AuthKindCLIToken, bearer)
	if err != nil {
		t.Fatal(err)
	}
	user.OriginalCredential = source
	readOnly := auth.WithOriginalCredential(context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxUser, user), ctxAuthKind, AuthKindCLIToken), ctxWorkspaceID, f.rig.wsID), *source)
	if _, err := validateOriginalHumanCredential(readOnly, f.rig.db, *source, "pages:read"); err != nil {
		t.Fatal("reader positive calibration", err)
	}
	input := craftNativeAgentTaskRequest{f.a.ChatID, f.a.MessageID, true}
	if _, err := m.produceTask(readOnly, "@native-agent-page", input); err == nil {
		t.Fatal("read-only original credential accepted edit task")
	}
	var count int
	if err := f.rig.db.QueryRow(`SELECT count(*) FROM work_items WHERE domain_kind=?`, craftNativeAgentDomain).Scan(&count); err != nil || count != 0 {
		t.Fatal("denied credential queued task", count, err)
	}
	if _, err := m.produceTask(human, "@native-agent-page", input); err != nil {
		t.Fatal("original writable human calibration", err)
	}
}
