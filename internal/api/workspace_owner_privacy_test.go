package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/automation"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// Capture the production Observer's enqueue boundary; never execute a routine.
type ownerPrivacyEnqueues struct {
	mu   sync.Mutex
	runs []pipeline.PendingRun
}

func (e *ownerPrivacyEnqueues) Enqueue(_ context.Context, run pipeline.PendingRun) (string, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.runs = append(e.runs, run)
	return run.ID, false, nil
}

func TestOwnerCreationCanonicalPrivacyConsumers(t *testing.T) {
	f := newOwnerCreationFixture(t, "create")
	if rr := f.mutate(); rr.Code != 201 {
		t.Fatalf("SETUP: actual owner creation status=%d", rr.Code)
	}
	var workspace, member, owner string
	if err := f.db.QueryRow(`SELECT workspace_id,id,user_id FROM workspace_members WHERE role='OWNER'`).Scan(&workspace, &member, &owner); err != nil {
		t.Fatal(err)
	}
	entries, _, err := journal.List(t.Context(), f.db, journal.Query{WorkspaceID: workspace, Types: []journal.EntryType{journal.EntryMemberOwnerCreated}})
	if err != nil || len(entries) != 1 {
		t.Fatalf("SETUP: actual canonical event count=%d err=%v", len(entries), err)
	}
	e := entries[0]
	next, ok := e.Payload["new_membership"].(map[string]any)
	caps, capsOK := next["effective_capabilities"].([]any)
	if !ok || e.ActorID != owner || e.Payload["member_id"] != member || e.Payload["action"] != "owner_created" || e.Payload["old_membership"] != nil || next["id"] != member || next["role"] != "OWNER" || next["access_mode"] != "trusted" || !capsOK || len(caps) == 0 {
		t.Fatal("SETUP: real owner snapshot missing or not bound to stored membership")
	}
	var raw, hash string
	if err := f.db.QueryRow(`SELECT payload,entry_hash FROM journal_entries WHERE id=?`, e.ID).Scan(&raw, &hash); err != nil || hash == "" {
		t.Fatalf("SETUP: canonical payload/hash err=%v", err)
	}
	verify := func() {
		t.Helper()
		proof, err := journal.VerifyChainWithKey(t.Context(), f.db, workspace, f.key)
		if err != nil || !proof.OK {
			t.Fatalf("configured canonical chain invalid: %v", err)
		}
	}
	verify()
	public := journal.SerializeEntry(e)["payload"].(map[string]any)
	if len(public) != 4 || public["member_id"] != member || public["subject_user_id"] != owner || public["action"] != "owner_created" || public["creation_path"] != "workspace_create" {
		t.Fatal("SETUP: existing public feed whitelist failed")
	}
	manager := seedMemberWithCapabilities(t, f.db, workspace, "MANAGER", `["chat"]`, "owner-privacy-manager")
	validator, err := auth.NewJWTValidator("owner-creation-authenticated-router-secret")
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.NewDBStore(f.db).Create(t.Context(), manager, "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	token, err := validator.IssueAccessToken(manager, session.ID, "Manager", "manager@example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Workspace-ID", workspace)
		r.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		f.router.ServeHTTP(rr, r)
		return rr
	}
	if rr := request("GET", "/api/v1/workspaces/"+workspace+"/members/"+owner+"/capabilities", ""); rr.Code != 403 {
		t.Fatalf("SETUP: MANAGER administrative read status=%d", rr.Code)
	}
	t.Run("Preview", func(t *testing.T) {
		rr := request("POST", "/api/v1/automations/preview", `{"event_type":"member.owner_created","matcher":{}}`)
		if rr.Code != 200 {
			t.Fatalf("SETUP: native Preview status=%d", rr.Code)
		}
		var response struct {
			Scanned, Matched int
			Samples          []journal.Entry
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Scanned != 1 || response.Matched != 1 || len(response.Samples) != 1 {
			t.Fatal("SETUP: native Preview did not select actual owner event")
		}
		for _, key := range []string{"old_membership", "new_membership"} {
			if _, exposed := response.Samples[0].Payload[key]; exposed {
				t.Errorf("Preview exposed canonical-only owner field %s", key)
			}
		}
		body, err := json.Marshal(map[string]any{"event_type": "member.owner_created", "matcher": automation.Matcher{PayloadEquals: map[string]any{"new_membership": next}}})
		if err != nil {
			t.Fatal(err)
		}
		rr = request("POST", "/api/v1/automations/preview", string(body))
		if rr.Code != 200 {
			t.Fatalf("SETUP: private predicate Preview status=%d", rr.Code)
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Matched != 0 {
			t.Errorf("Preview matched private owner snapshot: matched=%d", response.Matched)
		}
	})
	t.Run("Observer", func(t *testing.T) {
		private := automation.Matcher{PayloadEquals: map[string]any{"new_membership": next}}
		if !private.Matches(e) {
			t.Fatal("SETUP: canonical owner matcher does not match actual producer")
		}
		sink := &ownerPrivacyEnqueues{}
		registry := automation.NewRegistry(nil, sink, automation.Options{})
		makeRule := func(id string) automation.Resolved {
			return automation.Resolved{Automation: automation.Automation{ID: id, WorkspaceID: workspace, Name: id, Enabled: true, EventType: string(e.Type), ActionKind: automation.ActionKindRoutine, Action: automation.Action{RoutineSlug: "not-executed", Inputs: map[string]any{"public": "{{ event.payload.action }}", "private": "{{ event.payload.new_membership }}"}}, MaxPerHour: 60}, PipelineID: "not-executed", PipelineSlug: "not-executed"}
		}
		denied, allowed := makeRule("private-owner-match"), makeRule("public-owner-match")
		denied.Matcher = private
		registry.Load([]automation.Resolved{denied, allowed})
		registry.Observer([]journal.Entry{e})
		registry.Flush(t.Context())
		sink.mu.Lock()
		calls := append([]pipeline.PendingRun(nil), sink.runs...)
		sink.mu.Unlock()
		if len(calls) != 1 {
			t.Errorf("Observer private predicate fired or public event lost: enqueues=%d want=1", len(calls))
		}
		for _, run := range calls {
			var inputs map[string]any
			if err := json.Unmarshal([]byte(run.InputsJSON), &inputs); err != nil {
				t.Fatal(err)
			}
			if inputs["public"] != "owner_created" {
				t.Error("Observer lost public action")
			}
			if strings.Contains(run.InputsJSON, "effective_capabilities") || strings.Contains(run.InputsJSON, "OWNER") {
				t.Error("Observer template exposed canonical-only owner snapshot")
			}
		}
	})
	t.Run("FTS", func(t *testing.T) {
		for _, tc := range []struct {
			query string
			want  int
		}{{"Initial workspace owner created", 1}, {"new_membership", 0}, {"effective_capabilities", 0}, {"payload:effective_capabilities", 0}} {
			query := url.Values{"entry_type": {"member.owner_created"}, "q": {tc.query}}.Encode()
			for _, endpoint := range []string{"/api/v1/journal", "/api/v1/journal/count"} {
				rr := request("GET", endpoint+"?"+query, "")
				if rr.Code != 200 {
					t.Fatalf("SETUP: native FTS status=%d", rr.Code)
				}
				var response struct {
					Entries []journal.Entry
					Total   int
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				got := len(response.Entries)
				if endpoint == "/api/v1/journal/count" {
					got = response.Total
				}
				if got != tc.want {
					t.Errorf("owner snapshot FTS selected via %s query=%q got=%d want=%d", endpoint, tc.query, got, tc.want)
				}
			}
		}
	})
	var afterRaw, afterHash string
	if err := f.db.QueryRow(`SELECT payload,entry_hash FROM journal_entries WHERE id=?`, e.ID).Scan(&afterRaw, &afterHash); err != nil {
		t.Fatal(err)
	}
	if afterRaw != raw || afterHash != hash {
		t.Error("public consumers changed canonical owner payload/hash")
	}
	verify()
}
