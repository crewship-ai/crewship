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
)

func TestArtifactPublicationJournalKeepsPrivateSourceOutOfMemberRoutes(t *testing.T) {
	h, _, _, workspace, owner, pageID := artifactFixture(t)
	const member = "journal-ungranted-member"
	const slug = "private-artifact-journal-canary"
	const title = "Private artifact title canary"
	const sourceOne = "Private original source canary"
	const sourceTwo = "Private replacement source canary"
	pagesSeedUser(t, h, workspace, member, member+"@example.invalid", "MEMBER")
	execOrFatal(t, h.db, `UPDATE pages SET slug=?,name=? WHERE id=?`, slug, title, pageID)
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
	router, err := NewRouter(h.db, secret, newTestLogger(), WithJournal(writer), WithStoragePath(h.artifactStoragePath))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	server := httptest.NewServer(router)
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
	artifactPath := "/api/v1/pages/" + slug + "/artifact"
	for revision, source := range []string{sourceOne, sourceTwo} {
		body, _ := json.Marshal(map[string]any{"html": "<h1>" + source + "</h1>", "base_rev": revision, "mutation_id": fmt.Sprint("publish-", revision)})
		read(request(t.Context(), owner, "POST", artifactPath+"/revisions", string(body)), 201)
	}
	read(request(t.Context(), owner, "POST", artifactPath+"/restore", `{"revision":1,"base_rev":2,"mutation_id":"restore-original"}`), 201)
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
		for _, private := range []string{slug, title, sourceOne, sourceTwo, firstSHA, secondSHA} {
			if strings.Contains(string(raw), private) {
				t.Errorf("workspace journal exposed private Artifact metadata/content: %q", private)
			}
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
	if err = json.Unmarshal(list, &response); err != nil || len(response.Entries) != 3 {
		t.Fatalf("member did not read the three real publication events: count=%d err=%v body=%s", len(response.Entries), err, list)
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
		if seen == 3 {
			break
		}
	}
	if seen != 3 {
		t.Fatalf("member stream omitted publication audits: seen=%d err=%v", seen, scanner.Err())
	}
	cancel()
}
