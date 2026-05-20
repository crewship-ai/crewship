package manifest

import (
	"context"
	"strings"
	"testing"
)

// loadBundleOrFail is a tiny test helper — every auto-credential
// validation test starts by `Load`-ing a manifest body and we'd
// rather see "Load failed: ..." than have each test repeat the
// error-handling boilerplate.
func loadBundleOrFail(t *testing.T, body []byte) *Bundle {
	t.Helper()
	b, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return b
}

func TestValidate_AutoCredentialBadName(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  services:
    - name: pg
      image: postgres:16-alpine
      auto_credentials:
        - { name: "lower-case-bad" }
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`)
	b := loadBundleOrFail(t, body)
	if err := b.Validate(); err == nil {
		t.Fatal("expected validation error for lowercase auto_credential name")
	} else if !strings.Contains(err.Error(), "lower-case-bad") {
		t.Errorf("error should name the offending field: %v", err)
	}
}

func TestValidate_AutoCredentialLengthBelowFloor(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  services:
    - name: pg
      image: postgres:16-alpine
      auto_credentials:
        - { name: POSTGRES_PASSWORD, length: 8 }
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`)
	b := loadBundleOrFail(t, body)
	if err := b.Validate(); err == nil {
		t.Fatal("expected validation error for length < minAutoCredentialBytes")
	}
}

func TestValidate_AutoCredentialLengthZeroAllowed(t *testing.T) {
	// 0 means "use the default" (resolved in EffectiveLength()), so
	// it must NOT trip the floor check.
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  services:
    - name: pg
      image: postgres:16-alpine
      auto_credentials:
        - { name: POSTGRES_PASSWORD, length: 0 }
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`)
	b := loadBundleOrFail(t, body)
	if err := b.Validate(); err != nil {
		t.Errorf("length:0 should be treated as default, not a violation; got %v", err)
	}
}

func TestValidate_AutoCredentialBadInjectAsEnv(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  services:
    - name: pg
      image: postgres:16-alpine
      auto_credentials:
        - { name: POSTGRES_PASSWORD, inject_as_env: "9starts-with-digit" }
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`)
	b := loadBundleOrFail(t, body)
	if err := b.Validate(); err == nil {
		t.Fatal("expected validation error for inject_as_env starting with a digit")
	}
}

func TestValidate_AutoCredentialDuplicateInSameService(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  services:
    - name: pg
      image: postgres:16-alpine
      auto_credentials:
        - { name: POSTGRES_PASSWORD }
        - { name: POSTGRES_PASSWORD, inject_as_env: PG_PWD }
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`)
	b := loadBundleOrFail(t, body)
	if err := b.Validate(); err == nil {
		t.Fatal("expected validation error for duplicate auto_credential within same service")
	}
}

func TestValidate_AutoCredentialClashesWithCredentialsBlock(t *testing.T) {
	// Operator declared POSTGRES_PASSWORD as a manual credential AND
	// as an auto-credential — collision. The validator must refuse
	// to let apply proceed; the dispatch can't tell which one the
	// operator actually intended.
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  credentials:
    - { env: POSTGRES_PASSWORD, provider: NONE, type: GENERIC_SECRET }
  services:
    - name: pg
      image: postgres:16-alpine
      auto_credentials:
        - { name: POSTGRES_PASSWORD }
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`)
	b := loadBundleOrFail(t, body)
	if err := b.Validate(); err == nil {
		t.Fatal("expected validation error for auto_credential clashing with credentials[] block")
	} else if !strings.Contains(err.Error(), "credentials[] declaration") {
		t.Errorf("error should explain the collision: %v", err)
	}
}

func TestValidate_AutoCredentialHappyPath(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  services:
    - name: pg
      image: postgres:16-alpine
      auto_credentials:
        - { name: PG_REPLICATION_PASSWORD, inject_as_env: PG_REPL, length: 24, description: replication }
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`)
	b := loadBundleOrFail(t, body)
	if err := b.Validate(); err != nil {
		t.Fatalf("valid auto_credential rejected: %v", err)
	}
}

// Cross-crew collision detection: an AUTO_MANAGED credential already
// exists in the workspace for a *different* crew. The apply must
// refuse, not silently bind to the wrong row.
func TestPlan_CrossCrewAutoCredentialCollision(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: B, slug: b}
spec:
  services:
    - { name: pg, image: postgres:16-alpine }
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`)
	b := loadBundleOrFail(t, body)
	fake := newFakeAPI(t)
	// Pre-seed: an AUTO_MANAGED credential already exists for crew "a".
	other := "a/pg"
	fake.credsByName["POSTGRES_PASSWORD"] = map[string]any{
		"id":                      "cred_pre",
		"name":                    "POSTGRES_PASSWORD",
		"provider":                "AUTO_MANAGED",
		"status":                  "ACTIVE",
		"provisioned_for_service": other,
	}
	client := NewClient(fake)
	_, err := Apply(context.Background(), client, b, Options{Mode: ApplyUpsert, Yes: true})
	if err == nil {
		t.Fatal("expected error for cross-crew POSTGRES_PASSWORD collision")
	}
	if !strings.Contains(err.Error(), "auto-managed for a/pg") {
		t.Errorf("error should mention the existing tag: %v", err)
	}
}

// Same-crew re-apply: the existing AUTO_MANAGED row has a matching
// provisioned_for_service tag → re-apply is idempotent, no error.
func TestPlan_SameCrewAutoCredentialReapplyIsIdempotent(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: B, slug: b}
spec:
  services:
    - { name: pg, image: postgres:16-alpine }
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`)
	b := loadBundleOrFail(t, body)
	fake := newFakeAPI(t)
	same := "b/pg"
	fake.credsByName["POSTGRES_PASSWORD"] = map[string]any{
		"id":                      "cred_pre",
		"name":                    "POSTGRES_PASSWORD",
		"provider":                "AUTO_MANAGED",
		"status":                  "ACTIVE",
		"provisioned_for_service": same,
	}
	client := NewClient(fake)
	if _, err := Apply(context.Background(), client, b, Options{Mode: ApplyUpsert, Yes: true}); err != nil {
		t.Fatalf("re-apply of same crew should be idempotent, got error: %v", err)
	}
}
