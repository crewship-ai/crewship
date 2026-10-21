package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/ws"
)

// Persist the real journal inline so replay assertions need no polling or sleeps.
type folderRealtimeJournal struct{ *journal.Writer }

func (j folderRealtimeJournal) Emit(ctx context.Context, e journal.Entry) (string, error) {
	return j.EmitSync(ctx, e)
}

func TestPrivatePageFolderRealtime(t *testing.T) {
	h, workspace, _, _, create := documentCreationFixture(t)
	creationExpect(t, create("private", documentCreationBody), 201)
	var privateID string
	var privateAudience, hasDocument int
	if err := h.db.QueryRow(`SELECT p.id,p.private_audience,EXISTS(SELECT 1 FROM documents d WHERE d.page_id=p.id) FROM pages p WHERE p.workspace_id=? AND p.slug='created-doc'`, workspace).Scan(&privateID, &privateAudience, &hasDocument); err != nil || privateAudience != 1 || hasDocument != 1 {
		t.Fatalf("real private document precondition: %d %d %v", privateAudience, hasDocument, err)
	}
	pagesSeedUser(t, h, workspace, "folder-owner", "folder-owner@example.invalid", "OWNER")
	pagesSeedUser(t, h, workspace, "folder-subscriber", "folder-subscriber@example.invalid", "MEMBER")
	pagesSeedUser(t, h, workspace, "erin", "folder-admin@example.invalid", "ADMIN")
	ordinaryID := pagesCreateWith(t, h, workspace, "folder-owner", pagesReachBody("ordinary-folder-page", "principal-crew"))
	f := &foldersFixture{h: h, wsID: workspace, owner: "folder-owner"}
	f.createFolder(t, "first-folder", "First folder", "crew/principal-crew")
	f.createFolder(t, "second-folder", "Second folder", "crew/principal-crew")
	if rr := pagesGetStatus(t, h, workspace, "folder-subscriber", "MEMBER", "created-doc"); rr.Code != 404 {
		t.Fatalf("subscriber must not reach private document: %d", rr.Code)
	}
	hub := newTestHubForStream(t)
	hub.SetChannelAuthorizer(ws.NewDBChannelAuthorizer(h.db))
	channel := "workspace:" + workspace
	if allowed, err := hub.CanSubscribeChannel(t.Context(), "folder-subscriber", channel); err != nil || !allowed {
		t.Fatalf("native workspace subscription: %v %v", allowed, err)
	}
	// Observer receives exactly the native hub's socket frames, after the real
	// DB channel authorization above; this does not test a WebSocket handshake.
	observer := hub.AddObserver(channel, "folder-subscriber", 16)
	t.Cleanup(func() { hub.RemoveObserver(channel, observer) })
	h.hub = hub
	writer := journal.NewWriter(h.db, newTestLogger(), journal.WriterOptions{ChainKey: []byte("synthetic-folder-realtime-key"), FlushInterval: time.Hour})
	t.Cleanup(func() {
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
	})
	h.SetJournal(folderRealtimeJournal{writer})
	journalHandler := NewJournalHandler(h.db, newTestLogger(), writer)
	replay := func(t *testing.T) string {
		t.Helper()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		request := httptest.NewRequestWithContext(ctx, "GET", "/api/v1/journal/stream?entry_type=page.folder_membership_changed", nil)
		request = withWorkspaceUser(request, "folder-subscriber", workspace, "MEMBER")
		recorder := httptest.NewRecorder()
		journalHandler.Stream(&authorityStreamWriter{ResponseRecorder: recorder, afterFlush: cancel}, request)
		if recorder.Code != 200 || !strings.Contains(recorder.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(recorder.Body.String(), "page.folder_membership_changed") {
			t.Fatalf("actual SSE replay unavailable: %d %s", recorder.Code, recorder.Body.String())
		}
		return recorder.Body.String()
	}
	frame := func(t *testing.T) string {
		t.Helper()
		select {
		case data := <-observer.Frames():
			var event ws.ServerMessage
			if err := json.Unmarshal(data, &event); err != nil || event.Type != "page.updated" || event.Channel != channel {
				t.Fatalf("unexpected native frame: %s %v", data, err)
			}
			if observer.Dropped() {
				t.Fatal("native frame dropped")
			}
			return string(data)
		case <-time.After(time.Second):
			t.Fatal("page.updated invalidation missing")
			return ""
		}
	}
	t.Run("ordinary-page-calibration", func(t *testing.T) {
		rr := f.call(t, "POST", "/api/v1/page-folders/first-folder/pages", "folder-owner", "OWNER", f.moveBody(t, "first-folder", "ordinary-folder-page"))
		if rr.Code != 200 {
			t.Fatalf("ordinary move: %d %s", rr.Code, rr.Body.String())
		}
		if data := frame(t); !strings.Contains(data, ordinaryID) || !strings.Contains(data, "ordinary-folder-page") {
			t.Fatalf("ordinary invalidation metadata lost: %s", data)
		}
		if data := replay(t); !strings.Contains(data, ordinaryID) || !strings.Contains(data, "ordinary-folder-page") {
			t.Fatal("ordinary journal calibration missing")
		}
	})
	for _, operation := range []string{"add", "move", "remove", "batch"} {
		t.Run(operation, func(t *testing.T) {
			method, path, body := "POST", "/api/v1/page-folders/first-folder/pages", f.moveBody(t, "first-folder", "created-doc")
			switch operation {
			case "move":
				path = "/api/v1/page-folders/second-folder/pages"
				body = f.moveBody(t, "second-folder", "created-doc")
			case "remove":
				method = "DELETE"
				path = "/api/v1/page-folders/second-folder/pages/created-doc"
				body = fmt.Sprintf(`{"pages_version":%d}`, f.pagesVersion(t, "created-doc"))
			case "batch":
				path = "/api/v1/page-folders/first-folder/pages:batch"
				body = fmt.Sprintf(`{"pages":[{"page":"created-doc","pages_version":%d}],"acl_version":%d}`, f.pagesVersion(t, "created-doc"), f.aclVersion(t, "first-folder"))
			}
			rr := f.call(t, method, path, "folder-owner", "OWNER", body)
			if rr.Code != 200 {
				t.Fatalf("authorized %s: %d %s", operation, rr.Code, rr.Body.String())
			}
			for _, wire := range []struct{ name, raw string }{{"workspace-hub", frame(t)}, {"journal-SSE", replay(t)}} {
				for _, forbidden := range []string{privateID, "created-doc", "Created report"} {
					if strings.Contains(wire.raw, forbidden) {
						t.Errorf("%s disclosed private Page metadata %q after %s", wire.name, forbidden, operation)
					}
				}
			}
			var canonicalCount int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='page.folder_membership_changed' AND json_valid(payload) AND json_extract(payload,'$.page_id')=? AND json_extract(payload,'$.page')='created-doc' AND json_type(payload,'$.private_audience')='true'`, privateID).Scan(&canonicalCount); err != nil || canonicalCount == 0 {
				t.Errorf("canonical audit identity/classification lost: %d %v", canonicalCount, err)
			}
			if rr := pagesGetStatus(t, h, workspace, "folder-subscriber", "MEMBER", "created-doc"); rr.Code != 404 {
				t.Errorf("folder operation widened private audience: %d", rr.Code)
			}
		})
	}

	// Persist old wire shapes through the real configured-key writer. These are
	// historical-format storage fixtures, not new admission/provenance evidence.
	for _, tc := range []struct {
		name, id, slug string
		badMarker      bool
	}{
		{"historical-ordinary-GET", ordinaryID, "ordinary-folder-page", false},
		{"historical-private-GET", privateID, "created-doc", false},
		{"malformed-marker-GET", privateID, "created-doc", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{"page_id": tc.id, "page": tc.slug, "op": "moved", "from": "first-folder", "to": "second-folder"}
			if tc.badMarker {
				payload["private_audience"] = "false"
				payload["private_output_metadata"] = false
			}
			id, err := writer.EmitSync(t.Context(), journal.Entry{WorkspaceID: workspace, Type: "page.folder_membership_changed", ActorType: journal.ActorUser, ActorID: "folder-owner", Summary: "Moved page " + tc.slug, Payload: payload, Refs: map[string]any{"page_id": tc.id}})
			if err != nil {
				t.Fatal(err)
			}
			var beforeSummary, beforePayload, beforeRefs string
			if err := h.db.QueryRow(`SELECT summary,payload,refs FROM journal_entries WHERE id=?`, id).Scan(&beforeSummary, &beforePayload, &beforeRefs); err != nil {
				t.Fatal(err)
			}
			request := pagesRequest(t, "GET", "/api/v1/journal/"+id, workspace, "folder-subscriber", "MEMBER", "")
			request.SetPathValue("id", id)
			rr := httptest.NewRecorder()
			journalHandler.Get(rr, request)
			if rr.Code != 200 {
				t.Fatalf("native historical GET: %d %s", rr.Code, rr.Body.String())
			}
			for _, forbidden := range []string{tc.id, tc.slug, "private_audience", "private_output_metadata"} {
				if strings.Contains(rr.Body.String(), forbidden) {
					t.Errorf("historical GET disclosed %q", forbidden)
				}
			}
			var summary, payloadRaw, refs string
			if err := h.db.QueryRow(`SELECT summary,payload,refs FROM journal_entries WHERE id=?`, id).Scan(&summary, &payloadRaw, &refs); err != nil || summary != beforeSummary || payloadRaw != beforePayload || refs != beforeRefs {
				t.Errorf("GET modified canonical history: %v", err)
			}
		})
	}
	t.Run("public-FTS-list-and-count", func(t *testing.T) {
		for _, endpoint := range []string{"/api/v1/journal", "/api/v1/journal/count"} {
			for _, tc := range []struct {
				query    string
				wantZero bool
			}{{"ordinary-folder-page", false}, {"created-doc", true}} {
				r := pagesRequest(t, "GET", endpoint+"?q="+tc.query+"&entry_type=page.folder_membership_changed", workspace, "folder-subscriber", "MEMBER", "")
				rr := httptest.NewRecorder()
				if strings.HasSuffix(endpoint, "/count") {
					journalHandler.Count(rr, r)
				} else {
					journalHandler.List(rr, r)
				}
				var body struct {
					Count *int `json:"count"`
					Total *int `json:"total"`
				}
				if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &body) != nil {
					t.Fatalf("native FTS response %s: %d %s", endpoint, rr.Code, rr.Body.String())
				}
				count := body.Count
				if strings.HasSuffix(endpoint, "/count") {
					count = body.Total
				}
				if count == nil {
					t.Fatal("native response missing expected count/total field")
				}
				if tc.wantZero && *count != 0 {
					t.Errorf("%s confirmed private Page slug via FTS count=%d", endpoint, *count)
				}
				if !tc.wantZero && *count < 1 {
					t.Errorf("%s ordinary native FTS calibration missing", endpoint)
				}
			}
		}
	})
	t.Run("malformed-historical-FTS", func(t *testing.T) {
		execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,entry_type,actor_type,summary,payload) VALUES('malformed-folder-history',?,'page.folder_membership_changed','user','malformed-private-canary','{not-json')`, workspace)
		for _, endpoint := range []string{"/api/v1/journal", "/api/v1/journal/count"} {
			request := pagesRequest(t, "GET", endpoint+"?q=malformed-private-canary", workspace, "folder-subscriber", "MEMBER", "")
			rr := httptest.NewRecorder()
			if strings.HasSuffix(endpoint, "/count") {
				journalHandler.Count(rr, request)
			} else {
				journalHandler.List(rr, request)
			}
			var result struct {
				Count int `json:"count"`
				Total int `json:"total"`
			}
			if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &result) != nil || result.Count != 0 || result.Total != 0 {
				t.Errorf("malformed history must be unavailable, not SQLerror: %d %s", rr.Code, rr.Body.String())
			}
		}
	})
	t.Run("preview-samples-and-rejection", func(t *testing.T) {
		automationHandler := NewAutomationHandler(h.db, newTestLogger())
		for _, matcher := range []string{`{}`, `{"payload_equals":{"page":"never-matches-canary"}}`, `{"payload_equals":{"page":"ordinary-folder-page"}}`} {
			request := pagesRequest(t, "POST", "/api/v1/automations/preview", workspace, "folder-subscriber", "MEMBER", `{"event_type":"page.folder_membership_changed","matcher":`+matcher+`}`)
			rr := httptest.NewRecorder()
			automationHandler.PreviewMatch(rr, request)
			if rr.Code != 200 {
				t.Fatalf("native preview: %d %s", rr.Code, rr.Body.String())
			}
			if strings.Contains(matcher, "ordinary-folder-page") && (!strings.Contains(rr.Body.String(), ordinaryID) || !strings.Contains(rr.Body.String(), "ordinary-folder-page")) {
				t.Error("preview lost explicit ordinary metadata through projection")
			}
			for _, forbidden := range []string{privateID, "created-doc", "Created report"} {
				if strings.Contains(rr.Body.String(), forbidden) {
					t.Errorf("preview disclosed private Page metadata %q for matcher %s", forbidden, matcher)
				}
			}
		}
	})
}

func TestFolderMembershipClassificationUnavailable(t *testing.T) {
	for _, state := range []string{"SQL-failure", "missing-page", "empty-human-staging", "late-document-attachment"} {
		t.Run(state, func(t *testing.T) {
			f := newFoldersFixture(t)
			record, err := f.h.loadPage(t.Context(), f.wsID, "fleet-201")
			if err != nil {
				t.Fatal(err)
			}
			hub := newTestHubForStream(t)
			f.h.hub = hub
			observer := hub.AddObserver("workspace:"+f.wsID, "frank", 2)
			t.Cleanup(func() { hub.RemoveObserver("workspace:"+f.wsID, observer) })
			switch state {
			case "SQL-failure":
				if err := f.h.db.Close(); err != nil {
					t.Fatal(err)
				}
			case "missing-page":
				execOrFatal(t, f.h.db, `DELETE FROM pages WHERE id=?`, record.ID)
			case "empty-human-staging":
				execOrFatal(t, f.h.db, `DELETE FROM page_panels WHERE page_id=?`, record.ID)
			case "late-document-attachment":
				// Native storage fixture for an attachment after the Page was read;
				// it supplies no run authority and does not claim an authorized init flow.
				execOrFatal(t, f.h.db, `DELETE FROM page_panels WHERE page_id=?`, record.ID)
				execOrFatal(t, f.h.db, `INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES(?,?,3,X'01',CAST('{}' AS BLOB))`, f.wsID, record.ID)
			}
			f.h.publishFolderMembership(t.Context(), f.wsID, &AuthUser{ID: f.owner}, record, "moved", "one", "two")
			var data []byte
			select {
			case data = <-observer.Frames():
			case <-time.After(time.Second):
				t.Fatal("invalidation missing")
			}
			if strings.Contains(string(data), record.Slug) {
				t.Error("unavailable/staging classification disclosed slug")
			}
			if strings.Contains(string(data), record.ID) != (state == "empty-human-staging" || state == "late-document-attachment") {
				t.Error("unknown identity must be hidden; staging opaque ID retained")
			}
			canonical := f.spy.entries[len(f.spy.entries)-1]
			if canonical.Payload["page_id"] != record.ID || canonical.Payload["page"] != record.Slug {
				t.Error("canonical audit target lost")
			}
			projected, _ := json.Marshal(journal.SerializeEntry(canonical))
			if strings.Contains(string(projected), record.Slug) || strings.Contains(string(projected), record.ID) != (state == "empty-human-staging" || state == "late-document-attachment") {
				t.Error("journal classification diverged from invalidation")
			}
		})
	}
}
