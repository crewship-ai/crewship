package main

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// fakeSlugGetter implements the minimal Get interface
// suggestSimilarRoutineSlugs accepts, returning a canned response and
// recording the requested path.
type fakeSlugGetter struct {
	status  int
	body    string
	err     error
	gotPath string
}

func (f *fakeSlugGetter) Get(path string) (*http.Response, error) {
	f.gotPath = path
	if f.err != nil {
		return nil, f.err
	}
	return &http.Response{
		StatusCode: f.status,
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

func TestSuggestSimilarRoutineSlugs_EmptyInputs(t *testing.T) {
	t.Parallel()

	g := &fakeSlugGetter{status: 200, body: `[]`}
	if got := suggestSimilarRoutineSlugs(g, "", "daily-report"); got != "" {
		t.Errorf("empty ws: got %q want empty", got)
	}
	if got := suggestSimilarRoutineSlugs(g, "ws1", ""); got != "" {
		t.Errorf("empty target: got %q want empty", got)
	}
	if g.gotPath != "" {
		t.Errorf("no request should be made for empty inputs; got %q", g.gotPath)
	}
}

func TestSuggestSimilarRoutineSlugs_FetchFailures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		g    *fakeSlugGetter
	}{
		{"transport error", &fakeSlugGetter{err: errors.New("connection refused")}},
		{"non-200", &fakeSlugGetter{status: 500, body: `{"error":"boom"}`}},
		{"bad JSON", &fakeSlugGetter{status: 200, body: `not json`}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := suggestSimilarRoutineSlugs(tc.g, "ws1", "daily"); got != "" {
				t.Errorf("got %q want empty (best-effort, never crash)", got)
			}
		})
	}
}

func TestSuggestSimilarRoutineSlugs_PathEscapesWorkspace(t *testing.T) {
	t.Parallel()

	g := &fakeSlugGetter{status: 200, body: `[]`}
	_ = suggestSimilarRoutineSlugs(g, "team/a", "daily")
	if !strings.Contains(g.gotPath, "/api/v1/workspaces/team%2Fa/pipelines") {
		t.Errorf("workspace not path-escaped: got %q", g.gotPath)
	}
}

func TestSuggestSimilarRoutineSlugs_NoRoutinesRegistered(t *testing.T) {
	t.Parallel()

	g := &fakeSlugGetter{status: 200, body: `[]`}
	got := suggestSimilarRoutineSlugs(g, "ws1", "daily")
	if !strings.Contains(got, "no routines registered yet") {
		t.Errorf("got %q; want seed hint", got)
	}
}

func TestSuggestSimilarRoutineSlugs_EditDistanceMatch(t *testing.T) {
	t.Parallel()

	g := &fakeSlugGetter{
		status: 200,
		body:   `[{"slug":"daily-report"},{"slug":"weekly-digest"},{"slug":""}]`,
	}
	got := suggestSimilarRoutineSlugs(g, "ws1", "daily-repot")
	if !strings.HasPrefix(got, "did you mean: ") {
		t.Fatalf("got %q; want did-you-mean hint", got)
	}
	if !strings.Contains(got, "daily-report") {
		t.Errorf("got %q; want daily-report suggested", got)
	}
	if strings.Contains(got, "weekly-digest") {
		t.Errorf("got %q; weekly-digest is too far away to suggest", got)
	}
}

func TestSuggestSimilarRoutineSlugs_SubstringFallback(t *testing.T) {
	t.Parallel()

	g := &fakeSlugGetter{
		status: 200,
		body: `[{"slug":"eval-extract-orders"},{"slug":"eval-extract-users"},
			{"slug":"eval-extract-items"},{"slug":"eval-extract-logs"},{"slug":"unrelated"}]`,
	}
	got := suggestSimilarRoutineSlugs(g, "ws1", "EXTRACT")
	if !strings.Contains(got, `routines containing "EXTRACT"`) {
		t.Fatalf("got %q; want substring fallback with interpolated target", got)
	}
	// Capped at 3 suggestions.
	count := strings.Count(got, "eval-extract-")
	if count != 3 {
		t.Errorf("got %d substring suggestions, want 3: %q", count, got)
	}
	if strings.Contains(got, "unrelated") {
		t.Errorf("got %q; unrelated slug must not appear", got)
	}
}

func TestSuggestSimilarRoutineSlugs_NoMatchAtAll(t *testing.T) {
	t.Parallel()

	g := &fakeSlugGetter{
		status: 200,
		body:   `[{"slug":"daily-report"},{"slug":"weekly-digest"}]`,
	}
	if got := suggestSimilarRoutineSlugs(g, "ws1", "zzzzzzzzzz"); got != "" {
		t.Errorf("got %q; want empty when nothing is close", got)
	}
}
