package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Acceptance for `crewship consolidate proposed`, driven through the BUILT
// BINARY rather than by calling RunE in-process — same reasoning as
// acceptance_credential_openrouter_test.go: these commands build their path
// from a proposal id, so the path argument never renders to an `/api/…`
// literal and cli_route_contract_test.go drops every one of them silently.
//
// The behaviour this file pins, beyond "the right URL was called":
//
//   - approve is a WRITE to canonical crew memory. It must not run
//     unconfirmed, and a refused confirmation must not reach the server.
//   - approve --diff must fetch the diff BEFORE it asks, because a preview
//     shown after the decision is not a preview. The server guarantees the
//     preview is byte-equal to what approve writes
//     (internal/api/consolidate_proposed_diff_handler_test.go:305), so the
//     pairing is meaningful rather than decorative.
//   - reject's --reason is accepted and sent, but the server does not persist
//     it yet (internal/api/consolidate_proposed_handler.go:132). The command
//     says so rather than implying an audit trail that is not there.
//   - a 404 on an unknown id must exit ExitNotFound, not ExitGeneric — an
//     agent branches on that to tell "already decided elsewhere" from "the
//     server is broken".
//
// No network: httptest stub on 127.0.0.1, the binary pointed at it through a
// config file, ambient CREWSHIP_* cleared.

type proposedStubServer struct {
	mu sync.Mutex
	// posted maps path -> raw request body bytes (possibly empty).
	posted map[string][]byte
	// order records the sequence of paths hit, so "diff before approve" is
	// decidable rather than assumed.
	order []string
	// approveStatus lets a case make approve fail with a chosen status.
	approveStatus int
}

const proposedStubID = "mp_20260826T101500-abcdef0123456789"

func (s *proposedStubServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	base := "/api/v1/consolidate/proposed/" + proposedStubID
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		s.mu.Lock()
		if s.posted == nil {
			s.posted = map[string][]byte{}
		}
		raw, _ := io.ReadAll(r.Body)
		s.posted[r.URL.Path] = raw
		s.order = append(s.order, r.URL.Path)
		approveStatus := s.approveStatus
		s.mu.Unlock()

		switch r.URL.Path {
		case base + "/explain":
			_, _ = w.Write([]byte(`{
				"proposal_id":"` + proposedStubID + `",
				"workspace_id":"ws_test","crew_id":"crew_backend","status":"pending",
				"proposal_path":".proposed/proposal-20260826T101500-abcdef0123456789.md",
				"rules_count":3,"entries_scanned":42,"created_at":"2026-08-26T10:15:00Z",
				"evidence":{"entries":["je_1","je_2"]},"scores":{"confidence":0.82}
			}`))

		case base + "/diff":
			_, _ = w.Write([]byte(`{
				"proposal_id":"` + proposedStubID + `",
				"workspace_id":"ws_test","crew_id":"crew_backend","status":"pending",
				"canonical_path":"learned-2026-08-26.md","canonical_exists":true,
				"proposal_path":".proposed/proposal-20260826T101500-abcdef0123456789.md",
				"rules_count":3,
				"diff":"--- learned-2026-08-26.md\n+++ learned-2026-08-26.md\n@@\n+- Always run migrations before restarting the API.\n",
				"stats":{"additions":3,"deletions":0,"rules_appended":3}
			}`))

		case base + "/approve":
			if approveStatus != 0 {
				w.WriteHeader(approveStatus)
				_, _ = w.Write([]byte(`{"error":"memory proposal not found"}`))
				return
			}
			_, _ = w.Write([]byte(`{
				"proposal_id":"` + proposedStubID + `","canonical_path":"learned-2026-08-26.md",
				"rules_merged":3,"workspace_id":"ws_test","crew_id":"crew_backend",
				"decided_by":"user_demo","version_sha":"abc123"
			}`))

		case base + "/reject":
			_, _ = w.Write([]byte(`{
				"proposal_id":"` + proposedStubID + `","status":"rejected",
				"decided_by":"user_demo","reason":"over-fitted to one incident"
			}`))

		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"memory proposal not found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *proposedStubServer) hit(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.posted[path]
	return ok
}

func (s *proposedStubServer) callOrder() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.order...)
}

func (s *proposedStubServer) rejectBody(t *testing.T) map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.posted["/api/v1/consolidate/proposed/"+proposedStubID+"/reject"]
	if !ok {
		t.Fatal("POST …/reject was never called")
	}
	body := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("reject body is not JSON: %v (%q)", err, raw)
		}
	}
	return body
}

func proposedStubConfig(t *testing.T, serverURL string) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + serverURL + "\nworkspace: ws_test\ntoken: fake-token\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

func runProposedCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	// Nothing may block on a prompt: an unconfirmed approve must fail, not hang.
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// explain is the read-only "why is this being proposed" surface. It is the
// first thing an operator runs and, until now, had no consumer at all — not
// even the web UI reads it.
func TestAcceptance_ConsolidateProposedExplain(t *testing.T) {
	stub := &proposedStubServer{}
	srv := stub.start(t)
	cfg := proposedStubConfig(t, srv.URL)

	out, err := runProposedCLI(t, cfg, "consolidate", "proposed", "explain", proposedStubID)
	if err != nil {
		t.Fatalf("explain: %v\noutput: %s", err, out)
	}
	for _, want := range []string{"pending", "crew_backend", "3", "42"} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output missing %q:\n%s", want, out)
		}
	}

	jsonOut, err := runProposedCLI(t, cfg, "consolidate", "proposed", "explain", proposedStubID, "--format", "json")
	if err != nil {
		t.Fatalf("explain --format json: %v\noutput: %s", err, jsonOut)
	}
	var got struct {
		ProposalID     string          `json:"proposal_id"`
		RulesCount     int             `json:"rules_count"`
		EntriesScanned int             `json:"entries_scanned"`
		Scores         json.RawMessage `json:"scores"`
		Evidence       json.RawMessage `json:"evidence"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &got); err != nil {
		t.Fatalf("explain --format json is not JSON: %v\noutput: %s", err, jsonOut)
	}
	if got.ProposalID != proposedStubID || got.RulesCount != 3 || got.EntriesScanned != 42 {
		t.Errorf("explain json = %+v", got)
	}
	// evidence and scores are opaque JSON on the wire; they must survive the
	// round trip rather than being flattened to a string or dropped.
	if !strings.Contains(string(got.Scores), "confidence") {
		t.Errorf("scores did not survive as JSON: %s", got.Scores)
	}
	if !strings.Contains(string(got.Evidence), "je_1") {
		t.Errorf("evidence did not survive as JSON: %s", got.Evidence)
	}
}

// diff is the byte-level preview of what approve would append.
func TestAcceptance_ConsolidateProposedDiff(t *testing.T) {
	stub := &proposedStubServer{}
	srv := stub.start(t)
	cfg := proposedStubConfig(t, srv.URL)

	out, err := runProposedCLI(t, cfg, "consolidate", "proposed", "diff", proposedStubID)
	if err != nil {
		t.Fatalf("diff: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "Always run migrations before restarting the API.") {
		t.Errorf("diff output does not contain the diff body:\n%s", out)
	}
	if !strings.Contains(out, "learned-2026-08-26.md") {
		t.Errorf("diff output does not name the canonical file it would change:\n%s", out)
	}
}

// approve writes to canonical crew memory, so it must not run unconfirmed.
func TestAcceptance_ConsolidateProposedApproveRequiresConfirmation(t *testing.T) {
	stub := &proposedStubServer{}
	srv := stub.start(t)
	cfg := proposedStubConfig(t, srv.URL)

	out, err := runProposedCLI(t, cfg, "consolidate", "proposed", "approve", proposedStubID)
	if err == nil {
		t.Fatalf("approve ran without confirmation\noutput: %s", out)
	}
	if stub.hit("/api/v1/consolidate/proposed/" + proposedStubID + "/approve") {
		t.Error("an unconfirmed approve still reached the server")
	}
}

func TestAcceptance_ConsolidateProposedApproveWithYes(t *testing.T) {
	stub := &proposedStubServer{}
	srv := stub.start(t)
	cfg := proposedStubConfig(t, srv.URL)

	out, err := runProposedCLI(t, cfg, "consolidate", "proposed", "approve", proposedStubID, "--yes")
	if err != nil {
		t.Fatalf("approve --yes: %v\noutput: %s", err, out)
	}
	if !stub.hit("/api/v1/consolidate/proposed/" + proposedStubID + "/approve") {
		t.Fatal("approve --yes never reached the server")
	}
	// The operator needs to know what changed and where.
	for _, want := range []string{"learned-2026-08-26.md", "3"} {
		if !strings.Contains(out, want) {
			t.Errorf("approve output missing %q:\n%s", want, out)
		}
	}
}

// --diff makes the preview part of the decision: it must be fetched BEFORE the
// approve is sent, or it is not a preview.
func TestAcceptance_ConsolidateProposedApproveDiffFetchesPreviewFirst(t *testing.T) {
	stub := &proposedStubServer{}
	srv := stub.start(t)
	cfg := proposedStubConfig(t, srv.URL)

	out, err := runProposedCLI(t, cfg, "consolidate", "proposed", "approve", proposedStubID, "--diff", "--yes")
	if err != nil {
		t.Fatalf("approve --diff --yes: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "Always run migrations before restarting the API.") {
		t.Errorf("approve --diff did not show the preview:\n%s", out)
	}

	order := stub.callOrder()
	diffAt, approveAt := -1, -1
	for i, p := range order {
		if strings.HasSuffix(p, "/diff") && diffAt < 0 {
			diffAt = i
		}
		if strings.HasSuffix(p, "/approve") && approveAt < 0 {
			approveAt = i
		}
	}
	if diffAt < 0 {
		t.Fatalf("--diff never fetched the preview; calls were %v", order)
	}
	if approveAt < 0 {
		t.Fatalf("--diff --yes never approved; calls were %v", order)
	}
	if diffAt > approveAt {
		t.Errorf("the preview was fetched after the approve, so it previewed nothing; calls were %v", order)
	}
}

// …and under a machine format the preview must not be printed as prose next to
// the JSON. Two documents on one stdout is not JSON, and `| jq .` on it fails —
// which is the whole audience for --format json.
func TestAcceptance_ConsolidateProposedApproveDiffJSONStaysParseable(t *testing.T) {
	stub := &proposedStubServer{}
	srv := stub.start(t)
	cfg := proposedStubConfig(t, srv.URL)

	out, err := runProposedCLI(t, cfg, "consolidate", "proposed", "approve", proposedStubID,
		"--diff", "--yes", "--format", "json")
	if err != nil {
		t.Fatalf("approve --diff --format json: %v\noutput: %s", err, out)
	}

	var got struct {
		ProposalID    string `json:"proposal_id"`
		CanonicalPath string `json:"canonical_path"`
		RulesMerged   int    `json:"rules_merged"`
		Preview       *struct {
			Diff  string `json:"diff"`
			Stats struct {
				Additions int `json:"additions"`
			} `json:"stats"`
		} `json:"preview"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("approve --diff --format json did not emit one JSON document: %v\noutput: %s", err, out)
	}
	if got.RulesMerged != 3 {
		t.Errorf("rules_merged = %d", got.RulesMerged)
	}
	// The flag was asked for, so it must produce something rather than being
	// silently dropped: the preview travels inside the document.
	if got.Preview == nil {
		t.Fatalf("--diff produced no preview under --format json:\n%s", out)
	}
	if !strings.Contains(got.Preview.Diff, "Always run migrations") {
		t.Errorf("preview diff = %q", got.Preview.Diff)
	}
	if got.Preview.Stats.Additions != 3 {
		t.Errorf("preview stats.additions = %d", got.Preview.Stats.Additions)
	}
	// The preview must still have been fetched first — the ordering guarantee
	// does not weaken just because the output is machine-readable.
	order := stub.callOrder()
	diffAt, approveAt := -1, -1
	for i, p := range order {
		if strings.HasSuffix(p, "/diff") && diffAt < 0 {
			diffAt = i
		}
		if strings.HasSuffix(p, "/approve") && approveAt < 0 {
			approveAt = i
		}
	}
	if diffAt < 0 || approveAt < 0 || diffAt > approveAt {
		t.Errorf("preview was not fetched before the approve; calls were %v", order)
	}
}

// reject sends the reason, and is honest that the server does not keep it.
func TestAcceptance_ConsolidateProposedRejectSendsReason(t *testing.T) {
	stub := &proposedStubServer{}
	srv := stub.start(t)
	cfg := proposedStubConfig(t, srv.URL)

	out, err := runProposedCLI(t, cfg, "consolidate", "proposed", "reject", proposedStubID,
		"--reason", "over-fitted to one incident", "--yes")
	if err != nil {
		t.Fatalf("reject: %v\noutput: %s", err, out)
	}
	if got := stub.rejectBody(t)["reason"]; got != "over-fitted to one incident" {
		t.Errorf("reason = %v", got)
	}
	if !strings.Contains(out, "not stored") {
		t.Errorf("reject does not say the reason is not persisted server-side:\n%s", out)
	}
}

// A proposal id that does not exist is ExitNotFound, so a script can tell it
// apart from a server fault.
func TestAcceptance_ConsolidateProposedUnknownIDIsNotFound(t *testing.T) {
	stub := &proposedStubServer{}
	srv := stub.start(t)
	cfg := proposedStubConfig(t, srv.URL)

	out, err := runProposedCLI(t, cfg, "consolidate", "proposed", "explain", "mp_does_not_exist")
	if err == nil {
		t.Fatalf("explain of an unknown proposal succeeded\noutput: %s", out)
	}
	if got := exitCodeOf(t, err); got != cli.ExitNotFound {
		t.Errorf("exit code = %d, want ExitNotFound (%d)\noutput: %s", got, cli.ExitNotFound, out)
	}
}

// The whole surface is id-addressed and the API has no list endpoint, so the
// help has to say where an id comes from or the commands are unusable.
func TestAcceptance_ConsolidateProposedHelpNamesTheEnumerationPath(t *testing.T) {
	out, err := runProposedCLI(t, proposedStubConfig(t, "http://127.0.0.1:1"),
		"consolidate", "proposed", "--help")
	if err != nil {
		t.Fatalf("help: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "inbox") {
		t.Errorf("help does not say proposal ids come from the inbox, and nothing else lists them:\n%s", out)
	}
	if !strings.Contains(out, "CREWSHIP_CONSOLIDATE_HITL") {
		t.Errorf("help does not name the env var without which no proposal is ever created:\n%s", out)
	}
}
