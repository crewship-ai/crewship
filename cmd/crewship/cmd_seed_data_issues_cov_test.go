package main

// Coverage tests for cmd_seed_data_issues.go — the demo-issue seeder.
// Drives seedIssues against a stub server using the real builtin
// seeddata catalogue (labels / projects / issues from builtin/issues.yaml).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestSeedIssues_CancelledContext(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := seedIssues(ctx, covStubClient(s), nil, nil); err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if len(s.Calls()) != 0 {
		t.Errorf("no HTTP traffic expected after cancel")
	}
}

// covSeedProjectStub registers a POST /api/v1/projects handler that mints
// sequential ids.
func covSeedProjectStub(s *clitest.StubServer) {
	var n int64
	s.OnPost("/api/v1/projects", func(_ *http.Request, _ []byte) (int, []byte, string) {
		id := atomic.AddInt64(&n, 1)
		b, _ := json.Marshal(map[string]string{"id": fmt.Sprintf("proj-%d", id)})
		return 201, b, "application/json"
	})
}

func TestSeedIssues_UnknownCrewsSkipIssueCreation(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/labels", clitest.JSONResponse(201, map[string]string{"id": "lab-1"}))
	covSeedProjectStub(s)

	// No crews resolved → every issue is skipped; labels + projects still
	// get created and the seeder reports success.
	if err := seedIssues(context.Background(), covStubClient(s), map[string]string{}, map[string]string{}); err != nil {
		t.Fatalf("seedIssues: %v", err)
	}
	if got := len(s.CallsFor("POST", "/api/v1/labels")); got != len(seeddata.Labels) {
		t.Errorf("label POSTs = %d, want %d", got, len(seeddata.Labels))
	}
	if got := len(s.CallsFor("POST", "/api/v1/projects")); got != len(seeddata.Projects) {
		t.Errorf("project POSTs = %d, want %d", got, len(seeddata.Projects))
	}
	// No issue endpoint was ever touched.
	for _, c := range s.Calls() {
		if strings.Contains(c.Path, "/issues") {
			t.Errorf("unexpected issue call: %s %s", c.Method, c.Path)
		}
	}
}

func TestSeedIssues_FullSeedAgainstStub(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/labels", clitest.JSONResponse(201, map[string]string{"id": "lab-1"}))
	covSeedProjectStub(s)

	// Resolve every builtin crew slug and assignee so the create /
	// transition / assign / comment / relation branches all fire.
	crewIDs := map[string]string{}
	for i, c := range seeddata.Crews {
		crewIDs[c.Slug] = fmt.Sprintf("crew-%d", i)
	}
	agentIDs := map[string]string{}
	for _, iss := range seeddata.Issues {
		if iss.Assignee != "" {
			agentIDs[iss.Assignee] = "agent-" + iss.Assignee
		}
	}

	// Issue creation mints a unique identifier per call so relations
	// address distinct issues.
	var issueN int64
	for slug := range crewIDs {
		crewID := crewIDs[slug]
		s.OnPost("/api/v1/crews/"+crewID+"/issues", func(_ *http.Request, _ []byte) (int, []byte, string) {
			n := atomic.AddInt64(&issueN, 1)
			ident := fmt.Sprintf("SEED-%d", n)
			b, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("iss-%d", n), "identifier": ident})
			return 201, b, "application/json"
		})
	}
	// Transitions / assignment PATCH, comments and relations POST land on
	// per-identifier paths — catch them with a permissive fallback that
	// only allows issue-scoped subpaths.
	s.SetFallback(func(r *http.Request, _ []byte) (int, []byte, string) {
		if strings.Contains(r.URL.Path, "/issues/SEED-") {
			return 200, []byte(`{}`), "application/json"
		}
		return 404, []byte("unexpected path " + r.URL.Path), "text/plain"
	})

	if err := seedIssues(context.Background(), covStubClient(s), crewIDs, agentIDs); err != nil {
		t.Fatalf("seedIssues: %v", err)
	}

	// Every catalogue issue must have been POSTed exactly once.
	created := 0
	patched := 0
	relations := 0
	for _, c := range s.Calls() {
		switch {
		case c.Method == "POST" && strings.HasSuffix(c.Path, "/issues"):
			created++
		case c.Method == "PATCH" && strings.Contains(c.Path, "/issues/SEED-"):
			patched++
		case c.Method == "POST" && strings.HasSuffix(c.Path, "/relations"):
			relations++
		}
	}
	if created != len(seeddata.Issues) {
		t.Errorf("issue creates = %d, want %d", created, len(seeddata.Issues))
	}
	// The builtin catalogue has issues with non-BACKLOG target states and
	// assignees, so transitions/assignments must have happened.
	if patched == 0 {
		t.Error("expected at least one PATCH (transition or assignment)")
	}
	// The hardcoded relation defs reference 4 catalogue titles; with every
	// issue created they all resolve.
	if relations != 4 {
		t.Errorf("relation POSTs = %d, want 4", relations)
	}

	// Spot-check an assignment body shape on one PATCH.
	foundAssign := false
	for _, c := range s.Calls() {
		if c.Method == "PATCH" && strings.Contains(string(c.Body), `"assignee_type":"agent"`) {
			foundAssign = true
			if !strings.Contains(string(c.Body), `"assignee_id":"agent-`) {
				t.Errorf("assignment body = %s", c.Body)
			}
			break
		}
	}
	if !foundAssign {
		t.Error("no assignment PATCH observed")
	}
}

func TestSeedIssues_ProjectConflictResolvesExisting(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/labels", clitest.JSONResponse(201, map[string]string{"id": "lab-1"}))
	s.OnPost("/api/v1/projects", clitest.ErrorResponse(http.StatusConflict, "exists"))

	existing := make([]map[string]string, 0, len(seeddata.Projects))
	for i, p := range seeddata.Projects {
		existing = append(existing, map[string]string{"id": fmt.Sprintf("existing-%d", i), "name": p.Name})
	}
	s.OnGet("/api/v1/projects", clitest.JSONResponse(200, existing))

	// Conflicting projects resolve to the existing rows; with no crews the
	// run then finishes cleanly.
	if err := seedIssues(context.Background(), covStubClient(s), map[string]string{}, nil); err != nil {
		t.Fatalf("seedIssues with project conflicts: %v", err)
	}
	if got := len(s.CallsFor("GET", "/api/v1/projects")); got != len(seeddata.Projects) {
		t.Errorf("expected %d resolve lookups, got %d", len(seeddata.Projects), got)
	}
}

func TestSeedIssues_ProjectConflictUnresolvableFails(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/labels", clitest.JSONResponse(201, map[string]string{"id": "lab-1"}))
	s.OnPost("/api/v1/projects", clitest.ErrorResponse(http.StatusConflict, "exists"))
	s.OnGet("/api/v1/projects", clitest.JSONResponse(200, []map[string]string{})) // nothing matches

	err := seedIssues(context.Background(), covStubClient(s), map[string]string{}, nil)
	if err == nil || !strings.Contains(err.Error(), "conflict but existing record could not be resolved") {
		t.Fatalf("got %v", err)
	}
}

func TestSeedIssues_ProjectHardFailureAborts(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/labels", clitest.JSONResponse(201, map[string]string{"id": "lab-1"}))
	s.OnPost("/api/v1/projects", clitest.ErrorResponse(500, "db down"))

	err := seedIssues(context.Background(), covStubClient(s), map[string]string{}, nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("got %v", err)
	}
}

// ─── additional error paths ──────────────────────────────────────────────

func TestSeedIssues_CancelDuringLabelPhase(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Serving the first label POST cancels the context — the loop's next
	// iteration must abort the seed.
	s.OnPost("/api/v1/labels", func(_ *http.Request, _ []byte) (int, []byte, string) {
		cancel()
		return 201, []byte(`{"id":"lab-1"}`), "application/json"
	})

	if err := seedIssues(ctx, covStubClient(s), map[string]string{}, nil); err != context.Canceled {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if got := len(s.CallsFor("POST", "/api/v1/labels")); got != 1 {
		t.Errorf("label POSTs after cancel = %d, want 1", got)
	}
}

func TestSeedIssues_CancelDuringProjectPhase(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.OnPost("/api/v1/labels", clitest.JSONResponse(201, map[string]string{"id": "lab-1"}))
	s.OnPost("/api/v1/projects", func(_ *http.Request, _ []byte) (int, []byte, string) {
		cancel()
		return 201, []byte(`{"id":"proj-1"}`), "application/json"
	})

	if err := seedIssues(ctx, covStubClient(s), map[string]string{}, nil); err != context.Canceled {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if got := len(s.CallsFor("POST", "/api/v1/projects")); got != 1 {
		t.Errorf("project POSTs after cancel = %d, want 1", got)
	}
}

func TestSeedIssues_IssueCreateFailuresAreSoft(t *testing.T) {
	// Issue creation failing (or returning garbage) skips that issue but
	// never aborts the whole seed; relations referencing failed issues are
	// skipped instead of mis-wired.
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/labels", clitest.JSONResponse(201, map[string]string{"id": "lab-1"}))
	covSeedProjectStub(s)

	crewIDs := map[string]string{}
	for i, c := range seeddata.Crews {
		crewIDs[c.Slug] = fmt.Sprintf("crew-%d", i)
	}
	// First crew rejects creation, the rest return undecodable bodies.
	first := true
	for slug := range crewIDs {
		crewID := crewIDs[slug]
		if first {
			s.OnPost("/api/v1/crews/"+crewID+"/issues", clitest.ErrorResponse(422, "invalid"))
			first = false
			continue
		}
		s.OnPost("/api/v1/crews/"+crewID+"/issues", clitest.TextResponse(201, "not json"))
	}

	if err := seedIssues(context.Background(), covStubClient(s), crewIDs, map[string]string{}); err != nil {
		t.Fatalf("soft failures must not abort: %v", err)
	}
	// Nothing was tracked → no transition/assign/comment/relation traffic.
	for _, c := range s.Calls() {
		if c.Method == "PATCH" || strings.HasSuffix(c.Path, "/relations") || strings.HasSuffix(c.Path, "/comments") {
			t.Errorf("unexpected follow-up call for failed issues: %s %s", c.Method, c.Path)
		}
	}
}

func TestSeedIssues_AssignmentFailuresAreSoft(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/labels", clitest.JSONResponse(201, map[string]string{"id": "lab-1"}))
	covSeedProjectStub(s)

	crewIDs := map[string]string{}
	for i, c := range seeddata.Crews {
		crewIDs[c.Slug] = fmt.Sprintf("crew-%d", i)
	}
	agentIDs := map[string]string{}
	for _, iss := range seeddata.Issues {
		if iss.Assignee != "" {
			agentIDs[iss.Assignee] = "agent-" + iss.Assignee
		}
	}
	var issueN int64
	for slug := range crewIDs {
		crewID := crewIDs[slug]
		s.OnPost("/api/v1/crews/"+crewID+"/issues", func(_ *http.Request, _ []byte) (int, []byte, string) {
			n := atomic.AddInt64(&issueN, 1)
			b, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("i-%d", n), "identifier": fmt.Sprintf("SEED-%d", n)})
			return 201, b, "application/json"
		})
	}
	// Every PATCH (assignment) is rejected; comments/relations accepted.
	s.SetFallback(func(r *http.Request, _ []byte) (int, []byte, string) {
		if r.Method == "PATCH" {
			return 500, []byte(`{"error":"assign failed"}`), "application/json"
		}
		if strings.Contains(r.URL.Path, "/issues/SEED-") {
			return 200, []byte(`{}`), "application/json"
		}
		return 404, nil, ""
	})

	if err := seedIssues(context.Background(), covStubClient(s), crewIDs, agentIDs); err != nil {
		t.Fatalf("assignment failures must not abort: %v", err)
	}
}

func TestSeedIssues_TransportFailuresLabelThenProject(t *testing.T) {
	// Against a dead server every label POST soft-fails (logged + skipped)
	// and the first project POST hard-fails the seed.
	client := covDeadClient(t)
	err := seedIssues(context.Background(), client, map[string]string{}, nil)
	if err == nil || !strings.Contains(err.Error(), "project") {
		t.Fatalf("got %v", err)
	}
}

func TestSeedIssues_CancelDuringIssuePhase(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.OnPost("/api/v1/labels", clitest.JSONResponse(201, map[string]string{"id": "lab-1"}))
	covSeedProjectStub(s)

	crewIDs := map[string]string{}
	for i, c := range seeddata.Crews {
		crewIDs[c.Slug] = fmt.Sprintf("crew-%d", i)
	}
	issuePosts := int64(0)
	for slug := range crewIDs {
		crewID := crewIDs[slug]
		s.OnPost("/api/v1/crews/"+crewID+"/issues", func(_ *http.Request, _ []byte) (int, []byte, string) {
			atomic.AddInt64(&issuePosts, 1)
			cancel() // first creation cancels the seed
			return 201, []byte(`{"id":"i-1","identifier":"SEED-1"}`), "application/json"
		})
	}
	s.SetFallback(func(r *http.Request, _ []byte) (int, []byte, string) {
		if strings.Contains(r.URL.Path, "/issues/SEED-") {
			return 200, []byte(`{}`), "application/json"
		}
		return 404, nil, ""
	})

	if err := seedIssues(ctx, covStubClient(s), crewIDs, map[string]string{}); err != context.Canceled {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if got := atomic.LoadInt64(&issuePosts); got != 1 {
		t.Errorf("issue POSTs after cancel = %d, want 1", got)
	}
}

func TestSeedIssues_UnknownAssigneeIsLoggedNotFatal(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/labels", clitest.JSONResponse(201, map[string]string{"id": "lab-1"}))
	covSeedProjectStub(s)

	crewIDs := map[string]string{}
	for i, c := range seeddata.Crews {
		crewIDs[c.Slug] = fmt.Sprintf("crew-%d", i)
	}
	var issueN int64
	for slug := range crewIDs {
		crewID := crewIDs[slug]
		s.OnPost("/api/v1/crews/"+crewID+"/issues", func(_ *http.Request, _ []byte) (int, []byte, string) {
			n := atomic.AddInt64(&issueN, 1)
			b, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("i-%d", n), "identifier": fmt.Sprintf("SEED-%d", n)})
			return 201, b, "application/json"
		})
	}
	s.SetFallback(func(r *http.Request, _ []byte) (int, []byte, string) {
		if strings.Contains(r.URL.Path, "/issues/SEED-") {
			return 200, []byte(`{}`), "application/json"
		}
		return 404, nil, ""
	})

	// Empty agentIDs: every assignee lookup misses → logged, no PATCH sent.
	if err := seedIssues(context.Background(), covStubClient(s), crewIDs, map[string]string{}); err != nil {
		t.Fatalf("unknown assignees must not abort: %v", err)
	}
	for _, c := range s.Calls() {
		if c.Method == "PATCH" {
			t.Errorf("no assignment PATCH expected: %s", c.Path)
		}
	}
}

func TestSeedIssues_CancelDuringRelationPhase(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.OnPost("/api/v1/labels", clitest.JSONResponse(201, map[string]string{"id": "lab-1"}))
	covSeedProjectStub(s)

	crewIDs := map[string]string{}
	for i, c := range seeddata.Crews {
		crewIDs[c.Slug] = fmt.Sprintf("crew-%d", i)
	}
	var issueN int64
	for slug := range crewIDs {
		crewID := crewIDs[slug]
		s.OnPost("/api/v1/crews/"+crewID+"/issues", func(_ *http.Request, _ []byte) (int, []byte, string) {
			n := atomic.AddInt64(&issueN, 1)
			b, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("i-%d", n), "identifier": fmt.Sprintf("SEED-%d", n)})
			return 201, b, "application/json"
		})
	}
	relPosts := int64(0)
	s.SetFallback(func(r *http.Request, _ []byte) (int, []byte, string) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/relations") {
			atomic.AddInt64(&relPosts, 1)
			cancel() // first relation cancels the seed
			return 201, []byte(`{}`), "application/json"
		}
		if strings.Contains(r.URL.Path, "/issues/SEED-") {
			return 200, []byte(`{}`), "application/json"
		}
		return 404, nil, ""
	})

	if err := seedIssues(ctx, covStubClient(s), crewIDs, map[string]string{}); err != context.Canceled {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if got := atomic.LoadInt64(&relPosts); got != 1 {
		t.Errorf("relation POSTs after cancel = %d, want 1", got)
	}
}
