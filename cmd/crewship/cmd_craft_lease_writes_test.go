package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// PRD section 0 point 9, CLI side: every CLI call that POSTs to a native Craft
// route must be classified, and the generation-bearing ones must be validated
// before the request is sent. A new CLI write path fails until classified; the
// server-side refusal of each route is proved in internal/api.
func TestCraftCLIWritePathsAreLeaseClassified(t *testing.T) {
	classes := map[string]string{
		"/craft/document/save": "generation", "/craft/document/import": "generation",
		"/craft/document/restore": "generation", "/craft/document/share": "generation",
		"/craft/document/reconcile": "exempt", "/craft/lease": "exempt",
		"/craft/lease/acquire": "exempt", "/craft/lease/renew": "generation",
		"/craft/lease/release": "generation", "/craft/lease/request-handoff": "exempt",
		"/craft/lease/handoff": "generation", "/craft/sessions": "exempt",
		"/craft/sessions/reauthorize": "exempt", "/craft/initialize": "exempt",
		"/craft/tasks": "exempt", "/craft/tasks/cancel": "exempt",
		"/craft/proposals/{id}/accept": "generation", "/craft/proposals/{id}/reject": "exempt",
	}
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	post := regexp.MustCompile(`\.Post\(\s*"/api/v1/pages/"\s*\+\s*[^,]*?\+\s*"(/craft/[^"]*)"(\s*\+\s*[A-Za-z.]+\s*\+\s*"(/[a-z]+)")?`)
	found := map[string]bool{}
	for _, f := range files {
		n := f.Name()
		if !strings.HasPrefix(n, "cmd_") || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		raw, _ := os.ReadFile(n)
		for _, m := range post.FindAllStringSubmatch(string(raw), -1) {
			path := m[1]
			if m[3] != "" {
				path = "/craft/proposals/{id}" + m[3]
			}
			found[path] = true
		}
	}
	var paths []string
	for p := range found {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if len(paths) < 12 {
		t.Fatalf("CLI craft POST scan looks broken: %v", paths)
	}
	for _, p := range paths {
		if _, ok := classes[p]; !ok {
			t.Errorf("CLI posts to %s which is not lease-classified; add it and prove the server refuses a stale generation", p)
		}
	}
	// The generation-bearing actions must be validated before sending.
	raw, _ := os.ReadFile("cmd_craft.go")
	src := string(raw)
	for _, a := range []string{`"renew"`, `"release"`, `"handoff"`, `"accept-proposal"`} {
		if !strings.Contains(src, `a.Action == `+a) {
			t.Errorf("cmd_craft.go no longer validates the generation for action %s", a)
		}
	}
	for _, a := range []string{"share", "save", "import", "restore"} {
		if !strings.Contains(src, `"`+a+`"`) {
			t.Errorf("action %s missing", a)
		}
	}
}
