package main

// #2201 acceptance — `crewship crew suggest --goal "…"` driven through the
// BUILT BINARY against the REAL api router.
//
// The command could never succeed on any server. It posted
// {"goal": "…"} and CrewAIHandler.Suggest decodes {"description": "…"}, so
// body.Description was always "" and every invocation died on the handler's
// 10-character minimum — a message naming a field the CLI user has no flag
// for. No path through the command reached a model.
//
// It survived CI because the only test of the payload asserted the CLI's own
// spelling against a stub that answered 200 to any body:
//
//	strings.Contains(string(calls[0].Body), `"goal":"grow the userbase"`)
//
// A stub cannot fail a request it never validates, so the test passed
// *because* the key was wrong. This file removes that class of lie for the
// command: the server here is api.NewRouter over a migrated database, and the
// body assertion binds to api.CrewAISuggestRequest — the very struct the
// handler decodes into — rather than to a JSON literal.
//
// The happy path is deliberately NOT driven end to end: a 200 needs a live
// Anthropic API key and a real model call. The reachable proof is the
// handler's own 422 for "no credential in this workspace", which sits
// immediately *after* the validation the bug never got past. 400 means the
// body is still wrong; 422 means the request reached the provider lookup.

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// suggestWorkspaceID is CUID-shaped so the CLI treats it as an already
// resolved workspace id and fires no slug→id round-trip.
const suggestWorkspaceID = "csuggestws0000000001a"

// suggestServer is the real router plus a tee that records the request bodies
// POSTed to the suggest route, so a test can assert both what the server
// answered and what it was actually sent.
type suggestServer struct {
	url string

	mu     sync.Mutex
	bodies [][]byte
}

// posted returns the decoded suggest bodies, failing when the endpoint was
// never reached — which is itself the assertion for the locally-refused cases.
func (s *suggestServer) posted(t *testing.T) []api.CrewAISuggestRequest {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]api.CrewAISuggestRequest, 0, len(s.bodies))
	for _, raw := range s.bodies {
		var req api.CrewAISuggestRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatalf("suggest body is not valid JSON (%v): %s", err, raw)
		}
		out = append(out, req)
	}
	return out
}

func (s *suggestServer) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

// startSuggestServer builds a real router over a migrated SQLite DB holding one
// workspace and one OWNER with a CLI token, and writes a CLI config pointing at
// it. The workspace deliberately has NO Anthropic credential: that is what
// makes 422 the best outcome a test can reach without spending tokens.
func startSuggestServer(t *testing.T) (*suggestServer, string) {
	t.Helper()

	dbh := testutil.MigratedDB(t)
	db := dbh.DB
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Suggest', 'suggest-ws')`, suggestWorkspaceID)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('sg-owner', 'owner@ex.com', 'Owner')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('sgm-owner', ?, 'sg-owner', 'OWNER')`,
		suggestWorkspaceID)

	const ownerToken = "crewship_cli_sgowner00000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-sg-owner', 'sg-owner', 't', ?, datetime('now'))`,
		sha256HexToken(ownerToken))

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	s := &suggestServer{}
	tee := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/crew-ai-suggest" {
			raw, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			s.mu.Lock()
			s.bodies = append(s.bodies, raw)
			s.mu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		router.ServeHTTP(w, r)
	})

	srv := httptest.NewServer(tee)
	t.Cleanup(srv.Close)
	s.url = srv.URL

	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + srv.URL + "\nworkspace: " + suggestWorkspaceID +
		"\ntoken: " + ownerToken + "\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return s, cfgPath
}

// runSuggestCLI runs the built binary against the real router.
func runSuggestCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestAcceptance_CrewSuggest_ReachesTheProviderLookup is the headline
// contract. The goal the user typed has to arrive in the field the handler
// reads, and the command has to get past validation to the credential check.
func TestAcceptance_CrewSuggest_ReachesTheProviderLookup(t *testing.T) {
	srv, cfgPath := startSuggestServer(t)

	const goal = "a crew that triages GitHub issues and drafts release notes"
	out, err := runSuggestCLI(t, cfgPath, "crew", "suggest", "--goal", goal)

	// A 200 is unreachable here — it would mean a real Anthropic call. The
	// endpoint's own "no credential" answer is the proof the request got
	// through validation, so a nil error would mean the stub-shaped world
	// crept back in.
	if err == nil {
		t.Fatalf("expected the endpoint's 422 (this workspace has no Anthropic key), got success:\n%s", out)
	}
	if code := exitCodeOf(t, err); code != 2 {
		t.Errorf("exit code = %d, want 2 (validation); output:\n%s", code, out)
	}
	if strings.Contains(out, "description must be at least 10 characters") {
		t.Errorf("the request never reached the model call — the handler rejected the body "+
			"before looking at credentials, which is #2201: the CLI is posting a key "+
			"CrewAISuggestRequest does not read.\noutput:\n%s", out)
	}
	if !strings.Contains(out, "No Anthropic API key found") {
		t.Errorf("want the endpoint's credential 422, got:\n%s", out)
	}

	posted := srv.posted(t)
	if len(posted) != 1 {
		t.Fatalf("suggest endpoint called %d times, want 1", len(posted))
	}
	if posted[0].Description != goal {
		t.Errorf("api.CrewAISuggestRequest.Description = %q, want %q — the goal must land in "+
			"the field the handler decodes, whatever the flag is called",
			posted[0].Description, goal)
	}
}

// TestAcceptance_CrewSuggest_RefusesAShortGoalByName is the #2191 precedent
// applied here: a local refusal names the flag the user typed. The server's
// own message names `description`, a field the CLI exposes no way to set, so
// forwarding a goal that cannot pass buys the user nothing but confusion.
func TestAcceptance_CrewSuggest_RefusesAShortGoalByName(t *testing.T) {
	srv, cfgPath := startSuggestServer(t)

	out, err := runSuggestCLI(t, cfgPath, "crew", "suggest", "--goal", "billing")
	if err == nil {
		t.Fatalf("expected a refusal for a 7-character goal, got success:\n%s", out)
	}
	if code := exitCodeOf(t, err); code != 2 {
		t.Errorf("exit code = %d, want 2 — a local refusal stands in for the server's 400 "+
			"and must not change what the shell sees; output:\n%s", code, out)
	}
	if !strings.Contains(out, "--goal") {
		t.Errorf("the refusal must name the flag the user typed, got:\n%s", out)
	}
	if strings.Contains(out, "description must be at least") {
		t.Errorf("the user was handed a message about a field they cannot set:\n%s", out)
	}
	if n := srv.calls(); n != 0 {
		t.Errorf("a goal that cannot pass validation still cost a round-trip (%d calls)", n)
	}
}
