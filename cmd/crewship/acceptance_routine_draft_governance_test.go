package main

// Binary acceptance for the routine draft lifecycle and the maker-checker /
// airbag governance verbs. Every case runs the BUILT crewship binary against
// the REAL api router and a migrated DB (startRoutineTriggerAcceptanceServerDB):
//
//   GET  /pipelines/drafts            routine draft list
//   POST /pipelines/drafts            routine draft save
//   GET  /pipelines/{slug}/draft      routine draft get
//   DELETE /pipelines/{slug}/draft    routine draft discard
//   POST /pipelines/{slug}/publish    routine draft publish
//   POST /pipelines/{slug}/approve    routine approve   (MANAGER+)
//   POST /pipelines/{slug}/reject     routine reject    (MANAGER+)
//   POST /pipelines/{slug}/disable    routine disable   (OWNER/ADMIN)
//   POST /pipelines/{slug}/enable     routine enable    (OWNER/ADMIN)
//
// acceptance_routine_draft_browser_test.go covers the browser byte contract
// in-process over HTTP; this file is the CLI contract.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// addRoutineAcceptanceMember seeds a workspace member with the given role and
// a CLI token, and returns a CLI config pointing at the same server.
func addRoutineAcceptanceMember(t *testing.T, db *sql.DB, serverURL, id, role string) string {
	t.Helper()
	token := "crewship_cli_" + id + strings.Repeat("0", 28-len(id))
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`, []any{id, id + "@trg-ex.com", role}},
		{`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, ?)`, []any{"m-" + id, routineTriggerAcceptanceWorkspaceID, id, role}},
		{`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES (?, ?, 't', ?, datetime('now'))`, []any{"clt-" + id, id, sha256HexToken(token)}},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatalf("seed %s: %v", role, err)
		}
	}
	path := filepath.Join(t.TempDir(), role+"-config.yaml")
	cfg := "server: " + serverURL + "\nworkspace: " + routineTriggerAcceptanceWorkspaceID + "\ntoken: " + token + "\nformat: table\n"
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// runRoutineCLISplit runs the binary and keeps stdout apart from stderr, so a
// --format json case proves stdout alone is one parseable document.
func runRoutineCLISplit(t *testing.T, cfgPath string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath, "NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		exitCode = exitErr.ExitCode()
	default:
		t.Fatalf("run %v: %v", args, err)
	}
	return out.String(), errOut.String(), exitCode
}

func decodeRoutineJSON(t *testing.T, label, raw string, into any) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), into); err != nil {
		t.Fatalf("%s: stdout is not one JSON document: %v\n%s", label, err, raw)
	}
}

// writeRoutineDraftEnvelope writes the envelope as literal bytes: keys in
// authoring order (not alphabetical) and a prompt with & < >. Publication
// proof is bound to the definition's exact bytes, so a CLI that re-sorted or
// HTML-escaped them on the way through would fail publish with a 422.
func writeRoutineDraftEnvelope(t *testing.T, slug string) string {
	t.Helper()
	raw := `{"slug":"` + slug + `","revision":0,"document":{"slug":"` + slug + `","name":"` + slug + `","description":"draft acceptance",` +
		`"author_crew_id":"trg-crew","author_agent_id":"trg-agent","definition":` + routineDraftDefinition(slug) + `}}`
	path := filepath.Join(t.TempDir(), slug+".draft.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func routineDraftDefinition(slug string) string {
	return `{"name":"` + slug + `","dsl_version":"1.0","description":"Sales & Ops <review>",` +
		`"steps":[{"id":"a","type":"agent_run","prompt":"Summarise Q&A <draft>","agent_slug":"trg-agent"}]}`
}

type routineDraftEnvelope struct {
	ID       string          `json:"id"`
	Slug     string          `json:"slug"`
	Revision int             `json:"revision"`
	Document json.RawMessage `json:"document"`
}

func listedDraftRevision(t *testing.T, cfg, slug string) int {
	t.Helper()
	out, errOut, code := runRoutineCLISplit(t, cfg, "routine", "draft", "list", "--format", "json")
	if code != 0 {
		t.Fatalf("draft list: exit %d\n%s%s", code, out, errOut)
	}
	var rows []struct {
		Slug     string `json:"slug"`
		Revision int    `json:"revision"`
	}
	decodeRoutineJSON(t, "draft list", out, &rows)
	for _, r := range rows {
		if r.Slug == slug {
			return r.Revision
		}
	}
	return 0
}

func TestAcceptance_RoutineDraftLifecycleCLI(t *testing.T) {
	cfg, _, _, _ := startRoutineTriggerAcceptanceServerDB(t)
	dir := t.TempDir()

	// save → a revision envelope with a server-issued id.
	out, errOut, code := runRoutineCLISplit(t, cfg, "routine", "draft", "save", writeRoutineDraftEnvelope(t, "acc-draft"), "--format", "json")
	if code != 0 {
		t.Fatalf("draft save: exit %d\n%s%s", code, out, errOut)
	}
	var saved routineDraftEnvelope
	decodeRoutineJSON(t, "draft save", out, &saved)
	if saved.ID == "" || saved.Slug != "acc-draft" || saved.Revision != 1 {
		t.Fatalf("draft save returned %+v", saved)
	}
	savedPath := filepath.Join(dir, "saved.json")
	if err := os.WriteFile(savedPath, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	if rev := listedDraftRevision(t, cfg, "acc-draft"); rev != 1 {
		t.Fatalf("draft list revision = %d, want 1", rev)
	}

	// get returns the same revision; a draft does not create the live recipe.
	out, errOut, code = runRoutineCLISplit(t, cfg, "routine", "draft", "get", "acc-draft", "--format", "json")
	if code != 0 {
		t.Fatalf("draft get: exit %d\n%s%s", code, out, errOut)
	}
	var got routineDraftEnvelope
	decodeRoutineJSON(t, "draft get", out, &got)
	var gotDoc struct {
		Definition struct {
			Steps []struct {
				AgentSlug string `json:"agent_slug"`
			} `json:"steps"`
		} `json:"definition"`
	}
	if err := json.Unmarshal(got.Document, &gotDoc); err != nil {
		t.Fatalf("draft get document: %v", err)
	}
	if got.ID != saved.ID || got.Revision != 1 || len(gotDoc.Definition.Steps) != 1 || gotDoc.Definition.Steps[0].AgentSlug != "trg-agent" {
		t.Fatalf("draft get returned %+v", got)
	}
	if out, errOut, code := runRoutineCLISplit(t, cfg, "routine", "get", "acc-draft"); code == 0 {
		t.Fatalf("an unpublished draft must not be a live routine:\n%s%s", out, errOut)
	}

	// publish validates the file's exact document and makes it live.
	out, errOut, code = runRoutineCLISplit(t, cfg, "routine", "draft", "publish", savedPath, "--format", "json")
	if code != 0 {
		t.Fatalf("draft publish: exit %d\n%s%s", code, out, errOut)
	}
	var published map[string]any
	decodeRoutineJSON(t, "draft publish", out, &published)
	if out, errOut, code := runRoutineCLISplit(t, cfg, "routine", "get", "acc-draft"); code != 0 {
		t.Fatalf("published routine not readable: exit %d\n%s%s", code, out, errOut)
	}
	if rev := listedDraftRevision(t, cfg, "acc-draft"); rev != 0 {
		t.Fatalf("publication must consume the draft, list still shows revision %d", rev)
	}
	// The consumed revision cannot be published twice.
	_, errOut, code = runRoutineCLISplit(t, cfg, "routine", "draft", "publish", savedPath)
	if code != 5 || !strings.Contains(errOut, "409") {
		t.Fatalf("republish of a consumed draft: exit %d, want 5 (conflict)\n%s", code, errOut)
	}

	// discard removes an unpublished draft; a stale revision is a readable conflict.
	out, errOut, code = runRoutineCLISplit(t, cfg, "routine", "draft", "save", writeRoutineDraftEnvelope(t, "acc-discard"), "--format", "json")
	if code != 0 {
		t.Fatalf("second draft save: exit %d\n%s%s", code, out, errOut)
	}
	discardPath := filepath.Join(dir, "discard.json")
	var toDiscard routineDraftEnvelope
	decodeRoutineJSON(t, "second draft save", out, &toDiscard)
	stale := toDiscard
	stale.Revision = 7
	staleRaw, _ := json.Marshal(stale)
	stalePath := filepath.Join(dir, "stale.json")
	if err := os.WriteFile(stalePath, staleRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(discardPath, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errOut, code = runRoutineCLISplit(t, cfg, "routine", "draft", "discard", stalePath)
	if code != 5 || !strings.Contains(errOut, "409") {
		t.Fatalf("stale discard: exit %d, want 5 (conflict)\n%s", code, errOut)
	}
	out, errOut, code = runRoutineCLISplit(t, cfg, "routine", "draft", "discard", discardPath, "--format", "json")
	if code != 0 {
		t.Fatalf("draft discard: exit %d\n%s%s", code, out, errOut)
	}
	var discarded map[string]bool
	decodeRoutineJSON(t, "draft discard", out, &discarded)
	if !discarded["discarded"] {
		t.Fatalf("draft discard returned %s", out)
	}
	if rev := listedDraftRevision(t, cfg, "acc-discard"); rev != 0 {
		t.Fatalf("discarded draft still listed at revision %d", rev)
	}
}

// A draft saved by the browser (literal JSON.stringify bytes) must publish
// from the CLI: get → file → publish validates exactly the stored definition.
func TestAcceptance_RoutineDraftPublishBrowserSavedCLI(t *testing.T) {
	cfg, _, _, serverURL := startRoutineTriggerAcceptanceServerDB(t)
	raw, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var token string
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, "token: "); ok {
			token = v
		}
	}
	body := `{"slug":"browser-draft","document":{"slug":"browser-draft","name":"Sales & Ops","author_crew_id":"trg-crew","definition":` + routineDraftDefinition("browser-draft") + `}}`
	req, err := http.NewRequest(http.MethodPost, serverURL+"/api/v1/workspaces/"+routineTriggerAcceptanceWorkspaceID+"/pipelines/drafts", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("browser draft save: status %d", resp.StatusCode)
	}

	out, errOut, code := runRoutineCLISplit(t, cfg, "routine", "draft", "get", "browser-draft", "--format", "json")
	if code != 0 {
		t.Fatalf("draft get: exit %d\n%s%s", code, out, errOut)
	}
	if !strings.Contains(out, `"name": "browser-draft",`+"\n"+`      "dsl_version"`) || !strings.Contains(out, "Sales & Ops <review>") {
		t.Fatalf("draft get must print the stored definition bytes (order, no HTML escapes):\n%s", out)
	}
	path := filepath.Join(t.TempDir(), "browser.json")
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, code = runRoutineCLISplit(t, cfg, "routine", "draft", "publish", path, "--format", "json")
	if code != 0 {
		t.Fatalf("publish of a browser-saved draft: exit %d\n%s%s", code, out, errOut)
	}
	if out, errOut, code := runRoutineCLISplit(t, cfg, "routine", "get", "browser-draft"); code != 0 {
		t.Fatalf("published routine not readable: exit %d\n%s%s", code, out, errOut)
	}
}

// writeRiskyRoutineDefinition declares an http step, which classifyRoutineRisk
// treats as risky, so a user save lands as 'proposed' and needs approval.
func writeRiskyRoutineDefinition(t *testing.T, name string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"dsl_version": "1.0",
		"name":        name,
		"description": "needs review",
		"steps": []map[string]any{
			{"id": "fetch", "type": "http", "http": map[string]any{"method": "GET", "url": "https://example.test/"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name+".json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func routineStatus(t *testing.T, cfg, slug string) string {
	t.Helper()
	out, errOut, code := runRoutineCLISplit(t, cfg, "routine", "get", slug, "--format", "json")
	if code != 0 {
		return ""
	}
	var r struct {
		Status string `json:"status"`
	}
	decodeRoutineJSON(t, "routine get "+slug+errOut, out, &r)
	return r.Status
}

func TestAcceptance_RoutineGovernanceCLI(t *testing.T) {
	owner, crewID, db, serverURL := startRoutineTriggerAcceptanceServerDB(t)
	member := addRoutineAcceptanceMember(t, db, serverURL, "trg-member", "MEMBER")
	manager := addRoutineAcceptanceMember(t, db, serverURL, "trg-manager", "MANAGER")

	propose := func(slug string) {
		t.Helper()
		out, errOut, code := runRoutineCLISplit(t, owner, "routine", "save", "--name", slug,
			"--definition", writeRiskyRoutineDefinition(t, slug), "--author-crew", crewID, "--author-agent", "trg-agent")
		if code != 0 {
			t.Fatalf("save %s: exit %d\n%s%s", slug, code, out, errOut)
		}
		if got := routineStatus(t, owner, slug); got != "proposed" {
			t.Fatalf("%s status = %q, want proposed (http step is a risk factor)", slug, got)
		}
	}
	refused := func(cfg, verb, slug, role string) {
		t.Helper()
		out, errOut, code := runRoutineCLISplit(t, cfg, "routine", verb, slug)
		if code != 4 {
			t.Fatalf("%s by insufficient role: exit %d, want 4 (auth)\n%s%s", verb, code, out, errOut)
		}
		// The bare middleware "Forbidden" must come back with the role it needs.
		if !strings.Contains(errOut, "403") || !strings.Contains(errOut, role) {
			t.Fatalf("%s refusal must name the required role %q:\n%s", verb, role, errOut)
		}
	}
	decide := func(verb, slug, want string) map[string]any {
		t.Helper()
		out, errOut, code := runRoutineCLISplit(t, owner, "routine", verb, slug, "--format", "json")
		if code != 0 {
			t.Fatalf("%s: exit %d\n%s%s", verb, code, out, errOut)
		}
		var body map[string]any
		decodeRoutineJSON(t, verb, out, &body)
		if body["slug"] != slug || body["status"] != want {
			t.Fatalf("%s returned %v, want slug=%s status=%s", verb, body, slug, want)
		}
		return body
	}

	propose("acc-approve")
	refused(member, "approve", "acc-approve", "MANAGER")
	if got := routineStatus(t, owner, "acc-approve"); got != "proposed" {
		t.Fatalf("refused approve changed status to %q", got)
	}
	decide("approve", "acc-approve", "active")
	if got := routineStatus(t, owner, "acc-approve"); got != "active" {
		t.Fatalf("approved routine status = %q", got)
	}
	// A second approval is a readable conflict, not a silent success.
	_, errOut, code := runRoutineCLISplit(t, owner, "routine", "approve", "acc-approve")
	if code != 5 || !strings.Contains(errOut, "not awaiting approval") {
		t.Fatalf("re-approve: exit %d, want 5 with reason\n%s", code, errOut)
	}
	// Human output stays the one-line receipt.
	out, errOut, code := runRoutineCLISplit(t, owner, "routine", "disable", "acc-approve")
	if code != 0 || !strings.Contains(out, "Routine acc-approve → disabled") {
		t.Fatalf("human disable: exit %d\n%s%s", code, out, errOut)
	}
	out, errOut, code = runRoutineCLISplit(t, owner, "routine", "enable", "acc-approve")
	if code != 0 || !strings.Contains(out, "Routine acc-approve → active") {
		t.Fatalf("human enable: exit %d\n%s%s", code, out, errOut)
	}

	// The airbag is OWNER/ADMIN only: a MANAGER who may approve may not disable.
	refused(manager, "disable", "acc-approve", "ADMIN")
	body := decide("disable", "acc-approve", "disabled")
	if body["cancelled_runs"] != float64(0) {
		t.Fatalf("disable must report cancelled_runs, got %v", body)
	}
	if got := routineStatus(t, owner, "acc-approve"); got != "disabled" {
		t.Fatalf("disabled routine status = %q", got)
	}
	refused(manager, "enable", "acc-approve", "ADMIN")
	decide("enable", "acc-approve", "active")
	if got := routineStatus(t, owner, "acc-approve"); got != "active" {
		t.Fatalf("enabled routine status = %q", got)
	}

	propose("acc-reject")
	refused(member, "reject", "acc-reject", "MANAGER")
	decide("reject", "acc-reject", "rejected")
	if got := routineStatus(t, owner, "acc-reject"); got != "" {
		t.Fatalf("rejected routine is still readable with status %q", got)
	}
}
