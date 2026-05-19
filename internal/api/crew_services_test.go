package api

import (
	"strings"
	"testing"
)

func TestValidateServicesJSON_AcceptsWellFormed(t *testing.T) {
	body := `[
		{"name": "redis", "image": "redis:7-alpine", "ports": ["6379"]},
		{"name": "postgres", "image": "postgres:16",
		 "env": {"POSTGRES_DB": "app"},
		 "env_refs": ["PG_PASS"],
		 "volumes": [{"name": "pg-data", "mount": "/var/lib/postgresql/data"}],
		 "healthcheck": {"test": ["CMD", "pg_isready"], "interval": "5s", "retries": 3}}
	]`
	if err := validateServicesJSON(body); err != nil {
		t.Errorf("expected accept, got %v", err)
	}
}

func TestValidateServicesJSON_RejectsMalformed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		wants string
	}{
		{"not-json", `not really json`, "JSON"},
		{"empty-name", `[{"image": "redis"}]`, "name is required"},
		{"bad-name", `[{"name": "BAD_NAME", "image": "redis"}]`, "DNS"},
		{"missing-image", `[{"name": "redis"}]`, "image is required"},
		{"duplicate-name", `[{"name": "redis", "image": "redis"}, {"name": "redis", "image": "redis"}]`, "duplicate"},
		{"bind-mount", `[{"name": "pg", "image": "pg",
			"volumes": [{"name": "/host/data", "mount": "/data"}]}]`, "bind mount"},
		{"duplicate-mount", `[{"name": "pg", "image": "pg",
			"volumes": [{"name": "a", "mount": "/x"}, {"name": "b", "mount": "/x"}]}]`, "duplicate mount"},
		{"healthcheck-no-test", `[{"name": "redis", "image": "redis",
			"healthcheck": {"interval": "5s"}}]`, "without test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateServicesJSON(tc.body)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("error %q doesn't mention %q", err, tc.wants)
			}
		})
	}
}

func TestValidateServicesJSON_AcceptsEmpty(t *testing.T) {
	if err := validateServicesJSON(""); err != nil {
		t.Errorf("empty services_json should be accepted, got %v", err)
	}
	if err := validateServicesJSON("   \n  "); err != nil {
		t.Errorf("whitespace-only should be accepted, got %v", err)
	}
}

func TestServicesFromJSON_ResolvesEnvRefs(t *testing.T) {
	body := `[{"name": "pg", "image": "postgres:16",
		"env": {"POSTGRES_DB": "app"},
		"env_refs": ["POSTGRES_PASSWORD", "MISSING_REF"]}]`
	lookup := func(s string) string {
		if s == "POSTGRES_PASSWORD" {
			return "secret-from-vault"
		}
		return ""
	}
	out, err := servicesFromJSON(body, lookup)
	if err != nil {
		t.Fatalf("servicesFromJSON: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 service, got %d", len(out))
	}
	svc := out[0]
	if svc.Env["POSTGRES_DB"] != "app" {
		t.Error("literal env not preserved")
	}
	if svc.Env["POSTGRES_PASSWORD"] != "secret-from-vault" {
		t.Error("env_ref not resolved from lookup")
	}
	if _, ok := svc.Env["MISSING_REF"]; ok {
		t.Error("missing env_ref should be omitted, not empty")
	}
}
