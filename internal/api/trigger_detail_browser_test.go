//go:build linux && (trigger_detail_browser || conformance)

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
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// The browser uses the real static frontend and Router. Only list failures
// are injected; Retry and missing-row handling use real successful API reads.
func TestTriggerDetailLoadErrorBrowser(t *testing.T) {
	if os.Getenv("CREWSHIP_TRIGGER_DETAIL_BROWSER") != "1" {
		t.Fatal("requires CREWSHIP_TRIGGER_DETAIL_BROWSER=1, built frontend and Chromium")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(repo, "out", "index.html")); err != nil {
		t.Fatal("build the real frontend first", err)
	}
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	seedWebhookPipeline(t, db, workspace, "trigger-browser-pipeline", "trigger-browser-pipeline")
	hook := seedWebhookRow(t, db, workspace, "trigger-browser-pipeline", "synthetic-trigger-signing-secret", true)
	const secret = "synthetic-trigger-browser-session-secret-long-enough"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.NewDBStore(db).Create(t.Context(), owner, "synthetic", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	token, err := validator.IssueAccessToken(owner, session.ID, "Synthetic owner", "owner@example.test")
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(db, secret, newTestLogger(), WithInternalToken("synthetic-trigger-internal"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	router.PipelinesHandler.SetWebhookStore(pipeline.NewWebhookStore(db))
	mux := http.NewServeMux()
	mux.Handle("/api/", router)
	mux.Handle("/ws", router)
	mux.Handle("/", StaticFileHandler(os.DirFS(filepath.Join(repo, "out"))))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	fixture, err := json.Marshal(map[string]string{"server": server.URL, "workspace": workspace, "trigger": hook.ID, "name": hook.Name, "token": token})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "trigger-browser.json")
	if err = os.WriteFile(path, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "--experimental-strip-types", filepath.Join(repo, "scripts/acceptance-trigger-detail-browser.mjs"))
	command.Env = append(os.Environ(), "CREWSHIP_TRIGGER_DETAIL_FIXTURE="+path)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err = command.Run(); err != nil {
		t.Fatal("trigger detail browser", err)
	}
}
