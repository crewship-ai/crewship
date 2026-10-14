//go:build linux && (board_mermaid_workflow_browser || conformance)

package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/pagebuild"
)

// This fixture supplies synthetic admitted agent output, then exercises actual
// human sessions, Router transactions, production UI and the pinned converter.
// It neither executes an agent nor mocks an API response.
func TestBoardMermaidWorkflowBrowser(t *testing.T) {
	if os.Getenv("CREWSHIP_BOARD_MERMAID_BROWSER") != "1" {
		t.Fatal("tagged execution requires CREWSHIP_BOARD_MERMAID_BROWSER=1, a real static export and Chromium")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"index.html", "board-converter/runner.js"} {
		if _, err = os.Stat(filepath.Join(repo, "out", file)); err != nil {
			t.Fatal("build the real production frontend and pinned converter first", err)
		}
	}
	h, workspace, owner, page, token, job := boardJobFixture(t)
	execOrFatal(t, h.db, `UPDATE page_board_requests SET status='running',run_id='artifact-run' WHERE id=?`, job)
	const source = "flowchart TD\nA[Plan] --> B[Build]\nB --> C[Review]"
	publish, _ := json.Marshal(map[string]any{"mutation_id": "browser-source", "source": source, "dialect": "flowchart-v1", "converter_version": "2.2.2"})
	artifactExpect(t, boardJobAgentCall(t, h, workspace, token, job, string(publish), h.PublishBoardRequestMermaid), 201)
	const secret = "synthetic-mermaid-browser-session-secret-long-enough"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	tokens := []string{}
	for _, label := range []string{"first-tab", "second-tab"} {
		session, err := sessions.NewDBStore(h.db).Create(t.Context(), owner, label, "127.0.0.1", auth.RefreshTokenTTL)
		if err != nil {
			t.Fatal(err)
		}
		jwt, err := validator.IssueAccessToken(owner, session.ID, owner, owner+"@example.test")
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, jwt)
	}
	studioMux, runtimeMux := http.NewServeMux(), http.NewServeMux()
	studio := httptest.NewUnstartedServer(studioMux)
	studio.StartTLS()
	t.Cleanup(studio.Close)
	runtime := httptest.NewUnstartedServer(runtimeMux)
	if err = runtime.Listener.Close(); err != nil {
		t.Fatal(err)
	}
	runtime.Listener, err = net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Fatal(err)
	}
	runtime.StartTLS()
	t.Cleanup(runtime.Close)
	if err = pagebuild.ValidateRuntimeOrigin(runtime.URL, studio.URL); err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(h.db, secret, newTestLogger(), WithInternalToken(h.artifactMasterToken), WithStoragePath(h.artifactStoragePath), WithPageRuntime(runtime.URL, studio.URL))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	studioMux.Handle("/api/", router)
	studioMux.Handle("/", StaticFileHandler(os.DirFS(filepath.Join(repo, "out"))))
	runtimeMux.Handle("/api/", router)
	// Mirror the public constant asset's production opaque-origin CORS headers;
	// the bootstrap itself comes from the canonical Router handler unchanged.
	runtimeMux.HandleFunc(pagebuild.BoardConverterScriptPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		http.ServeFile(w, r, filepath.Join(repo, "out", "board-converter", "runner.js"))
	})
	fixture, err := json.Marshal(map[string]any{"server": studio.URL, "runtime": runtime.URL, "workspace": workspace, "slug": "report", "request_id": job, "source": source, "tokens": tokens})
	if err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(t.TempDir(), "mermaid-browser.json")
	if err = os.WriteFile(fixturePath, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "node", filepath.Join(repo, "scripts/acceptance-board-mermaid-browser.mjs"))
	command.Env = append(os.Environ(), "CREWSHIP_BOARD_MERMAID_FIXTURE="+fixturePath)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err = command.Run(); err != nil {
		t.Fatal("Board Mermaid production browser", err)
	}
	var status, idsJSON, actor string
	var resultSeq, seq, elements, mutations int
	if err = h.db.QueryRow(`SELECT status,result_seq,inserted_element_ids_json,inserted_by_user_id FROM page_board_requests WHERE id=?`, job).Scan(&status, &resultSeq, &idsJSON, &actor); err != nil {
		t.Fatal(err)
	}
	var ids []string
	if err = json.Unmarshal([]byte(idsJSON), &ids); err != nil {
		t.Fatal(err)
	}
	if err = h.db.QueryRow(`SELECT seq FROM page_boards WHERE page_id=?`, page).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if err = h.db.QueryRow(`SELECT COUNT(*) FROM page_board_elements WHERE board_page_id=? AND origin_request_id=? AND author_user_id=? AND author_agent_id IS NULL AND author_run_id IS NULL`, page, job, owner).Scan(&elements); err != nil {
		t.Fatal(err)
	}
	if err = h.db.QueryRow(`SELECT COUNT(*) FROM page_board_mutations WHERE board_page_id=? AND result_seq>0`, page).Scan(&mutations); err != nil {
		t.Fatal(err)
	}
	if status != "inserted" || seq != 1 || resultSeq != 1 || elements != len(ids) || elements < 5 || mutations != 1 || actor != owner {
		t.Fatalf("once-only human insertion failed: status=%s seq=%d result=%d elements=%d ids=%d mutations=%d actor=%s", status, seq, resultSeq, elements, len(ids), mutations, actor)
	}
}
