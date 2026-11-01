package api

import (
	"context"
	"errors"
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
