//go:build linux && (slides_selected_browser || conformance)

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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
	"github.com/crewship-ai/crewship/internal/ws"
)

// Actual human UI/Router/SQLite and production worker; deterministic trusted
// Store replacement substitutes only for model dispatch. It is not a model or
// sidecar qualification (the latter has its own real transport test).
func TestSlidesSelectedBrowser(t *testing.T) {
	if os.Getenv("CREWSHIP_SLIDES_SELECTED_BROWSER") != "1" {
		t.Fatal("requires explicit opt-in, production export and Chromium")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(repo, "out", "index.html")); err != nil {
		t.Fatal("build production export first", err)
	}
	f, _, _ := selectedSlidesHTTPFixture(t)
	seedInstanceUser(t, f.db, "selected-reader", "selected-reader@example.invalid", "ws-old", "VIEWER")
	mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('selected-page','user','selected-reader','read','boss')`)
	validator, err := auth.NewJWTValidator("this-is-a-32-char-test-secret-pad")
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, user := range []string{"boss", "selected-reader"} {
		session, e := sessions.NewDBStore(f.db).Create(t.Context(), user, "synthetic-selected-browser", "127.0.0.1", auth.RefreshTokenTTL)
		if e != nil {
			t.Fatal(e)
		}
		tokens[user], e = validator.IssueAccessToken(user, session.ID, user, user+"@example.invalid")
		if e != nil {
			t.Fatal(e)
		}
	}
	key := slides.Key{WorkspaceID: "ws-old", PageID: "selected-page"}
	store := f.r.pages.newSlidesStore()
	f.r.pages.artifactDispatch = func(ctx context.Context, _, _, _ string, opts ws.ChatMessageOption) error {
		id := opts.SlidesRequestID
		run := "browser_run_" + id
		live := func(ctx context.Context, tx *sql.Tx) (slides.SelectedAuthorityState, error) {
			record, e := slides.ReadSelectedAuthorityTx(ctx, tx, key, id)
			if e != nil {
				return record, e
			}
			_, e = (&slideHTTP{pages: f.r.pages}).selectedAdmissionLive(ctx, tx, key, record.Admission)
			return record, e
		}
		human := func(ctx context.Context, tx *sql.Tx) (string, error) {
			record, e := live(ctx, tx)
			return record.Summary.HumanActor, e
		}
		if e := store.BindSelectedRun(ctx, key, id, run, human, func(context.Context, *sql.Tx, slides.SelectedAuthorityState) error { return nil }); e != nil {
			return e
		}
		agent := func(ctx context.Context, tx *sql.Tx) (string, error) {
			record, e := live(ctx, tx)
			return record.Summary.AgentActor + ":" + run, e
		}
		_, e := store.ReplaceSelectedText(ctx, key, id, run, "Upravený výběr 🐎", agent)
		return e
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", f.r)
	mux.Handle("/", StaticFileHandler(os.DirFS(filepath.Join(repo, "out"))))
	// This private control exists only on the owned synthetic httptest server.
	// It advances the real worker deterministically, never exposes capabilities.
	mux.HandleFunc("POST /__selected_fixture/dispatch", func(w http.ResponseWriter, r *http.Request) {
		f.r.pages.dispatchNextSelectedSlidesRequest(r.Context())
		w.WriteHeader(204)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	raw, err := json.Marshal(map[string]any{"server": server.URL, "workspace": "ws-old", "owner_token": tokens["boss"], "reader_token": tokens["selected-reader"]})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "selected-browser.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join(repo, "scripts/acceptance-slides-selected-browser.mjs"))
	cmd.Env = append(os.Environ(), "CREWSHIP_SLIDES_SELECTED_FIXTURE="+path)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err = cmd.Run(); err != nil {
		t.Fatal("actual selected UI browser", err)
	}
	var results int
	if err = f.db.QueryRow(`SELECT COUNT(*) FROM slides_requests WHERE result_revision IS NOT NULL AND status='succeeded'`).Scan(&results); err != nil || results != 1 {
		t.Fatal(fmt.Sprintf("request-owned results=%d", results), err)
	}
}
