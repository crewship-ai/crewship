package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/craftapps"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

// Native Sheet tools for an ordinary chat run (#5317 D1). The run's human owns a
// private Sheet and attaches it to the message that starts the run.
type chatRunSheetRig struct {
	f      *artifactPrincipalFixture
	m      *craftNativeAgentManager
	a      *artifactRunAuthority
	source *auth.OriginalCredential
}

func newChatRunSheetRig(t *testing.T, flagOn bool, attach ...string) *chatRunSheetRig {
	t.Helper()
	f, source := originalSessionFixture(t)
	db, ws := f.rig.db, f.a.WorkspaceID
	mustExec(t, db, `PRAGMA synchronous=FULL`)
	mustExec(t, db, `UPDATE chats SET visibility='private' WHERE id=?`, f.a.ChatID)
	mustExec(t, db, `DELETE FROM chat_participants WHERE chat_id=?`, f.a.ChatID)
	mustExec(t, db, `INSERT INTO workspace_artifact_creation_policy(workspace_id,enabled,version,updated_at) VALUES(?,1,1,'2026-10-17T00:00:00Z')`, ws)
	mustExec(t, db, `INSERT INTO workspace_artifact_creation_crews(workspace_id,crew_id) VALUES(?,?)`, ws, f.a.CrewID)
	seed := func(id, owner, application string) {
		mustExec(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES(?,?,?,?,?,'{}',1)`, id, ws, id, id, owner)
		mustExec(t, db, `INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES(?,?,?,?,?,?)`, id, ws, application, strings.Repeat("a", 40), strings.Repeat("b", 64), strings.Repeat("c", 64))
	}
	seed("sheet-own", f.a.HumanUserID, "gridcraft")
	// Its first saved revision exists before anything is attached, as for an
	// uploaded Sheet: the attachment's consent names it.
	versions := artifacts.NewVersionStore(db, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
	if _, err := versions.Apply(context.Background(), artifacts.VersionKey{WorkspaceID: ws, PageID: "sheet-own"}, artifacts.VersionRequest{EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {MediaType: "application/json", Content: []byte(`{"sheets":[]}`)}}}, func(context.Context, *sql.Tx) (string, error) { return "user:" + f.a.HumanUserID, nil }); err != nil {
		t.Fatal(err)
	}
	seed("sheet-second", f.a.HumanUserID, "gridcraft")
	seed("sheet-admins", f.rig.adminID, "gridcraft")
	seed("word-own", f.a.HumanUserID, "wordcraft")
	if flagOn {
		mustExec(t, db, `INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled) VALUES('chatrun-flag-on','ff_artifact_agent_access_modes',?,1)`, ws)
	}
	receipt, err := NewInternalHandler(db, "synthetic-chatrun-master", newTestLogger()).originalHumanChatReceipt(t.Context(), f.a.ChatID, f.a.HumanUserID, nil, source)
	if err != nil || receipt == nil {
		t.Fatalf("receipt: %v", err)
	}
	f.receipt = receipt
	if len(attach) > 0 {
		if err = f.pages.RecordArtifactRefs(t.Context(), f.receipt, f.a.MessageID, attach); err != nil {
			t.Fatalf("record refs: %v", err)
		}
	}
	if code := artifactRefsStartRun(t, f, f.a.RunID, f.a.MessageID); code != 201 {
		t.Fatalf("run start: %d", code)
	}
	f.pages.artifactMasterToken = "synthetic-chatrun-master"
	h := newCraftNativeHTTP(f.pages, true, &collaborationAdmissionTestClock{boot: "chatrun", now: time.Second})
	d := newCraftNativeDocumentsHTTP(h, map[string]craftNativeApplication{"gridcraft": {Store: mooring.NativeStoreConfig{Application: "gridcraft"}}, "wordcraft": {Store: mooring.NativeStoreConfig{Application: "wordcraft"}}})
	m := newCraftNativeAgentManager(h, d, CraftNativeHTTPConfig{AgentsEnabled: true})
	h.agents = m
	a := f.a
	return &chatRunSheetRig{f: f, m: m, a: &a, source: source}
}

// ctx is what artifactAgentRequestMode puts on a chat run's request: the run's
// human as the acting user, in the workspace, with their membership role.
func (r *chatRunSheetRig) ctx() context.Context {
	ctx := context.WithValue(context.Background(), artifactRunKey{}, r.a)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: r.a.HumanUserID})
	ctx = context.WithValue(ctx, ctxWorkspaceID, r.a.WorkspaceID)
	return context.WithValue(ctx, ctxRole, "MEMBER")
}

func (r *chatRunSheetRig) attach(t *testing.T) (*craftNativeAgentEntry, error) {
	t.Helper()
	return r.m.attachChatRunEntry(r.ctx(), r.a, sha256.Sum256([]byte("bearer")))
}

func TestChatRunSheetAttachedAndOwnedIsBound(t *testing.T) {
	r := newChatRunSheetRig(t, true, "sheet-own")
	e, err := r.attach(t)
	if err != nil {
		t.Fatalf("an owner's attached Sheet was refused: %v", err)
	}
	if e.grant.Scope.ArtifactID != "sheet-own" || e.chat == nil || r.m.runs[r.a.RunID] != e || e.attempt.runID != r.a.RunID {
		t.Fatalf("entry not bound to the attached Sheet: %+v", e.grant.Scope)
	}
	if err = func() error {
		tx, err := r.f.rig.db.BeginTx(r.ctx(), nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		return r.m.checkRun(r.ctx(), tx, e)
	}(); err != nil {
		t.Fatalf("a bound run failed its own check: %v", err)
	}
}

func (r *chatRunSheetRig) check(e *craftNativeAgentEntry) error {
	tx, err := r.f.rig.db.BeginTx(r.ctx(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return r.m.checkRun(r.ctx(), tx, e)
}

func TestChatRunSheetRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		attach []string
		before func(*chatRunSheetRig)
	}{
		"flag turned off before the first call": {[]string{"sheet-own"}, func(r *chatRunSheetRig) {
			mustExec(t, r.f.rig.db, `DELETE FROM feature_flag_overrides WHERE id='chatrun-flag-on'`)
		}},
		"nothing attached":     {nil, nil},
		"a Word document":      {[]string{"word-own"}, nil},
		"two Sheets":           {[]string{"sheet-own", "sheet-second"}, nil},
		"crew left the policy": {[]string{"sheet-own"}, func(r *chatRunSheetRig) { mustExec(t, r.f.rig.db, `DELETE FROM workspace_artifact_creation_crews`) }},
		"policy disabled": {[]string{"sheet-own"}, func(r *chatRunSheetRig) {
			mustExec(t, r.f.rig.db, `UPDATE workspace_artifact_creation_policy SET enabled=0`)
		}},
		"chat no longer private": {[]string{"sheet-own"}, func(r *chatRunSheetRig) {
			mustExec(t, r.f.rig.db, `UPDATE chats SET visibility='group' WHERE id=?`, r.a.ChatID)
		}},
		"Sheet no longer private": {[]string{"sheet-own"}, func(r *chatRunSheetRig) {
			mustExec(t, r.f.rig.db, `UPDATE pages SET private_audience=0 WHERE id='sheet-own'`)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			r := newChatRunSheetRig(t, true, tc.attach...)
			if tc.before != nil {
				tc.before(r)
			}
			if _, err := r.attach(t); err == nil {
				t.Fatal("accepted")
			}
			if len(r.m.runs) != 0 || len(r.m.entries) != 0 {
				t.Fatal("a refused run left a registration")
			}
		})
	}
}

// What a run cannot attach in the first place is refused when the message is
// sent: someone else's Sheet never reaches the run scope.
func TestChatRunSheetSomeoneElsesSheetCannotBeAttached(t *testing.T) {
	f, source := originalSessionFixture(t)
	_ = source
	mustExec(t, f.rig.db, `INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled) VALUES('x','ff_artifact_agent_access_modes',?,1)`, f.a.WorkspaceID)
	mustExec(t, f.rig.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES('sheet-admins',?,'sheet-admins','x',?,'{}',1)`, f.a.WorkspaceID, f.rig.adminID)
	if err := f.pages.RecordArtifactRefs(t.Context(), f.receipt, f.a.MessageID, []string{"sheet-admins"}); err == nil {
		t.Fatal("a Sheet the sender neither owns nor reads was recorded as a reference")
	}
}

// Every later call re-derives its authority: losing what made the first call
// legal ends the access, without waiting for the run to end.
func TestChatRunSheetEveryCallRechecksItsAuthority(t *testing.T) {
	for name, change := range map[string]func(*chatRunSheetRig){
		"flag turned off": func(r *chatRunSheetRig) {
			mustExec(t, r.f.rig.db, `DELETE FROM feature_flag_overrides WHERE id='chatrun-flag-on'`)
		},
		"owner changed": func(r *chatRunSheetRig) {
			mustExec(t, r.f.rig.db, `UPDATE pages SET owner_user_id=? WHERE id='sheet-own'`, r.f.rig.adminID)
		},
		"crew left the policy": func(r *chatRunSheetRig) { mustExec(t, r.f.rig.db, `DELETE FROM workspace_artifact_creation_crews`) },
		"policy disabled": func(r *chatRunSheetRig) {
			mustExec(t, r.f.rig.db, `UPDATE workspace_artifact_creation_policy SET enabled=0`)
		},
		"human removed from the workspace": func(r *chatRunSheetRig) {
			mustExec(t, r.f.rig.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, r.a.WorkspaceID, r.a.HumanUserID)
		},
		"human suspended": func(r *chatRunSheetRig) {
			mustExec(t, r.f.rig.db, `UPDATE users SET suspended_at='2026-11-25T10:00:00Z' WHERE id=?`, r.a.HumanUserID)
		},
		"reference removed": func(r *chatRunSheetRig) {
			mustExec(t, r.f.rig.db, `DELETE FROM artifact_run_scope WHERE run_id=?`, r.a.RunID)
		},
		"chat no longer private": func(r *chatRunSheetRig) {
			mustExec(t, r.f.rig.db, `UPDATE chats SET visibility='group' WHERE id=?`, r.a.ChatID)
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newChatRunSheetRig(t, true, "sheet-own")
			e, err := r.attach(t)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.check(e); err != nil {
				t.Fatalf("before the change: %v", err)
			}
			change(r)
			if err = r.check(e); err == nil {
				t.Fatal("the access survived the change")
			}
		})
	}
}

// Attaching writes the audit row the other kinds write, once per run, page
// and operation.
func TestChatRunSheetAttachIsAudited(t *testing.T) {
	r := newChatRunSheetRig(t, true, "sheet-own")
	if _, err := r.attach(t); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := r.f.rig.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE workspace_id=? AND action='artifact.agent_run_accessed' AND entity_id='sheet-own' AND user_id=?`, r.a.WorkspaceID, r.a.HumanUserID).Scan(&n); err != nil || n < 1 {
		t.Fatalf("audit rows %d %v", n, err)
	}
}

// A run that did not start from this message's references has no Sheet.
func TestChatRunSheetAnotherRunOfTheSameHumanHasNoReach(t *testing.T) {
	r := newChatRunSheetRig(t, true, "sheet-own")
	other := *r.a
	other.RunID = "some-other-run"
	if _, err := r.m.attachChatRunEntry(r.ctx(), &other, sha256.Sum256([]byte("bearer"))); err == nil {
		t.Fatal("a run without its own scope was bound")
	}
}

// toolRig adds what the HTTP tool needs: a configured Sheet application, a
// first saved version, counting engine stubs and the run's real bearer token.
type chatRunToolRig struct {
	*chatRunSheetRig
	calls *int
	token string
}

func newChatRunToolRig(t *testing.T) *chatRunToolRig {
	t.Helper()
	r := newChatRunSheetRig(t, true, "sheet-own")
	cfg := mooring.NativeStoreConfig{Application: "gridcraft", EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Validate: func(context.Context, []byte) error { return nil }}
	r.m.documents.applications["gridcraft"] = craftNativeApplication{Store: cfg}
	r.m.documents.profiles = map[string]CraftNativeDocumentProfile{"gridcraft": {Application: "gridcraft"}}
	calls := new(int)
	r.m.config.AgentApply = func(context.Context, CraftNativeDocumentProfile, []byte, json.RawMessage) ([]byte, error) {
		*calls++
		return fmt.Appendf(nil, `{"sheets":[{"name":"S","edit":%d}]}`, *calls), nil
	}
	r.m.config.AgentInspect = func(context.Context, CraftNativeDocumentProfile, []byte, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"command":"cell.get","result":{"value":1}}`), nil
	}
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(r.f.pages.artifactMasterToken, r.a.WorkspaceID, r.a.CrewID), r.a.WorkspaceID, r.a.AgentID, r.a.RunID)
	return &chatRunToolRig{chatRunSheetRig: r, calls: calls, token: token}
}

func (r *chatRunToolRig) call(runToken, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/craft-native/chat-run", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+runToken)
	ctx := context.WithValue(context.WithValue(req.Context(), ctxInternalTokenWS, r.a.WorkspaceID), ctxInternalTokenCrew, r.a.CrewID)
	w := httptest.NewRecorder()
	r.m.chatRunTool(w, req.WithContext(ctx))
	return w
}

// The same operations a task uses, over the chat run's own binding: lease,
// compare and swap, receipt, durable attribution to the agent and the exact
// retry that never reruns the engine.
func TestChatRunSheetToolAppliesWithLeaseReceiptAndAttribution(t *testing.T) {
	r := newChatRunToolRig(t)
	acquired := r.call(r.token, `{"operation":"acquire"}`)
	if acquired.Code != 200 {
		t.Fatal("acquire", acquired.Code, acquired.Body)
	}
	var lease struct {
		Generation string `json:"generation"`
	}
	json.Unmarshal(acquired.Body.Bytes(), &lease)
	packet := fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"first","commands":[{"command":"cell.set","params":{"cell":"A1","input":"x"}}]}`, lease.Generation)
	first := r.call(r.token, packet)
	if first.Code != 200 {
		t.Fatal("apply", first.Code, first.Body)
	}
	var result craftNativeChatApplyResult
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil || result.PageID != "sheet-own" || result.Slug != "sheet-own" || result.Version != 2 || result.ChangedCells != 1 {
		t.Fatal("typed result", first.Body, err)
	}
	// What the live view needs to follow the change without reading it again:
	// the version it was made on, the hash of exactly the stored bytes and the
	// commands the server accepted.
	stored := sha256.Sum256([]byte(`{"sheets":[{"name":"S","edit":1}]}`))
	if result.BaseVersion != 1 || result.ContentHash != "sha256:"+hex.EncodeToString(stored[:]) || result.CommandsOmitted ||
		string(result.Commands) != `[{"command":"cell.set","params":{"cell":"A1","input":"x"}}]` {
		t.Fatalf("live-view fields: base=%d hash=%s omitted=%v commands=%s", result.BaseVersion, result.ContentHash, result.CommandsOmitted, result.Commands)
	}
	var receipt mooring.NativeSaveReceipt
	if err := json.Unmarshal(result.Receipt, &receipt); err != nil || receipt.Version != "2" || receipt.SessionID != "native-agent:"+r.a.RunID || receipt.Phase != "committed" {
		t.Fatal("receipt", receipt, err)
	}
	if again := r.call(r.token, packet); again.Code != 200 || again.Body.String() != first.Body.String() || *r.calls != 1 {
		t.Fatal("exact retry reran the engine or changed the receipt", again.Code, *r.calls)
	}
	if changed := r.call(r.token, strings.Replace(packet, `"input":"x"`, `"input":"y"`, 1)); changed.Code != 409 || *r.calls != 1 {
		t.Fatal("same request id accepted a changed edit", changed.Code)
	}
	if stale := r.call(r.token, fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"stale","commands":[{"command":"cell.set","params":{"cell":"A1","input":"z"}}]}`, lease.Generation)); stale.Code != 409 || *r.calls != 1 {
		t.Fatal("a stale base reached the engine", stale.Code, *r.calls)
	}
	var actor string
	if err := r.f.rig.db.QueryRow(`SELECT actor FROM artifact_versions WHERE page_id='sheet-own' AND version=2`).Scan(&actor); err != nil || actor != "agent:"+r.a.AgentID {
		t.Fatal("version not attributed to the agent", actor, err)
	}
	if released := r.call(r.token, fmt.Sprintf(`{"operation":"release","generation":%q}`, lease.Generation)); released.Code != 200 {
		t.Fatal("release", released.Code, released.Body)
	}
	if read := r.call(r.token, `{"operation":"read","query":{"command":"cell.get","params":{"cell":"A1"}}}`); read.Code != 200 {
		t.Fatal("read", read.Code, read.Body)
	}
}

// The tools carry no page, run or mandate: anything but the closed grammar is
// refused before it reaches the engine, and a bearer that is not this run's
// reaches nothing.
func TestChatRunSheetToolRefusesForeignFieldsAndForeignBearers(t *testing.T) {
	r := newChatRunToolRig(t)
	for _, body := range []string{
		`{"operation":"read","page_id":"sheet-second"}`,
		`{"operation":"read","page":"sheet-second","query":{"command":"cell.get","params":{"cell":"A1"}}}`,
		`{"operation":"acquire","run_id":"other"}`,
		`{"operation":"acquire","mandate_id":"m"}`,
		`{"operation":"apply","generation":"1","base_version":"1","request_id":"r","slug":"@sheet-second","commands":[{"command":"cell.set","params":{"cell":"A1","input":"x"}}]}`,
	} {
		if w := r.call(r.token, body); w.Code == 200 {
			t.Errorf("accepted: %s", body)
		}
	}
	if *r.calls != 0 {
		t.Fatalf("the engine ran %d times for refused input", *r.calls)
	}
	other := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(r.f.pages.artifactMasterToken, r.a.WorkspaceID, r.a.CrewID), r.a.WorkspaceID, r.a.AgentID, "another-run")
	if w := r.call(other, `{"operation":"acquire"}`); w.Code == 200 {
		t.Fatal("a run without a principal reached the Sheet")
	}
	if w := r.call("not-a-token", `{"operation":"acquire"}`); w.Code == 200 {
		t.Fatal("a forged bearer reached the Sheet")
	}
	if len(r.m.runs) > 1 {
		t.Fatalf("%d registrations for one run", len(r.m.runs))
	}
}

func TestNativeAgentChangedCellsCountsWritesNotContent(t *testing.T) {
	for raw, want := range map[string]int{
		`[{"command":"cell.set","params":{"cell":"A1","input":"x"}}]`:                                                                        1,
		`[{"command":"range.setValues","params":{"range":"A1","values":[[1,2,3],[4,5,6]]}}]`:                                                 6,
		`[{"command":"cell.set","params":{"cell":"A1","input":"x"}},{"command":"range.setValues","params":{"range":"B1","values":[[1,2]]}}]`: 3,
		`not json`: 0,
	} {
		if got := nativeAgentChangedCells(json.RawMessage(raw)); got != want {
			t.Errorf("%s: %d, want %d", raw, got, want)
		}
	}
}

// Authority lost while the engine runs must not publish, even when it comes
// back before the result does (ownership moves away and returns), and a
// permanent loss (the workspace flag turned off) stops the same publication.
func TestChatRunSheetRevocationDuringTheEngineNeverPublishes(t *testing.T) {
	for name, mutate := range map[string]func(*chatRunToolRig){
		"owner moves away and back": func(r *chatRunToolRig) {
			mustExec(t, r.f.rig.db, `UPDATE pages SET owner_user_id=? WHERE id='sheet-own'`, r.f.rig.adminID)
			mustExec(t, r.f.rig.db, `UPDATE pages SET owner_user_id=? WHERE id='sheet-own'`, r.a.HumanUserID)
		},
		"flag turned off": func(r *chatRunToolRig) {
			mustExec(t, r.f.rig.db, `DELETE FROM feature_flag_overrides WHERE id='chatrun-flag-on'`)
		},
		"human suspended": func(r *chatRunToolRig) {
			mustExec(t, r.f.rig.db, `UPDATE users SET suspended_at='2026-11-25T10:00:00Z' WHERE id=?`, r.a.HumanUserID)
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newChatRunToolRig(t)
			acquired := r.call(r.token, `{"operation":"acquire"}`)
			if acquired.Code != 200 {
				t.Fatal(acquired.Code, acquired.Body)
			}
			var lease struct {
				Generation string `json:"generation"`
			}
			json.Unmarshal(acquired.Body.Bytes(), &lease)
			r.m.config.AgentApply = func(context.Context, CraftNativeDocumentProfile, []byte, json.RawMessage) ([]byte, error) {
				*r.calls++
				mutate(r)
				return []byte(`{"sheets":[{"name":"must not commit"}]}`), nil
			}
			result := r.call(r.token, fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"revoked","commands":[{"command":"cell.set","params":{"cell":"A1","input":"x"}}]}`, lease.Generation))
			if result.Code == 200 || *r.calls != 1 {
				t.Fatal("an engine result survived the revocation", result.Code, result.Body, *r.calls)
			}
			var head int64
			if err := r.f.rig.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE page_id='sheet-own'`).Scan(&head); err != nil || head != 1 {
				t.Fatal("the revocation crossed publication", head, err)
			}
		})
	}
}

// A person editing the Sheet keeps it: the run's acquire is refused with the
// holder's kind and the engine never runs.
func TestChatRunSheetHumanHoldingTheLeaseRefusesTheAgent(t *testing.T) {
	r := newChatRunToolRig(t)
	human := auth.WithOriginalCredential(context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxUser, &AuthUser{ID: r.a.HumanUserID, SessionID: r.source.ID, OriginalCredential: r.source}), ctxAuthKind, AuthKindSession), ctxWorkspaceID, r.a.WorkspaceID), *r.source)
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{}`)).WithContext(human)
	req.SetPathValue("slug", "@sheet-own")
	w := httptest.NewRecorder()
	r.m.human.sessions(w, req)
	var session struct {
		Token string `json:"session_token"`
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &session) != nil {
		t.Skipf("human admission is not available in this fixture: %d %s", w.Code, w.Body)
	}
	if held := craftNativeLeaseCall(r.m.human, human, session.Token, "@sheet-own", "acquire", `{}`); held.Code != 200 {
		t.Fatal("human acquire", held.Code, held.Body)
	}
	refused := r.call(r.token, `{"operation":"acquire"}`)
	if refused.Code != 409 || !strings.Contains(refused.Body.String(), `"reason":"holder_human"`) || *r.calls != 0 {
		t.Fatal("a person's lease did not stop the run", refused.Code, refused.Body, *r.calls)
	}
}

// A server restart forgets every registration. The old generation is then
// worthless: the next call derives the Sheet again from the database, but an
// apply under the lease it held before the restart is refused and nothing
// is published.
func TestChatRunSheetServerRestartMidRunPublishesNothingUnderTheOldLease(t *testing.T) {
	r := newChatRunToolRig(t)
	acquired := r.call(r.token, `{"operation":"acquire"}`)
	if acquired.Code != 200 {
		t.Fatal(acquired.Code, acquired.Body)
	}
	var lease struct {
		Generation string `json:"generation"`
	}
	json.Unmarshal(acquired.Body.Bytes(), &lease)
	r.m.mu.Lock()
	r.m.entries, r.m.runs = map[string]*craftNativeAgentEntry{}, map[string]*craftNativeAgentEntry{} // the restart
	r.m.mu.Unlock()
	result := r.call(r.token, fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"after-restart","commands":[{"command":"cell.set","params":{"cell":"A1","input":"x"}}]}`, lease.Generation))
	if result.Code == 200 || *r.calls != 0 {
		t.Fatal("an apply under a pre-restart lease was accepted", result.Code, result.Body, *r.calls)
	}
	var head int64
	if err := r.f.rig.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE page_id='sheet-own'`).Scan(&head); err != nil || head != 1 {
		t.Fatal("the restart let a write through", head, err)
	}
}

// The agent's implicit direct write holds only while the Sheet stays in its
// owner's personal space; once it is not, the run may only propose.
func TestChatRunSheetLeavingThePersonalSpaceEndsDirectWrites(t *testing.T) {
	r := newChatRunToolRig(t)
	if first := r.call(r.token, `{"operation":"acquire"}`); first.Code != 200 {
		t.Fatal(first.Code, first.Body)
	}
	mustExec(t, r.f.rig.db, `UPDATE pages SET private_audience=0 WHERE id='sheet-own'`)
	refused := r.call(r.token, `{"operation":"acquire"}`)
	if refused.Code == 200 || *r.calls != 0 {
		t.Fatal("a write path stayed open after the Sheet left the personal space", refused.Code, refused.Body)
	}
}

// Every refusal carries a stable machine reason from a closed list and a
// fixed sentence: nothing of the engine's text, a document or another Sheet.
func TestChatRunSheetRefusalsCarryAStableReason(t *testing.T) {
	reasonOf := func(w *httptest.ResponseRecorder) string {
		var body struct{ Error, Reason string }
		if json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Error == "" {
			t.Fatalf("not a reason body: %s", w.Body)
		}
		if strings.Contains(w.Body.String(), "sheet-") {
			t.Fatalf("a refusal named a page: %s", w.Body)
		}
		return body.Reason
	}
	r := newChatRunToolRig(t)
	acquired := r.call(r.token, `{"operation":"acquire"}`)
	var lease struct {
		Generation string `json:"generation"`
	}
	json.Unmarshal(acquired.Body.Bytes(), &lease)
	apply := func(base, request, input string) *httptest.ResponseRecorder {
		return r.call(r.token, fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":%q,"request_id":%q,"commands":[{"command":"cell.set","params":{"cell":"A1","input":%q}}]}`, lease.Generation, base, request, input))
	}
	if first := apply("1", "ok", "x"); first.Code != 200 {
		t.Fatal(first.Code, first.Body)
	}
	if got := reasonOf(apply("1", "stale-two", "y")); got != CraftSheetReasonStale {
		t.Fatalf("stale base: %q", got)
	}
	if got := reasonOf(r.call(r.token, `{"operation":"read","page_id":"x"}`)); got != CraftSheetReasonInvalid {
		t.Fatalf("foreign field: %q", got)
	}
	if got := reasonOf(apply("1", "f", "=1+1")); got != CraftSheetReasonStale {
		t.Fatalf("formula on a stale base: %q", got)
	}
	other := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(r.f.pages.artifactMasterToken, r.a.WorkspaceID, r.a.CrewID), r.a.WorkspaceID, r.a.AgentID, "another-run")
	if w := r.call(other, `{"operation":"acquire"}`); w.Code == 200 {
		t.Fatal("foreign run accepted")
	}
}

func TestChatRunSheetBindingReasons(t *testing.T) {
	for name, tc := range map[string]struct {
		before func(*chatRunSheetRig)
		want   string
	}{
		"flag off": {func(r *chatRunSheetRig) {
			mustExec(t, r.f.rig.db, `DELETE FROM feature_flag_overrides WHERE id='chatrun-flag-on'`)
		}, CraftSheetReasonFeatureOff},
		"crew excluded": {func(r *chatRunSheetRig) { mustExec(t, r.f.rig.db, `DELETE FROM workspace_artifact_creation_crews`) }, CraftSheetReasonAgentPolicy},
		"someone else's": {func(r *chatRunSheetRig) {
			mustExec(t, r.f.rig.db, `UPDATE pages SET owner_user_id=? WHERE id='sheet-own'`, r.f.rig.adminID)
		}, CraftSheetReasonNotAttached},
		"no longer private": {func(r *chatRunSheetRig) {
			mustExec(t, r.f.rig.db, `UPDATE pages SET private_audience=0 WHERE id='sheet-own'`)
		}, CraftSheetReasonNotAttached},
	} {
		t.Run(name, func(t *testing.T) {
			r := newChatRunSheetRig(t, true, "sheet-own")
			tc.before(r)
			_, err := r.attach(t)
			reason := CraftSheetReasonNotAttached
			var refusal craftNativeChatRefusal
			if errors.As(err, &refusal) {
				reason = refusal.reason
			}
			if err == nil || reason != tc.want {
				t.Fatalf("err=%v reason=%q, want %q", err, reason, tc.want)
			}
		})
	}
}

// A third Sheet while the engine's session slots are all running gets a
// readable "busy" answer. Nothing ran: the same request ID is sent again
// later and saves exactly once.
func TestChatRunSheetBusyEngineIsRetryable(t *testing.T) {
	r := newChatRunToolRig(t)
	acquired := r.call(r.token, `{"operation":"acquire"}`)
	var lease struct {
		Generation string `json:"generation"`
	}
	json.Unmarshal(acquired.Body.Bytes(), &lease)
	realApply, realInspect := r.m.config.AgentApply, r.m.config.AgentInspect
	busy := true
	r.m.config.AgentApply = func(ctx context.Context, p CraftNativeDocumentProfile, b []byte, c json.RawMessage) ([]byte, error) {
		if busy {
			return nil, ErrCraftNativeSheetBusy
		}
		return realApply(ctx, p, b, c)
	}
	r.m.config.AgentInspect = func(ctx context.Context, p CraftNativeDocumentProfile, b []byte, q json.RawMessage) (json.RawMessage, error) {
		if busy {
			return nil, ErrCraftNativeSheetBusy
		}
		return realInspect(ctx, p, b, q)
	}
	apply := fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"same","commands":[{"command":"cell.set","params":{"cell":"A1","input":"x"}}]}`, lease.Generation)
	for name, body := range map[string]string{"apply": apply, "read": `{"operation":"read","query":{"command":"cell.get","params":{"cell":"A1"}}}`} {
		w := r.call(r.token, body)
		var answer struct{ Error, Reason string }
		json.Unmarshal(w.Body.Bytes(), &answer)
		if w.Code != 503 || answer.Reason != CraftSheetReasonBusy || answer.Error == "" {
			t.Fatalf("%s while busy: %d %s", name, w.Code, w.Body)
		}
	}
	if *r.calls != 0 {
		t.Fatal("the engine ran while busy")
	}
	busy = false
	if w := r.call(r.token, apply); w.Code != 200 || *r.calls != 1 {
		t.Fatalf("the same request ID was not retryable after busy: %d %s calls=%d", w.Code, w.Body, *r.calls)
	}
}

// A routine, webhook, cron, assignment or agent-query run has no human
// message and no attached Sheet. Through the real handler none of its tools
// reaches a Sheet: not read, not lease, not apply, and nothing is audited.
func TestChatRunSheetToolRefusesRunsWithoutAHumanMessage(t *testing.T) {
	r := newChatRunToolRig(t)
	writer := journal.NewWriter(r.f.rig.db, newTestLogger(), journal.WriterOptions{ChainKey: []byte("synthetic-artifact-refs-chain"), FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	bodies := []string{
		`{"operation":"acquire"}`,
		`{"operation":"read","query":{"command":"cell.get","params":{"cell":"A1"}}}`,
		`{"operation":"apply","generation":"1","base_version":"1","request_id":"u","commands":[{"command":"cell.set","params":{"cell":"A1","input":"x"}}]}`,
		`{"operation":"propose","base_version":"1","request_id":"p","summary":"s","commands":[{"command":"cell.set","params":{"cell":"A1","input":"x"}}]}`,
	}
	for i, trigger := range []string{"ROUTINE", "WEBHOOK", "CRON", "ASSIGNMENT", "PEER_QUERY", "SCHEDULED"} {
		run := fmt.Sprintf("unattended-%d", i)
		// What the host writes for a run admitted without a human message.
		if _, err := writer.EmitSync(t.Context(), journal.Entry{ID: "run-start:" + run, WorkspaceID: r.a.WorkspaceID, CrewID: r.a.CrewID, AgentID: r.a.AgentID, Type: journal.EntryRunStarted, Severity: journal.SeverityInfo, ActorType: journal.ActorSidecar, Summary: "run started", TraceID: run,
			Payload: map[string]any{"trigger_type": trigger, "host_admitted": true, "chat_id": r.a.ChatID}}); err != nil {
			t.Fatalf("%s run start: %v", trigger, err)
		}
		token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(r.f.pages.artifactMasterToken, r.a.WorkspaceID, r.a.CrewID), r.a.WorkspaceID, r.a.AgentID, run)
		for _, body := range bodies {
			got := r.call(token, body)
			// Refused at the door (no human principal) or by the binding
			// (not_attached): either way 403 and nothing about any Sheet.
			if got.Code != 403 {
				t.Fatalf("%s run, %.30s: %d %s", trigger, body, got.Code, got.Body)
			}
			if strings.Contains(got.Body.String(), "sheet-") {
				t.Fatalf("a refusal named a Sheet: %s", got.Body)
			}
		}
		if r.m.runs[run] != nil {
			t.Fatalf("%s run holds a Sheet binding", trigger)
		}
	}
	var n int
	if err := r.f.rig.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='artifact.agent_run_accessed' AND entity_id='sheet-own'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("audited %d accesses of runs without a human message (err %v)", n, err)
	}
	if *r.calls != 0 {
		t.Fatal("the engine ran for a run without a human message")
	}
}

// The internal listener closes a slow answer after its write timeout (30 s in
// production). A chat run's first call waits for the engine to open a large
// Sheet, so the tool extends the deadline of its own response. The listener
// here times out after 200 ms, the engine takes 700 ms.
func TestChatRunSheetToolAnswerOutlivesTheListenersWriteTimeout(t *testing.T) {
	r := newChatRunToolRig(t)
	acquired := r.call(r.token, `{"operation":"acquire"}`)
	var lease struct {
		Generation string `json:"generation"`
	}
	json.Unmarshal(acquired.Body.Bytes(), &lease)
	realApply := r.m.config.AgentApply
	r.m.config.AgentApply = func(ctx context.Context, p CraftNativeDocumentProfile, b []byte, c json.RawMessage) ([]byte, error) {
		time.Sleep(700 * time.Millisecond)
		return realApply(ctx, p, b, c)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := context.WithValue(context.WithValue(req.Context(), ctxInternalTokenWS, r.a.WorkspaceID), ctxInternalTokenCrew, r.a.CrewID)
		r.m.chatRunTool(w, req.WithContext(ctx))
	}))
	srv.Config.WriteTimeout = 200 * time.Millisecond
	srv.Start()
	defer srv.Close()
	body := fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"slow","commands":[{"command":"cell.set","params":{"cell":"A1","input":"x"}}]}`, lease.Generation)
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+r.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("the answer was cut off by the listener's write timeout: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// A run delegated from the conversation (it inherits the human's authority
// through an origin) does not receive the Sheet the person attached to the
// parent message: the Sheet belongs to the run that message started.
func TestChatRunSheetToolRefusesADelegatedRun(t *testing.T) {
	r := newChatRunToolRig(t)
	tx, err := r.f.rig.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := runauthority.CaptureOrigin(t.Context(), tx, r.a.RunID, r.a.AgentID, r.a.WorkspaceID)
	if err != nil {
		t.Fatalf("capture origin: %v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	writer := journal.NewWriter(r.f.rig.db, newTestLogger(), journal.WriterOptions{ChainKey: []byte("synthetic-artifact-refs-chain"), FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	const child = "delegated-child-run"
	if _, err = writer.EmitSync(t.Context(), journal.Entry{ID: "run-start:" + child, WorkspaceID: r.a.WorkspaceID, CrewID: r.a.CrewID, AgentID: r.a.AgentID, Type: journal.EntryRunStarted, Severity: journal.SeverityInfo, ActorType: journal.ActorSidecar, Summary: "run started", TraceID: child,
		Payload: map[string]any{"trigger_type": "ASSIGNMENT", "host_admitted": true, "chat_id": r.a.ChatID, "chat_origin_id": origin.ID}}); err != nil {
		t.Fatal(err)
	}
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(r.f.pages.artifactMasterToken, r.a.WorkspaceID, r.a.CrewID), r.a.WorkspaceID, r.a.AgentID, child)
	for _, body := range []string{
		`{"operation":"acquire"}`,
		`{"operation":"read","query":{"command":"cell.get","params":{"cell":"A1"}}}`,
		`{"operation":"apply","generation":"1","base_version":"1","request_id":"d","commands":[{"command":"cell.set","params":{"cell":"A1","input":"x"}}]}`,
		`{"operation":"propose","base_version":"1","request_id":"dp","summary":"s","commands":[{"command":"cell.set","params":{"cell":"A1","input":"x"}}]}`,
	} {
		got := r.call(token, body)
		if got.Code != 403 || strings.Contains(got.Body.String(), "sheet-") {
			t.Fatalf("delegated run, %.30s: %d %s", body, got.Code, got.Body)
		}
	}
	if r.m.runs[child] != nil {
		t.Fatal("a delegated run holds a Sheet binding")
	}
	var n int
	if err = r.f.rig.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='artifact.agent_run_accessed' AND entity_id='sheet-own'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("audited %d accesses of the Sheet by a delegated run (err %v)", n, err)
	}
	if *r.calls != 0 {
		t.Fatal("the engine ran for a delegated run")
	}
	// The parent run itself still reaches its Sheet.
	if w := r.call(r.token, `{"operation":"acquire"}`); w.Code != 200 {
		t.Fatalf("the parent run lost its Sheet: %d %s", w.Code, w.Body)
	}
}

// The per-run read limit counts standing and folder reads of OTHER pages. The
// Sheet the person attached to the message is reached by that attachment, so
// a run whose read limit is already used up still reads and edits it,
// and the tool never charges a read for it.
func TestChatRunSheetToolIsNotChargedAgainstTheRunReadLimit(t *testing.T) {
	r := newChatRunToolRig(t)
	// The smallest limit, already used up by a standing read of another page.
	mustExec(t, r.f.rig.db, `UPDATE workspace_artifact_creation_policy SET run_read_limit=1 WHERE workspace_id=?`, r.a.WorkspaceID)
	mustExec(t, r.f.rig.db, `INSERT INTO artifact_run_access(run_id,workspace_id,page_id,source,created_at) VALUES(?,?,'sheet-second','standing','2026-11-25T10:00:00Z')`, r.a.RunID, r.a.WorkspaceID)
	acquired := r.call(r.token, `{"operation":"acquire"}`)
	var lease struct {
		Generation string `json:"generation"`
	}
	json.Unmarshal(acquired.Body.Bytes(), &lease)
	for i := 0; i < 5; i++ {
		if w := r.call(r.token, `{"operation":"read","query":{"command":"cell.get","params":{"cell":"A1"}}}`); w.Code != 200 {
			t.Fatalf("read %d with the read limit used up: %d %s", i, w.Code, w.Body)
		}
	}
	apply := fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"limit","commands":[{"command":"cell.set","params":{"cell":"A1","input":"x"}}]}`, lease.Generation)
	if w := r.call(r.token, apply); w.Code != 200 {
		t.Fatalf("apply with the read limit used up: %d %s", w.Code, w.Body)
	}
	var charged int
	if err := r.f.rig.db.QueryRow(`SELECT COUNT(*) FROM artifact_run_access WHERE run_id=?`, r.a.RunID).Scan(&charged); err != nil || charged != 1 {
		t.Fatalf("the run holds %d read charges, want only the one inserted for the other page (err %v)", charged, err)
	}
}

// Prewarm is a hint over the same binding as every Sheet call: it opens the
// attached Sheet in the engine in the background, once per run, and nothing
// else. A run with nothing attached, or after the flag was turned off, is
// refused; it reads no answer, takes no lease and charges nothing.
func TestChatRunSheetPrewarmOpensOnceAndTouchesNothing(t *testing.T) {
	r := newChatRunToolRig(t)
	opened := make(chan json.RawMessage, 4)
	r.m.config.AgentInspect = func(_ context.Context, _ CraftNativeDocumentProfile, _ []byte, q json.RawMessage) (json.RawMessage, error) {
		opened <- q
		return json.RawMessage(`{"command":"document.inspect","result":{}}`), nil
	}
	first := r.call(r.token, `{"operation":"prewarm"}`)
	if first.Code != 202 || !strings.Contains(first.Body.String(), "started") {
		t.Fatalf("prewarm: %d %s", first.Code, first.Body)
	}
	select {
	case q := <-opened:
		if !strings.Contains(string(q), "document.inspect") {
			t.Fatalf("prewarm asked the engine %s", q)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the engine was not asked to open the Sheet")
	}
	again := r.call(r.token, `{"operation":"prewarm"}`)
	if again.Code != 202 || !strings.Contains(again.Body.String(), "skipped") {
		t.Fatalf("a second prewarm of the run: %d %s", again.Code, again.Body)
	}
	select {
	case <-opened:
		t.Fatal("a second prewarm opened the Sheet again")
	case <-time.After(200 * time.Millisecond):
	}
	if *r.calls != 0 {
		t.Fatal("prewarm applied a change")
	}
	var leases, charged int
	r.f.rig.db.QueryRow(`SELECT COUNT(*) FROM artifact_run_access WHERE run_id=?`, r.a.RunID).Scan(&charged)
	r.f.rig.db.QueryRow(`SELECT COUNT(*) FROM mooring_edit_leases`).Scan(&leases)
	if charged != 0 || leases != 0 {
		t.Fatalf("prewarm charged %d reads and took %d leases", charged, leases)
	}
	// Anything else in the request is not a prewarm.
	if w := r.call(r.token, `{"operation":"prewarm","page_id":"x"}`); w.Code == 202 {
		t.Fatal("a prewarm with extra fields was accepted")
	}
	// The flag off: refused, nothing opened.
	other := newChatRunToolRig(t)
	mustExec(t, other.f.rig.db, `UPDATE feature_flag_overrides SET enabled=0 WHERE id='chatrun-flag-on'`)
	if w := other.call(other.token, `{"operation":"prewarm"}`); w.Code != 403 {
		t.Fatalf("a prewarm with the flag off: %d %s", w.Code, w.Body)
	}
}

func TestChatRunSheetPrewarmIsLimitedPerConversation(t *testing.T) {
	m := &craftNativeAgentManager{}
	if !m.reservePrewarm("chat-1") || m.reservePrewarm("chat-1") {
		t.Fatal("a conversation may start one prewarm at a time")
	}
	if !m.reservePrewarm("chat-2") {
		t.Fatal("another conversation was blocked")
	}
	m.prewarmMu.Lock()
	m.prewarmChats["chat-1"] = time.Now().Add(-craftNativeChatPrewarmEvery - time.Second)
	m.prewarmMu.Unlock()
	if !m.reservePrewarm("chat-1") {
		t.Fatal("the window never reopened")
	}
}

// A run whose message carries no Sheet, and whose agent was given none, is refused a prewarm without a trace:
// no binding, no audit row, nothing opened. The sidecar sends the hint for every
// run, so this is the common case.
func TestChatRunSheetPrewarmWithNothingAttachedLeavesNoTrace(t *testing.T) {
	r := newChatRunToolRig(t)
	mustExec(t, r.f.rig.db, `DELETE FROM artifact_message_refs`)
	mustExec(t, r.f.rig.db, `DELETE FROM artifact_run_scope`)
	mustExec(t, r.f.rig.db, `DELETE FROM agent_files`) // nor given to the agent (agent_files would let it read)
	opened := make(chan struct{}, 1)
	r.m.config.AgentInspect = func(context.Context, CraftNativeDocumentProfile, []byte, json.RawMessage) (json.RawMessage, error) {
		opened <- struct{}{}
		return json.RawMessage(`{}`), nil
	}
	for i := 0; i < 3; i++ {
		if w := r.call(r.token, `{"operation":"prewarm"}`); w.Code != 403 {
			t.Fatalf("prewarm %d with nothing attached: %d %s", i, w.Code, w.Body)
		}
	}
	var audits int
	if err := r.f.rig.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='artifact.agent_run_accessed'`).Scan(&audits); err != nil || audits != 0 {
		t.Fatalf("%d audit rows for a run without a Sheet (err %v)", audits, err)
	}
	if r.m.runs[r.a.RunID] != nil {
		t.Fatal("a run without a Sheet holds a binding")
	}
	select {
	case <-opened:
		t.Fatal("the engine was asked to open something")
	case <-time.After(150 * time.Millisecond):
	}
}

// A chat run's apply is lease-fenced like every other content write: with no
// lease, with another generation, or after the lease was released it writes
// nothing and never reaches the engine.
func TestChatRunSheetApplyWithoutAValidLeaseWritesNothing(t *testing.T) {
	r := newChatRunToolRig(t)
	apply := func(generation, request string) *httptest.ResponseRecorder {
		return r.call(r.token, fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":%q,"commands":[{"command":"cell.set","params":{"cell":"A1","input":"x"}}]}`, generation, request))
	}
	versions := func() int {
		var n int
		r.f.rig.db.QueryRow(`SELECT COUNT(*) FROM artifact_versions WHERE page_id='sheet-own'`).Scan(&n)
		return n
	}
	before := versions()
	if w := apply("1", "no-lease"); w.Code == 200 {
		t.Fatalf("an apply without any lease succeeded: %s", w.Body)
	}
	acquired := r.call(r.token, `{"operation":"acquire"}`)
	var lease struct {
		Generation string `json:"generation"`
	}
	json.Unmarshal(acquired.Body.Bytes(), &lease)
	if w := apply("999999", "wrong-generation"); w.Code == 200 {
		t.Fatalf("an apply with another generation succeeded: %s", w.Body)
	}
	if w := r.call(r.token, fmt.Sprintf(`{"operation":"release","generation":%q}`, lease.Generation)); w.Code != 200 {
		t.Fatalf("release: %d %s", w.Code, w.Body)
	}
	if w := apply(lease.Generation, "after-release"); w.Code == 200 {
		t.Fatalf("an apply after the release succeeded: %s", w.Body)
	}
	if *r.calls != 0 || versions() != before {
		t.Fatalf("a refused apply reached the engine (%d calls) or wrote a version (%d -> %d)", *r.calls, before, versions())
	}
}

// The administrator sees whether an agent can work on a Sheet in chat, and why not.
func TestChatRunReadinessNamesWhatIsMissing(t *testing.T) {
	r := newChatRunToolRig(t) // an enabled manager with both adapters and a pinned Sheet engine
	if state, reason := r.m.ChatRunReadiness(); state != craftapps.StateReady || reason != craftapps.ReasonNone {
		t.Fatalf("a configured manager: %s %s", state, reason)
	}
	var none *craftNativeAgentManager
	if state, reason := none.ChatRunReadiness(); state != craftapps.StateUnavailable || reason != craftapps.ReasonAgentRuntimeUnavailable {
		t.Fatalf("no manager: %s %s", state, reason)
	}
	apply := r.m.config.AgentApply
	r.m.config.AgentApply = nil
	if _, reason := r.m.ChatRunReadiness(); reason != craftapps.ReasonAgentEngineMissing {
		t.Fatalf("no apply adapter: %s", reason)
	}
	r.m.config.AgentApply = apply
	profiles := r.m.documents.profiles
	r.m.documents.profiles = nil
	if _, reason := r.m.ChatRunReadiness(); reason != craftapps.ReasonSheetEngineNotPinned {
		t.Fatalf("no pinned engine: %s", reason)
	}
	r.m.documents.profiles = profiles
	r.m.enabled = false
	if state, reason := r.m.ChatRunReadiness(); state != craftapps.StateDisabled || reason != craftapps.ReasonAppDisabled {
		t.Fatalf("agents off: %s %s", state, reason)
	}
	r.m.enabled = true
	if _, reason := r.m.TasksReadiness(); reason != craftapps.ReasonAgentRuntimeUnavailable && reason != craftapps.ReasonAgentDispatcherMissing && reason != craftapps.ReasonNone {
		t.Fatalf("tasks readiness gave %q", reason)
	}
	for _, reason := range []craftapps.Reason{craftapps.ReasonAgentRuntimeUnavailable, craftapps.ReasonAgentEngineMissing, craftapps.ReasonSheetEngineNotPinned, craftapps.ReasonAgentDispatcherMissing} {
		if reason.Message() == "" {
			t.Fatalf("%s has no sentence", reason)
		}
	}
}

// A connection that is not synchronous FULL is refused as before, and the
// refusal names the cause for the operator's log.
func TestChatRunSheetWriterRefusesANotDurableDatabase(t *testing.T) {
	r := newChatRunSheetRig(t, true, "sheet-own")
	db := r.f.rig.db
	db.SetMaxOpenConns(1)
	mustExec(t, db, `PRAGMA synchronous=NORMAL`)
	tx, err := r.m.writer(t.Context())
	if err == nil {
		tx.Rollback()
		t.Fatal("the writer accepted a NORMAL connection")
	}
	if !errors.Is(err, errCraftNativeAgentDenied) || !strings.Contains(err.Error(), "database not durable") {
		t.Fatalf("refusal %v", err)
	}
	mustExec(t, db, `PRAGMA synchronous=FULL`)
	if tx, err = r.m.writer(t.Context()); err != nil {
		t.Fatalf("FULL connection refused: %v", err)
	}
	tx.Rollback()
}
