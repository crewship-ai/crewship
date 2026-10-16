//go:build linux && (slides_comments_browser || conformance)

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

// Separate owned identities exercise live COMMENT versus owner WRITE authority.
func TestSlidesCommentsBrowser(t *testing.T) {
	if os.Getenv("CREWSHIP_SLIDES_COMMENTS_BROWSER") != "1" {
		t.Fatal("requires explicit browser opt-in, production export and Chromium")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(repo, "out", "index.html")); err != nil {
		t.Fatal("build production export first", err)
	}
	f := newInstanceFixture(t)
	t.Cleanup(f.r.Shutdown)
	seedInstanceUser(t, f.db, "slide-commenter", "commenter@example.invalid", "ws-old", "MEMBER")
	validator, err := auth.NewJWTValidator("this-is-a-32-char-test-secret-pad")
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, user := range []string{"boss", "slide-commenter"} {
		session, e := sessions.NewDBStore(f.db).Create(t.Context(), user, "synthetic-comments-browser", "127.0.0.1", auth.RefreshTokenTTL)
		if e != nil {
			t.Fatal(e)
		}
		tokens[user], e = validator.IssueAccessToken(user, session.ID, user, user+"@example.invalid")
		if e != nil {
			t.Fatal(e)
		}
	}
	wmSeedPage(t, f.db, "ws-old", "comments-native-page", "comments-native", "Native comments", "boss", "")
	deck := slideTestDeck(t, "comments-native-page")
	wantCode(t, f.slideDo(f.boss, "POST", "/api/v1/pages/comments-native/slides/initialize", slideJSON(t, slides.InitializeRequest{PageID: deck.DeckID, SchemaVersion: 1, MutationID: "comments-init", Deck: deck})), 200, "initialize")
	execOrFatal(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('comments-native-page','user','slide-commenter','comment','boss')`)
	// More than one default UI page proves actual opaque cursor continuation.
	for i := 0; i < 26; i++ {
		id := "seed_" + time.Unix(int64(i), 0).UTC().Format("150405")
		wantCode(t, f.slideDo(f.boss, "POST", "/api/v1/pages/comments-native/slides/comments", slideJSON(t, slides.CreateCommentRequest{PageID: deck.DeckID, SchemaVersion: 1, BaseRev: 1, MutationID: id, Anchor: slides.CommentAnchor{AnchorSchemaVersion: 1, Kind: "slide", SlideID: deck.SlideOrder[0], Revision: 1}, Body: "Seed thread " + id})), 200, "seed pagination")
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", f.r)
	mux.Handle("/", StaticFileHandler(os.DirFS(filepath.Join(repo, "out"))))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	raw, err := json.Marshal(map[string]any{"server": server.URL, "workspace": "ws-old", "owner_token": tokens["boss"], "commenter_token": tokens["slide-commenter"]})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "comments-browser.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join(repo, "scripts/acceptance-slides-comments-browser.mjs"))
	cmd.Env = append(os.Environ(), "CREWSHIP_SLIDES_COMMENTS_FIXTURE="+path)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err = cmd.Run(); err != nil {
		t.Fatal("actual native comments browser", err)
	}
}
