package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newSearchEngine builds an Engine over a temp dir with the given
// markdown files indexed, mirroring engine_test.go's setup style.
func newSearchEngine(t *testing.T, files map[string]string) *Engine {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e, err := New(dir, Config{SearchEnabled: true})
	if err != nil {
		t.Fatalf("New engine: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if err := e.Reindex(); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	return e
}

func TestSearch_LimitDefaultsAndClamp(t *testing.T) {
	e := newSearchEngine(t, map[string]string{
		"AGENT.md": "# Notes\n\nthe zebra crossed the road\n",
	})

	// limit <= 0 → default 10.
	res, err := e.Search(context.Background(), "zebra", 0)
	if err != nil {
		t.Fatalf("Search limit=0: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("limit=0 results = %d, want 1", len(res))
	}
	if res[0].File == "" || !strings.Contains(res[0].Snippet, "zebra") {
		t.Errorf("unexpected hit shape: %+v", res[0])
	}

	// limit > 50 → clamped to 50 (must not error, same single hit).
	res, err = e.Search(context.Background(), "zebra", 5000)
	if err != nil {
		t.Fatalf("Search limit=5000: %v", err)
	}
	if len(res) != 1 {
		t.Errorf("limit=5000 results = %d, want 1", len(res))
	}
}

func TestSearch_QuerySanitizesToEmpty_NilNil(t *testing.T) {
	e := newSearchEngine(t, map[string]string{"AGENT.md": "# x\n\ncontent\n"})
	res, err := e.Search(context.Background(), "{}:^()", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res != nil {
		t.Errorf("dangerous-only query must return nil results, got %+v", res)
	}
}

func TestSearch_LongSnippetTruncatedAt300(t *testing.T) {
	long := "needle " + strings.Repeat("padding words to inflate the chunk body well past the threshold ", 10)
	e := newSearchEngine(t, map[string]string{
		"AGENT.md": "# Section\n\n" + long + "\n",
	})
	res, err := e.Search(context.Background(), "needle", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) == 0 {
		t.Fatal("expected a hit for needle")
	}
	snip := res[0].Snippet
	if !strings.HasSuffix(snip, "...") {
		t.Errorf("long snippet should end with ellipsis, got %q", snip)
	}
	if len(snip) != 303 { // 300 chars + "..."
		t.Errorf("snippet length = %d, want 303", len(snip))
	}
}

func TestSanitizeFTSQuery_DangerousPathRebuild(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		// Column filter stripped, words re-quoted.
		{`title:secret`, `"title" "secret"`},
		// Operators preserved case-insensitively even on the dirty path.
		{`foo AND (bar)`, `"foo" AND "bar"`},
		{`foo or bar:`, `"foo" OR "bar"`},
		{`not baz +`, `NOT "baz"`},
		// Internal quotes removed and re-wrapped.
		{`we"ird:`, `"weird"`},
		// Quote-only word vanishes entirely.
		{`" :`, ``},
		// Trailing wildcard preserved on the dirty path.
		{`(pre*)`, `"pre"*`},
		// Wildcard with empty base is dropped.
		{`(***)`, ``},
		// NEAR-ish constructs lose their operator characters.
		{`NEAR(a, b)`, `"NEAR" "a," "b"`},
		// Caret and tilde stripped.
		{`^boost~2 word`, `"boost" "2" "word"`},
	}
	for _, tc := range cases {
		if got := sanitizeFTSQuery(tc.input); got != tc.want {
			t.Errorf("sanitizeFTSQuery(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestSanitizeFTSQuery_CleanOperatorsPassThrough(t *testing.T) {
	// Each operator form on the clean path must pass through verbatim.
	for _, q := range []string{"foo OR bar", "foo NOT bar", `"a phrase"`, "wild*"} {
		if got := sanitizeFTSQuery(q); got != q {
			t.Errorf("sanitizeFTSQuery(%q) = %q, want pass-through", q, got)
		}
	}
}
