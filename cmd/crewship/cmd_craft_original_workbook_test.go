package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCraftCLIOriginalWorkbookPreservesBytes(t *testing.T) {
	stub := pageStub(t)
	base := "/api/v1/pages/health/craft"
	admission := strings.Repeat("a", 64)
	original := "  {\n  \"sheets\": []\n}\n"
	stub.OnPost(base+"/sessions", func(*http.Request, []byte) (int, []byte, string) {
		return 201, []byte(`{"session_token":"` + admission + `","session_id":"original-sheet-test"}`), "application/json"
	})
	stub.OnGet(base+"/document/original-workbook", func(req *http.Request, _ []byte) (int, []byte, string) {
		if req.Header.Get("X-Craft-Session") != admission {
			t.Error("download omitted its private admission")
		}
		return 200, []byte(original), "application/json"
	})
	out, err := runPageCLI(t, "", "craft", "original-workbook", "health")
	if err != nil || out != original {
		t.Fatalf("stdout: %q, %v", out, err)
	}
	path := filepath.Join(t.TempDir(), "original.json")
	out, err = runPageCLI(t, "", "craft", "original-workbook", "health", "--output", path)
	if err != nil || out != "" {
		t.Fatalf("file output: %q, %v", out, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != original {
		t.Fatalf("file contents: %q, %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("file mode: %v", info.Mode().Perm())
	}
	if len(stub.CallsFor("POST", base+"/sessions")) != 2 || len(stub.CallsFor("POST", base+"/lease/acquire")) != 0 {
		t.Fatal("download did not use exactly one admission per invocation without a lease")
	}
}
