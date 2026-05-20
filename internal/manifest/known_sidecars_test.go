package manifest

import (
	"reflect"
	"sort"
	"testing"
)

func TestNormalizeImageName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"postgres", "postgres"},
		{"postgres:16-alpine", "postgres"},
		{"postgres:latest", "postgres"},
		{"library/postgres:16", "postgres"},
		{"docker.io/library/postgres:16-alpine", "postgres"},
		{"harbor.acme.io/library/postgres:16-alpine@sha256:deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "postgres"},
		// Registry with port: tag is `latest`, port stays in host part.
		{"localhost:5000/postgres:latest", "postgres"},
		// Mixed case → lowercase output.
		{"POSTGRES:16", "postgres"},
		// Digest-only, no tag.
		{"postgres@sha256:abc", "postgres"},
		// Empty / nonsense.
		{"", ""},
		{":", ""},
		// Unknown image still normalises cleanly.
		{"my-org/secret-sauce:1.2.3", "secret-sauce"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := normalizeImageName(tc.in)
			if got != tc.want {
				t.Errorf("normalizeImageName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestLookupSidecarDefaults_KnownImages(t *testing.T) {
	cases := []struct {
		image       string
		wantOK      bool
		wantCredHas string // env name we expect in AutoCredentials
	}{
		{"postgres:16-alpine", true, "POSTGRES_PASSWORD"},
		{"postgres", true, "POSTGRES_PASSWORD"},
		{"docker.io/library/postgres:17", true, "POSTGRES_PASSWORD"},
		{"mariadb:11", true, "MARIADB_ROOT_PASSWORD"},
		{"mysql:8", true, "MYSQL_ROOT_PASSWORD"},
		{"mongo:7", true, "MONGO_INITDB_ROOT_PASSWORD"},
		{"rabbitmq:3-management", true, "RABBITMQ_DEFAULT_PASS"},
		{"elasticsearch:8.13.0", true, "ELASTIC_PASSWORD"},
		// Negative: redis has no auto-cred (auth not on by default).
		{"redis:7-alpine", false, ""},
		// Negative: unknown image.
		{"nginx:latest", false, ""},
		// Negative: empty.
		{"", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.image, func(t *testing.T) {
			got, ok := lookupSidecarDefaults(tc.image)
			if ok != tc.wantOK {
				t.Fatalf("lookupSidecarDefaults(%q) ok = %v, want %v", tc.image, ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			found := false
			for _, ac := range got.AutoCredentials {
				if ac.Name == tc.wantCredHas {
					found = true
					if ac.Description == "" {
						t.Errorf("auto-cred %s missing Description (UI surfaces it on hover)", ac.Name)
					}
					break
				}
			}
			if !found {
				t.Errorf("expected AutoCredentials to contain %q for image %q; got %+v",
					tc.wantCredHas, tc.image, got.AutoCredentials)
			}
		})
	}
}

func TestService_ResolveAutoCredentials_SugarOnly(t *testing.T) {
	// Operator wrote nothing under auto_credentials; sugar must fire.
	s := &Service{Name: "postgres", Image: "postgres:16-alpine"}
	got := s.ResolveAutoCredentials()
	if len(got) != 1 || got[0].Name != "POSTGRES_PASSWORD" {
		t.Fatalf("sugar-only postgres: want [POSTGRES_PASSWORD], got %+v", got)
	}
}

func TestService_ResolveAutoCredentials_ExplicitOnly(t *testing.T) {
	// Unknown image, explicit list — list passes through as-is.
	s := &Service{
		Name:  "custom",
		Image: "ghcr.io/me/secret-sauce:v1",
		AutoCredentials: []AutoCredential{
			{Name: "API_KEY", Length: 64},
		},
	}
	got := s.ResolveAutoCredentials()
	if len(got) != 1 || got[0].Name != "API_KEY" || got[0].Length != 64 {
		t.Fatalf("explicit-only: want [API_KEY len=64], got %+v", got)
	}
}

func TestService_ResolveAutoCredentials_OperatorWinsOnConflict(t *testing.T) {
	// Operator's POSTGRES_PASSWORD entry should shadow the sugar one.
	custLen := 64
	s := &Service{
		Name:  "postgres",
		Image: "postgres:16-alpine",
		AutoCredentials: []AutoCredential{
			{Name: "POSTGRES_PASSWORD", Length: custLen, InjectAsEnv: "PG_PWD"},
		},
	}
	got := s.ResolveAutoCredentials()
	if len(got) != 1 {
		t.Fatalf("expected 1 entry (operator shadows sugar), got %d: %+v", len(got), got)
	}
	if got[0].InjectAsEnv != "PG_PWD" || got[0].Length != custLen {
		t.Errorf("operator entry should win: %+v", got[0])
	}
}

func TestService_ResolveAutoCredentials_MergesDifferentNames(t *testing.T) {
	// Operator can ADD credentials beyond the sugar set — both
	// should land in the resolved output.
	s := &Service{
		Name:  "postgres",
		Image: "postgres:16-alpine",
		AutoCredentials: []AutoCredential{
			{Name: "PG_REPLICATION_PASSWORD"},
		},
	}
	got := s.ResolveAutoCredentials()
	names := make([]string, 0, len(got))
	for _, ac := range got {
		names = append(names, ac.Name)
	}
	sort.Strings(names)
	want := []string{"PG_REPLICATION_PASSWORD", "POSTGRES_PASSWORD"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("merged: want %v, got %v", want, names)
	}
}

func TestService_ResolveAutoCredentials_NilServiceSafe(t *testing.T) {
	if got := (*Service)(nil).ResolveAutoCredentials(); got != nil {
		t.Errorf("nil receiver should return nil, got %+v", got)
	}
}

func TestService_ResolveEnv_SugarMergesWithOperator(t *testing.T) {
	// Sugar provides POSTGRES_USER=postgres; operator adds POSTGRES_DB=app.
	s := &Service{
		Name:  "postgres",
		Image: "postgres:16-alpine",
		Env:   map[string]string{"POSTGRES_DB": "app"},
	}
	got := s.ResolveEnv()
	if got["POSTGRES_USER"] != "postgres" {
		t.Errorf("sugar key lost: POSTGRES_USER = %q", got["POSTGRES_USER"])
	}
	if got["POSTGRES_DB"] != "app" {
		t.Errorf("operator key lost: POSTGRES_DB = %q", got["POSTGRES_DB"])
	}
}

func TestService_ResolveEnv_OperatorOverridesSugar(t *testing.T) {
	// Operator chose POSTGRES_USER=app (not postgres).
	s := &Service{
		Name:  "postgres",
		Image: "postgres:16-alpine",
		Env:   map[string]string{"POSTGRES_USER": "app"},
	}
	got := s.ResolveEnv()
	if got["POSTGRES_USER"] != "app" {
		t.Errorf("operator override lost: POSTGRES_USER = %q (want \"app\")", got["POSTGRES_USER"])
	}
}

func TestService_ResolveEnv_UnknownImageNoEnv_Nil(t *testing.T) {
	s := &Service{Name: "x", Image: "nginx:latest"}
	if got := s.ResolveEnv(); got != nil {
		t.Errorf("nil expected when nothing to merge; got %+v", got)
	}
}

func TestAutoCredential_EffectiveHelpers(t *testing.T) {
	var trueVal = true
	var falseVal = false

	cases := []struct {
		name              string
		ac                AutoCredential
		wantInjectAsEnv   string
		wantInjectToAgent bool
		wantLength        int
	}{
		{
			name:              "all defaults",
			ac:                AutoCredential{Name: "POSTGRES_PASSWORD"},
			wantInjectAsEnv:   "POSTGRES_PASSWORD",
			wantInjectToAgent: true,
			wantLength:        32,
		},
		{
			name:              "override inject env",
			ac:                AutoCredential{Name: "PASSWORD", InjectAsEnv: "PG_PWD"},
			wantInjectAsEnv:   "PG_PWD",
			wantInjectToAgent: true,
			wantLength:        32,
		},
		{
			name:              "explicit inject_to_agents=false",
			ac:                AutoCredential{Name: "X", InjectToAgents: &falseVal},
			wantInjectAsEnv:   "X",
			wantInjectToAgent: false,
			wantLength:        32,
		},
		{
			name:              "explicit inject_to_agents=true (same as default)",
			ac:                AutoCredential{Name: "X", InjectToAgents: &trueVal},
			wantInjectAsEnv:   "X",
			wantInjectToAgent: true,
			wantLength:        32,
		},
		{
			name:              "custom length",
			ac:                AutoCredential{Name: "X", Length: 48},
			wantInjectAsEnv:   "X",
			wantInjectToAgent: true,
			wantLength:        48,
		},
		{
			name:              "zero length defaults",
			ac:                AutoCredential{Name: "X", Length: 0},
			wantInjectAsEnv:   "X",
			wantInjectToAgent: true,
			wantLength:        32,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ac.EffectiveInjectAsEnv(); got != tc.wantInjectAsEnv {
				t.Errorf("EffectiveInjectAsEnv = %q, want %q", got, tc.wantInjectAsEnv)
			}
			if got := tc.ac.EffectiveInjectToAgents(); got != tc.wantInjectToAgent {
				t.Errorf("EffectiveInjectToAgents = %v, want %v", got, tc.wantInjectToAgent)
			}
			if got := tc.ac.EffectiveLength(); got != tc.wantLength {
				t.Errorf("EffectiveLength = %d, want %d", got, tc.wantLength)
			}
		})
	}
}
