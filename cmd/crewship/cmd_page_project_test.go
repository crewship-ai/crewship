package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pages"
)

func cliProjectSource() *pages.SourceProject {
	return &pages.SourceProject{Format: pages.SourceProjectFormat, Runtime: pages.SourceProjectRuntime, Files: []pages.ProjectFile{
		{Path: "package.json", Encoding: "utf8", Content: "{}\n"},
		{Path: "pnpm-lock.yaml", Encoding: "utf8", Content: "lockfileVersion: '9.0'\n"},
		{Path: "index.html", Encoding: "utf8", Content: "<div>Český dashboard</div>\n"},
		{Path: "public/icon.bin", Encoding: "base64", Content: base64.StdEncoding.EncodeToString([]byte{0, 4, 255})},
	}}
}

func TestPageProjectCLIExportImportPreservesSource(t *testing.T) {
	stub := pageStub(t)
	var bundle pages.TransferBundle
	if err := json.Unmarshal([]byte(pageXferBundle), &bundle); err != nil {
		t.Fatal(err)
	}
	bundle.Format = pages.TransferV2
	bundle.Project = cliProjectSource()
	bundle.Page.Panels[0].Actions = []pages.PanelAction{{ID: "run", Kind: pages.ActionCall, Label: "Spustit", Routine: "check"}}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	stub.OnGet(pageExportRoute, func(*http.Request, []byte) (int, []byte, string) { return 200, raw, "application/json" })
	stub.OnPost(pageImportRoute, func(*http.Request, []byte) (int, []byte, string) {
		return 201, []byte(`{"slug":"copy","panels":[]}`), "application/json"
	})
	out, err := runPageTransferCLI(t, "", "page", "export", pageXferSlug)
	if err != nil {
		t.Fatal(err)
	}
	file := pageBundleFile(t, out)
	if _, err := runPageTransferCLI(t, "", "page", "import", file, "--slug", "copy"); err != nil {
		t.Fatal(err)
	}
	calls := stub.CallsFor("POST", pageImportRoute)
	if len(calls) != 1 {
		t.Fatal("missing import request")
	}
	var req pages.TransferImport
	if err := json.Unmarshal(calls[0].Body, &req); err != nil {
		t.Fatal(err)
	}
	want, _ := bundle.Project.Digest()
	got, err := req.Project.Digest()
	if err != nil || got != want {
		t.Fatalf("source lost: %v", err)
	}
	if len(req.Page.Panels[0].Actions) != 1 || req.Page.Panels[0].Actions[0].Routine != "check" {
		t.Fatal("action lost")
	}
	if !strings.Contains(out, "crewship-page-bundle/v2") {
		t.Fatal("wrong bundle format")
	}
}

func TestPageProjectCLISetPassesExpectedRevision(t *testing.T) {
	stub := pageStub(t)
	route := "/api/v1/pages/health/project"
	stub.OnPut(route, func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(`{"revision":1,"state":"draft"}`), "application/json"
	})
	b, err := cliProjectSource().MarshalYAMLSource()
	if err != nil {
		t.Fatal(err)
	}
	file := pageBundleFile(t, string(b))
	if _, err := runPageCLI(t, "", "page", "project", "set", "health", "--file", file, "--revision", "0"); err != nil {
		t.Fatal(err)
	}
	calls := stub.CallsFor("PUT", route)
	if len(calls) != 1 {
		t.Fatal("missing save")
	}
	var req struct {
		Revision *int64               `json:"expected_revision"`
		Project  *pages.SourceProject `json:"project"`
	}
	if err := json.Unmarshal(calls[0].Body, &req); err != nil {
		t.Fatal(err)
	}
	if req.Revision == nil || *req.Revision != 0 || req.Project == nil {
		t.Fatal("missing source/revision")
	}
	// Restore flags held by the process-global command for other CLI tests.
	project := findSubcommand(pageCmd.Commands(), "project")
	set := findSubcommand(project.Commands(), "set")
	_ = set.Flags().Set("file", "")
	_ = set.Flags().Set("revision", "-1")
}

func TestPageProjectCLIRejectsUnknownV2Fields(t *testing.T) {
	var bundle pages.TransferBundle
	if err := json.Unmarshal([]byte(pageXferBundle), &bundle); err != nil {
		t.Fatal(err)
	}
	bundle.Format = pages.TransferV2
	bundle.Project = cliProjectSource()
	raw, err := pages.MarshalProjectTransfer(bundle)
	if err != nil {
		t.Fatal(err)
	}
	file := pageBundleFile(t, string(raw)+"grants: admin\n")
	if _, err := pageReadBundle(file); err == nil {
		t.Fatal("unknown privilege-bearing field accepted")
	}
}

func TestPageProjectCLIGetHonoursFormat(t *testing.T) {
	stub := pageStub(t)
	stub.OnGet("/api/v1/pages/health/project", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(`{"revision":2,"digest":"test"}`), "application/json"
	})
	for _, format := range []string{"json", "yaml", "ndjson"} {
		out, err := runPageCLI(t, "", "page", "project", "get", "health", "--format", format)
		if err != nil {
			t.Fatal(err)
		}
		if format == "yaml" {
			if !strings.Contains(out, "revision: 2") {
				t.Fatalf("YAML: %s", out)
			}
		} else if !json.Valid([]byte(out)) {
			t.Fatalf("%s: %s", format, out)
		}
	}
}

func TestPageProjectGetSourceOnly(t *testing.T) {
	for _, revision := range []string{"", "3"} {
		t.Run("revision="+revision, func(t *testing.T) {
			stub := pageStub(t)
			route := "/api/v1/pages/health/project"
			args := []string{"page", "project", "get", "health", "--source-only"}
			if revision != "" {
				route += "/history/" + revision
				args = append(args, "--revision", revision)
			}
			route += "/source"
			stub.OnGet(route, func(*http.Request, []byte) (int, []byte, string) {
				return 200, []byte(`{"revision":3,"project":{"files":[]}}`), "application/json"
			})
			if _, err := runPageTransferCLI(t, "", args...); err != nil {
				t.Fatal(err)
			}
			if len(stub.CallsFor("GET", route)) != 1 {
				t.Fatal("source-only endpoint was not read")
			}
		})
	}
}
