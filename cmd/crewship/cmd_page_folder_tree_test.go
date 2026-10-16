package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestPageFolderCLI_ReparentUsesExplicitFencesWithoutRetry(t *testing.T) {
	stub := pageStub(t)
	var body map[string]any
	stub.OnPost("/api/v1/page-folders/child/parent", func(_ *http.Request, raw []byte) (int, []byte, string) {
		_ = json.Unmarshal(raw, &body)
		return 409, []byte(`{"error":"folder structure changed; refresh and confirm again","tree_version":9}`), "application/json"
	})
	_, err := runPageFolderCLI(t, "page", "folder", "reparent", "child", "--parent", "parent", "--tree-version", "3", "--acl-version", "4")
	if err == nil {
		t.Fatal("conflict accepted")
	}
	if body["parent"] != "parent" || body["tree_version"] != float64(3) || body["acl_version"] != float64(4) {
		t.Fatalf("body=%v", body)
	}
	if len(stub.Calls()) != 1 {
		t.Fatalf("requests=%v", stub.Calls())
	}
}

func TestPageFolderCLI_TreePreconditionsRequiredOnlyForNestedWrites(t *testing.T) {
	stub := pageStub(t)
	for _, args := range [][]string{
		{"page", "folder", "reparent", "child", "--root"},
		{"page", "folder", "reparent", "child", "--parent", "parent", "--tree-version", "3"},
		{"page", "folder", "reparent", "child", "--root", "--parent", "parent", "--tree-version", "3"},
		{"page", "folder", "create", "child", "--owner", "ops", "--parent", "parent"},
	} {
		if _, err := runPageFolderCLI(t, args...); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if len(stub.Calls()) != 0 {
		t.Fatalf("invalid invocation sent requests=%v", stub.Calls())
	}
}
