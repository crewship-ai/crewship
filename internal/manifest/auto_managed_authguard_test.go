package manifest

// Enforcement coverage for the "recognised datastores are always
// authenticated" invariant (#1363). When operator precedence would skip
// auto-credential generation because the operator supplied their own
// command/env, that config MUST actually carry auth — otherwise the
// apply fails, unless the service explicitly opts out with
// allow_unauthenticated: true.

import (
	"reflect"
	"strings"
	"testing"
)

// Operator supplies a command that DOES set --requirepass: they own the
// secret, generation is skipped, no error, and the command is untouched.
func TestRedis_OperatorCommandWithRequirepass_OK(t *testing.T) {
	cmd := []string{"redis-server", "--requirepass", "my-own-secret"}
	spec := &CrewSpec{
		Services: []Service{{Name: "redis", Image: "redis:7-alpine", Command: cmd}},
		Agents:   []Agent{{Slug: "lead", AgentRole: "LEAD"}},
	}
	planned, err := expandAutoCredentialsInCrewSpec(spec, "")
	if err != nil {
		t.Fatalf("operator-provided auth must not error: %v", err)
	}
	if len(planned) != 0 {
		t.Fatalf("operator owns the secret; no generation expected, got %+v", planned)
	}
	if !reflect.DeepEqual(spec.Services[0].Command, cmd) {
		t.Fatalf("operator command mutated: %+v", spec.Services[0].Command)
	}
	if containsString(spec.Agents[0].EnvRefs, "REDIS_PASSWORD") {
		t.Fatalf("agent env_ref appended despite operator-owned secret: %+v", spec.Agents[0].EnvRefs)
	}
}

// The explicit acknowledged opt-out: a passwordless command plus
// allow_unauthenticated: true is accepted silently — no error, no
// generation, command untouched.
func TestRedis_AllowUnauthenticated_PasswordlessCommandOK(t *testing.T) {
	passwordless := []string{"redis-server"}
	spec := &CrewSpec{
		Services: []Service{{
			Name:                 "redis",
			Image:                "redis:7-alpine",
			Command:              passwordless,
			AllowUnauthenticated: true,
		}},
		Agents: []Agent{{Slug: "lead", AgentRole: "LEAD"}},
	}
	planned, err := expandAutoCredentialsInCrewSpec(spec, "")
	if err != nil {
		t.Fatalf("allow_unauthenticated must suppress the error: %v", err)
	}
	if len(planned) != 0 {
		t.Fatalf("opt-out means no generation; got %+v", planned)
	}
	if !reflect.DeepEqual(spec.Services[0].Command, passwordless) {
		t.Fatalf("command mutated under opt-out: %+v", spec.Services[0].Command)
	}
}

// Env-injected datastore (postgres): an operator who pins the password
// env to the empty string provides NO auth and is rejected; a non-empty
// value is accepted (operator owns it, no generation).
func TestPostgres_OperatorEnv(t *testing.T) {
	t.Run("empty is rejected", func(t *testing.T) {
		spec := &CrewSpec{
			Services: []Service{{
				Name:  "postgres",
				Image: "postgres:16-alpine",
				Env:   map[string]string{"POSTGRES_PASSWORD": ""},
			}},
			Agents: []Agent{{Slug: "lead", AgentRole: "LEAD"}},
		}
		_, err := expandAutoCredentialsInCrewSpec(spec, "")
		if err == nil {
			t.Fatalf("empty operator password env must be rejected; got nil")
		}
		for _, want := range []string{"postgres", "POSTGRES_PASSWORD", "allow_unauthenticated"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q missing %q", err.Error(), want)
			}
		}
	})

	t.Run("empty opt-out OK", func(t *testing.T) {
		spec := &CrewSpec{
			Services: []Service{{
				Name:                 "postgres",
				Image:                "postgres:16-alpine",
				Env:                  map[string]string{"POSTGRES_PASSWORD": ""},
				AllowUnauthenticated: true,
			}},
			Agents: []Agent{{Slug: "lead", AgentRole: "LEAD"}},
		}
		planned, err := expandAutoCredentialsInCrewSpec(spec, "")
		if err != nil {
			t.Fatalf("opt-out must suppress the error: %v", err)
		}
		if len(planned) != 0 {
			t.Fatalf("opt-out means no generation; got %+v", planned)
		}
	})

	t.Run("non-empty is accepted, no generation", func(t *testing.T) {
		spec := &CrewSpec{
			Services: []Service{{
				Name:  "postgres",
				Image: "postgres:16-alpine",
				Env:   map[string]string{"POSTGRES_PASSWORD": "operator-secret"},
			}},
			Agents: []Agent{{Slug: "lead", AgentRole: "LEAD"}},
		}
		planned, err := expandAutoCredentialsInCrewSpec(spec, "")
		if err != nil {
			t.Fatalf("operator-provided password must not error: %v", err)
		}
		if len(planned) != 0 {
			t.Fatalf("operator owns the secret; no generation expected, got %+v", planned)
		}
		if spec.Services[0].Env["POSTGRES_PASSWORD"] != "operator-secret" {
			t.Fatalf("operator env overwritten: %+v", spec.Services[0].Env)
		}
		if containsString(spec.Agents[0].EnvRefs, "POSTGRES_PASSWORD") {
			t.Fatalf("agent env_ref appended despite operator-owned secret: %+v", spec.Agents[0].EnvRefs)
		}
	})
}

// The happy path is unchanged: a stock catalog datastore with no
// operator command/env generates its secret exactly as before.
func TestStockDatastore_GenerationUnchanged(t *testing.T) {
	t.Run("redis (command channel)", func(t *testing.T) {
		spec := &CrewSpec{
			Services: []Service{{Name: "redis", Image: "redis:7-alpine"}},
			Agents:   []Agent{{Slug: "lead", AgentRole: "LEAD"}},
		}
		planned, err := expandAutoCredentialsInCrewSpec(spec, "")
		if err != nil {
			t.Fatalf("stock redis must generate cleanly: %v", err)
		}
		if len(planned) != 1 || planned[0].Name != "REDIS_PASSWORD" {
			t.Fatalf("want one REDIS_PASSWORD entry, got %+v", planned)
		}
		cmd := spec.Services[0].Command
		if len(cmd) != 3 || cmd[0] != "redis-server" || cmd[1] != "--requirepass" || cmd[2] != planned[0].Value {
			t.Fatalf("generated redis command wrong: %+v", cmd)
		}
	})

	t.Run("postgres (env channel)", func(t *testing.T) {
		spec := &CrewSpec{
			Services: []Service{{Name: "postgres", Image: "postgres:16-alpine"}},
			Agents:   []Agent{{Slug: "lead", AgentRole: "LEAD"}},
		}
		planned, err := expandAutoCredentialsInCrewSpec(spec, "")
		if err != nil {
			t.Fatalf("stock postgres must generate cleanly: %v", err)
		}
		if len(planned) != 1 || planned[0].Name != "POSTGRES_PASSWORD" {
			t.Fatalf("want one POSTGRES_PASSWORD entry, got %+v", planned)
		}
		if spec.Services[0].Env["POSTGRES_PASSWORD"] != planned[0].Value {
			t.Fatalf("generated password not written to sidecar env: %+v", spec.Services[0].Env)
		}
	})
}

// An UNKNOWN image with an explicit operator command lacking auth is NOT
// enforced — the always-auth invariant is scoped to recognised catalog
// datastores. Operators own non-catalog images entirely.
func TestUnknownImage_OperatorCommandNotEnforced(t *testing.T) {
	spec := &CrewSpec{
		Services: []Service{{
			Name:    "cache",
			Image:   "ghcr.io/example/custom-cache:v1", // not in the catalog
			Command: []string{"custom-cache", "--no-auth"},
			AutoCredentials: []AutoCredential{
				{Name: "CACHE_TOKEN", InjectAsCommand: []string{"custom-cache", "--token", autoCredentialValuePlaceholder}},
			},
		}},
		Agents: []Agent{{Slug: "lead", AgentRole: "LEAD"}},
	}
	planned, err := expandAutoCredentialsInCrewSpec(spec, "")
	if err != nil {
		t.Fatalf("unknown-image operator command must not be enforced: %v", err)
	}
	if len(planned) != 0 {
		t.Fatalf("operator command owns the secret on an unknown image; got %+v", planned)
	}
}

// --- helper: authFlagFromCommandTemplate ------------------------------

func TestAuthFlagFromCommandTemplate(t *testing.T) {
	cases := []struct {
		name     string
		tmpl     []string
		wantFlag string
		wantOK   bool
	}{
		{"redis requirepass", []string{"redis-server", "--requirepass", autoCredentialValuePlaceholder}, "--requirepass", true},
		{"no placeholder", []string{"redis-server", "--maxmemory", "256mb"}, "", false},
		{"placeholder first", []string{autoCredentialValuePlaceholder, "--x"}, "", false},
		{"placeholder preceded by placeholder", []string{autoCredentialValuePlaceholder, autoCredentialValuePlaceholder}, "", false},
		{"empty template", nil, "", false},
		{"flag then value mid argv", []string{"srv", "--flag", autoCredentialValuePlaceholder, "--after", "x"}, "--flag", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotFlag, gotOK := authFlagFromCommandTemplate(tc.tmpl)
			if gotFlag != tc.wantFlag || gotOK != tc.wantOK {
				t.Errorf("authFlagFromCommandTemplate = (%q,%v), want (%q,%v)", gotFlag, gotOK, tc.wantFlag, tc.wantOK)
			}
		})
	}
}

// --- helper: commandProvidesAuth --------------------------------------

func TestCommandProvidesAuth(t *testing.T) {
	cases := []struct {
		name string
		cmd  []string
		flag string
		want bool
	}{
		{"flag present with arg", []string{"redis-server", "--requirepass", "s3cret"}, "--requirepass", true},
		{"flag absent", []string{"redis-server", "--maxmemory", "256mb"}, "--requirepass", false},
		{"flag present but empty arg", []string{"redis-server", "--requirepass", ""}, "--requirepass", false},
		{"flag is trailing token, no arg", []string{"redis-server", "--requirepass"}, "--requirepass", false},
		{"multiple, first empty second set", []string{"redis-server", "--requirepass", "", "--requirepass", "real"}, "--requirepass", true},
		{"multiple, both empty", []string{"redis-server", "--requirepass", "", "--requirepass", ""}, "--requirepass", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandProvidesAuth(tc.cmd, tc.flag); got != tc.want {
				t.Errorf("commandProvidesAuth(%v, %q) = %v, want %v", tc.cmd, tc.flag, got, tc.want)
			}
		})
	}
}
