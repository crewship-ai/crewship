package main

// cmd_page_folder_test.go — the client half of `crewship page folder …` and
// `crewship page move` (#2527). The endpoints are proved in
// internal/api/pages_folders_test.go; this file proves what only the CLI can:
//
//   - a move READS both fences and sends exactly what it read, in order
//     (page, then folder, then the write), and never a zero it made up;
//   - --unfiled resolves the page's current folder from the page, so the
//     operator names no folder;
//   - update sends only the flags that were passed, and "" clears;
//   - a 409 carrying `conflict` is rendered with the current pair;
//   - a malformed invocation is refused before any request is sent.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// runPageFolderCLI is runPageCLI with the folder and move flags reset first
// (see runPageGrantCLI for why cobra needs that between Execute calls).
func runPageFolderCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	page := findSubcommand(rootCmd.Commands(), "page")
	if page == nil {
		return runPageCLI(t, "", args...)
	}
	var subs []*pflag.FlagSet
	if move := findSubcommand(page.Commands(), "move"); move != nil {
		subs = append(subs, move.Flags())
	}
	if folder := findSubcommand(page.Commands(), "folder"); folder != nil {
		for _, sub := range folder.Commands() {
			subs = append(subs, sub.Flags())
		}
	}
	for _, fs := range subs {
		fs.VisitAll(func(f *pflag.Flag) {
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		})
	}
	return runPageCLI(t, "", args...)
}

func pageFolderStubFolder(slug string, grants int64, count int) []byte {
	return []byte(`{"id":"fld_1","slug":"` + slug + `","name":"Ops board","icon":"rocket","color":"amber",
		"owner":"crew/ops","owner_crew_name":"Operations","page_count":` + strconv.Itoa(count) + `,
		"shared":"none","acl_version":` + strconv.FormatInt(grants, 10) + `,"created_at":"2026-09-13T06:00:00Z","updated_at":"2026-09-13T06:00:00Z","pages":[]}`)
}

func TestPageFolderCLI_CreateSendsSlugNameOwnerIconAndColor(t *testing.T) {
	stub := pageStub(t)
	var body map[string]any
	stub.OnPost("/api/v1/page-folders", func(_ *http.Request, raw []byte) (int, []byte, string) {
		_ = json.Unmarshal(raw, &body)
		return http.StatusCreated, pageFolderStubFolder("ops-board", 0, 0), "application/json"
	})
	out, err := runPageFolderCLI(t, "page", "folder", "create", "ops-board", "--name", "Ops board",
		"--owner", "ops", "--icon", "rocket", "--color", "amber")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	for k, want := range map[string]any{"slug": "ops-board", "name": "Ops board", "owner": "crew/ops", "icon": "rocket", "color": "amber"} {
		if body[k] != want {
			t.Errorf("body %s = %v, want %v", k, body[k], want)
		}
	}
	if !strings.Contains(out, "Created folder ops-board") {
		t.Errorf("output: %s", out)
	}
	// --owner is required and refused locally.
	stub.ResetCalls()
	if _, err := runPageFolderCLI(t, "page", "folder", "create", "x"); err == nil || !strings.Contains(err.Error(), "--owner") {
		t.Errorf("create without --owner: err = %v", err)
	}
	if n := len(stub.Calls()); n != 0 {
		t.Errorf("a refused invocation still sent %d request(s)", n)
	}
}

func TestPageFolderCLI_AddReadsBothFencesThenWrites(t *testing.T) {
	stub := pageStub(t)
	stub.OnGet("/api/v1/pages/fleet-201", func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusOK, []byte(`{"slug":"fleet-201","pages_version":3,"folder":null,"panels":[]}`), "application/json"
	})
	stub.OnGet("/api/v1/page-folders/ops-board", func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusOK, pageFolderStubFolder("ops-board", 2, 0), "application/json"
	})
	var body map[string]any
	stub.OnPost("/api/v1/page-folders/ops-board/pages", func(_ *http.Request, raw []byte) (int, []byte, string) {
		_ = json.Unmarshal(raw, &body)
		return http.StatusOK, []byte(`{"page":{"slug":"fleet-201","pages_version":4,"folder":{"slug":"ops-board"}}}`), "application/json"
	})
	for _, args := range [][]string{
		{"page", "folder", "add", "ops-board", "fleet-201"},
		{"page", "move", "fleet-201", "--folder", "ops-board"},
	} {
		stub.ResetCalls()
		body = nil
		out, err := runPageFolderCLI(t, args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		if body["page"] != "fleet-201" || body["pages_version"] != float64(3) || body["acl_version"] != float64(2) {
			t.Errorf("%v sent %v, want page fleet-201 with the fences it read (3, 2)", args, body)
		}
		calls := stub.Calls()
		if len(calls) != 3 || calls[0].Method != "GET" || calls[1].Method != "GET" || calls[2].Method != "POST" {
			t.Errorf("%v made %d calls, want GET page, GET folder, POST: %+v", args, len(calls), calls)
		}
		if !strings.Contains(out, "Filed page fleet-201 in folder ops-board (pages_version 4)") {
			t.Errorf("%v output: %s", args, out)
		}
	}
}

func TestPageFolderCLI_UnfiledResolvesTheFolderFromThePage(t *testing.T) {
	stub := pageStub(t)
	stub.OnGet("/api/v1/pages/fleet-201", func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusOK, []byte(`{"slug":"fleet-201","pages_version":4,"folder":{"slug":"ops-board","name":"Ops board"},"panels":[]}`), "application/json"
	})
	var body map[string]any
	stub.OnDelete("/api/v1/page-folders/ops-board/pages/fleet-201", func(_ *http.Request, raw []byte) (int, []byte, string) {
		_ = json.Unmarshal(raw, &body)
		return http.StatusOK, []byte(`{"page":{"slug":"fleet-201","pages_version":5,"folder":null}}`), "application/json"
	})
	out, err := runPageFolderCLI(t, "page", "move", "fleet-201", "--unfiled")
	if err != nil {
		t.Fatalf("move --unfiled: %v\n%s", err, out)
	}
	if body["pages_version"] != float64(4) {
		t.Errorf("DELETE body = %v, want the pages_version it read (4)", body)
	}
	if !strings.Contains(out, "Took page fleet-201 out of folder ops-board (pages_version 5)") {
		t.Errorf("output: %s", out)
	}
	// The explicit form names the folder and reads only the page.
	stub.ResetCalls()
	if out, err := runPageFolderCLI(t, "page", "folder", "remove", "ops-board", "fleet-201"); err != nil {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	if calls := stub.Calls(); len(calls) != 2 || calls[1].Method != "DELETE" {
		t.Errorf("remove made %+v, want GET page then DELETE", calls)
	}
	// Both destinations at once is refused before any request.
	stub.ResetCalls()
	if _, err := runPageFolderCLI(t, "page", "move", "fleet-201", "--folder", "x", "--unfiled"); err == nil || !strings.Contains(err.Error(), "both") {
		t.Errorf("move with both flags: err = %v", err)
	}
	if _, err := runPageFolderCLI(t, "page", "move", "fleet-201"); err == nil || !strings.Contains(err.Error(), "--folder") {
		t.Errorf("move with neither flag: err = %v", err)
	}
	if n := len(stub.Calls()); n != 0 {
		t.Errorf("refused invocations sent %d request(s)", n)
	}
}

func TestPageFolderCLI_UpdateSendsOnlyWhatChangedAndEmptyClears(t *testing.T) {
	stub := pageStub(t)
	var raw []byte
	stub.OnPatch("/api/v1/page-folders/ops-board", func(_ *http.Request, body []byte) (int, []byte, string) {
		raw = body
		return http.StatusOK, pageFolderStubFolder("ops-board", 0, 1), "application/json"
	})
	if out, err := runPageFolderCLI(t, "page", "folder", "update", "ops-board", "--icon", ""); err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(raw)); got != `{"icon":""}` {
		t.Errorf("PATCH body = %s, want exactly {\"icon\":\"\"}", got)
	}
	stub.ResetCalls()
	if _, err := runPageFolderCLI(t, "page", "folder", "update", "ops-board"); err == nil || !strings.Contains(err.Error(), "nothing to change") {
		t.Errorf("update with no flags: err = %v", err)
	}
	if n := len(stub.Calls()); n != 0 {
		t.Errorf("a refused update still sent %d request(s)", n)
	}
}

func TestPageFolderCLI_ListShowAndDelete(t *testing.T) {
	stub := pageStub(t)
	stub.OnGet("/api/v1/page-folders", func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusOK, []byte(`{"folders":[{"slug":"ops-board","name":"Ops board","owner":"crew/ops","page_count":2,"icon":"rocket","color":"amber"},
			{"slug":"empty","name":"Empty","owner":"crew/ops","page_count":0,"icon":"","color":""}]}`), "application/json"
	})
	out, err := runPageFolderCLI(t, "page", "folder", "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	for _, want := range []string{"SLUG", "PAGES", "ops-board", "crew/ops", "rocket", "empty"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output lacks %q:\n%s", want, out)
		}
	}
	stub.OnGet("/api/v1/page-folders/ops-board", func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusOK, []byte(`{"slug":"ops-board","name":"Ops board","owner":"crew/ops","owner_crew_name":"Operations","page_count":1,
			"pages":[{"slug":"fleet-201","name":"Fleet","owner":"user/alice","state":"fresh","reach":["owner"]}]}`), "application/json"
	})
	out, err = runPageFolderCLI(t, "page", "folder", "show", "ops-board")
	if err != nil {
		t.Fatalf("show: %v\n%s", err, out)
	}
	for _, want := range []string{"Ops board", "Operations", "fleet-201", "owner"} {
		if !strings.Contains(out, want) {
			t.Errorf("show output lacks %q:\n%s", want, out)
		}
	}
	stub.OnDelete("/api/v1/page-folders/ops-board", func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusNoContent, nil, "application/json"
	})
	if out, err := runPageFolderCLI(t, "page", "folder", "delete", "ops-board", "--yes"); err != nil || !strings.Contains(out, "Deleted folder ops-board") {
		t.Errorf("delete --yes: err %v, out %s", err, out)
	}
}

func TestPageFolderCLI_ConflictRendersTheCurrentPair(t *testing.T) {
	stub := pageStub(t)
	stub.OnGet("/api/v1/pages/fleet-201", func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusOK, []byte(`{"slug":"fleet-201","pages_version":3,"folder":null,"panels":[]}`), "application/json"
	})
	stub.OnGet("/api/v1/page-folders/ops-board", func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusOK, pageFolderStubFolder("ops-board", 2, 0), "application/json"
	})
	stub.OnPost("/api/v1/page-folders/ops-board/pages", func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusConflict, []byte(`{"error":"the page's folder membership changed since you read it","conflict":"pages_version","pages_version":4,"acl_version":2}`), "application/json"
	})
	_, err := runPageFolderCLI(t, "page", "move", "fleet-201", "--folder", "ops-board")
	if err == nil {
		t.Fatal("a 409 did not surface as an error")
	}
	for _, want := range []string{"changed since you read it", "pages_version 4", "acl_version 2", "re-run"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("conflict error lacks %q: %v", want, err)
		}
	}
}
