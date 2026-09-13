package main

// cmd_page_folder_acl_test.go — the client half of `crewship page folder
// acl|share|unshare`, the batch `page move`, `page grant --workspace` and
// `page access --me` (#2533). The endpoints are proved in
// internal/api/pages_folder_acl_test.go; this file proves what only the CLI
// can: the subject spelling is parsed here and an agent refused before any
// request, --edit sends can_write and --view does not, the batch move reads
// every fence and sends them in one request, the workspace subject travels
// without a subject id, and a 409 naming a page is rendered with it.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// runPageAccessCLI is runPageCLI with the access command's flags reset first
// (see runPageGrantCLI for why cobra needs that between Execute calls).
func runPageAccessCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	if page := findSubcommand(rootCmd.Commands(), "page"); page != nil {
		if access := findSubcommand(page.Commands(), "access"); access != nil {
			access.Flags().VisitAll(func(f *pflag.Flag) {
				_ = f.Value.Set(f.DefValue)
				f.Changed = false
			})
		}
	}
	return runPageCLI(t, "", args...)
}

func pageFolderStubACL() []byte {
	return []byte(`{"folder":"ops-board","acl_version":3,"acl":[
		{"subject_type":"user","subject_id":"u1","label":"ada@example.com","can_read":true,"can_write":true,"set_by":"root@example.com","set_at":"2026-09-13T06:00:00Z"},
		{"subject_type":"workspace","subject_id":"","label":"workspace","can_read":true,"can_write":false,"set_by":"","set_at":"2026-09-13T06:01:00Z"}]}`)
}

func TestPageFolderCLI_ShareParsesTheSubjectAndSendsTheRight(t *testing.T) {
	stub := pageStub(t)
	var body map[string]any
	stub.OnPut("/api/v1/page-folders/ops-board/acl", func(_ *http.Request, raw []byte) (int, []byte, string) {
		_ = json.Unmarshal(raw, &body)
		return http.StatusOK, pageFolderStubACL(), "application/json"
	})
	cases := []struct {
		args    []string
		subject map[string]any
		says    string
	}{
		{[]string{"user:ada@example.com", "--edit"}, map[string]any{"subject_type": "user", "subject_id": "ada@example.com", "can_write": true}, "can view and edit"},
		{[]string{"crew:support", "--view"}, map[string]any{"subject_type": "crew", "subject_id": "support", "can_write": false}, "crew:support: can view"},
		{[]string{"workspace", "--view"}, map[string]any{"subject_type": "workspace", "can_write": false}, "everyone in the workspace"},
	}
	for _, tc := range cases {
		body = nil
		out, err := runPageFolderCLI(t, append([]string{"page", "folder", "share", "ops-board"}, tc.args...)...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", tc.args, err, out)
		}
		for k, want := range tc.subject {
			if body[k] != want {
				t.Errorf("%v sent %s = %v, want %v", tc.args, k, body[k], want)
			}
		}
		if _, present := body["subject_id"]; present && tc.subject["subject_type"] == "workspace" {
			t.Errorf("the workspace subject sent a subject_id: %v", body)
		}
		if !strings.Contains(out, tc.says) || !strings.Contains(out, "ada@example.com") || !strings.Contains(out, "acl_version 3") {
			t.Errorf("%v output: %s", tc.args, out)
		}
	}
	// Refused locally, before any request.
	for _, tc := range []struct {
		args []string
		says string
	}{
		{[]string{"page", "folder", "share", "ops-board", "user:ada@example.com"}, "--view"},
		{[]string{"page", "folder", "share", "ops-board", "user:ada@example.com", "--view", "--edit"}, "both given"},
		{[]string{"page", "folder", "share", "ops-board", "agent:watcher", "--view"}, "never shared with an agent"},
		{[]string{"page", "folder", "share", "ops-board", "ada@example.com", "--view"}, "user:<email or id>"},
		{[]string{"page", "folder", "share", "ops-board", "user:", "--view"}, "user:<email or id>"},
	} {
		stub.ResetCalls()
		if _, err := runPageFolderCLI(t, tc.args...); err == nil || !strings.Contains(err.Error(), tc.says) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.says)
		}
		if len(stub.Calls()) != 0 {
			t.Errorf("%v reached the server", tc.args)
		}
	}
}

func TestPageFolderCLI_UnshareAndACL(t *testing.T) {
	stub := pageStub(t)
	stub.OnDelete("/api/v1/page-folders/ops-board/acl/crew/support", func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusNoContent, nil, ""
	})
	stub.OnDelete("/api/v1/page-folders/ops-board/acl/workspace/workspace", func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusNoContent, nil, ""
	})
	stub.OnGet("/api/v1/page-folders/ops-board/acl", func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusOK, pageFolderStubACL(), "application/json"
	})
	out, err := runPageFolderCLI(t, "page", "folder", "unshare", "ops-board", "crew:support")
	if err != nil || !strings.Contains(out, "no longer shared with crew:support") {
		t.Errorf("unshare crew: %v %s", err, out)
	}
	out, err = runPageFolderCLI(t, "page", "folder", "unshare", "ops-board", "workspace")
	if err != nil || !strings.Contains(out, "everyone in the workspace") {
		t.Errorf("unshare workspace: %v %s", err, out)
	}
	out, err = runPageFolderCLI(t, "page", "folder", "acl", "ops-board")
	if err != nil {
		t.Fatalf("acl: %v %s", err, out)
	}
	for _, want := range []string{"user:ada@example.com", "can view and edit", "everyone in the workspace", "root@example.com", "acl_version 3"} {
		if !strings.Contains(out, want) {
			t.Errorf("acl output lacks %q: %s", want, out)
		}
	}
}

func TestPageFolderCLI_MoveManyReadsEveryFenceAndSendsOneBatch(t *testing.T) {
	stub := pageStub(t)
	for slug, version := range map[string]int{"a": 1, "b": 5} {
		stub.OnGet("/api/v1/pages/"+slug, func(_ *http.Request, _ []byte) (int, []byte, string) {
			return http.StatusOK, []byte(`{"slug":"` + slug + `","pages_version":` + string(rune('0'+version)) + `,"folder":null,"panels":[]}`), "application/json"
		})
	}
	stub.OnGet("/api/v1/page-folders/ops-board", func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusOK, pageFolderStubFolder("ops-board", 7, 0), "application/json"
	})
	var body map[string]any
	stub.OnPost("/api/v1/page-folders/ops-board/pages:batch", func(_ *http.Request, raw []byte) (int, []byte, string) {
		_ = json.Unmarshal(raw, &body)
		return http.StatusOK, []byte(`{"pages":[{"slug":"a","pages_version":2},{"slug":"b","pages_version":6}],"acl_version":7}`), "application/json"
	})
	out, err := runPageFolderCLI(t, "page", "move", "a", "b", "--folder", "ops-board")
	if err != nil {
		t.Fatalf("move many: %v\n%s", err, out)
	}
	pages, _ := body["pages"].([]any)
	if len(pages) != 2 || body["acl_version"] != float64(7) {
		t.Fatalf("batch body = %v", body)
	}
	first, second := pages[0].(map[string]any), pages[1].(map[string]any)
	if first["page"] != "a" || first["pages_version"] != float64(1) || second["page"] != "b" || second["pages_version"] != float64(5) {
		t.Errorf("batch body = %v, want the fences as read (a:1, b:5)", body)
	}
	calls := stub.Calls()
	if len(calls) != 4 || calls[3].Method != "POST" || !strings.HasSuffix(calls[3].Path, "pages:batch") {
		t.Errorf("calls: %+v, want GET a, GET b, GET folder, one POST", calls)
	}
	if !strings.Contains(out, "Filed 2 pages in folder ops-board") || !strings.Contains(out, "b (pages_version 6)") {
		t.Errorf("output: %s", out)
	}
	// A refused batch names the page.
	stub.OnPost("/api/v1/page-folders/ops-board/pages:batch", func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusConflict, []byte(`{"error":"the page's folder membership changed since you read it","conflict":"pages_version","page":"b","pages_version":6,"acl_version":7}`), "application/json"
	})
	if _, err := runPageFolderCLI(t, "page", "move", "a", "b", "--folder", "ops-board"); err == nil || !strings.Contains(err.Error(), "for page b") {
		t.Errorf("refused batch: err = %v, want the page named", err)
	}
}

func TestPageCLI_GrantWorkspaceTravelsWithoutASubject(t *testing.T) {
	stub := pageStub(t)
	var body map[string]any
	stub.OnPut("/api/v1/pages/fleet-201/grants", func(_ *http.Request, raw []byte) (int, []byte, string) {
		_ = json.Unmarshal(raw, &body)
		return http.StatusOK, []byte(`{"page":"fleet-201","grants":[{"subject_type":"workspace","subject":"workspace","subject_id":"","level":"read","granted_by":"root@example.com","live":true}]}`), "application/json"
	})
	out, err := runPageGrantCLI(t, "page", "grant", "fleet-201", "--workspace", "--level", "read")
	if err != nil {
		t.Fatalf("grant --workspace: %v\n%s", err, out)
	}
	if body["subject_type"] != "workspace" || body["subject"] != "" {
		t.Errorf("body = %v, want subject_type workspace and an empty subject", body)
	}
	if _, err := runPageGrantCLI(t, "page", "grant", "fleet-201", "--workspace", "--user", "ada@example.com", "--level", "read"); err == nil || !strings.Contains(err.Error(), "both given") {
		t.Errorf("two subjects: err = %v", err)
	}
	var query string
	stub.OnDelete("/api/v1/pages/fleet-201/grants", func(r *http.Request, _ []byte) (int, []byte, string) {
		query = r.URL.RawQuery
		return http.StatusOK, []byte(`{"page":"fleet-201","grants":[],"changed":2}`), "application/json"
	})
	out, err = runPageGrantCLI(t, "page", "revoke", "fleet-201", "--workspace")
	if err != nil || !strings.Contains(out, "everyone in the workspace") {
		t.Fatalf("revoke --workspace: %v\n%s", err, out)
	}
	if !strings.Contains(query, "subject_type=workspace") || strings.Contains(query, "subject=") {
		t.Errorf("revoke query = %q, want subject_type=workspace and no subject", query)
	}
}

func TestPageCLI_AccessMePrintsTheServersPaths(t *testing.T) {
	stub := pageStub(t)
	stub.OnGet("/api/v1/pages/fleet-201/access/me", func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusOK, []byte(`{"page":"fleet-201","subject_type":"user","subject_id":"u1","label":"ada@example.com","paths":["folder:ops-board","grant"],"folder":"ops-board","shared":"people"}`), "application/json"
	})
	out, err := runPageAccessCLI(t, "page", "access", "fleet-201", "--me")
	if err != nil {
		t.Fatalf("access --me: %v\n%s", err, out)
	}
	for _, want := range []string{"ada@example.com reaches page fleet-201 by: folder:ops-board, grant", "In folder ops-board (shared: people)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q: %s", want, out)
		}
	}
	// Without --me the same command is the owner's listing (page access,
	// #2528); --me asks one question about one page and refuses a --subject
	// beside it, locally, before anything reaches the server.
	stub.ResetCalls()
	if _, err := runPageAccessCLI(t, "page", "access", "fleet-201", "--me", "--subject", "user:ada@example.com"); err == nil || !strings.Contains(err.Error(), "--me") {
		t.Errorf("--me with --subject: err = %v", err)
	}
	if len(stub.Calls()) != 0 {
		t.Error("a refused invocation reached the server")
	}
}
