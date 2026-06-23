package manifest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// covRoute describes one canned response for the covStubAPI. Either
// err is returned directly (transport failure), or a response with
// the given status and raw JSON body.
type covRoute struct {
	status int
	body   string
	err    error
}

// covStubAPI is a route-table APIClient used by the *_cov_test.go
// files. Unlike fakeAPIClient (apply_test.go) it gives each test full
// control over the exact bytes returned per METHOD+path, which the
// decode-shape and error-path tests need.
type covStubAPI struct {
	wsID   string
	routes map[string]covRoute // key: "GET /api/v1/..."
	calls  []fakeCall
}

func newCovStub() *covStubAPI {
	return &covStubAPI{wsID: "ws_cov", routes: map[string]covRoute{}}
}

func (s *covStubAPI) on(method, path string, status int, body string) {
	s.routes[method+" "+path] = covRoute{status: status, body: body}
}

func (s *covStubAPI) onErr(method, path string, err error) {
	s.routes[method+" "+path] = covRoute{err: err}
}

func (s *covStubAPI) countCalls(method, path string) int {
	n := 0
	for _, c := range s.calls {
		if c.Method == method && c.Path == path {
			n++
		}
	}
	return n
}

func (s *covStubAPI) respond(method, path string, body any) (*http.Response, error) {
	bmap, _ := body.(map[string]any)
	s.calls = append(s.calls, fakeCall{Method: method, Path: path, Body: bmap})
	r, ok := s.routes[method+" "+path]
	if !ok {
		return &http.Response{
			StatusCode: 404,
			Body:       io.NopCloser(strings.NewReader(`{"error":"no route"}`)),
			Header:     http.Header{},
		}, nil
	}
	if r.err != nil {
		return nil, r.err
	}
	return &http.Response{
		StatusCode: r.status,
		Body:       io.NopCloser(bytes.NewReader([]byte(r.body))),
		Header:     http.Header{},
	}, nil
}

func (s *covStubAPI) Get(_ context.Context, path string) (*http.Response, error) {
	return s.respond("GET", path, nil)
}
func (s *covStubAPI) Post(_ context.Context, path string, body any) (*http.Response, error) {
	return s.respond("POST", path, body)
}
func (s *covStubAPI) Patch(_ context.Context, path string, body any) (*http.Response, error) {
	return s.respond("PATCH", path, body)
}
func (s *covStubAPI) Delete(_ context.Context, path string) (*http.Response, error) {
	return s.respond("DELETE", path, nil)
}
func (s *covStubAPI) GetWorkspaceID() string { return s.wsID }

// ---------- crews ----------

func TestClient_DeleteCrew_InvalidatesCrewCache(t *testing.T) {
	stub := newCovStub()
	stub.on("GET", "/api/v1/crews", 200, `[{"id":"crew_1","slug":"a","name":"A"}]`)
	stub.on("DELETE", "/api/v1/crews/crew_1", 204, ``)

	c := NewClient(stub)
	if _, err := c.ListCrews(context.Background()); err != nil {
		t.Fatalf("ListCrews: %v", err)
	}
	// Cached: second list issues no extra GET.
	if _, err := c.ListCrews(context.Background()); err != nil {
		t.Fatalf("ListCrews (cached): %v", err)
	}
	if got := stub.countCalls("GET", "/api/v1/crews"); got != 1 {
		t.Fatalf("expected 1 GET before delete, got %d", got)
	}

	if err := c.DeleteCrew(context.Background(), "crew_1"); err != nil {
		t.Fatalf("DeleteCrew: %v", err)
	}
	if got := stub.countCalls("DELETE", "/api/v1/crews/crew_1"); got != 1 {
		t.Fatalf("expected exactly one DELETE call, got %d", got)
	}
	// Cache must be flushed: next list re-fetches.
	if _, err := c.ListCrews(context.Background()); err != nil {
		t.Fatalf("ListCrews after delete: %v", err)
	}
	if got := stub.countCalls("GET", "/api/v1/crews"); got != 2 {
		t.Errorf("expected cache invalidation to trigger a second GET, got %d", got)
	}
}

func TestClient_DeleteCrew_Errors(t *testing.T) {
	t.Run("server error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("DELETE", "/api/v1/crews/x", 500, `{"error":"boom"}`)
		c := NewClient(stub)
		if err := c.DeleteCrew(context.Background(), "x"); err == nil {
			t.Fatal("want error on 500, got nil")
		}
	})
	t.Run("transport error", func(t *testing.T) {
		stub := newCovStub()
		stub.onErr("DELETE", "/api/v1/crews/x", errors.New("conn refused"))
		c := NewClient(stub)
		err := c.DeleteCrew(context.Background(), "x")
		if err == nil || !strings.Contains(err.Error(), "delete crew") {
			t.Fatalf("want wrapped transport error, got %v", err)
		}
	})
}

func TestClient_ListCrews_DecodeError(t *testing.T) {
	stub := newCovStub()
	stub.on("GET", "/api/v1/crews", 200, `{"not":"an array"}`)
	c := NewClient(stub)
	_, err := c.ListCrews(context.Background())
	if err == nil || !strings.Contains(err.Error(), "decode crews") {
		t.Fatalf("want decode error, got %v", err)
	}
}

func TestClient_CreateUpdateCrew_ErrorPaths(t *testing.T) {
	ctx := context.Background()
	t.Run("create transport error", func(t *testing.T) {
		stub := newCovStub()
		stub.onErr("POST", "/api/v1/crews", errors.New("down"))
		_, err := NewClient(stub).CreateCrew(ctx, map[string]any{"slug": "x"})
		if err == nil || !strings.Contains(err.Error(), "create crew") {
			t.Fatalf("want wrapped error, got %v", err)
		}
	})
	t.Run("create http error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("POST", "/api/v1/crews", 400, `{"error":"bad"}`)
		if _, err := NewClient(stub).CreateCrew(ctx, map[string]any{}); err == nil {
			t.Fatal("want error on 400")
		}
	})
	t.Run("create decode error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("POST", "/api/v1/crews", 201, `[not json`)
		_, err := NewClient(stub).CreateCrew(ctx, map[string]any{})
		if err == nil || !strings.Contains(err.Error(), "decode crew") {
			t.Fatalf("want decode error, got %v", err)
		}
	})
	t.Run("update transport error", func(t *testing.T) {
		stub := newCovStub()
		stub.onErr("PATCH", "/api/v1/crews/c1", errors.New("down"))
		_, err := NewClient(stub).UpdateCrew(ctx, "c1", map[string]any{})
		if err == nil || !strings.Contains(err.Error(), "update crew") {
			t.Fatalf("want wrapped error, got %v", err)
		}
	})
	t.Run("update http error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("PATCH", "/api/v1/crews/c1", 404, `{"error":"missing"}`)
		if _, err := NewClient(stub).UpdateCrew(ctx, "c1", map[string]any{}); err == nil {
			t.Fatal("want error on 404")
		}
	})
	t.Run("update decode error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("PATCH", "/api/v1/crews/c1", 200, `not json`)
		_, err := NewClient(stub).UpdateCrew(ctx, "c1", map[string]any{})
		if err == nil || !strings.Contains(err.Error(), "decode crew") {
			t.Fatalf("want decode error, got %v", err)
		}
	})
	t.Run("update success invalidates cache", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/crews", 200, `[]`)
		stub.on("PATCH", "/api/v1/crews/c1", 200, `{"id":"c1","name":"N"}`)
		c := NewClient(stub)
		_, _ = c.ListCrews(ctx)
		crew, err := c.UpdateCrew(ctx, "c1", map[string]any{"name": "N"})
		if err != nil {
			t.Fatalf("UpdateCrew: %v", err)
		}
		if crew.Name != "N" {
			t.Errorf("decoded name = %q, want N", crew.Name)
		}
		_, _ = c.ListCrews(ctx)
		if got := stub.countCalls("GET", "/api/v1/crews"); got != 2 {
			t.Errorf("expected re-fetch after update, GET count = %d", got)
		}
	})
}

// ---------- agents ----------

func TestClient_FindLeadAgentByCrew(t *testing.T) {
	ctx := context.Background()
	t.Run("lead found", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/agents?crew_id=c1", 200,
			`[{"id":"a1","slug":"worker","agent_role":"AGENT"},{"id":"a2","slug":"boss","agent_role":"LEAD"}]`)
		lead, err := NewClient(stub).FindLeadAgentByCrew(ctx, "c1")
		if err != nil {
			t.Fatalf("FindLeadAgentByCrew: %v", err)
		}
		if lead == nil || lead.ID != "a2" {
			t.Fatalf("want lead a2, got %+v", lead)
		}
	})
	t.Run("no lead", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/agents?crew_id=c1", 200, `[{"id":"a1","agent_role":"AGENT"}]`)
		lead, err := NewClient(stub).FindLeadAgentByCrew(ctx, "c1")
		if err != nil || lead != nil {
			t.Fatalf("want (nil, nil), got (%+v, %v)", lead, err)
		}
	})
	t.Run("transport error", func(t *testing.T) {
		stub := newCovStub()
		stub.onErr("GET", "/api/v1/agents?crew_id=c1", errors.New("down"))
		if _, err := NewClient(stub).FindLeadAgentByCrew(ctx, "c1"); err == nil {
			t.Fatal("want error")
		}
	})
}

func TestClient_ListAgentsByCrew_CachesAndDecodeError(t *testing.T) {
	ctx := context.Background()
	t.Run("caches per crew", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/agents?crew_id=c1", 200, `[{"id":"a1","slug":"x"}]`)
		c := NewClient(stub)
		first, err := c.ListAgentsByCrew(ctx, "c1")
		if err != nil || len(first) != 1 {
			t.Fatalf("first list: %v %v", first, err)
		}
		_, _ = c.ListAgentsByCrew(ctx, "c1")
		if got := stub.countCalls("GET", "/api/v1/agents?crew_id=c1"); got != 1 {
			t.Errorf("want 1 GET (cached second call), got %d", got)
		}
	})
	t.Run("decode error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/agents?crew_id=c1", 200, `{"bad":"shape"}`)
		_, err := NewClient(stub).ListAgentsByCrew(ctx, "c1")
		if err == nil || !strings.Contains(err.Error(), "decode agents") {
			t.Fatalf("want decode error, got %v", err)
		}
	})
}

func TestClient_CreateUpdateDeleteAgent_Errors(t *testing.T) {
	ctx := context.Background()
	t.Run("create transport", func(t *testing.T) {
		stub := newCovStub()
		stub.onErr("POST", "/api/v1/agents", errors.New("down"))
		_, err := NewClient(stub).CreateAgent(ctx, map[string]any{})
		if err == nil || !strings.Contains(err.Error(), "create agent") {
			t.Fatalf("want wrapped error, got %v", err)
		}
	})
	t.Run("create http error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("POST", "/api/v1/agents", 422, `{"error":"invalid"}`)
		if _, err := NewClient(stub).CreateAgent(ctx, map[string]any{}); err == nil {
			t.Fatal("want error on 422")
		}
	})
	t.Run("update transport", func(t *testing.T) {
		stub := newCovStub()
		stub.onErr("PATCH", "/api/v1/agents/a1", errors.New("down"))
		_, err := NewClient(stub).UpdateAgent(ctx, "a1", map[string]any{})
		if err == nil || !strings.Contains(err.Error(), "update agent") {
			t.Fatalf("want wrapped error, got %v", err)
		}
	})
	t.Run("update http error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("PATCH", "/api/v1/agents/a1", 403, `{"error":"nope"}`)
		if _, err := NewClient(stub).UpdateAgent(ctx, "a1", map[string]any{}); err == nil {
			t.Fatal("want error on 403")
		}
	})
	t.Run("update invalidates agent cache", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/agents?crew_id=c1", 200, `[]`)
		stub.on("PATCH", "/api/v1/agents/a1", 200, `{"id":"a1","crew_id":"c1","name":"N"}`)
		c := NewClient(stub)
		_, _ = c.ListAgentsByCrew(ctx, "c1")
		if _, err := c.UpdateAgent(ctx, "a1", map[string]any{"name": "N"}); err != nil {
			t.Fatalf("UpdateAgent: %v", err)
		}
		_, _ = c.ListAgentsByCrew(ctx, "c1")
		if got := stub.countCalls("GET", "/api/v1/agents?crew_id=c1"); got != 2 {
			t.Errorf("expected agent cache flush, GET count = %d", got)
		}
	})
	t.Run("delete success", func(t *testing.T) {
		stub := newCovStub()
		stub.on("DELETE", "/api/v1/agents/a1", 204, ``)
		if err := NewClient(stub).DeleteAgent(ctx, "a1", "c1"); err != nil {
			t.Fatalf("DeleteAgent: %v", err)
		}
		if stub.countCalls("DELETE", "/api/v1/agents/a1") != 1 {
			t.Error("expected one DELETE call")
		}
	})
	t.Run("delete transport", func(t *testing.T) {
		stub := newCovStub()
		stub.onErr("DELETE", "/api/v1/agents/a1", errors.New("down"))
		err := NewClient(stub).DeleteAgent(ctx, "a1", "c1")
		if err == nil || !strings.Contains(err.Error(), "delete agent") {
			t.Fatalf("want wrapped error, got %v", err)
		}
	})
	t.Run("delete http error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("DELETE", "/api/v1/agents/a1", 500, `{"error":"boom"}`)
		if err := NewClient(stub).DeleteAgent(ctx, "a1", "c1"); err == nil {
			t.Fatal("want error on 500")
		}
	})
}

// ---------- skills ----------

func TestClient_ImportSkill(t *testing.T) {
	ctx := context.Background()
	t.Run("requires workspace id", func(t *testing.T) {
		stub := newCovStub()
		stub.wsID = ""
		_, err := NewClient(stub).ImportSkill(ctx, map[string]any{})
		if err == nil || !strings.Contains(err.Error(), "workspace_id is required") {
			t.Fatalf("want workspace_id error, got %v", err)
		}
	})
	t.Run("normalises skill_id to id", func(t *testing.T) {
		stub := newCovStub()
		stub.on("POST", "/api/v1/workspaces/ws_cov/skills/import", 201,
			`{"skill_id":"sk_42","slug":"house-style","created":true}`)
		sr, err := NewClient(stub).ImportSkill(ctx, map[string]any{"content": "x"})
		if err != nil {
			t.Fatalf("ImportSkill: %v", err)
		}
		if sr.ID != "sk_42" {
			t.Errorf("want normalised ID sk_42, got %q", sr.ID)
		}
	})
	t.Run("transport error", func(t *testing.T) {
		stub := newCovStub()
		stub.onErr("POST", "/api/v1/workspaces/ws_cov/skills/import", errors.New("down"))
		_, err := NewClient(stub).ImportSkill(ctx, map[string]any{})
		if err == nil || !strings.Contains(err.Error(), "import skill") {
			t.Fatalf("want wrapped error, got %v", err)
		}
	})
	t.Run("http error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("POST", "/api/v1/workspaces/ws_cov/skills/import", 422, `{"error":"bad license"}`)
		if _, err := NewClient(stub).ImportSkill(ctx, map[string]any{}); err == nil {
			t.Fatal("want error on 422")
		}
	})
}

func TestClient_ListSkills_FallbackPath(t *testing.T) {
	ctx := context.Background()
	t.Run("workspace path works", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/workspaces/ws_cov/skills", 200, `[{"id":"s1","slug":"a"}]`)
		skills, err := NewClient(stub).ListSkills(ctx)
		if err != nil || len(skills) != 1 || skills[0].Slug != "a" {
			t.Fatalf("got %v, %v", skills, err)
		}
	})
	t.Run("falls back to legacy path", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/workspaces/ws_cov/skills", 500, `{"error":"nope"}`)
		stub.on("GET", "/api/v1/skills", 200, `{"skills":[{"id":"s2","slug":"b"}]}`)
		skills, err := NewClient(stub).ListSkills(ctx)
		if err != nil || len(skills) != 1 || skills[0].ID != "s2" {
			t.Fatalf("fallback failed: %v, %v", skills, err)
		}
	})
	t.Run("both paths fail returns first error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/workspaces/ws_cov/skills", 500, `{"error":"primary"}`)
		stub.on("GET", "/api/v1/skills", 500, `{"error":"secondary"}`)
		if _, err := NewClient(stub).ListSkills(ctx); err == nil {
			t.Fatal("want error when both paths fail")
		}
	})
}

func TestDecodeSkillsList_Shapes(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		out, err := decodeSkillsList(nil)
		if err != nil || out != nil {
			t.Fatalf("want (nil, nil), got (%v, %v)", out, err)
		}
	})
	t.Run("flat", func(t *testing.T) {
		out, err := decodeSkillsList([]byte(`[{"id":"s1"}]`))
		if err != nil || len(out) != 1 {
			t.Fatalf("flat decode: %v, %v", out, err)
		}
	})
	t.Run("wrapped", func(t *testing.T) {
		out, err := decodeSkillsList([]byte(`{"skills":[{"id":"s1"},{"id":"s2"}]}`))
		if err != nil || len(out) != 2 {
			t.Fatalf("wrapped decode: %v, %v", out, err)
		}
	})
	t.Run("unknown shape", func(t *testing.T) {
		_, err := decodeSkillsList([]byte(`"just a string`))
		if err == nil || !strings.Contains(err.Error(), "unknown shape") {
			t.Fatalf("want unknown-shape error, got %v", err)
		}
	})
}

func TestClient_AddRemoveSkillToAgent(t *testing.T) {
	ctx := context.Background()
	t.Run("add conflict is idempotent", func(t *testing.T) {
		stub := newCovStub()
		stub.on("POST", "/api/v1/agents/a1/skills", 409, `{"error":"dup"}`)
		if err := NewClient(stub).AddSkillToAgent(ctx, "a1", "s1"); err != nil {
			t.Fatalf("409 should be swallowed, got %v", err)
		}
	})
	t.Run("add transport error", func(t *testing.T) {
		stub := newCovStub()
		stub.onErr("POST", "/api/v1/agents/a1/skills", errors.New("down"))
		err := NewClient(stub).AddSkillToAgent(ctx, "a1", "s1")
		if err == nil || !strings.Contains(err.Error(), "add skill to agent") {
			t.Fatalf("want wrapped error, got %v", err)
		}
	})
	t.Run("add posts skill_id", func(t *testing.T) {
		stub := newCovStub()
		stub.on("POST", "/api/v1/agents/a1/skills", 201, `{}`)
		if err := NewClient(stub).AddSkillToAgent(ctx, "a1", "s1"); err != nil {
			t.Fatalf("AddSkillToAgent: %v", err)
		}
		if got := stub.calls[0].Body["skill_id"]; got != "s1" {
			t.Errorf("POST body skill_id = %v, want s1", got)
		}
	})
	t.Run("remove 404 is idempotent", func(t *testing.T) {
		stub := newCovStub()
		stub.on("DELETE", "/api/v1/agents/a1/skills/s1", 404, `{"error":"gone"}`)
		if err := NewClient(stub).RemoveSkillFromAgent(ctx, "a1", "s1"); err != nil {
			t.Fatalf("404 should be swallowed, got %v", err)
		}
	})
	t.Run("remove transport error", func(t *testing.T) {
		stub := newCovStub()
		stub.onErr("DELETE", "/api/v1/agents/a1/skills/s1", errors.New("down"))
		err := NewClient(stub).RemoveSkillFromAgent(ctx, "a1", "s1")
		if err == nil || !strings.Contains(err.Error(), "remove skill from agent") {
			t.Fatalf("want wrapped error, got %v", err)
		}
	})
}

func TestClient_ListAgentSkillsAndCredentials_Errors(t *testing.T) {
	ctx := context.Background()
	t.Run("skills decode error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/agents/a1/skills", 200, `{"x":1}`)
		_, err := NewClient(stub).ListAgentSkills(ctx, "a1")
		if err == nil || !strings.Contains(err.Error(), "decode agent skills") {
			t.Fatalf("want decode error, got %v", err)
		}
	})
	t.Run("skills fetch error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/agents/a1/skills", 500, `{"error":"x"}`)
		if _, err := NewClient(stub).ListAgentSkills(ctx, "a1"); err == nil {
			t.Fatal("want error on 500")
		}
	})
	t.Run("credentials decode error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/agents/a1/credentials", 200, `{"x":1}`)
		_, err := NewClient(stub).ListAgentCredentials(ctx, "a1")
		if err == nil || !strings.Contains(err.Error(), "decode agent credentials") {
			t.Fatalf("want decode error, got %v", err)
		}
	})
	t.Run("credentials fetch error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/agents/a1/credentials", 500, `{"error":"x"}`)
		if _, err := NewClient(stub).ListAgentCredentials(ctx, "a1"); err == nil {
			t.Fatal("want error on 500")
		}
	})
}

func TestClient_RemoveCredentialFromAgent(t *testing.T) {
	ctx := context.Background()
	t.Run("success", func(t *testing.T) {
		stub := newCovStub()
		stub.on("DELETE", "/api/v1/agents/a1/credentials/bind1", 204, ``)
		if err := NewClient(stub).RemoveCredentialFromAgent(ctx, "a1", "bind1"); err != nil {
			t.Fatalf("RemoveCredentialFromAgent: %v", err)
		}
	})
	t.Run("404 idempotent", func(t *testing.T) {
		stub := newCovStub()
		stub.on("DELETE", "/api/v1/agents/a1/credentials/bind1", 404, `{"error":"gone"}`)
		if err := NewClient(stub).RemoveCredentialFromAgent(ctx, "a1", "bind1"); err != nil {
			t.Fatalf("404 should be swallowed, got %v", err)
		}
	})
	t.Run("transport error", func(t *testing.T) {
		stub := newCovStub()
		stub.onErr("DELETE", "/api/v1/agents/a1/credentials/bind1", errors.New("down"))
		err := NewClient(stub).RemoveCredentialFromAgent(ctx, "a1", "bind1")
		if err == nil || !strings.Contains(err.Error(), "remove credential from agent") {
			t.Fatalf("want wrapped error, got %v", err)
		}
	})
	t.Run("server error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("DELETE", "/api/v1/agents/a1/credentials/bind1", 500, `{"error":"boom"}`)
		if err := NewClient(stub).RemoveCredentialFromAgent(ctx, "a1", "bind1"); err == nil {
			t.Fatal("want error on 500")
		}
	})
}

// ---------- credentials ----------

func TestClient_ListCredentials_Shapes(t *testing.T) {
	ctx := context.Background()
	t.Run("wrapped shape", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/credentials", 200, `{"credentials":[{"id":"cr1","name":"KEY"}]}`)
		c := NewClient(stub)
		creds, err := c.ListCredentials(ctx)
		if err != nil || len(creds) != 1 || creds[0].Name != "KEY" {
			t.Fatalf("wrapped decode: %v, %v", creds, err)
		}
		// Second call is served from cache.
		_, _ = c.ListCredentials(ctx)
		if got := stub.countCalls("GET", "/api/v1/credentials"); got != 1 {
			t.Errorf("want cached second call, GET count = %d", got)
		}
	})
	t.Run("empty body", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/credentials", 200, ``)
		creds, err := NewClient(stub).ListCredentials(ctx)
		if err != nil || creds != nil {
			t.Fatalf("want (nil, nil), got (%v, %v)", creds, err)
		}
	})
	t.Run("unknown shape", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/credentials", 200, `"oops`)
		_, err := NewClient(stub).ListCredentials(ctx)
		if err == nil || !strings.Contains(err.Error(), "decode credentials") {
			t.Fatalf("want decode error, got %v", err)
		}
	})
	t.Run("fetch error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/credentials", 500, `{"error":"x"}`)
		if _, err := NewClient(stub).ListCredentials(ctx); err == nil {
			t.Fatal("want error on 500")
		}
	})
}

func TestClient_CreateCredential_Errors(t *testing.T) {
	ctx := context.Background()
	t.Run("transport error", func(t *testing.T) {
		stub := newCovStub()
		stub.onErr("POST", "/api/v1/credentials", errors.New("down"))
		_, err := NewClient(stub).CreateCredential(ctx, map[string]any{})
		if err == nil || !strings.Contains(err.Error(), "create credential") {
			t.Fatalf("want wrapped error, got %v", err)
		}
	})
	t.Run("http error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("POST", "/api/v1/credentials", 409, `{"error":"dup"}`)
		if _, err := NewClient(stub).CreateCredential(ctx, map[string]any{}); err == nil {
			t.Fatal("want error on 409")
		}
	})
	t.Run("success invalidates cred cache", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/credentials", 200, `[]`)
		stub.on("POST", "/api/v1/credentials", 201, `{"id":"cr1","name":"KEY"}`)
		c := NewClient(stub)
		_, _ = c.ListCredentials(ctx)
		cred, err := c.CreateCredential(ctx, map[string]any{"name": "KEY"})
		if err != nil || cred.ID != "cr1" {
			t.Fatalf("CreateCredential: %v, %v", cred, err)
		}
		_, _ = c.ListCredentials(ctx)
		if got := stub.countCalls("GET", "/api/v1/credentials"); got != 2 {
			t.Errorf("expected cred cache flush, GET count = %d", got)
		}
	})
}

func TestClient_LinkCredentialToAgent(t *testing.T) {
	ctx := context.Background()
	t.Run("posts ids and env name", func(t *testing.T) {
		stub := newCovStub()
		stub.on("POST", "/api/v1/agents/a1/credentials", 201, `{}`)
		if err := NewClient(stub).LinkCredentialToAgent(ctx, "a1", "cr1", "MY_KEY"); err != nil {
			t.Fatalf("LinkCredentialToAgent: %v", err)
		}
		body := stub.calls[0].Body
		if body["credential_id"] != "cr1" || body["env_var_name"] != "MY_KEY" {
			t.Errorf("unexpected POST body: %v", body)
		}
	})
	t.Run("409 idempotent", func(t *testing.T) {
		stub := newCovStub()
		stub.on("POST", "/api/v1/agents/a1/credentials", 409, `{"error":"dup"}`)
		if err := NewClient(stub).LinkCredentialToAgent(ctx, "a1", "cr1", "K"); err != nil {
			t.Fatalf("409 should be swallowed, got %v", err)
		}
	})
	t.Run("transport error", func(t *testing.T) {
		stub := newCovStub()
		stub.onErr("POST", "/api/v1/agents/a1/credentials", errors.New("down"))
		err := NewClient(stub).LinkCredentialToAgent(ctx, "a1", "cr1", "K")
		if err == nil || !strings.Contains(err.Error(), "link credential to agent") {
			t.Fatalf("want wrapped error, got %v", err)
		}
	})
}

// ---------- MCP integrations ----------

func TestClient_ListCrewIntegrations_Shapes(t *testing.T) {
	ctx := context.Background()
	t.Run("flat", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/crews/c1/integrations", 200, `[{"id":"m1","name":"gh"}]`)
		out, err := NewClient(stub).ListCrewIntegrations(ctx, "c1")
		if err != nil || len(out) != 1 || out[0].Name != "gh" {
			t.Fatalf("flat decode: %v, %v", out, err)
		}
	})
	t.Run("wrapped", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/crews/c1/integrations", 200, `{"integrations":[{"id":"m1"},{"id":"m2"}]}`)
		out, err := NewClient(stub).ListCrewIntegrations(ctx, "c1")
		if err != nil || len(out) != 2 {
			t.Fatalf("wrapped decode: %v, %v", out, err)
		}
	})
	t.Run("empty body", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/crews/c1/integrations", 200, ``)
		out, err := NewClient(stub).ListCrewIntegrations(ctx, "c1")
		if err != nil || out != nil {
			t.Fatalf("want (nil, nil), got (%v, %v)", out, err)
		}
	})
	t.Run("unknown shape", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/crews/c1/integrations", 200, `"oops`)
		_, err := NewClient(stub).ListCrewIntegrations(ctx, "c1")
		if err == nil || !strings.Contains(err.Error(), "unknown shape") {
			t.Fatalf("want unknown-shape error, got %v", err)
		}
	})
	t.Run("fetch error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/crews/c1/integrations", 500, `{"error":"x"}`)
		if _, err := NewClient(stub).ListCrewIntegrations(ctx, "c1"); err == nil {
			t.Fatal("want error on 500")
		}
	})
}

func TestClient_CreateCrewIntegration(t *testing.T) {
	ctx := context.Background()
	t.Run("success", func(t *testing.T) {
		stub := newCovStub()
		stub.on("POST", "/api/v1/crews/c1/integrations", 201, `{"id":"m1"}`)
		if err := NewClient(stub).CreateCrewIntegration(ctx, "c1", map[string]any{"name": "gh"}); err != nil {
			t.Fatalf("CreateCrewIntegration: %v", err)
		}
		if stub.calls[0].Body["name"] != "gh" {
			t.Errorf("unexpected POST body: %v", stub.calls[0].Body)
		}
	})
	t.Run("conflict surfaces error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("POST", "/api/v1/crews/c1/integrations", 409, `{"error":"duplicate name"}`)
		err := NewClient(stub).CreateCrewIntegration(ctx, "c1", map[string]any{})
		if err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("want already-exists error, got %v", err)
		}
	})
	t.Run("transport error", func(t *testing.T) {
		stub := newCovStub()
		stub.onErr("POST", "/api/v1/crews/c1/integrations", errors.New("down"))
		err := NewClient(stub).CreateCrewIntegration(ctx, "c1", map[string]any{})
		if err == nil || !strings.Contains(err.Error(), "create crew integration") {
			t.Fatalf("want wrapped error, got %v", err)
		}
	})
	t.Run("server error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("POST", "/api/v1/crews/c1/integrations", 500, `{"error":"boom"}`)
		if err := NewClient(stub).CreateCrewIntegration(ctx, "c1", map[string]any{}); err == nil {
			t.Fatal("want error on 500")
		}
	})
}

func TestClient_DeleteCrewIntegration(t *testing.T) {
	ctx := context.Background()
	t.Run("success", func(t *testing.T) {
		stub := newCovStub()
		stub.on("DELETE", "/api/v1/crews/c1/integrations/m1", 204, ``)
		if err := NewClient(stub).DeleteCrewIntegration(ctx, "c1", "m1"); err != nil {
			t.Fatalf("DeleteCrewIntegration: %v", err)
		}
	})
	t.Run("404 idempotent", func(t *testing.T) {
		stub := newCovStub()
		stub.on("DELETE", "/api/v1/crews/c1/integrations/m1", 404, `{"error":"gone"}`)
		if err := NewClient(stub).DeleteCrewIntegration(ctx, "c1", "m1"); err != nil {
			t.Fatalf("404 should be swallowed, got %v", err)
		}
	})
	t.Run("transport error", func(t *testing.T) {
		stub := newCovStub()
		stub.onErr("DELETE", "/api/v1/crews/c1/integrations/m1", errors.New("down"))
		err := NewClient(stub).DeleteCrewIntegration(ctx, "c1", "m1")
		if err == nil || !strings.Contains(err.Error(), "delete crew integration") {
			t.Fatalf("want wrapped error, got %v", err)
		}
	})
	t.Run("server error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("DELETE", "/api/v1/crews/c1/integrations/m1", 500, `{"error":"boom"}`)
		if err := NewClient(stub).DeleteCrewIntegration(ctx, "c1", "m1"); err == nil {
			t.Fatal("want error on 500")
		}
	})
}

// ---------- helpers ----------

func TestClient_FetchSkillContent(t *testing.T) {
	ctx := context.Background()
	t.Run("returns content", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/skills/s1", 200, `{"content":"# body"}`)
		if got := NewClient(stub).fetchSkillContent(ctx, "s1"); got != "# body" {
			t.Errorf("got %q, want body", got)
		}
	})
	t.Run("null content", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/skills/s1", 200, `{"content":null}`)
		if got := NewClient(stub).fetchSkillContent(ctx, "s1"); got != "" {
			t.Errorf("want empty for null content, got %q", got)
		}
	})
	t.Run("fetch error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/skills/s1", 500, `{"error":"x"}`)
		if got := NewClient(stub).fetchSkillContent(ctx, "s1"); got != "" {
			t.Errorf("want empty on error, got %q", got)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/skills/s1", 200, `not json`)
		if got := NewClient(stub).fetchSkillContent(ctx, "s1"); got != "" {
			t.Errorf("want empty on bad json, got %q", got)
		}
	})
}

func TestFirstBytes(t *testing.T) {
	if got := firstBytes([]byte("short"), 80); got != "short" {
		t.Errorf("short input: got %q", got)
	}
	long := strings.Repeat("x", 100)
	got := firstBytes([]byte(long), 80)
	if !strings.HasPrefix(got, strings.Repeat("x", 80)) || !strings.HasSuffix(got, "…") {
		t.Errorf("long input should be truncated with ellipsis, got %q", got)
	}
}

func TestClient_InvalidateMCPs(t *testing.T) {
	stub := newCovStub()
	c := NewClient(stub)
	c.mcpsByCrew["c1"] = []MCPServerResponse{{ID: "m1"}}
	c.invalidateMCPs("c1")
	if _, ok := c.mcpsByCrew["c1"]; ok {
		t.Error("invalidateMCPs should drop the cache entry")
	}
}

// ---------- cliAdapter / NewClientFromCLI ----------

func TestNewClientFromCLI_AdapterRoutesAllVerbs(t *testing.T) {
	type seen struct{ method, path string }
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, seen{r.Method, r.URL.Path})
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/crews":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == "POST" && r.URL.Path == "/api/v1/crews":
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":"c1","slug":"t"}`))
		case r.Method == "PATCH" && r.URL.Path == "/api/v1/crews/c1":
			_, _ = w.Write([]byte(`{"id":"c1","slug":"t"}`))
		case r.Method == "DELETE" && r.URL.Path == "/api/v1/crews/c1":
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"no route"}`))
		}
	}))
	defer srv.Close()

	// Workspace ID shaped like a CUID so GetWorkspaceID returns it
	// without a resolution round-trip.
	wsID := "c0000000000000000000ws"
	cliClient := cli.NewClient(srv.URL, "tok", wsID)
	mc := NewClientFromCLI(cliClient)

	ctx := context.Background()
	if _, err := mc.ListCrews(ctx); err != nil {
		t.Fatalf("ListCrews via adapter: %v", err)
	}
	if _, err := mc.CreateCrew(ctx, map[string]any{"slug": "t"}); err != nil {
		t.Fatalf("CreateCrew via adapter: %v", err)
	}
	if _, err := mc.UpdateCrew(ctx, "c1", map[string]any{"name": "T"}); err != nil {
		t.Fatalf("UpdateCrew via adapter: %v", err)
	}
	if err := mc.DeleteCrew(ctx, "c1"); err != nil {
		t.Fatalf("DeleteCrew via adapter: %v", err)
	}
	if id := mc.api.GetWorkspaceID(); id != wsID {
		t.Errorf("GetWorkspaceID = %q, want %q", id, wsID)
	}

	wantOrder := []seen{
		{"GET", "/api/v1/crews"},
		{"POST", "/api/v1/crews"},
		{"PATCH", "/api/v1/crews/c1"},
		{"DELETE", "/api/v1/crews/c1"},
	}
	if len(got) != len(wantOrder) {
		t.Fatalf("server saw %d requests, want %d: %+v", len(got), len(wantOrder), got)
	}
	for i, w := range wantOrder {
		if got[i] != w {
			t.Errorf("request[%d] = %+v, want %+v", i, got[i], w)
		}
	}
}
