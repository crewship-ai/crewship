package manifest

import (
	"strings"
	"testing"
)

// validBundleAgent returns the minimal valid agent used as filler so
// the "at least one agent" rule doesn't drown out the branch under test.
func validBundleAgent() Agent {
	return Agent{Slug: "ok-agent", Name: "OK", AgentRole: "LEAD", Prompt: "x"}
}

func validateCrewSpecBundle(spec *CrewSpec) error {
	b := &Bundle{Documents: []Document{{
		APIVersion: APIVersion,
		Kind:       KindCrew,
		Metadata:   Metadata{Name: "T", Slug: "t"},
		Spec:       spec,
	}}}
	return b.Validate()
}

func TestValidationError_Error(t *testing.T) {
	single := &ValidationError{Messages: []string{"only one"}}
	if single.Error() != "only one" {
		t.Errorf("single message should render bare, got %q", single.Error())
	}
	multi := &ValidationError{Messages: []string{"first", "second"}}
	out := multi.Error()
	if !strings.Contains(out, "2 validation errors") || !strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Errorf("multi message render wrong: %q", out)
	}
}

func TestValidate_CrewDocShapeErrors(t *testing.T) {
	t.Run("missing slug", func(t *testing.T) {
		b := &Bundle{Documents: []Document{{Metadata: Metadata{Name: "T"}, Spec: &CrewSpec{Agents: []Agent{validBundleAgent()}}}}}
		err := b.Validate()
		if err == nil || !strings.Contains(err.Error(), "slug is required") {
			t.Fatalf("want slug-required error, got %v", err)
		}
	})
	t.Run("nil spec", func(t *testing.T) {
		b := &Bundle{Documents: []Document{{Metadata: Metadata{Name: "T", Slug: "t"}}}}
		err := b.Validate()
		if err == nil || !strings.Contains(err.Error(), "spec is required") {
			t.Fatalf("want spec-required error, got %v", err)
		}
	})
	t.Run("no agents", func(t *testing.T) {
		err := validateCrewSpecBundle(&CrewSpec{})
		if err == nil || !strings.Contains(err.Error(), "at least one agent is required") {
			t.Fatalf("want agent-required error, got %v", err)
		}
	})
}

func TestValidate_AgentFieldErrors(t *testing.T) {
	cases := []struct {
		name    string
		agents  []Agent
		wantErr string
	}{
		{"missing name", []Agent{{Slug: "a", AgentRole: "LEAD", Prompt: "x"}}, "name is required"},
		{"duplicate slug", []Agent{
			{Slug: "a", Name: "A", AgentRole: "LEAD", Prompt: "x"},
			{Slug: "a", Name: "A2", Prompt: "x"},
		}, "duplicate slug within crew"},
		{"bad cli adapter", []Agent{{Slug: "a", Name: "A", AgentRole: "LEAD", CLIAdapter: "VIM", Prompt: "x"}}, `cli_adapter "VIM" invalid`},
		{"bad tool profile", []Agent{{Slug: "a", Name: "A", AgentRole: "LEAD", ToolProfile: "EVERYTHING", Prompt: "x"}}, `tool_profile "EVERYTHING" invalid`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCrewSpecBundle(&CrewSpec{Agents: tc.agents})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidate_CredentialErrors(t *testing.T) {
	cases := []struct {
		name    string
		creds   []Credential
		wantErr string
	}{
		{"missing env", []Credential{{Provider: "P", Type: "T"}}, "missing env"},
		{"duplicate env", []Credential{
			{EnvVar: "K", Provider: "P", Type: "T"},
			{EnvVar: "K", Provider: "P", Type: "T"},
		}, `duplicate credential env "K"`},
		{"missing provider", []Credential{{EnvVar: "K", Type: "T"}}, "provider is required"},
		{"missing type", []Credential{{EnvVar: "K", Provider: "P"}}, "type is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCrewSpecBundle(&CrewSpec{
				Credentials: tc.creds,
				Agents:      []Agent{validBundleAgent()},
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidate_SkillErrors(t *testing.T) {
	cases := []struct {
		name    string
		skills  []Skill
		wantErr string
	}{
		{"missing slug", []Skill{{Inline: "x"}}, "missing slug"},
		{"duplicate slug", []Skill{
			{Slug: "s", Inline: "x"},
			{Slug: "s", Inline: "y"},
		}, `duplicate skill slug "s"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCrewSpecBundle(&CrewSpec{
				Skills: tc.skills,
				Agents: []Agent{validBundleAgent()},
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidate_MCPServerErrors(t *testing.T) {
	cases := []struct {
		name    string
		servers []MCPServer
		wantErr string
	}{
		{"missing name", []MCPServer{{Transport: "stdio", Command: "x"}}, "missing name"},
		{"duplicate name", []MCPServer{
			{Name: "m", Transport: "stdio", Command: "x"},
			{Name: "m", Transport: "stdio", Command: "x"},
		}, `duplicate mcp server name "m"`},
		{"stdio without command", []MCPServer{{Name: "m", Transport: "stdio"}}, "stdio transport requires command"},
		{"http without endpoint", []MCPServer{{Name: "m", Transport: "http"}}, "http transport requires endpoint"},
		{"sse without endpoint", []MCPServer{{Name: "m", Transport: "sse"}}, "sse transport requires endpoint"},
		{"streamable-http without endpoint", []MCPServer{{Name: "m", Transport: "streamable-http"}}, "streamable-http transport requires endpoint"},
		{"missing transport", []MCPServer{{Name: "m"}}, "transport is required"},
		{"unknown transport", []MCPServer{{Name: "m", Transport: "carrier-pigeon"}}, `unknown transport "carrier-pigeon"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCrewSpecBundle(&CrewSpec{
				MCPServers: tc.servers,
				Agents:     []Agent{validBundleAgent()},
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidate_ServiceErrors(t *testing.T) {
	cases := []struct {
		name     string
		services []Service
		wantErr  string
	}{
		{"missing name", []Service{{Image: "redis:7"}}, "missing name"},
		{"bad dns name", []Service{{Name: "Bad_Name", Image: "redis:7"}}, "name must be a DNS label"},
		{"duplicate name", []Service{
			{Name: "redis", Image: "redis:7"},
			{Name: "redis", Image: "redis:7"},
		}, `duplicate service name "redis"`},
		{"missing image", []Service{{Name: "redis"}}, "image is required"},
		{"volume missing fields", []Service{{Name: "db", Image: "pg:16",
			Volumes: []ServiceVolume{{Name: "", Mount: ""}}}}, "needs both name and mount"},
		{"duplicate mount", []Service{{Name: "db", Image: "pg:16",
			Volumes: []ServiceVolume{
				{Name: "v1", Mount: "/data"},
				{Name: "v2", Mount: "/data"},
			}}}, `duplicate mount "/data"`},
		{"bind mount rejected", []Service{{Name: "db", Image: "pg:16",
			Volumes: []ServiceVolume{{Name: "./host-dir", Mount: "/data"}}}}, "looks like a bind mount"},
		{"healthcheck without test", []Service{{Name: "db", Image: "pg:16",
			Healthcheck: &ServiceHealthcheck{}}}, "healthcheck declared without a test command"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCrewSpecBundle(&CrewSpec{
				Services: tc.services,
				Agents:   []Agent{validBundleAgent()},
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidate_AutoCredentialErrors(t *testing.T) {
	svc := func(acs ...AutoCredential) []Service {
		return []Service{{Name: "vault", Image: "example/custom:1", AutoCredentials: acs}}
	}
	cases := []struct {
		name     string
		services []Service
		creds    []Credential
		wantErr  string
	}{
		{"missing name", svc(AutoCredential{}), nil, "missing name"},
		{"invalid env name", svc(AutoCredential{Name: "1BAD-NAME"}), nil, "is not a valid env-var name"},
		{"duplicate name", svc(AutoCredential{Name: "P"}, AutoCredential{Name: "P"}), nil, `duplicate auto_credential name "P"`},
		{"collides with credentials block", svc(AutoCredential{Name: "SHARED"}),
			[]Credential{{EnvVar: "SHARED", Provider: "P", Type: "T"}},
			"collides with the crew's credentials[] declaration"},
		{"invalid inject_as_env", svc(AutoCredential{Name: "GOOD", InjectAsEnv: "also-bad"}), nil, "inject_as_env"},
		{"length below floor", svc(AutoCredential{Name: "GOOD", Length: 8}), nil, "below the 16-byte minimum"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCrewSpecBundle(&CrewSpec{
				Services:    tc.services,
				Credentials: tc.creds,
				Agents:      []Agent{validBundleAgent()},
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidate_WorkspaceCrewErrors(t *testing.T) {
	validNested := CrewSpec{
		SlugOverride: "good",
		Name:         "Good",
		Agents:       []Agent{validBundleAgent()},
	}
	t.Run("crew without slug", func(t *testing.T) {
		b := &Bundle{Workspaces: []WorkspaceDocument{{
			Metadata: Metadata{Name: "W", Slug: "w"},
			Spec:     WorkspaceSpec{Crews: []CrewSpec{{Name: "NoSlug"}}},
		}}}
		err := b.Validate()
		if err == nil || !strings.Contains(err.Error(), "crews[0] needs a slug") {
			t.Fatalf("want slug-needed error, got %v", err)
		}
	})
	t.Run("crew with malformed slug", func(t *testing.T) {
		b := &Bundle{Workspaces: []WorkspaceDocument{{
			Metadata: Metadata{Name: "W", Slug: "w"},
			Spec:     WorkspaceSpec{Crews: []CrewSpec{{SlugOverride: "Bad.Slug"}}},
		}}}
		err := b.Validate()
		if err == nil || !strings.Contains(err.Error(), "invalid slug") {
			t.Fatalf("want invalid-slug error, got %v", err)
		}
	})
	t.Run("duplicate crew slug", func(t *testing.T) {
		b := &Bundle{Workspaces: []WorkspaceDocument{{
			Metadata: Metadata{Name: "W", Slug: "w"},
			Spec:     WorkspaceSpec{Crews: []CrewSpec{validNested, validNested}},
		}}}
		err := b.Validate()
		if err == nil || !strings.Contains(err.Error(), `duplicate crew slug "good"`) {
			t.Fatalf("want duplicate-slug error, got %v", err)
		}
	})
}

func TestValidEnums(t *testing.T) {
	if validAgentRole("OBSERVER") || validAgentRole("") {
		t.Error("OBSERVER/empty must not be valid roles")
	}
	if !validAgentRole("AGENT") || !validAgentRole("LEAD") {
		t.Error("AGENT/LEAD must be valid roles")
	}
	for _, a := range []string{"CLAUDE_CODE", "OPENCODE", "CODEX_CLI", "GEMINI_CLI", "CURSOR_CLI", "FACTORY_DROID"} {
		if !validCLIAdapter(a) {
			t.Errorf("%s should be a valid adapter", a)
		}
	}
	if validCLIAdapter("EMACS") {
		t.Error("EMACS must not be a valid adapter")
	}
	for _, p := range []string{"FULL", "CODING", "MINIMAL"} {
		if !validToolProfile(p) {
			t.Errorf("%s should be a valid profile", p)
		}
	}
	if validToolProfile("ULTRA") {
		t.Error("ULTRA must not be a valid profile")
	}
}
