package main

// Acceptance for the issue SUB-RESOURCE verbs, driven through the BUILT
// BINARY against the real api.Router (no stub server):
//
//	issue comments | activity | subtasks          GET  …/issues/{identifier}/{comments,activity,subtasks}
//	issue relations | relate                      GET/POST …/issues/{identifier}/relations
//	issue links | link | relink                   GET/POST …/code-links, POST …/code-links/{id}/refresh
//	issue attachments | attach | attachment       GET/POST …/attachments, GET …/attachments/{id}
//
// Each verb is checked for: a success path, `--format json` on STDOUT ALONE
// (stderr kept apart, so a receipt mixed into the document fails; `relate`
// is the one exception, see below), a refusal
// for a caller without the right (VIEWER on writes, a restricted guest and a
// non-member on reads) and an unknown identifier. Deletes (unrelate, unlink,
// detach) are deliberately out of scope here.
//
// All accounts, tokens and records are synthetic fixtures. No provider (GitHub
// or GitLab) is contacted: with no stored credential the code-link routes
// refuse before any outbound request, which is the boundary asserted.

import (
	"bytes"
	"context"
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
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestAcceptanceIssueSubresources(t *testing.T) {
	t.Setenv("CREWSHIP_RATELIMIT_DISABLED", "true")
	db := testutil.MigratedDB(t).DB
	sql := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	sql(`INSERT INTO workspaces(id,name,slug) VALUES('csubresourcews0000000001','Subresource fixture','csubresourcews0000000001'),('other-ws','Other fixture','other-ws')`)
	sql(`INSERT INTO users(id,email,full_name) VALUES
		('sub-owner','owner@sub.invalid','Owner Fixture'),
		('sub-viewer','viewer@sub.invalid','Viewer Fixture'),
		('sub-guest','guest@sub.invalid','Guest Fixture'),
		('sub-outsider','outsider@sub.invalid','Outsider Fixture')`)
	sql(`INSERT INTO workspace_members(id,user_id,workspace_id,role) VALUES
		('m-owner','sub-owner','csubresourcews0000000001','OWNER'),
		('m-viewer','sub-viewer','csubresourcews0000000001','VIEWER'),
		('m-guest','sub-guest','csubresourcews0000000001','VIEWER'),
		('m-outsider','sub-outsider','other-ws','OWNER')`)
	sql(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('sub-crew','csubresourcews0000000001','Sub crew','sub')`)
	sql(`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role) VALUES('sub-lead','csubresourcews0000000001','sub-crew','Lead fixture','lead','LEAD')`)
	sql(`INSERT INTO projects(id,workspace_id,name,slug) VALUES('sub-project','csubresourcews0000000001','Sub project','sub-project')`)
	mission := func(id, identifier, title string, number int, parent any) {
		t.Helper()
		sql(`INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title,status,number,identifier,priority,sort_order,mission_type,project_id,parent_issue_id,created_at,updated_at)
			VALUES(?,'csubresourcews0000000001','sub-crew','sub-lead',?,?,'BACKLOG',?,?,'medium',0,'issue','sub-project',?,datetime('now'),datetime('now'))`,
			id, "trace-"+id, title, number, identifier, parent)
	}
	mission("sub-1", "SUB-1", "PRIVATE_PARENT_TITLE", 1, nil)
	mission("sub-2", "SUB-2", "Child fixture", 2, "sub-1")
	mission("sub-3", "SUB-3", "Relation target fixture", 3, nil)
	sql(`INSERT INTO mission_code_links(id,workspace_id,mission_id,provider,host,owner,repo,number,kind,url,title,state,created_by_user_id)
		VALUES('sub-link-1','csubresourcews0000000001','sub-1','GITHUB','github.com','acme','thing',7,'pull_request','https://github.com/acme/thing/pull/7','Fixture PR','OPEN','sub-owner')`)

	// The guest is a restricted member with an explicit read share on the
	// project — the narrowest real grant — so its refusal is the product's
	// restricted-access boundary, not merely "no membership".
	store := access.Store{DB: db}
	guestMember, err := store.Membership(t.Context(), "sub-guest", "csubresourcews0000000001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Replace(t.Context(), "sub-owner", "sub-guest", "csubresourcews0000000001", "restricted", guestMember,
		[]access.Right{{Kind: "project", ID: "sub-project", Operation: "read"}}); err != nil {
		t.Fatal(err)
	}

	tokens := map[string]string{}
	for i, user := range []string{"sub-owner", "sub-viewer", "sub-guest", "sub-outsider"} {
		tokens[user] = "crewship_cli_" + strings.Repeat(string(rune('a'+i)), 48)
		sql(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES(?,?,'fixture',?,datetime('now'))`,
			"tok-"+user, user, sha256HexToken(tokens[user]))
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	writer := journal.NewWriter(db, logger, journal.WriterOptions{})
	t.Cleanup(func() { _ = writer.Close() })
	router, err := api.NewRouter(db, "subresource-cli-router-fixture-secret", logger, api.WithJournal(writer), api.WithStoragePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)

	type call struct {
		method, path string
		status       int
	}
	var mu sync.Mutex
	var observed []call
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, r)
		mu.Lock()
		observed = append(observed, call{r.Method, r.URL.Path, rec.Code})
		mu.Unlock()
		for k, vs := range rec.Header() {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}))
	t.Cleanup(server.Close)
	binary := buildCrewshipBinary(t)
	workDir := t.TempDir()

	type result struct {
		stdout, stderr string
		last           call
	}
	// run drives the binary as `user` and asserts the exit status matches
	// the final HTTP status, and that the final request hit `wantPath`
	// (suffix match), so a refusal is proven to come from the route under
	// test rather than an earlier lookup. The workspace id is CUID-shaped so the
	// CLI sends it as-is instead of resolving a slug through /workspaces —
	// otherwise a non-member would be refused client-side and never reach the
	// server-side membership check this test asserts.
	run := func(user, workspace string, wantStatus int, wantPath string, args ...string) result {
		t.Helper()
		mu.Lock()
		observed = nil
		mu.Unlock()
		cfg := filepath.Join(t.TempDir(), "cli.yaml")
		if err := os.WriteFile(cfg, []byte("server: "+server.URL+"\nworkspace: "+workspace+"\ntoken: "+tokens[user]+"\nformat: table\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = workDir
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_WORKSPACE=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "NO_COLOR=1")
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		runErr := cmd.Run()
		mu.Lock()
		calls := append([]call(nil), observed...)
		mu.Unlock()
		if (runErr == nil) != (wantStatus < 400) {
			t.Fatalf("%s %v: exit=%v want HTTP %d\ncalls=%v\nstdout=%s\nstderr=%s", user, args, runErr, wantStatus, calls, stdout.String(), stderr.String())
		}
		if len(calls) == 0 {
			t.Fatalf("%s %v: no HTTP request reached the router", user, args)
		}
		last := calls[len(calls)-1]
		if last.status != wantStatus || !strings.HasSuffix(last.path, wantPath) {
			t.Fatalf("%s %v: final call %v, want HTTP %d on …%s\nall=%v\nstderr=%s", user, args, last, wantStatus, wantPath, calls, stderr.String())
		}
		if user != "sub-owner" && user != "sub-viewer" && strings.Contains(stdout.String()+stderr.String(), "PRIVATE_PARENT_TITLE") {
			t.Fatalf("%s %v leaked the issue title", user, args)
		}
		return result{stdout.String(), stderr.String(), last}
	}
	owner := func(status int, path string, args ...string) result {
		t.Helper()
		return run("sub-owner", "csubresourcews0000000001", status, path, args...)
	}
	decode := func(r result, v any) {
		t.Helper()
		if err := json.Unmarshal([]byte(r.stdout), v); err != nil {
			t.Fatalf("stdout is not JSON: %v\nstdout=%q\nstderr=%q", err, r.stdout, r.stderr)
		}
	}
	const base = "/crews/sub-crew/issues/SUB-1"

	// ── comments + activity ─────────────────────────────────────────────
	owner(201, base+"/comments", "issue", "comment", "SUB-1", "fixture comment body")
	var comments []map[string]any
	decode(owner(200, base+"/comments", "issue", "comments", "SUB-1", "--format", "json"), &comments)
	if len(comments) != 1 || comments[0]["body"] != "fixture comment body" || comments[0]["author_id"] != "sub-owner" {
		t.Fatalf("comments json = %v", comments)
	}
	if table := owner(200, base+"/comments", "issue", "comments", "SUB-1"); !strings.Contains(table.stdout, "fixture comment body") || !strings.Contains(table.stdout, "AUTHOR") {
		t.Fatalf("comments table = %q", table.stdout)
	}
	var activity []map[string]any
	decode(owner(200, base+"/activity", "issue", "activity", "SUB-1", "--format", "json"), &activity)
	if activity == nil {
		t.Fatalf("activity json must be an array, got %q", "null")
	}
	viewerComments := run("sub-viewer", "csubresourcews0000000001", 200, base+"/comments", "issue", "comments", "SUB-1", "-f", "json")
	if !strings.Contains(viewerComments.stdout, "fixture comment body") {
		t.Fatalf("viewer comments = %q", viewerComments.stdout)
	}

	// ── subtasks ────────────────────────────────────────────────────────
	var subtasks []map[string]any
	decode(owner(200, base+"/subtasks", "issue", "subtasks", "SUB-1", "--format", "json"), &subtasks)
	if len(subtasks) != 1 || subtasks[0]["identifier"] != "SUB-2" {
		t.Fatalf("subtasks json = %v", subtasks)
	}

	// ── relations ───────────────────────────────────────────────────────
	run("sub-viewer", "csubresourcews0000000001", 403, base+"/relations", "issue", "relate", "SUB-1", "SUB-3", "--type", "blocks")
	// `issue relate` does not yet put its result on stdout under -f json (it
	// lives in cmd_issue_extra.go, which another open PR changes); the
	// relation id is read back through `issue relations` instead. When relate
	// is brought onto the format contract, assert its stdout here.
	owner(201, base+"/relations", "issue", "relate", "SUB-1", "SUB-3", "--type", "blocks")
	if human := owner(201, base+"/relations", "issue", "relate", "SUB-1", "SUB-2", "--type", "relates-to"); human.stdout != "" || !strings.Contains(human.stderr, "Created relates_to relation") {
		t.Fatalf("relate human receipt moved: stdout=%q stderr=%q", human.stdout, human.stderr)
	}
	if dup := owner(409, base+"/relations", "issue", "relate", "SUB-1", "SUB-3", "--type", "blocks"); !strings.Contains(dup.stderr, "Relation already exists") {
		t.Fatalf("duplicate relate error = %q", dup.stderr)
	}
	var relations []map[string]any
	decode(owner(200, base+"/relations", "issue", "relations", "SUB-1", "--format", "json"), &relations)
	byTarget := map[string]string{}
	for _, rel := range relations {
		if id, _ := rel["id"].(string); id == "" {
			t.Fatalf("relation without id: %v", rel)
		}
		byTarget[rel["target_identifier"].(string)] = rel["relation_type"].(string)
	}
	if len(relations) != 2 || byTarget["SUB-3"] != "blocks" || byTarget["SUB-2"] != "relates_to" {
		t.Fatalf("relations json = %v", relations)
	}

	// ── code links ──────────────────────────────────────────────────────
	var links []map[string]any
	decode(owner(200, base+"/code-links", "issue", "links", "SUB-1", "--format", "json"), &links)
	if len(links) != 1 || links[0]["id"] != "sub-link-1" || links[0]["state"] != "OPEN" || links[0]["number"] != float64(7) {
		t.Fatalf("links json = %v", links)
	}
	if table := owner(200, base+"/code-links", "issue", "links", "SUB-1"); !strings.Contains(table.stdout, "acme/thing#7") {
		t.Fatalf("links table = %q", table.stdout)
	}
	run("sub-viewer", "csubresourcews0000000001", 403, base+"/code-links", "issue", "link", "SUB-1", "https://github.com/acme/thing/pull/8")
	if noCred := owner(412, base+"/code-links", "issue", "link", "SUB-1", "https://github.com/acme/thing/pull/8"); !strings.Contains(noCred.stderr, "No ACTIVE") {
		t.Fatalf("link without credential error = %q", noCred.stderr)
	}
	if bad := owner(400, base+"/code-links", "issue", "link", "SUB-1", "https://example.com/not-a-pr"); bad.stderr == "" {
		t.Fatal("unsupported URL produced no error text")
	}
	if dup := owner(409, base+"/code-links", "issue", "link", "SUB-1", "https://github.com/acme/thing/pull/7"); !strings.Contains(dup.stderr, "already linked") {
		t.Fatalf("duplicate link error = %q", dup.stderr)
	}
	run("sub-viewer", "csubresourcews0000000001", 403, base+"/code-links/sub-link-1/refresh", "issue", "relink", "SUB-1", "sub-link-1")
	if noCred := owner(412, base+"/code-links/sub-link-1/refresh", "issue", "relink", "SUB-1", "sub-link-1"); !strings.Contains(noCred.stderr, "No ACTIVE") {
		t.Fatalf("relink without credential error = %q", noCred.stderr)
	}
	if missing := owner(404, base+"/code-links/nope/refresh", "issue", "relink", "SUB-1", "nope"); !strings.Contains(missing.stderr, "Code link not found") {
		t.Fatalf("relink unknown link error = %q", missing.stderr)
	}

	// ── attachments ─────────────────────────────────────────────────────
	payload := []byte("fixture log line\n")
	if err := os.WriteFile(filepath.Join(workDir, "fixture.log"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	run("sub-viewer", "csubresourcews0000000001", 403, base+"/attachments", "issue", "attach", "SUB-1", "fixture.log")
	var att map[string]any
	attached := owner(201, base+"/attachments", "issue", "attach", "SUB-1", "fixture.log", "--format", "json")
	if attached.stderr != "" {
		t.Fatalf("attach --format json wrote to stderr (no receipt expected): %q", attached.stderr)
	}
	decode(attached, &att)
	attID, _ := att["id"].(string)
	if attID == "" || att["filename"] != "fixture.log" || att["size_bytes"] != float64(len(payload)) {
		t.Fatalf("attach json = %v", att)
	}
	var atts []map[string]any
	decode(owner(200, base+"/attachments", "issue", "attachments", "SUB-1", "--format", "json"), &atts)
	if len(atts) != 1 || atts[0]["id"] != attID {
		t.Fatalf("attachments json = %v", atts)
	}
	if got := run("sub-viewer", "csubresourcews0000000001", 200, base+"/attachments/"+attID, "issue", "attachment", "SUB-1", attID); got.stdout != string(payload) {
		t.Fatalf("attachment bytes = %q", got.stdout)
	}
	out := filepath.Join(t.TempDir(), "copy.log")
	owner(200, base+"/attachments/"+attID, "issue", "attachment", "SUB-1", attID, "-o", out)
	if got, err := os.ReadFile(out); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("attachment -o wrote %q, %v", got, err)
	}
	if missing := owner(404, base+"/attachments/nope", "issue", "attachment", "SUB-1", "nope"); missing.stderr == "" || missing.stdout != "" {
		t.Fatalf("unknown attachment: stdout=%q stderr=%q", missing.stdout, missing.stderr)
	}

	// ── refusals before the sub-resource: guest, non-member, unknown id ─
	readVerbs := [][]string{
		{"issue", "comments", "SUB-1"},
		{"issue", "activity", "SUB-1"},
		{"issue", "subtasks", "SUB-1"},
		{"issue", "relations", "SUB-1"},
		{"issue", "links", "SUB-1"},
		{"issue", "attachments", "SUB-1"},
		{"issue", "attachment", "SUB-1", attID},
	}
	for _, args := range readVerbs {
		guest := run("sub-guest", "csubresourcews0000000001", 404, "/issues/SUB-1", args...)
		if guest.stdout != "" {
			t.Fatalf("guest %v printed %q", args, guest.stdout)
		}
		run("sub-outsider", "csubresourcews0000000001", 403, "/issues/SUB-1", args...)
		unknown := append(append([]string{}, args...), "-f", "json")
		unknown[2] = "SUB-999"
		if res := owner(404, "/issues/SUB-999", unknown...); !strings.Contains(res.stderr, "not found") || res.stdout != "" {
			t.Fatalf("unknown identifier %v: stdout=%q stderr=%q", unknown, res.stdout, res.stderr)
		}
	}
}

// TestAcceptanceIssueSubresources_LinkJSON covers the one success path the
// real-router test above cannot: `issue link` and `issue relink` succeeding
// needs a provider fetch, and no GitHub/GitLab is reachable from a test. A
// stub server stands in for the Crewship API (the provider call happens
// server-side, so the CLI contract is identical) and the built binary must
// put the link document alone on STDOUT under -f json with nothing on
// stderr (AutoHuman skips the receipt for machine formats), and put the
// human receipt on stderr only in the default human format.
func TestAcceptanceIssueSubresources_LinkJSON(t *testing.T) {
	link := `{"id":"lnk_1","provider":"GITHUB","host":"github.com","owner":"acme","repo":"thing","number":8,` +
		`"url":"https://github.com/acme/thing/pull/8","title":"Stub PR","state":"MERGED"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/issues/SUB-1":
			_, _ = w.Write([]byte(`{"id":"sub-1","crew_id":"sub-crew","identifier":"SUB-1","title":"t","status":"BACKLOG"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/crews/sub-crew/issues/SUB-1/code-links":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(link))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/crews/sub-crew/issues/SUB-1/code-links/lnk_1/refresh":
			_, _ = w.Write([]byte(link))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"no stub for ` + r.URL.Path + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	binary := buildCrewshipBinary(t)
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: csubresourcews0000000001\ntoken: fake-token\nformat: table\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, string) {
		t.Helper()
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_WORKSPACE=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "NO_COLOR=1")
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("%v: %v\nstderr=%s", args, err, stderr.String())
		}
		return stdout.String(), stderr.String()
	}
	for _, args := range [][]string{
		{"issue", "link", "SUB-1", "https://github.com/acme/thing/pull/8"},
		{"issue", "relink", "SUB-1", "lnk_1"},
	} {
		stdout, jsonStderr := run(append(args, "-f", "json")...)
		if jsonStderr != "" {
			t.Fatalf("%v -f json wrote to stderr (no receipt expected): %q", args, jsonStderr)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("%v -f json stdout is not JSON: %v\n%q", args, err, stdout)
		}
		if got["id"] != "lnk_1" || got["state"] != "MERGED" || got["number"] != float64(8) {
			t.Fatalf("%v -f json = %v", args, got)
		}
		stdout, stderr := run(args...)
		if stdout != "" || !strings.Contains(stderr, "acme/thing#8") {
			t.Fatalf("%v human receipt moved: stdout=%q stderr=%q", args, stdout, stderr)
		}
	}
}
