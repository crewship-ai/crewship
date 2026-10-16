package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func resetDocCommand(t *testing.T) {
	t.Helper()
	old := findSubcommand(rootCmd.Commands(), "doc")
	fresh := newDocCommand()
	rootCmd.RemoveCommand(old)
	rootCmd.AddCommand(fresh)
	t.Cleanup(func() { rootCmd.RemoveCommand(fresh); rootCmd.AddCommand(old) })
}
func TestDocumentCLIPreservesSemanticPreconditions(t *testing.T) {
	stub := pageStub(t)
	resetDocCommand(t)
	payload := `{"id":"retry","schema":1,"epoch":7,"ifRev":3,"target":"block","ifHash":"digest","text":"changed"}`
	stub.OnPost("/api/v1/pages/report/document/patch", func(r *http.Request, body []byte) (int, []byte, string) {
		for _, field := range []string{`"id":"retry"`, `"epoch":7`, `"ifRev":3`, `"target":"block"`, `"ifHash":"digest"`} {
			if !strings.Contains(string(body), field) {
				t.Errorf("lost precondition %s in %s", field, body)
			}
		}
		return 409, []byte(`{"error":"semantic precondition conflict"}`), "application/json"
	})
	_, err := runPageCLI(t, payload, "doc", "patch", "report", "--file", "-")
	if err == nil || !strings.Contains(err.Error(), "semantic precondition conflict") {
		t.Fatalf("conflict hidden: %v", err)
	}
	if len(stub.CallsFor("POST", "/api/v1/pages/report/document/patch")) != 1 {
		t.Fatal("CLI automatically retried/rebased conflicting edit")
	}
}

func TestDocumentCLIReadParityRoutes(t *testing.T) {
	for _, tc := range []struct {
		args []string
		path string
	}{
		{[]string{"status"}, "/api/v1/documents/status"},
		{[]string{"checkpoint-read", "report", "pin-1"}, "/api/v1/pages/report/document/checkpoints/pin-1"},
		{[]string{"checkpoints", "report"}, "/api/v1/pages/report/document/checkpoints"},
		{[]string{"comments", "report"}, "/api/v1/pages/report/document/comments"},
		{[]string{"request-list", "report"}, "/api/v1/pages/report/document/requests"},
	} {
		t.Run(tc.args[0], func(t *testing.T) {
			stub := pageStub(t)
			resetDocCommand(t)
			stub.OnGet(tc.path, func(_ *http.Request, _ []byte) (int, []byte, string) {
				return 200, []byte(`{"enabled":true}`), "application/json"
			})
			_, err := runPageCLI(t, "", append([]string{"doc"}, tc.args...)...)
			if err != nil {
				t.Fatal(err)
			}
			if len(stub.CallsFor("GET", tc.path)) != 1 {
				t.Fatal("command did not call exact declared endpoint")
			}
		})
	}
}

// A human request is an immutable selection snapshot, not a command to look
// up whichever text happens to occupy the selected range when a run starts.
func TestDocumentCLIRequestPreservesSelectionAndStopsOnStaleBase(t *testing.T) {
	stub := pageStub(t)
	resetDocCommand(t)
	payload := `{"page_id":"page-original","chat_id":"private-chat","instruction":"Revise only this quotation","base_rev":17,"submission_id":"same-submission","targets":[{"id":"block-one","hash":"confirmed-full-block-hash","from":2,"to":6,"quote":"A😀B"}]}`
	stub.OnPost("/api/v1/pages/report/document/requests", func(r *http.Request, body []byte) (int, []byte, string) {
		var got, want any
		if json.Unmarshal(body, &got) != nil || json.Unmarshal([]byte(payload), &want) != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("changed immutable request: %s", body)
		}
		return 409, []byte(`{"error":"document request base revision changed"}`), "application/json"
	})
	_, err := runPageCLI(t, payload, "doc", "request", "report", "--file", "-")
	if err == nil || !strings.Contains(err.Error(), "base revision changed") {
		t.Fatalf("stale request conflict hidden: %v", err)
	}
	if len(stub.CallsFor("POST", "/api/v1/pages/report/document/requests")) != 1 {
		t.Fatal("CLI retried or rebased a stale request")
	}
	if len(stub.CallsFor("GET", "/api/v1/pages/report/document")) != 0 {
		t.Fatal("CLI fetched new state to rebase request")
	}
}

func TestDocumentCLIUpgradeAcceptsFileAndPreservesOriginalFences(t *testing.T) {
	for _, status := range []int{200, 409} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			stub := pageStub(t)
			resetDocCommand(t)
			payload := `{"id":"upgrade-original","schema":2,"target_schema":3,"page_id":"original-page","epoch":"opaque-original-epoch","ifRev":7}`
			const path = "/api/v1/pages/report/document/upgrade"
			stub.OnPost(path, func(_ *http.Request, body []byte) (int, []byte, string) {
				var got, want any
				if json.Unmarshal(body, &got) != nil || json.Unmarshal([]byte(payload), &want) != nil || !reflect.DeepEqual(got, want) {
					t.Errorf("changed upgrade envelope: %s", body)
				}
				if status == 409 {
					return status, []byte(`{"error":"original document revision changed"}`), "application/json"
				}
				return status, []byte(`{"schema":3,"rev":8}`), "application/json"
			})
			output, err := runPageCLI(t, payload, "doc", "upgrade", "report", "--file", "-")
			if status == 200 {
				var result map[string]any
				if err != nil || json.Unmarshal([]byte(output), &result) != nil || result["schema"] != float64(3) || result["rev"] != float64(8) {
					t.Fatalf("upgrade flag/request failed: %v %s", err, output)
				}
			}
			if status == 409 && (err == nil || !strings.Contains(err.Error(), "original document revision changed")) {
				t.Fatalf("upgrade conflict hidden: %v", err)
			}
			if len(stub.CallsFor("POST", path)) != 1 {
				t.Fatal("upgrade was not submitted exactly once")
			}
			if len(stub.CallsFor("GET", "/api/v1/pages/report/document")) != 0 {
				t.Fatal("upgrade silently fetched a new base")
			}
		})
	}
}

func TestDocumentCLIBlockReadPreservesSelectorAndSourceEnvelope(t *testing.T) {
	stub := pageStub(t)
	resetDocCommand(t)
	const path = "/api/v1/pages/report/document"
	stub.OnGet(path, func(r *http.Request, _ []byte) (int, []byte, string) {
		if r.URL.Query().Get("block_id") != "stable_1" || r.URL.Query().Get("workspace_id") != covWSCli3 || len(r.URL.Query()) != 2 {
			t.Errorf("selector lost: %s", r.URL)
		}
		return 200, []byte(`{"page_id":"original-page","schema":3,"rev":7,"generation":2,"epoch":"original","block":{"id":"stable_1","type":"paragraph","parent":null,"hash":"original-hash","text":"Original plaintext"}}`), "application/json"
	})
	output, err := runPageCLI(t, "", "doc", "read", "report", "--block", "stable_1")
	if err != nil {
		t.Fatal(err)
	}
	var got DocumentBlockReadResponseForCLI
	if json.Unmarshal([]byte(output), &got) != nil || got.PageID != "original-page" || got.Rev != 7 || got.Block.ID != "stable_1" || got.Block.Hash != "original-hash" {
		t.Fatalf("lost source envelope: %s", output)
	}
	if len(stub.CallsFor("GET", path)) != 1 {
		t.Fatal("block read did not call current document once")
	}
}

// Decode the exact CLI output without importing API implementation details.
type DocumentBlockReadResponseForCLI struct {
	PageID string `json:"page_id"`
	Rev    int64  `json:"rev"`
	Block  struct {
		ID   string `json:"id"`
		Hash string `json:"hash"`
	} `json:"block"`
}

func TestDocumentCLIBlockReadRejectsInvalidSelectorBeforeHTTP(t *testing.T) {
	for _, selector := range []string{"", "../other", strings.Repeat("a", 81)} {
		t.Run(selector, func(t *testing.T) {
			stub := pageStub(t)
			resetDocCommand(t)
			_, err := runPageCLI(t, "", "doc", "read", "report", "--block", selector)
			if err == nil || !strings.Contains(err.Error(), "--block requires") {
				t.Fatalf("invalid selector accepted: %v", err)
			}
			if len(stub.CallsFor("GET", "/api/v1/pages/report/document")) != 0 {
				t.Fatal("invalid selector reached HTTP")
			}
		})
	}
}
