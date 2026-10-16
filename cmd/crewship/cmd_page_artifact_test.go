package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func resetPageArtifactCommand(t *testing.T) {
	t.Helper()
	old := findSubcommand(pageCmd.Commands(), "artifact")
	fresh := newPageArtifactCommand()
	pageCmd.RemoveCommand(old)
	pageCmd.AddCommand(fresh)
	t.Cleanup(func() { pageCmd.RemoveCommand(fresh); pageCmd.AddCommand(old) })
}

func TestPageArtifactCLIRequests(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, stdin string
		args                      []string
		want                      map[string]any
		revision                  string
	}{
		{"create human page", "POST", "/api/v1/pages", "", []string{"create", "report", "--name", "Report"}, map[string]any{"name": "Report", "slug": "report", "allow_empty": true, "panels": []any{}}, ""},
		{"show head", "GET", "/api/v1/pages/report/artifact", "", []string{"show", "report"}, nil, ""},
		{"show historical", "GET", "/api/v1/pages/report/artifact", "", []string{"show", "report", "--revision", "2"}, nil, "2"},
		{"first publish stdin", "POST", "/api/v1/pages/report/artifact/revisions", "<h1>Report</h1>", []string{"publish", "report", "--file", "-", "--base-rev", "0", "--mutation-id", "retry-first"}, map[string]any{"html": "<h1>Report</h1>", "base_rev": float64(0), "mutation_id": "retry-first"}, ""},
		{"restore CAS", "POST", "/api/v1/pages/report/artifact/restore", "", []string{"restore", "report", "--revision", "1", "--base-rev", "3", "--mutation-id", "retry-restore"}, map[string]any{"revision": float64(1), "base_rev": float64(3), "mutation_id": "retry-restore"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := pageStub(t)
			resetPageArtifactCommand(t)
			handler := func(r *http.Request, raw []byte) (int, []byte, string) {
				if got := r.URL.Query().Get("revision"); got != tc.revision {
					t.Errorf("revision=%q want %q", got, tc.revision)
				}
				if tc.method == "POST" {
					var body map[string]any
					if err := json.Unmarshal(raw, &body); err != nil {
						t.Fatal(err)
					}
					got, _ := json.Marshal(body)
					want, _ := json.Marshal(tc.want)
					if string(got) != string(want) {
						t.Errorf("body=%s want %s", got, want)
					}
					if _, exists := body["owner"]; exists {
						t.Error("CLI supplied owner instead of authenticated human default")
					}
				}
				return 200, []byte(`{"head_rev":3,"revision":2,"html":"<h1>Report</h1>\u001b[31m"}`), "application/json"
			}
			if tc.method == "GET" {
				stub.OnGet(tc.path, handler)
			} else {
				stub.OnPost(tc.path, handler)
			}
			out, err := runPageCLI(t, tc.stdin, append([]string{"page", "artifact"}, tc.args...)...)
			if err != nil {
				t.Fatalf("command: %v\n%s", err, out)
			}
			if len(stub.CallsFor(tc.method, tc.path)) != 1 {
				t.Fatal("expected exactly one request")
			}
			if strings.Contains(out, "\x1b") || strings.Contains(out, "<h1>") {
				t.Fatalf("source not JSON-escaped: %q", out)
			}
		})
	}
}

func TestPageArtifactCLIPublishFileAndGeneratedIdentity(t *testing.T) {
	stub := pageStub(t)
	resetPageArtifactCommand(t)
	file := filepath.Join(t.TempDir(), "report.html")
	if err := os.WriteFile(file, []byte("<p>file source</p>"), 0600); err != nil {
		t.Fatal(err)
	}
	const path = "/api/v1/pages/report/artifact/revisions"
	stub.OnPost(path, func(_ *http.Request, raw []byte) (int, []byte, string) {
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		if body["html"] != "<p>file source</p>" || body["base_rev"] != float64(4) {
			t.Errorf("wrong body: %#v", body)
		}
		if identity, ok := body["mutation_id"].(string); !ok || len(identity) != 36 {
			t.Errorf("missing generated UUID: %#v", body)
		}
		return 201, []byte(`{"revision":5,"head_rev":5}`), "application/json"
	})
	if out, err := runPageCLI(t, "", "page", "artifact", "publish", "report", "--file", file, "--base-rev", "4"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestPageArtifactCLIRejectsInvalidInputsBeforeRequest(t *testing.T) {
	for _, args := range [][]string{
		{"show"}, {"show", "report", "--revision", "0"},
		{"create", "report"},
		{"publish", "report", "--file", "-"},
		{"publish", "report", "--base-rev", "0"},
		{"publish", "report", "--file", "-", "--base-rev", "-1"},
		{"restore", "report", "--base-rev", "1"},
		{"restore", "report", "--revision", "1"},
		{"restore", "report", "--revision", "1", "--base-rev", "0", "--mutation-id", ""},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := newPageArtifactCommand()
			cmd.SetArgs(args)
			if err := cmd.Execute(); err == nil {
				t.Fatal("accepted invalid inputs")
			}
		})
	}
}

func TestPageArtifactCLIRejectsOversizeAndInvalidUTF8(t *testing.T) {
	for _, content := range []string{strings.Repeat("x", pageArtifactMaxHTMLBytes+1), string([]byte{0xff}), ""} {
		cmd := newPageArtifactCommand()
		cmd.SetArgs([]string{"publish", "report", "--file", "-", "--base-rev", "0"})
		cmd.SetIn(strings.NewReader(content))
		if err := cmd.Execute(); err == nil {
			t.Fatal("accepted invalid HTML source")
		}
	}
}

func TestPageArtifactCLIPropagatesConflict(t *testing.T) {
	stub := pageStub(t)
	resetPageArtifactCommand(t)
	stub.OnPost("/api/v1/pages/report/artifact/restore", func(_ *http.Request, _ []byte) (int, []byte, string) {
		return 409, []byte(`{"error":"artifact revision changed; reload before saving"}`), "application/json"
	})
	_, err := runPageCLI(t, "", "page", "artifact", "restore", "report", "--revision", "1", "--base-rev", "2")
	if err == nil || !strings.Contains(err.Error(), "artifact revision changed") {
		t.Fatalf("conflict lost: %v", err)
	}
}
