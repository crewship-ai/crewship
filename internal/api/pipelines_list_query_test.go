package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// ODCH-RT-03: GET /pipelines?q= searches the routine's name, slug,
// description and the text of its steps (ids, names, prompts, agents,
// commands), so the panel can find a routine by what it does. The list
// response still carries no definition.
func TestPipelinesAPI_List_QuerySearchesSteps(t *testing.T) {
	db := openSmokeDB(t)
	seedSmokePipelineDef(t, db, "weekly", `{"name":"weekly","steps":[{"id":"scan","type":"agent_run","agent_slug":"agent_lead","prompt":"Compare competitor pricing pages"}]}`)
	seedSmokePipelineDef(t, db, "digest", `{"name":"digest","steps":[{"id":"collect","type":"agent_run","agent_slug":"agent_lead","prompt":"Summarise yesterday's merged pull requests"}]}`)
	seedSmokePipelineDef(t, db, "cleanup", `{"name":"cleanup","steps":[{"id":"purge","type":"agent_run","agent_slug":"agent_lead","prompt":"Remove stale branches"}]}`)

	cases := []struct {
		name  string
		query string
		want  []string
	}{
		{"matches prompt text of a step", "q=pricing", []string{"weekly"}},
		{"case-insensitive", "q=PULL+REQUESTS", []string{"digest"}},
		{"matches a step id", "q=purge", []string{"cleanup"}},
		{"matches the slug", "q=weekly", []string{"weekly"}},
		{"several words must all match", "q=competitor+pricing", []string{"weekly"}},
		{"words in different steps of other routines do not match", "q=pricing+branches", nil},
		{"no match is an empty list", "q=nothing-like-this", nil},
		{"blank q lists everything", "q=+", []string{"cleanup", "digest", "weekly"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewPipelineHandler(db, slog.Default(), nil, nil)
			req := withWorkspaceCtx(httptest.NewRequest("GET", "/api/v1/workspaces/ws_smoke/pipelines?order=name&"+tc.query, nil), "ws_smoke")
			w := httptest.NewRecorder()
			h.List(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}
			var out []pipelineResponse
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			got := []string{}
			for _, p := range out {
				got = append(got, p.Slug)
				if p.Definition != nil {
					t.Errorf("list must not return the definition (%s)", p.Slug)
				}
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestPipelinesAPI_List_QueryPaginatesTheMatches(t *testing.T) {
	db := openSmokeDB(t)
	for _, s := range []string{"a-scan", "b-scan", "c-other"} {
		seedSmokePipelineDef(t, db, s, `{"name":"`+s+`","steps":[{"id":"x","type":"agent_run","agent_slug":"agent_lead","prompt":"scan the thing"}]}`)
	}
	h := NewPipelineHandler(db, slog.Default(), nil, nil)
	q := url.Values{"q": {"scan"}, "order": {"name"}, "limit": {"1"}, "offset": {"1"}}
	req := withWorkspaceCtx(httptest.NewRequest("GET", "/api/v1/workspaces/ws_smoke/pipelines?"+q.Encode(), nil), "ws_smoke")
	w := httptest.NewRecorder()
	h.List(w, req)
	var out []pipelineResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Slug != "b-scan" {
		t.Fatalf("page 2 of the matches = %+v, want b-scan only", out)
	}
}
