//go:build linux && (slides_workflow_browser || conformance)

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/slides"
)

// Uses owned synthetic sessions and a real migrated Router/database/static UI.
// No API response mocks, borrowed credentials, login or model execution.
func TestSlidesWorkflowBrowser(t *testing.T) {
	if os.Getenv("CREWSHIP_SLIDES_WORKFLOW_BROWSER") != "1" {
		t.Fatal("tagged test requires CREWSHIP_SLIDES_WORKFLOW_BROWSER=1, production export and Chromium")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(repo, "out", "index.html")); err != nil {
		t.Fatal("build real static frontend first", err)
	}
	f := newInstanceFixture(t)
	t.Cleanup(f.r.Shutdown)
	seedInstanceUser(t, f.db, "slides-reader", "slides-reader@example.invalid", "ws-old", "VIEWER")
	validator, err := auth.NewJWTValidator("this-is-a-32-char-test-secret-pad")
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, user := range []string{"boss", "slides-reader"} {
		session, err := sessions.NewDBStore(f.db).Create(t.Context(), user, "synthetic-slides-browser", "127.0.0.1", auth.RefreshTokenTTL)
		if err != nil {
			t.Fatal(err)
		}
		tokens[user], err = validator.IssueAccessToken(user, session.ID, user, user+"@example.invalid")
		if err != nil {
			t.Fatal(err)
		}
	}
	wmSeedPage(t, f.db, "ws-old", "readonly-slides-page", "readonly-slides", "Read only Slides", "boss", "")
	deck := slideTestDeck(t, "readonly-slides-page")
	wantCode(t, f.slideDo(f.boss, "POST", "/api/v1/pages/readonly-slides/slides/initialize", slideJSON(t, slides.InitializeRequest{PageID: deck.DeckID, SchemaVersion: 1, MutationID: "readonly-init", Deck: deck})), 200, "initialize readonly fixture")
	execOrFatal(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('readonly-slides-page','user','slides-reader','read','boss')`)
	mux := http.NewServeMux()
	mux.Handle("/api/", f.r)
	mux.Handle("/", StaticFileHandler(os.DirFS(filepath.Join(repo, "out"))))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	fixture, err := json.Marshal(map[string]any{"server": server.URL, "workspace": "ws-old", "owner_token": tokens["boss"], "reader_token": tokens["slides-reader"]})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "slides-browser.json")
	if err = os.WriteFile(path, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "node", filepath.Join(repo, "scripts/acceptance-slides-workflow-browser.mjs"))
	command.Env = append(os.Environ(), "CREWSHIP_SLIDES_WORKFLOW_FIXTURE="+path)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err = command.Run(); err != nil {
		t.Fatal("native durable Slides browser", err)
	}
	for _, slug := range []string{"native-desktop", "native-touch"} {
		var revisions, receipts int
		if err = f.db.QueryRow(`SELECT (SELECT count(*) FROM slides_revisions WHERE page_id=p.id),(SELECT count(*) FROM slides_receipts WHERE page_id=p.id) FROM pages p WHERE p.workspace_id='ws-old' AND p.slug=?`, slug).Scan(&revisions, &receipts); err != nil {
			t.Fatal(err)
		}
		if revisions < 6 || revisions != receipts {
			t.Fatalf("durable browser receipts/history mismatch %s: revisions=%d receipts=%d", slug, revisions, receipts)
		}
	}
}
