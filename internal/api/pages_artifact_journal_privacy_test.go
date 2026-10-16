package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/ws"
	"golang.org/x/net/websocket"
)

func TestArtifactPublicationJournalKeepsPrivateSourceOutOfMemberRoutes(t *testing.T) {
	h, _, _, workspace, owner := newPagesFixture(t)
	h.artifactStoragePath = storageDir(t)
	const member = "journal-ungranted-member"
	const slug = "private-artifact-journal-canary"
	const title = "Private artifact title canary"
	const sourceOne = "Private original source canary"
	const sourceTwo = "Private replacement source canary"
	pagesSeedUser(t, h, workspace, member, member+"@example.invalid", "MEMBER")
	const secret = "synthetic-artifact-journal-session-secret-long-enough"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, user := range []string{owner, member} {
		session, err := sessions.NewDBStore(h.db).Create(t.Context(), user, "synthetic-journal", "127.0.0.1", auth.RefreshTokenTTL)
		if err != nil {
			t.Fatal(err)
		}
		tokens[user], err = validator.IssueAccessToken(user, session.ID, user, user+"@example.invalid")
		if err != nil {
			t.Fatal(err)
		}
	}
	writer := journal.NewWriter(h.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	hub := ws.NewHub(newTestLogger(), nil, validator, sessions.NewDBStore(h.db))
	hub.SetChannelAuthorizer(ws.NewDBChannelAuthorizer(h.db))
	hubContext, stopHub := context.WithCancel(t.Context())
	hubDone := make(chan struct{})
	go func() { defer close(hubDone); hub.Run(hubContext) }()
	t.Cleanup(func() { stopHub(); <-hubDone })
	router, err := NewRouter(h.db, secret, newTestLogger(), WithJournal(writer), WithStoragePath(h.artifactStoragePath), WithHub(hub))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	mux := http.NewServeMux()
	mux.Handle("/api/", router)
	mux.HandleFunc("/ws", hub.HandleUpgrade)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	request := func(ctx context.Context, user, method, path, body string) *http.Response {
		t.Helper()
		separator := "?"
		if strings.Contains(path, "?") {
			separator = "&"
		}
		r, err := http.NewRequestWithContext(ctx, method, server.URL+path+separator+"workspace_id="+url.QueryEscape(workspace), strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.AddCookie(&http.Cookie{Name: "authjs.session-token", Value: tokens[user]})
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", server.URL)
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	read := func(response *http.Response, status int) []byte {
		t.Helper()
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != status {
			t.Fatalf("HTTP status=%d want=%d body=%s", response.StatusCode, status, raw)
		}
		return raw
	}
	// The ungranted member uses a real session-backed WS ticket and the
	// production workspace channel authorizer, including the subscription gate.
	var ticket struct {
		Token string `json:"token"`
	}
	if err = json.Unmarshal(read(request(t.Context(), member, "GET", "/api/v1/ws-token", ""), 200), &ticket); err != nil {
		t.Fatal(err)
	}
	socket, err := websocket.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", "", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = socket.Close() })
	if err = websocket.JSON.Send(socket, map[string]string{"type": "auth", "token": ticket.Token}); err != nil {
		t.Fatal(err)
	}
	channel := "workspace:" + workspace
	if err = websocket.JSON.Send(socket, ws.ClientMessage{Type: "subscribe", Channel: channel}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !hub.IsUserSubscribed(channel, member) {
		if time.Now().After(deadline) {
			t.Fatal("authenticated member could not subscribe to workspace")
		}
		time.Sleep(time.Millisecond)
	}
	assertInvalidation := func(event, id string, legacySlug ...string) {
		t.Helper()
		if err := socket.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		var raw []byte
		if err := websocket.Message.Receive(socket, &raw); err != nil {
			t.Fatal(err)
		}
		var frame struct {
			Type    string         `json:"type"`
			Channel string         `json:"channel"`
			Payload map[string]any `json:"payload"`
		}
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatal(err)
		}
		wantFields := 1
		if len(legacySlug) > 0 {
			wantFields = 2
			if frame.Payload["slug"] != legacySlug[0] {
				t.Errorf("legacy Page invalidation changed: %s", raw)
			}
		}
		if frame.Type != event || frame.Channel != channel || frame.Payload["page_id"] != id || len(frame.Payload) != wantFields {
			t.Errorf("workspace invalidation must expose only opaque Page identity: %s", raw)
		}
	}
	created := read(request(t.Context(), owner, "POST", "/api/v1/pages", `{"slug":"`+slug+`","name":"`+title+`","panels":[],"allow_empty":true}`), 201)
	var page struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(created, &page); err != nil || page.ID == "" {
		t.Fatalf("created Page identity: %s %v", created, err)
	}
	pageID := page.ID
	assertInvalidation("page.updated", pageID)
	read(request(t.Context(), member, "GET", "/api/v1/pages/"+slug, ""), 404)
	var creationFingerprint string
	if err = h.db.QueryRow(`SELECT spec_json FROM pages WHERE id=?`, pageID).Scan(&creationFingerprint); err != nil {
		t.Fatal(err)
	}
	// Neither the human-derived slug nor the arrangement fingerprint belongs
	// to an audit readable by workspace members without Page access.
	var doc pages.Document
	if err = json.Unmarshal([]byte(creationFingerprint), &doc); err != nil {
		t.Fatal(err)
	}
	creationFingerprint = pageArrangementFingerprint(&doc)
	// Metadata edits also stay opaque before the first revision initializes it.
	read(request(t.Context(), owner, "PATCH", "/api/v1/pages/"+slug, `{"name":"Private staging title canary"}`), 200)
	assertInvalidation("page.updated", pageID)
	read(request(t.Context(), owner, "PATCH", "/api/v1/pages/"+slug, `{"color":"amber"}`), 200)
	assertInvalidation("page.updated", pageID)
	artifactPath := "/api/v1/pages/" + slug + "/artifact"
	for revision, source := range []string{sourceOne, sourceTwo} {
		body, _ := json.Marshal(map[string]any{"html": "<h1>" + source + "</h1>", "base_rev": revision, "mutation_id": fmt.Sprint("publish-", revision)})
		read(request(t.Context(), owner, "POST", artifactPath+"/revisions", string(body)), 201)
		assertInvalidation("page.updated", pageID)
	}
	read(request(t.Context(), owner, "POST", artifactPath+"/restore", `{"revision":1,"base_rev":2,"mutation_id":"restore-original"}`), 201)
	assertInvalidation("page.updated", pageID)
	read(request(t.Context(), owner, "PATCH", "/api/v1/pages/"+slug, `{"name":"Private published title canary"}`), 200)
	assertInvalidation("page.updated", pageID)
	read(request(t.Context(), owner, "PATCH", "/api/v1/pages/"+slug, `{"color":"cyan"}`), 200)
	assertInvalidation("page.updated", pageID)
	if err = writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	// This is an actual authenticated workspace MEMBER with no direct or
	// inherited Page access, not a handler context that bypasses middleware.
	read(request(t.Context(), member, "GET", artifactPath, ""), 404)
	var grants int
	if err = h.db.QueryRow(`SELECT COUNT(*) FROM page_grants WHERE page_id=?`, pageID).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("fixture unexpectedly granted private Page access: %d %v", grants, err)
	}
	var firstSHA, secondSHA, restoredSHA string
	if err = h.db.QueryRow(`SELECT (SELECT sha256 FROM page_artifact_revisions WHERE page_id=? AND revision=1),(SELECT sha256 FROM page_artifact_revisions WHERE page_id=? AND revision=2),(SELECT sha256 FROM page_artifact_revisions WHERE page_id=? AND revision=3)`, pageID, pageID, pageID).Scan(&firstSHA, &secondSHA, &restoredSHA); err != nil || len(firstSHA) != 64 || firstSHA == secondSHA || firstSHA != restoredSHA {
		t.Fatalf("canonical private source audit changed: first=%s second=%s restored=%s err=%v", firstSHA, secondSHA, restoredSHA, err)
	}
	ownerSource := read(request(t.Context(), owner, "GET", artifactPath+"?revision=3", ""), 200)
	if !strings.Contains(string(ownerSource), sourceOne) {
		t.Fatal("authorized restore did not retain the original private source")
	}
	type audit struct {
		ID      string         `json:"id"`
		Summary string         `json:"summary"`
		Payload map[string]any `json:"payload"`
	}
	assertOpaque := func(raw []byte, entry audit) {
		t.Helper()
		for _, private := range []string{slug, title, sourceOne, sourceTwo, firstSHA, secondSHA, creationFingerprint} {
			if strings.Contains(string(raw), private) {
				t.Errorf("workspace journal exposed private Artifact metadata/content: %q", private)
			}
		}
		if entry.Payload["created"] == true {
			if entry.Summary != "Page created" || entry.Payload["page_id"] != pageID || len(entry.Payload) != 2 {
				t.Errorf("staging create audit must be opaque: %+v", entry)
			}
			return
		}
		revision, ok := entry.Payload["artifact_revision"].(float64)
		if !ok || revision < 1 || revision > 3 || entry.Payload["page_id"] != pageID || entry.Payload["on_behalf_of_user_id"] != owner || entry.Summary != fmt.Sprintf("Artifact revision %d saved", int(revision)) {
			t.Errorf("opaque publication audit lost identity/revision/actor: %+v", entry)
		}
		if revision == 3 && entry.Payload["restored_from"] != float64(1) {
			t.Errorf("restored revision lost its audit pointer: %+v", entry.Payload)
		}
		allowed := map[string]bool{"page_id": true, "artifact_revision": true, "restored_from": true, "on_behalf_of_user_id": true, "agent_id": true, "run_id": true, "request_id": true}
		for key := range entry.Payload {
			if !allowed[key] {
				t.Errorf("workspace publication audit contains unapproved field %q", key)
			}
		}
	}
	list := read(request(t.Context(), member, "GET", "/api/v1/journal?entry_type=page.spec.changed", ""), 200)
	var response struct {
		Entries []audit `json:"entries"`
	}
	if err = json.Unmarshal(list, &response); err != nil || len(response.Entries) != 4 {
		t.Fatalf("member did not read the four real creation/publication events: count=%d err=%v body=%s", len(response.Entries), err, list)
	}
	for _, entry := range response.Entries {
		assertOpaque(list, entry)
		get := read(request(t.Context(), member, "GET", "/api/v1/journal/"+entry.ID, ""), 200)
		var individual audit
		if err = json.Unmarshal(get, &individual); err != nil {
			t.Fatal(err)
		}
		assertOpaque(get, individual)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stream := request(ctx, member, "GET", "/api/v1/journal/stream?entry_type=page.spec.changed", "")
	defer stream.Body.Close()
	if stream.StatusCode != 200 {
		t.Fatalf("member journal stream status=%d", stream.StatusCode)
	}
	scanner := bufio.NewScanner(stream.Body)
	seen := 0
	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "data: ") {
			continue
		}
		raw := []byte(strings.TrimPrefix(scanner.Text(), "data: "))
		var entry audit
		if err = json.Unmarshal(raw, &entry); err != nil {
			t.Fatal(err)
		}
		assertOpaque(raw, entry)
		seen++
		if seen == 4 {
			break
		}
	}
	if seen != 4 {
		t.Fatalf("member stream omitted publication audits: seen=%d err=%v", seen, scanner.Err())
	}
	cancel()
	read(request(t.Context(), owner, "DELETE", "/api/v1/pages/"+slug, ""), 204)
	assertInvalidation("page.deleted", pageID)
	// Board product APIs remain absent in the foundation. Seed only its durable
	// marker to verify the shared metadata handler's HasBoard branch as well.
	const boardSlug = "private-board-journal-canary"
	created = read(request(t.Context(), owner, "POST", "/api/v1/pages", `{"slug":"`+boardSlug+`","name":"Private Board title","panels":[],"allow_empty":true}`), 201)
	if err = json.Unmarshal(created, &page); err != nil {
		t.Fatal(err)
	}
	assertInvalidation("page.updated", page.ID)
	execOrFatal(t, h.db, `INSERT INTO page_boards(page_id,seq,format) VALUES(?,0,'crewship-board-excalidraw-0.18.1-v1')`, page.ID)
	read(request(t.Context(), member, "GET", "/api/v1/pages/"+boardSlug, ""), 404)
	for _, body := range []string{`{"name":"Private Board metadata"}`, `{"color":"amber"}`} {
		read(request(t.Context(), owner, "PATCH", "/api/v1/pages/"+boardSlug, body), 200)
		assertInvalidation("page.updated", page.ID)
	}
	read(request(t.Context(), owner, "DELETE", "/api/v1/pages/"+boardSlug, ""), 204)
	assertInvalidation("page.deleted", page.ID)
	// An uninitialized output shell must also remain opaque on deletion.
	created = read(request(t.Context(), owner, "POST", "/api/v1/pages", `{"slug":"private-empty-shell","name":"Private empty shell","panels":[],"allow_empty":true}`), 201)
	if err = json.Unmarshal(created, &page); err != nil {
		t.Fatal(err)
	}
	assertInvalidation("page.updated", page.ID)
	read(request(t.Context(), owner, "DELETE", "/api/v1/pages/private-empty-shell", ""), 204)
	assertInvalidation("page.deleted", page.ID)

	// Existing panel Page events deliberately retain their established contract.
	const legacySlug = "legacy-panel-event"
	created = read(request(t.Context(), owner, "POST", "/api/v1/pages", pagesSpecBody(legacySlug)), 201)
	if err = json.Unmarshal(created, &page); err != nil {
		t.Fatal(err)
	}
	assertInvalidation("page.updated", page.ID, legacySlug)
	if err = writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	list = read(request(t.Context(), member, "GET", "/api/v1/journal?entry_type=page.spec.changed", ""), 200)
	if err = json.Unmarshal(list, &response); err != nil {
		t.Fatal(err)
	}
	foundLegacy := false
	for _, entry := range response.Entries {
		if entry.Payload["page_id"] != page.ID {
			continue
		}
		foundLegacy = true
		panels, ok := entry.Payload["panels"].([]any)
		if entry.Payload["page"] != legacySlug || entry.Payload["created"] != true || !strings.Contains(entry.Summary, legacySlug) || entry.Payload["fingerprint"] == "" || !ok || len(panels) != 1 || panels[0] != "sluzby" {
			t.Errorf("legacy Page arrangement audit changed: %+v", entry)
		}
	}
	if !foundLegacy {
		t.Fatal("legacy Page arrangement event missing")
	}
	for _, body := range []string{`{"name":"Renamed legacy panel Page"}`, `{"color":"amber"}`} {
		read(request(t.Context(), owner, "PATCH", "/api/v1/pages/"+legacySlug, body), 200)
		assertInvalidation("page.updated", page.ID, legacySlug)
	}
	read(request(t.Context(), owner, "DELETE", "/api/v1/pages/"+legacySlug, ""), 204)
	assertInvalidation("page.deleted", page.ID, legacySlug)
}
