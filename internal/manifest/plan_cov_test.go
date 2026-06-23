package manifest

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestAction_String(t *testing.T) {
	cases := []struct {
		a    Action
		want string
	}{
		{ActionCreate, "+"},
		{ActionUpdate, "~"},
		{ActionUnchanged, "="},
		{ActionDelete, "-"},
		{Action(42), "?"},
	}
	for _, tc := range cases {
		if got := tc.a.String(); got != tc.want {
			t.Errorf("Action(%d).String() = %q, want %q", tc.a, got, tc.want)
		}
	}
}

func TestPlan_RenderAndSummary(t *testing.T) {
	p := &Plan{Items: []PlanItem{
		{Action: ActionCreate, Kind: "crew", Description: "ops"},
		{Action: ActionUpdate, Kind: "agent", Description: "ops/amy"},
		{Action: ActionUnchanged, Kind: "credential", Description: "KEY"},
		{Action: ActionDelete, Kind: "mcp", Description: "ops/old"},
	}}
	out := p.Render()
	for _, want := range []string{"+ crew ops", "~ agent ops/amy", "= credential KEY", "- mcp ops/old"} {
		if !strings.Contains(out, want) {
			t.Errorf("Render missing %q:\n%s", want, out)
		}
	}
	c, u, n, d := p.Summary()
	if c != 1 || u != 1 || n != 1 || d != 1 {
		t.Errorf("Summary = (%d,%d,%d,%d), want (1,1,1,1)", c, u, n, d)
	}
	if !p.HasDestructive() {
		t.Error("plan with delete must report destructive")
	}
}

func TestPlanActionOrder_Unknown(t *testing.T) {
	if got := planActionOrder(Action(99)); got != 4 {
		t.Errorf("unknown action order = %d, want 4", got)
	}
}

func TestKindOrder_DeleteReversal(t *testing.T) {
	// On create: crew (parent) before agent (child).
	if !(kindOrder("crew", ActionCreate) < kindOrder("agent", ActionCreate)) {
		t.Error("create: crew should rank before agent")
	}
	// On delete: agent (child) before crew (parent).
	if !(kindOrder("agent", ActionDelete) < kindOrder("crew", ActionDelete)) {
		t.Error("delete: agent should rank before crew")
	}
	// Unknown kinds rank last in both directions.
	if kindOrder("???", ActionCreate) != 99 || kindOrder("???", ActionDelete) != 99 {
		t.Error("unknown kind should rank 99 regardless of action")
	}
	// SPEC-2 kinds tear down before their FK parents.
	if !(kindOrder("Schedule", ActionDelete) < kindOrder("Routine", ActionDelete)) {
		t.Error("delete: Schedule should rank before Routine")
	}
}

func TestMCPBodyDiffers(t *testing.T) {
	strp := func(s string) *string { return &s }
	existing := &MCPServerResponse{Transport: "stdio", Command: strp("run"), Endpoint: strp("http://e")}
	cases := []struct {
		name string
		m    MCPServer
		want bool
	}{
		{"identical", MCPServer{Transport: "stdio", Command: "run", Endpoint: "http://e"}, false},
		{"transport drift", MCPServer{Transport: "sse", Command: "run"}, true},
		{"command drift", MCPServer{Transport: "stdio", Command: "other"}, true},
		{"endpoint drift", MCPServer{Transport: "stdio", Command: "run", Endpoint: "http://x"}, true},
		{"empty fields ignored", MCPServer{Transport: "stdio"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mcpBodyDiffers(existing, &tc.m); got != tc.want {
				t.Errorf("mcpBodyDiffers = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBuildMCPBody(t *testing.T) {
	enabled := false
	m := &MCPServer{
		Name:       "gh",
		Transport:  "stdio",
		Command:    "gh-mcp",
		Args:       []string{"--verbose"},
		Endpoint:   "http://e",
		Icon:       "github",
		Enabled:    &enabled,
		EnvMapping: map[string]string{"GITHUB_TOKEN": "GH_TOKEN"},
	}
	body := buildMCPBody(m)
	if body["name"] != "gh" || body["display_name"] != "gh" {
		t.Errorf("name/display_name: %v", body)
	}
	if body["enabled"] != false {
		t.Errorf("enabled override lost: %v", body["enabled"])
	}
	if body["command"] != "gh-mcp" || body["endpoint"] != "http://e" || body["icon"] != "github" {
		t.Errorf("fields lost: %v", body)
	}
	if _, ok := body["args"].([]string); !ok {
		t.Errorf("args missing: %v", body)
	}
	if _, ok := body["env_mapping"].(map[string]string); !ok {
		t.Errorf("env_mapping missing: %v", body)
	}

	// Defaults: display_name falls back to name, enabled defaults true.
	minimal := buildMCPBody(&MCPServer{Name: "x", DisplayName: "X", Transport: "sse"})
	if minimal["display_name"] != "X" || minimal["enabled"] != true {
		t.Errorf("minimal body: %v", minimal)
	}
	if _, ok := minimal["command"]; ok {
		t.Errorf("empty command should be omitted: %v", minimal)
	}
}

func TestAgentBodyDiffers_Matrix(t *testing.T) {
	strp := func(s string) *string { return &s }
	existing := &AgentResponse{
		Name: "Amy", AgentRole: "LEAD", CLIAdapter: "CLAUDE_CODE",
		LLMProvider: strp("ANTHROPIC"), LLMModel: strp("m1"),
		ToolProfile: "CODING", TimeoutSeconds: 1800, MemoryEnabled: true,
		SystemPrompt: strp("hi"), RoleTitle: strp("Lead"),
	}
	base := Agent{
		Name: "Amy", AgentRole: "LEAD", CLIAdapter: "CLAUDE_CODE",
		LLM:         AgentLLM{Provider: "ANTHROPIC", Model: "m1"},
		ToolProfile: "CODING", TimeoutSeconds: 1800, MemoryEnabled: true,
		Prompt: "hi", RoleTitle: "Lead",
	}
	if agentBodyDiffers(existing, &base) {
		t.Fatal("identical agent should not differ")
	}
	mutate := []struct {
		name string
		fn   func(a *Agent)
	}{
		{"name", func(a *Agent) { a.Name = "Bob" }},
		{"role", func(a *Agent) { a.AgentRole = "AGENT" }},
		{"adapter", func(a *Agent) { a.CLIAdapter = "OPENCODE" }},
		{"provider", func(a *Agent) { a.LLM.Provider = "OTHER" }},
		{"model", func(a *Agent) { a.LLM.Model = "m2" }},
		{"profile", func(a *Agent) { a.ToolProfile = "FULL" }},
		{"timeout", func(a *Agent) { a.TimeoutSeconds = 60 }},
		{"memory", func(a *Agent) { a.MemoryEnabled = false }},
		{"prompt", func(a *Agent) { a.Prompt = "bye" }},
		{"role title", func(a *Agent) { a.RoleTitle = "VP" }},
	}
	for _, m := range mutate {
		t.Run(m.name, func(t *testing.T) {
			a := base
			m.fn(&a)
			if !agentBodyDiffers(existing, &a) {
				t.Errorf("%s drift not detected", m.name)
			}
		})
	}
}

func TestDerefHelpers(t *testing.T) {
	if derefInt(nil) != 0 {
		t.Error("derefInt(nil) != 0")
	}
	v := 7
	if derefInt(&v) != 7 {
		t.Error("derefInt(&7) != 7")
	}
	if derefFloat(nil) != 0 {
		t.Error("derefFloat(nil) != 0")
	}
	f := 1.5
	if derefFloat(&f) != 1.5 {
		t.Error("derefFloat(&1.5) != 1.5")
	}
	if stringSliceEq([]string{"a"}, []string{"a", "b"}) {
		t.Error("length mismatch should not be equal")
	}
	if stringSliceEq([]string{"a"}, []string{"b"}) {
		t.Error("element mismatch should not be equal")
	}
	if !stringSliceEq([]string{"a", "b"}, []string{"a", "b"}) {
		t.Error("equal slices should be equal")
	}
}

func TestBuildPlan_WorkspaceBundle(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Workspace
metadata: { name: W, slug: w }
spec:
  credentials:
    - { env: API_KEY, provider: NONE, type: API_KEY }
  skills:
    - slug: ws-skill
      inline: |
        ---
        name: ws-skill
        description: x
        ---
        body
  crews:
    - slug: alpha
      name: Alpha
      agents:
        - { slug: a, name: A, agent_role: LEAD, prompt: x, skills: [ws-skill], env_refs: [API_KEY] }
`)
	b, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	fake := newFakeAPI(t)
	plan, err := BuildPlan(context.Background(), NewClient(fake), b, Options{Mode: ApplyUpsert})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	kindsSeen := map[string]int{}
	for _, it := range plan.Items {
		kindsSeen[it.Kind]++
	}
	for _, want := range []string{"credential", "skill", "crew", "agent"} {
		if kindsSeen[want] == 0 {
			t.Errorf("workspace plan missing %s item; got %v", want, kindsSeen)
		}
	}
	if len(plan.PendingCredentials) != 1 || plan.PendingCredentials[0] != "API_KEY" {
		t.Errorf("PendingCredentials = %v", plan.PendingCredentials)
	}
	// Creates sort before everything; credential before crew before agent.
	var order []string
	for _, it := range plan.Items {
		if it.Action == ActionCreate {
			order = append(order, it.Kind)
		}
	}
	idx := func(kind string) int {
		for i, k := range order {
			if k == kind {
				return i
			}
		}
		return -1
	}
	if !(idx("credential") < idx("crew") && idx("crew") < idx("agent")) {
		t.Errorf("create ordering wrong: %v", order)
	}
}

func TestPlanCredential_ExistingStates(t *testing.T) {
	ctx := context.Background()
	run := func(t *testing.T, credsJSON string) (*Plan, *planBuilder) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/credentials", 200, credsJSON)
		p := &Plan{}
		pb := &planBuilder{client: NewClient(stub), opts: Options{}, plan: p}
		if err := pb.planCredential(ctx, &Credential{EnvVar: "KEY", Provider: "P", Type: "API_KEY"}, ""); err != nil {
			t.Fatalf("planCredential: %v", err)
		}
		return p, pb
	}

	t.Run("existing ACTIVE is unchanged", func(t *testing.T) {
		p, _ := run(t, `[{"id":"c1","name":"KEY","provider":"P","status":"ACTIVE"}]`)
		if len(p.Items) != 1 || p.Items[0].Action != ActionUnchanged {
			t.Fatalf("items = %+v", p.Items)
		}
		if !strings.Contains(p.Items[0].Description, "ACTIVE") {
			t.Errorf("description should note status: %q", p.Items[0].Description)
		}
		if len(p.PendingCredentials) != 0 {
			t.Errorf("ACTIVE existing should not be pending: %v", p.PendingCredentials)
		}
	})
	t.Run("existing empty status reads ACTIVE", func(t *testing.T) {
		p, _ := run(t, `[{"id":"c1","name":"KEY","provider":"P"}]`)
		if !strings.Contains(p.Items[0].Description, "ACTIVE") {
			t.Errorf("empty status should render ACTIVE: %q", p.Items[0].Description)
		}
	})
	t.Run("existing PENDING lands in pending list", func(t *testing.T) {
		p, _ := run(t, `[{"id":"c1","name":"KEY","provider":"P","status":"PENDING"}]`)
		if len(p.PendingCredentials) != 1 || p.PendingCredentials[0] != "KEY" {
			t.Errorf("PendingCredentials = %v", p.PendingCredentials)
		}
	})
	t.Run("lookup error propagates", func(t *testing.T) {
		stub := newCovStub()
		stub.onErr("GET", "/api/v1/credentials", errors.New("down"))
		pb := &planBuilder{client: NewClient(stub), opts: Options{}, plan: &Plan{}}
		err := pb.planCredential(ctx, &Credential{EnvVar: "KEY"}, "")
		if err == nil || !strings.Contains(err.Error(), `look up credential "KEY"`) {
			t.Fatalf("want lookup error, got %v", err)
		}
	})
}

func TestPlanCredential_CrewScopedExec(t *testing.T) {
	ctx := context.Background()
	stub := newCovStub()
	stub.on("GET", "/api/v1/credentials", 200, `[]`)
	stub.on("POST", "/api/v1/credentials", 201, `{"id":"c1","name":"KEY"}`)
	client := NewClient(stub)
	p := &Plan{}
	pb := &planBuilder{client: client, opts: Options{}, plan: p}
	cred := &Credential{EnvVar: "KEY", Provider: "P", Type: "API_KEY", Description: "desc", Label: "acct"}
	if err := pb.planCredential(ctx, cred, "crew_9"); err != nil {
		t.Fatalf("planCredential: %v", err)
	}
	if len(p.Items) != 1 || p.Items[0].Action != ActionCreate {
		t.Fatalf("items = %+v", p.Items)
	}
	// Secrets nil at plan time → pending list.
	if len(p.PendingCredentials) != 1 {
		t.Errorf("want pending credential, got %v", p.PendingCredentials)
	}
	if err := p.Items[0].exec(ctx, client, Options{Secrets: NoSecretsSource{}}); err != nil {
		t.Fatalf("exec: %v", err)
	}
	var posted map[string]any
	for _, c := range stub.calls {
		if c.Method == "POST" && c.Path == "/api/v1/credentials" {
			posted = c.Body
		}
	}
	if posted == nil {
		t.Fatal("no credential POST issued")
	}
	if posted["scope"] != "CREW" || posted["crew_id"] != "crew_9" {
		t.Errorf("crew scoping lost: %v", posted)
	}
	if posted["description"] != "desc" || posted["account_label"] != "acct" {
		t.Errorf("description/label lost: %v", posted)
	}
	if posted["pending"] != true {
		t.Errorf("no-secrets source should create a pending slot: %v", posted)
	}
}

func TestPlanSkill_ExecSourceVariants(t *testing.T) {
	ctx := context.Background()
	newPB := func() (*planBuilder, *covStubAPI) {
		stub := newCovStub()
		stub.on("POST", "/api/v1/workspaces/ws_cov/skills/import", 201, `{"id":"s1","slug":"x","created":true}`)
		p := &Plan{}
		return &planBuilder{client: NewClient(stub), opts: Options{}, plan: p}, stub
	}

	t.Run("url source posts url", func(t *testing.T) {
		pb, stub := newPB()
		s := &Skill{Slug: "x", Source: "https://github.com/a/b/SKILL.md", AllowUnsafeLicense: true}
		if err := pb.planSkill(ctx, s, "crew t"); err != nil {
			t.Fatalf("planSkill: %v", err)
		}
		if err := pb.plan.Items[0].exec(ctx, pb.client, Options{}); err != nil {
			t.Fatalf("exec: %v", err)
		}
		body := stub.calls[len(stub.calls)-1].Body
		if body["url"] != "https://github.com/a/b/SKILL.md" {
			t.Errorf("url not posted: %v", body)
		}
		if body["allow_unsafe_license"] != true {
			t.Errorf("allow_unsafe_license lost: %v", body)
		}
	})
	t.Run("unresolved path errors at exec", func(t *testing.T) {
		pb, _ := newPB()
		s := &Skill{Slug: "x", Path: "./skills/x/SKILL.md"} // never resolved
		if err := pb.planSkill(ctx, s, "crew t"); err != nil {
			t.Fatalf("planSkill: %v", err)
		}
		err := pb.plan.Items[0].exec(ctx, pb.client, Options{})
		if err == nil || !strings.Contains(err.Error(), "body not resolved") {
			t.Fatalf("want body-not-resolved error, got %v", err)
		}
	})
	t.Run("no source errors at exec", func(t *testing.T) {
		pb, _ := newPB()
		s := &Skill{Slug: "x"}
		if err := pb.planSkill(ctx, s, "crew t"); err != nil {
			t.Fatalf("planSkill: %v", err)
		}
		err := pb.plan.Items[0].exec(ctx, pb.client, Options{})
		if err == nil || !strings.Contains(err.Error(), "no resolvable source") {
			t.Fatalf("want no-source error, got %v", err)
		}
	})
}

func TestBuildPlan_ReplaceModePlansDeleteThenCreate(t *testing.T) {
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
	fake.crewsBySlug["t"] = map[string]any{"id": "crew_old", "slug": "t", "workspace_id": fake.wsID, "name": "T"}
	plan, err := BuildPlan(context.Background(), NewClient(fake), b, Options{Mode: ApplyReplace})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	var sawDelete, sawCreate bool
	for _, it := range plan.Items {
		if it.Kind != "crew" {
			continue
		}
		switch it.Action {
		case ActionDelete:
			sawDelete = true
			if !strings.Contains(it.Description, "(replace)") {
				t.Errorf("delete item should be tagged (replace): %q", it.Description)
			}
		case ActionCreate:
			sawCreate = true
		}
	}
	if !sawDelete || !sawCreate {
		t.Errorf("replace mode wants delete+create crew items: %+v", plan.Items)
	}
	if !plan.HasDestructive() {
		t.Error("replace plan must be destructive")
	}
}

func TestBuildPlan_MCPDriftAndSyncDelete(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: { name: T, slug: t }
spec:
  mcp_servers:
    - { name: github, transport: stdio, command: new-cmd }
  agents:
    - { slug: a, name: A, agent_role: LEAD, prompt: x }
`)
	b, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	stub := newCovStub()
	stub.on("GET", "/api/v1/crews", 200, `[{"id":"c1","slug":"t","name":"T"}]`)
	stub.on("GET", "/api/v1/credentials", 200, `[]`)
	stub.on("GET", "/api/v1/agents?crew_id=c1", 200, `[]`)
	stub.on("GET", "/api/v1/crews/c1/integrations", 200, `[
		{"id":"m1","crew_id":"c1","name":"github","transport":"stdio","command":"old-cmd","enabled":true},
		{"id":"m2","crew_id":"c1","name":"legacy","transport":"stdio","command":"gone","enabled":true}
	]`)
	plan, err := BuildPlan(context.Background(), NewClient(stub), b, Options{Mode: ApplyUpsert})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	var driftItem, deleteItem *PlanItem
	for i := range plan.Items {
		it := &plan.Items[i]
		if it.Kind != "mcp" {
			continue
		}
		if it.Action == ActionUnchanged && strings.Contains(it.Description, "drift detected") {
			driftItem = it
		}
		if it.Action == ActionDelete && strings.Contains(it.Description, "legacy") {
			deleteItem = it
		}
	}
	if driftItem == nil {
		t.Errorf("expected drift-detected unchanged item for github; items=%+v", plan.Items)
	}
	if deleteItem == nil {
		t.Fatalf("expected delete item for undeclared mcp legacy; items=%+v", plan.Items)
	}
	// Execute the delete closure and assert the right row is targeted.
	stub.on("DELETE", "/api/v1/crews/c1/integrations/m2", 204, ``)
	if err := deleteItem.exec(context.Background(), NewClient(stub), Options{}); err != nil {
		t.Fatalf("delete exec: %v", err)
	}
	if stub.countCalls("DELETE", "/api/v1/crews/c1/integrations/m2") != 1 {
		t.Error("expected DELETE for integration m2")
	}
}

func TestBuildPlan_NewCrewChildExecResolvesCrewAtApplyTime(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: { name: T, slug: t }
spec:
  mcp_servers:
    - { name: github, transport: stdio, command: cmd }
  agents:
    - { slug: a, name: A, agent_role: LEAD, prompt: x }
`)
	b, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	stub := newCovStub()
	stub.on("GET", "/api/v1/crews", 200, `[]`)
	stub.on("GET", "/api/v1/credentials", 200, `[]`)
	plan, err := BuildPlan(context.Background(), NewClient(stub), b, Options{Mode: ApplyUpsert})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	// The crew was never created (crews list stays empty), so the
	// deferred crew-resolution in both child closures must fail with
	// the apply-time error rather than panic or silently no-op.
	for _, kind := range []string{"mcp", "agent"} {
		var item *PlanItem
		for i := range plan.Items {
			if plan.Items[i].Kind == kind && plan.Items[i].Action == ActionCreate {
				item = &plan.Items[i]
			}
		}
		if item == nil {
			t.Fatalf("no create item for %s; items=%+v", kind, plan.Items)
		}
		// Fresh client so the stale crew cache from planning doesn't mask the lookup.
		err := item.exec(context.Background(), NewClient(stub), Options{})
		if err == nil || !strings.Contains(err.Error(), `crew "t" not found at apply time`) {
			t.Errorf("%s exec: want apply-time crew error, got %v", kind, err)
		}
	}
}

func TestPlanAgentLinks_ExecErrorsAndCredDelete(t *testing.T) {
	ctx := context.Background()
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: { name: T, slug: t }
spec:
  credentials:
    - { env: E1, provider: NONE, type: API_KEY }
  skills:
    - slug: sk1
      inline: |
        ---
        name: sk1
        description: x
        ---
        b
  agents:
    - { slug: a, name: A, agent_role: LEAD, cli_adapter: CLAUDE_CODE, tool_profile: CODING, timeout_seconds: 1800, prompt: x, skills: [sk1], env_refs: [E1] }
`)
	b, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	stub := newCovStub()
	stub.on("GET", "/api/v1/crews", 200, `[{"id":"c1","slug":"t","name":"T"}]`)
	stub.on("GET", "/api/v1/credentials", 200, `[]`)
	stub.on("GET", "/api/v1/crews/c1/integrations", 200, `[]`)
	stub.on("GET", "/api/v1/agents?crew_id=c1", 200,
		`[{"id":"a1","slug":"a","name":"A","agent_role":"LEAD","cli_adapter":"CLAUDE_CODE","tool_profile":"CODING","timeout_seconds":1800,"system_prompt":"x"}]`)
	stub.on("GET", "/api/v1/agents/a1/skills", 200, `[]`)
	// One stale credential binding the manifest no longer declares.
	stub.on("GET", "/api/v1/agents/a1/credentials", 200,
		`[{"id":"bind1","agent_id":"a1","credential_id":"crX","credential_name":"OLD_ENV","env_var_name":"OLD_ENV"}]`)
	stub.on("GET", "/api/v1/workspaces/ws_cov/skills", 200, `[]`)

	plan, err := BuildPlan(ctx, NewClient(stub), b, Options{Mode: ApplyUpsert})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	var skillLink, credLink, credUnlink *PlanItem
	for i := range plan.Items {
		it := &plan.Items[i]
		switch {
		case it.Kind == "agent_skill" && it.Action == ActionCreate:
			skillLink = it
		case it.Kind == "agent_credential" && it.Action == ActionCreate:
			credLink = it
		case it.Kind == "agent_credential" && it.Action == ActionDelete:
			credUnlink = it
		}
	}
	if skillLink == nil || credLink == nil {
		t.Fatalf("missing link create items; items=%+v", plan.Items)
	}
	if credUnlink == nil || !strings.Contains(credUnlink.Description, "OLD_ENV") {
		t.Fatalf("missing credential unlink delete item; items=%+v", plan.Items)
	}

	// Exec the skill link against a workspace where the skill was
	// never imported → apply-time error.
	if err := skillLink.exec(ctx, NewClient(stub), Options{}); err == nil ||
		!strings.Contains(err.Error(), `skill "sk1" not found at apply time`) {
		t.Errorf("skill link exec: want not-found error, got %v", err)
	}
	// Same for the credential link.
	if err := credLink.exec(ctx, NewClient(stub), Options{}); err == nil ||
		!strings.Contains(err.Error(), `credential "E1" not found at apply time`) {
		t.Errorf("cred link exec: want not-found error, got %v", err)
	}
	// The unlink closure deletes the binding row by assignment ID.
	stub.on("DELETE", "/api/v1/agents/a1/credentials/bind1", 204, ``)
	if err := credUnlink.exec(ctx, NewClient(stub), Options{}); err != nil {
		t.Fatalf("unlink exec: %v", err)
	}
	if stub.countCalls("DELETE", "/api/v1/agents/a1/credentials/bind1") != 1 {
		t.Error("expected DELETE of agent credential binding bind1")
	}
}

func TestBuildPlan_AutoCredentialCollisions(t *testing.T) {
	manifest := `
apiVersion: crewship/v1
kind: Crew
metadata: { name: T, slug: t }
spec:
  services:
    - name: vault
      image: example/custom:1
      auto_credentials:
        - { name: VAULT_PASS }
  agents:
    - { slug: a, name: A, agent_role: LEAD, prompt: x }
`
	run := func(t *testing.T, credsJSON string) (*Plan, error) {
		b, err := Load([]byte(manifest))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		stub := newCovStub()
		stub.on("GET", "/api/v1/crews", 200, `[]`)
		stub.on("GET", "/api/v1/credentials", 200, credsJSON)
		return BuildPlan(context.Background(), NewClient(stub), b, Options{Mode: ApplyUpsert})
	}

	t.Run("operator credential clashes", func(t *testing.T) {
		_, err := run(t, `[{"id":"c1","name":"VAULT_PASS","provider":"MANUAL","status":"ACTIVE"}]`)
		if err == nil || !strings.Contains(err.Error(), "clashes with an existing workspace credential") {
			t.Fatalf("want clash error, got %v", err)
		}
		if err == nil || !strings.Contains(err.Error(), "operator-managed") {
			t.Errorf("clash error should name the owner: %v", err)
		}
	})
	t.Run("other crew auto-managed clashes", func(t *testing.T) {
		_, err := run(t, `[{"id":"c1","name":"VAULT_PASS","provider":"AUTO_MANAGED","provisioned_for_service":"other/vault","status":"ACTIVE"}]`)
		if err == nil || !strings.Contains(err.Error(), "auto-managed for other/vault") {
			t.Fatalf("want cross-crew clash error, got %v", err)
		}
	})
	t.Run("same tag is idempotent", func(t *testing.T) {
		plan, err := run(t, `[{"id":"c1","name":"VAULT_PASS","provider":"AUTO_MANAGED","provisioned_for_service":"t/vault","status":"ACTIVE"}]`)
		if err != nil {
			t.Fatalf("same-tag re-apply must not error: %v", err)
		}
		var found bool
		for _, it := range plan.Items {
			if it.Kind == "credential" && strings.Contains(it.Description, "auto-managed for t/vault") {
				found = true
				if it.Action != ActionUnchanged {
					t.Errorf("re-apply should predict unchanged, got %v", it.Action)
				}
			}
		}
		if !found {
			t.Errorf("no auto-managed credential item; items=%+v", plan.Items)
		}
	})
	t.Run("plan-time lookup error propagates", func(t *testing.T) {
		b, err := Load([]byte(manifest))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		stub := newCovStub()
		stub.on("GET", "/api/v1/crews", 200, `[]`)
		stub.onErr("GET", "/api/v1/credentials", errors.New("down"))
		_, err = BuildPlan(context.Background(), NewClient(stub), b, Options{Mode: ApplyUpsert})
		if err == nil {
			t.Fatal("want lookup error")
		}
	})
}

func TestPlanAutoManagedCredentials_ExecPaths(t *testing.T) {
	ctx := context.Background()
	planned := []plannedAutoCredential{{
		Name: "PG_PASS", Value: "secret-hex", Description: "auto", ProvisionedForService: "db",
	}}

	build := func(t *testing.T, credsJSON string) (*PlanItem, *covStubAPI, *Client) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/credentials", 200, credsJSON)
		stub.on("POST", "/api/v1/credentials", 201, `{"id":"cr1","name":"PG_PASS"}`)
		client := NewClient(stub)
		p := &Plan{}
		pb := &planBuilder{client: client, opts: Options{}, plan: p}
		if err := pb.planAutoManagedCredentials(ctx, "t", planned); err != nil {
			t.Fatalf("planAutoManagedCredentials: %v", err)
		}
		if len(p.Items) != 1 {
			t.Fatalf("want 1 item, got %+v", p.Items)
		}
		return &p.Items[0], stub, client
	}

	t.Run("creates fresh credential with provenance", func(t *testing.T) {
		item, stub, client := build(t, `[]`)
		if item.Action != ActionCreate {
			t.Fatalf("predicted action = %v", item.Action)
		}
		if err := item.exec(ctx, client, Options{}); err != nil {
			t.Fatalf("exec: %v", err)
		}
		var posted map[string]any
		for _, c := range stub.calls {
			if c.Method == "POST" && c.Path == "/api/v1/credentials" {
				posted = c.Body
			}
		}
		if posted == nil {
			t.Fatal("no POST issued")
		}
		if posted["provider"] != "AUTO_MANAGED" || posted["provisioned_for_service"] != "t/db" {
			t.Errorf("provenance fields wrong: %v", posted)
		}
		if posted["value"] != "secret-hex" || posted["created_by_actor_type"] != "system" {
			t.Errorf("value/attribution wrong: %v", posted)
		}
		if posted["description"] != "auto" {
			t.Errorf("description lost: %v", posted)
		}
	})
	t.Run("same tag no-ops", func(t *testing.T) {
		item, stub, client := build(t, `[{"id":"c1","name":"PG_PASS","provider":"AUTO_MANAGED","provisioned_for_service":"t/db"}]`)
		if item.Action != ActionUnchanged {
			t.Errorf("predicted action = %v, want unchanged", item.Action)
		}
		if err := item.exec(ctx, client, Options{}); err != nil {
			t.Fatalf("exec should no-op: %v", err)
		}
		if stub.countCalls("POST", "/api/v1/credentials") != 0 {
			t.Error("no POST expected on idempotent re-apply")
		}
	})
	t.Run("auto-managed other tag conflicts", func(t *testing.T) {
		item, _, client := build(t, `[{"id":"c1","name":"PG_PASS","provider":"AUTO_MANAGED","provisioned_for_service":"other/db"}]`)
		err := item.exec(ctx, client, Options{})
		if err == nil || !strings.Contains(err.Error(), "collides with another AUTO_MANAGED credential bound to other/db") {
			t.Fatalf("want collision error, got %v", err)
		}
	})
	t.Run("auto-managed missing tag conflicts", func(t *testing.T) {
		item, _, client := build(t, `[{"id":"c1","name":"PG_PASS","provider":"AUTO_MANAGED"}]`)
		err := item.exec(ctx, client, Options{})
		if err == nil || !strings.Contains(err.Error(), "(no provisioned_for_service)") {
			t.Fatalf("want missing-tag collision error, got %v", err)
		}
	})
	t.Run("operator credential conflicts", func(t *testing.T) {
		item, _, client := build(t, `[{"id":"c1","name":"PG_PASS","provider":"MANUAL"}]`)
		err := item.exec(ctx, client, Options{})
		if err == nil || !strings.Contains(err.Error(), "already exists with provider=MANUAL") {
			t.Fatalf("want provider conflict error, got %v", err)
		}
	})
	t.Run("exec lookup error", func(t *testing.T) {
		item, stub, _ := build(t, `[]`)
		// Re-point the credentials list at a failure and use a fresh
		// client so the exec-time lookup hits the wire again.
		stub.onErr("GET", "/api/v1/credentials", errors.New("down"))
		err := item.exec(ctx, NewClient(stub), Options{})
		if err == nil || !strings.Contains(err.Error(), "lookup existing") {
			t.Fatalf("want lookup error, got %v", err)
		}
	})
}
