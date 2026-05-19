package manifest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// fakeAPIClient is a minimal in-memory stand-in for *cli.Client. It
// records every call so a test can assert what the manifest layer
// would have done over the wire. Bodies are mutated in place when
// helpful (server-assigned IDs are returned in responses).
type fakeAPIClient struct {
	t                  *testing.T
	wsID               string
	crewID             int
	agentID            int
	skillID            int
	credID             int
	crewsBySlug        map[string]map[string]any
	agentsBySlug       map[string]map[string]any
	skillsBySlug       map[string]map[string]any
	credsByName        map[string]map[string]any
	integrationsByCrew map[string]map[string]map[string]any
	agentSkillBindings map[string][]map[string]any // agentID → bindings
	agentCredBindings  map[string][]map[string]any

	Calls []fakeCall
}

type fakeCall struct {
	Method string
	Path   string
	Body   map[string]any
}

func newFakeAPI(t *testing.T) *fakeAPIClient {
	return &fakeAPIClient{
		t:                  t,
		wsID:               "ws_test",
		crewsBySlug:        map[string]map[string]any{},
		agentsBySlug:       map[string]map[string]any{},
		skillsBySlug:       map[string]map[string]any{},
		credsByName:        map[string]map[string]any{},
		integrationsByCrew: map[string]map[string]map[string]any{},
		agentSkillBindings: map[string][]map[string]any{},
		agentCredBindings:  map[string][]map[string]any{},
	}
}

func (f *fakeAPIClient) GetWorkspaceID() string { return f.wsID }

func (f *fakeAPIClient) record(method, path string, body any) {
	bmap, _ := body.(map[string]any)
	f.Calls = append(f.Calls, fakeCall{Method: method, Path: path, Body: bmap})
}

func resp(status int, v any) *http.Response {
	data, _ := json.Marshal(v)
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(data)),
		Header:     http.Header{},
	}
}

func (f *fakeAPIClient) Get(path string) (*http.Response, error) {
	f.record("GET", path, nil)
	switch {
	case path == "/api/v1/crews":
		var out []map[string]any
		for _, c := range f.crewsBySlug {
			out = append(out, c)
		}
		return resp(200, out), nil
	case strings.HasPrefix(path, "/api/v1/agents?crew_id="):
		var out []map[string]any
		for _, a := range f.agentsBySlug {
			out = append(out, a)
		}
		return resp(200, out), nil
	case path == "/api/v1/credentials":
		var out []map[string]any
		for _, c := range f.credsByName {
			out = append(out, c)
		}
		return resp(200, out), nil
	case strings.HasPrefix(path, "/api/v1/crews/") && strings.HasSuffix(path, "/integrations"):
		return resp(200, []map[string]any{}), nil
	case strings.HasPrefix(path, "/api/v1/agents/") && strings.HasSuffix(path, "/skills"):
		agentID := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/agents/"), "/skills")
		return resp(200, f.agentSkillBindings[agentID]), nil
	case strings.HasPrefix(path, "/api/v1/agents/") && strings.HasSuffix(path, "/credentials"):
		agentID := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/agents/"), "/credentials")
		return resp(200, f.agentCredBindings[agentID]), nil
	case strings.Contains(path, "/skills"):
		var out []map[string]any
		for _, s := range f.skillsBySlug {
			out = append(out, s)
		}
		return resp(200, out), nil
	}
	return resp(404, map[string]any{"error": "not found"}), nil
}

func (f *fakeAPIClient) Post(path string, body any) (*http.Response, error) {
	f.record("POST", path, body)
	b, _ := body.(map[string]any)
	switch {
	case path == "/api/v1/crews":
		f.crewID++
		id := stringID("crew", f.crewID)
		b["id"] = id
		b["workspace_id"] = f.wsID
		slug, _ := b["slug"].(string)
		f.crewsBySlug[slug] = b
		return resp(201, b), nil
	case path == "/api/v1/agents":
		f.agentID++
		id := stringID("agent", f.agentID)
		b["id"] = id
		b["workspace_id"] = f.wsID
		slug, _ := b["slug"].(string)
		f.agentsBySlug[slug] = b
		return resp(201, b), nil
	case path == "/api/v1/credentials":
		f.credID++
		id := stringID("cred", f.credID)
		b["id"] = id
		b["workspace_id"] = f.wsID
		name, _ := b["name"].(string)
		// preserve status field for assertions
		if pending, _ := b["pending"].(bool); pending {
			b["status"] = "PENDING"
		} else {
			b["status"] = "ACTIVE"
		}
		f.credsByName[name] = b
		return resp(201, b), nil
	case strings.HasPrefix(path, "/api/v1/workspaces/") && strings.HasSuffix(path, "/skills/import"):
		f.skillID++
		id := stringID("skill", f.skillID)
		// Skill slug comes from frontmatter parsing on the real server;
		// the fake reads it from the inline content or url for testing.
		slug := extractSlug(b)
		response := map[string]any{
			"id":      id,
			"slug":    slug,
			"created": true,
		}
		f.skillsBySlug[slug] = response
		return resp(201, response), nil
	case strings.HasPrefix(path, "/api/v1/agents/") && strings.HasSuffix(path, "/skills"):
		return resp(201, map[string]any{"ok": true}), nil
	case strings.HasPrefix(path, "/api/v1/agents/") && strings.HasSuffix(path, "/credentials"):
		return resp(201, map[string]any{"ok": true}), nil
	case strings.HasPrefix(path, "/api/v1/crews/") && strings.HasSuffix(path, "/integrations"):
		return resp(201, map[string]any{"ok": true}), nil
	}
	return resp(404, map[string]any{"error": "not found"}), nil
}

func (f *fakeAPIClient) Patch(path string, body any) (*http.Response, error) {
	f.record("PATCH", path, body)
	return resp(200, body), nil
}

func (f *fakeAPIClient) Delete(path string) (*http.Response, error) {
	f.record("DELETE", path, nil)
	// Track deletes that sync mode performs so tests can assert
	// the right resource was targeted. The map state stays
	// authoritative — subsequent GETs reflect the removal.
	if strings.HasPrefix(path, "/api/v1/agents/") && !strings.Contains(path[len("/api/v1/agents/"):], "/") {
		agentID := strings.TrimPrefix(path, "/api/v1/agents/")
		for slug, a := range f.agentsBySlug {
			if id, _ := a["id"].(string); id == agentID {
				delete(f.agentsBySlug, slug)
				break
			}
		}
	}
	return resp(204, map[string]any{}), nil
}

func stringID(prefix string, n int) string {
	return prefix + "_" + leftPad(intToStr(n))
}

func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func leftPad(s string) string {
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}

func extractSlug(body map[string]any) string {
	// Real importer parses frontmatter; in tests we accept either an
	// inline body or a URL whose path-trailing segment is the slug.
	if inline, _ := body["content"].(string); inline != "" {
		for _, line := range strings.Split(inline, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "name:") {
				return strings.TrimSpace(strings.TrimPrefix(line, "name:"))
			}
		}
	}
	if url, _ := body["url"].(string); url != "" {
		parts := strings.Split(url, "/")
		if len(parts) > 1 {
			return parts[len(parts)-2]
		}
	}
	return "unknown-skill"
}

// ----------------- tests proper -----------------

func TestApply_Upsert_CreatesEverything(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: { name: T, slug: t }
spec:
  credentials:
    - { env: ANTHROPIC_API_KEY, provider: ANTHROPIC, type: API_KEY }
  skills:
    - slug: house-style
      inline: |
        ---
        name: house-style
        description: x
        license: MIT
        ---
        # body
  agents:
    - slug: alice
      name: Alice
      agent_role: LEAD
      cli_adapter: CLAUDE_CODE
      prompt: hi
      skills: [house-style]
      env_refs: [ANTHROPIC_API_KEY]
`)
	b, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for i := range b.Documents[0].Spec.Skills {
		b.Documents[0].Spec.Skills[i].SetResolved(b.Documents[0].Spec.Skills[i].Inline)
	}

	fake := newFakeAPI(t)
	client := NewClient(fake)
	res, err := Apply(context.Background(), client, b, Options{Mode: ApplyUpsert, Secrets: NoSecretsSource{}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Created == 0 {
		t.Fatal("expected created > 0")
	}
	if len(res.PendingCredentials) != 1 || res.PendingCredentials[0] != "ANTHROPIC_API_KEY" {
		t.Errorf("want one pending credential ANTHROPIC_API_KEY, got %v", res.PendingCredentials)
	}

	// Make sure the credential POST included pending: true (no value
	// supplied by NoSecretsSource).
	var sawPending bool
	for _, call := range fake.Calls {
		if call.Method == "POST" && call.Path == "/api/v1/credentials" {
			if v, _ := call.Body["pending"].(bool); v {
				sawPending = true
			}
		}
	}
	if !sawPending {
		t.Error("expected credential POST with pending:true")
	}
}

func TestApply_StrictRejectsExistingCrew(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: { name: T, slug: t }
spec:
  agents:
    - { slug: a, name: A, agent_role: LEAD, prompt: x }
`)
	b, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	fake := newFakeAPI(t)
	fake.crewsBySlug["t"] = map[string]any{"id": "crew_existing", "slug": "t", "workspace_id": fake.wsID, "name": "T"}
	client := NewClient(fake)
	_, err = Apply(context.Background(), client, b, Options{Mode: ApplyStrict})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("want strict-conflict error, got %v", err)
	}
}

func TestApply_UpsertUpdatesExistingCrew(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: { name: T-NEW, slug: t }
spec:
  agents:
    - { slug: a, name: A, agent_role: LEAD, prompt: x }
`)
	b, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	fake := newFakeAPI(t)
	fake.crewsBySlug["t"] = map[string]any{"id": "crew_existing", "slug": "t", "workspace_id": fake.wsID, "name": "T-OLD"}
	client := NewClient(fake)
	res, err := Apply(context.Background(), client, b, Options{Mode: ApplyUpsert})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Updated == 0 {
		t.Error("expected at least one updated record")
	}
}

func TestApply_FromEnvAttachesValue(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: { name: T, slug: t }
spec:
  credentials:
    - { env: MY_KEY, provider: NONE, type: API_KEY }
  agents:
    - { slug: a, name: A, agent_role: LEAD, prompt: x, env_refs: [MY_KEY] }
`)
	b, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	src := MapSecretsSource{"MY_KEY": "secret-from-env"}
	fake := newFakeAPI(t)
	client := NewClient(fake)
	res, err := Apply(context.Background(), client, b, Options{Mode: ApplyUpsert, Secrets: src})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.PendingCredentials) != 0 {
		t.Errorf("expected no pending creds when MapSecretsSource supplied, got %v", res.PendingCredentials)
	}
	// Verify the POST body carried the value.
	var got string
	for _, call := range fake.Calls {
		if call.Method == "POST" && call.Path == "/api/v1/credentials" {
			if v, _ := call.Body["value"].(string); v != "" {
				got = v
			}
		}
	}
	if got != "secret-from-env" {
		t.Errorf("expected credential POST to carry value, got %q", got)
	}
}

func TestApply_DryRunMakesNoMutatingCalls(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: { name: T, slug: t }
spec:
  agents:
    - { slug: a, name: A, agent_role: LEAD, prompt: x }
`)
	b, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	fake := newFakeAPI(t)
	client := NewClient(fake)
	_, err = Apply(context.Background(), client, b, Options{Mode: ApplyUpsert, DryRun: true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, call := range fake.Calls {
		if call.Method == "POST" || call.Method == "PATCH" || call.Method == "DELETE" {
			t.Errorf("dry-run should not POST/PATCH/DELETE, got %s %s", call.Method, call.Path)
		}
	}
}

func TestApply_IdempotentReapply(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: { name: T, slug: t }
spec:
  agents:
    - { slug: a, name: A, agent_role: LEAD, prompt: x }
`)
	b1, errLoad1 := Load(body)
	if errLoad1 != nil {
		t.Fatalf("Load: %v", errLoad1)
	}
	b2, errLoad2 := Load(body)
	if errLoad2 != nil {
		t.Fatalf("Load: %v", errLoad2)
	}

	fake := newFakeAPI(t)
	client := NewClient(fake)

	first, err := Apply(context.Background(), client, b1, Options{Mode: ApplyUpsert})
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if first.Created == 0 {
		t.Fatal("first apply should create resources")
	}

	second, err := Apply(context.Background(), client, b2, Options{Mode: ApplyUpsert, Yes: true})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if second.Created != 0 {
		t.Errorf("second apply created %d new resources (want 0)", second.Created)
	}
	if second.Updated != 0 {
		t.Errorf("second apply updated %d resources (want 0 — manifest matches state)", second.Updated)
	}
	if second.Unchanged == 0 {
		t.Error("second apply should mark resources unchanged, got 0")
	}
}
