package chatbridge

import (
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// ---------- decodeServicesForRuntime ----------

func TestDecodeServicesForRuntimeEmptyBody(t *testing.T) {
	t.Parallel()
	for _, body := range []string{"", "   \n\t  "} {
		got, err := decodeServicesForRuntime(body, nil)
		if err != nil {
			t.Errorf("body %q: unexpected error: %v", body, err)
		}
		if got != nil {
			t.Errorf("body %q: expected nil services, got %v", body, got)
		}
	}
}

func TestDecodeServicesForRuntimeBadJSON(t *testing.T) {
	t.Parallel()
	_, err := decodeServicesForRuntime("{not json", nil)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "services_json") {
		t.Errorf("error should mention services_json, got: %v", err)
	}
}

func TestDecodeServicesForRuntimeSchemaGuards(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "missing name",
			body:    `[{"image":"postgres:16"}]`,
			wantErr: "services[0]: name and image are required",
		},
		{
			name:    "missing image",
			body:    `[{"name":"db"}]`,
			wantErr: "services[0]: name and image are required",
		},
		{
			name:    "whitespace-only name",
			body:    `[{"name":"   ","image":"postgres:16"}]`,
			wantErr: "name and image are required",
		},
		{
			name:    "healthcheck without test",
			body:    `[{"name":"db","image":"postgres:16","healthcheck":{"retries":3}}]`,
			wantErr: `services["db"]: healthcheck declared without a test command`,
		},
		{
			name:    "second entry invalid carries index",
			body:    `[{"name":"db","image":"postgres:16"},{"image":"redis:7"}]`,
			wantErr: "services[1]: name and image are required",
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := decodeServicesForRuntime(tc.body, nil)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want contains %q", err, tc.wantErr)
			}
		})
	}
}

func TestDecodeServicesForRuntimeFullService(t *testing.T) {
	t.Parallel()
	body := `[{
		"name": "db",
		"image": "postgres:16",
		"command": ["postgres", "-c", "max_connections=50"],
		"env": {"POSTGRES_DB": "app"},
		"env_refs": ["POSTGRES_PASSWORD", "MISSING_REF"],
		"ports": ["5432"],
		"volumes": [{"name": "pgdata", "mount": "/var/lib/postgresql/data"}],
		"healthcheck": {
			"test": ["CMD-SHELL", "pg_isready"],
			"interval": "10s",
			"timeout": "2s",
			"retries": 5,
			"start_period": "30s"
		}
	}]`
	lookup := func(envVar string) string {
		if envVar == "POSTGRES_PASSWORD" {
			return "s3cret"
		}
		return "" // MISSING_REF → dropped
	}
	svcs, err := decodeServicesForRuntime(body, lookup)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(svcs) != 1 {
		t.Fatalf("services = %d, want 1", len(svcs))
	}
	s := svcs[0]
	if s.Name != "db" || s.Image != "postgres:16" {
		t.Errorf("name/image = %q/%q", s.Name, s.Image)
	}
	if len(s.Command) != 3 || s.Command[0] != "postgres" {
		t.Errorf("command = %v", s.Command)
	}
	if s.Env["POSTGRES_DB"] != "app" {
		t.Errorf("static env not preserved: %v", s.Env)
	}
	if s.Env["POSTGRES_PASSWORD"] != "s3cret" {
		t.Errorf("env_ref not resolved: %v", s.Env)
	}
	if _, ok := s.Env["MISSING_REF"]; ok {
		t.Error("unresolvable env_ref must be omitted, not set to empty string")
	}
	if len(s.Ports) != 1 || s.Ports[0] != "5432" {
		t.Errorf("ports = %v", s.Ports)
	}
	if len(s.Volumes) != 1 || s.Volumes[0].Name != "pgdata" || s.Volumes[0].Mount != "/var/lib/postgresql/data" {
		t.Errorf("volumes = %+v", s.Volumes)
	}
	if s.Healthcheck == nil {
		t.Fatal("healthcheck should be populated")
	}
	if got := s.Healthcheck.Test; len(got) != 2 || got[1] != "pg_isready" {
		t.Errorf("healthcheck test = %v", got)
	}
	if s.Healthcheck.Interval != 10*time.Second {
		t.Errorf("interval = %v, want 10s", s.Healthcheck.Interval)
	}
	if s.Healthcheck.Timeout != 2*time.Second {
		t.Errorf("timeout = %v, want 2s", s.Healthcheck.Timeout)
	}
	if s.Healthcheck.StartPeriod != 30*time.Second {
		t.Errorf("start_period = %v, want 30s", s.Healthcheck.StartPeriod)
	}
	if s.Healthcheck.Retries != 5 {
		t.Errorf("retries = %d, want 5", s.Healthcheck.Retries)
	}
}

func TestDecodeServicesForRuntimeNilLookupDropsEnvRefs(t *testing.T) {
	t.Parallel()
	body := `[{"name":"db","image":"postgres:16","env_refs":["TOKEN"]}]`
	svcs, err := decodeServicesForRuntime(body, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(svcs) != 1 {
		t.Fatalf("services = %d, want 1", len(svcs))
	}
	if _, ok := svcs[0].Env["TOKEN"]; ok {
		t.Errorf("nil lookup must drop env_refs, env = %v", svcs[0].Env)
	}
}

func TestDecodeServicesForRuntimeHealthcheckDurationDefaults(t *testing.T) {
	t.Parallel()
	// Empty + unparseable durations must land on the documented defaults
	// (5s interval, 3s timeout, 0 start period).
	body := `[{"name":"db","image":"img","healthcheck":{"test":["CMD","true"],"interval":"not-a-duration"}}]`
	svcs, err := decodeServicesForRuntime(body, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hc := svcs[0].Healthcheck
	if hc.Interval != 5*time.Second {
		t.Errorf("interval = %v, want default 5s for unparseable input", hc.Interval)
	}
	if hc.Timeout != 3*time.Second {
		t.Errorf("timeout = %v, want default 3s for empty input", hc.Timeout)
	}
	if hc.StartPeriod != 0 {
		t.Errorf("start_period = %v, want default 0", hc.StartPeriod)
	}
}

func TestDecodeServicesForRuntimeNoHealthcheck(t *testing.T) {
	t.Parallel()
	svcs, err := decodeServicesForRuntime(`[{"name":"redis","image":"redis:7"}]`, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svcs[0].Healthcheck != nil {
		t.Errorf("healthcheck should be nil when not declared, got %+v", svcs[0].Healthcheck)
	}
}

// ---------- parseDuration ----------

func TestParseDuration(t *testing.T) {
	t.Parallel()
	def := 7 * time.Second
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"", def},
		{"garbage", def},
		{"250ms", 250 * time.Millisecond},
		{"1m30s", 90 * time.Second},
		{"0s", 0},
	}
	for _, tc := range tests {
		if got := parseDuration(tc.in, def); got != tc.want {
			t.Errorf("parseDuration(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// ---------- buildServiceEnvLookup ----------

func TestBuildServiceEnvLookup(t *testing.T) {
	t.Parallel()
	lookup := buildServiceEnvLookup([]orchestrator.Credential{
		{EnvVarName: "API_KEY", PlainValue: "k1"},
		{EnvVarName: "PENDING_CRED", PlainValue: ""}, // status=PENDING → empty value
	})
	if got := lookup("API_KEY"); got != "k1" {
		t.Errorf("lookup(API_KEY) = %q, want k1", got)
	}
	if got := lookup("PENDING_CRED"); got != "" {
		t.Errorf("lookup(PENDING_CRED) = %q, want empty", got)
	}
	if got := lookup("UNKNOWN"); got != "" {
		t.Errorf("lookup(UNKNOWN) = %q, want empty", got)
	}
}

func TestBuildServiceEnvLookupEmptyCreds(t *testing.T) {
	t.Parallel()
	lookup := buildServiceEnvLookup(nil)
	if got := lookup("ANYTHING"); got != "" {
		t.Errorf("lookup on empty creds = %q, want empty", got)
	}
}
