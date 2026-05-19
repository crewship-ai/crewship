package manifest

import (
	"context"
	"strings"
	"testing"
)

func TestServices_AcceptedInManifest(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  services:
    - name: redis
      image: redis:7-alpine
      ports: ["6379"]
  credentials:
    - {env: PGPASS, provider: NONE, type: GENERIC_SECRET}
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`)
	b, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := b.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := len(b.Documents[0].Spec.Services); got != 1 {
		t.Errorf("want 1 service, got %d", got)
	}
}

func TestServices_RejectsBadName(t *testing.T) {
	_, err := Load([]byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  services:
    - name: "Bad Name"
      image: redis:7
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`))
	// Load won't reject; Validate will.
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b, _ := Load([]byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  services:
    - name: "Bad-Name-_"
      image: redis:7
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`))
	err = b.Validate()
	if err == nil || !strings.Contains(err.Error(), "DNS label") {
		t.Errorf("want DNS-label error, got %v", err)
	}
}

func TestServices_RejectsMissingImage(t *testing.T) {
	b, err := Load([]byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  services:
    - {name: redis}
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = b.Validate()
	if err == nil || !strings.Contains(err.Error(), "image") {
		t.Errorf("want missing-image error, got %v", err)
	}
}

func TestServices_RejectsDanglingEnvRef(t *testing.T) {
	b, err := Load([]byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  services:
    - {name: redis, image: redis:7, env_refs: [GHOST_VAR]}
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = b.Validate()
	if err == nil || !strings.Contains(err.Error(), "unknown credential") {
		t.Errorf("want dangling-env-ref error, got %v", err)
	}
}

func TestServices_RejectsBindMount(t *testing.T) {
	b, err := Load([]byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  services:
    - name: postgres
      image: postgres:16
      volumes:
        - {name: /host/path, mount: /var/lib/postgresql/data}
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = b.Validate()
	if err == nil || !strings.Contains(err.Error(), "bind mount") {
		t.Errorf("want bind-mount rejection, got %v", err)
	}
}

func TestServices_PlanEmitsCreateAction(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  services:
    - {name: redis, image: redis:7}
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`)
	b, _ := Load(body)
	fake := newFakeAPI(t)
	client := NewClient(fake)
	plan, err := BuildPlan(context.Background(), client, b, Options{Mode: ApplyUpsert})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	var sawServiceCreate bool
	for _, it := range plan.Items {
		if it.Kind == "service" && it.Action == ActionCreate &&
			strings.Contains(it.Description, "redis") &&
			strings.Contains(it.Description, "redis:7") {
			sawServiceCreate = true
		}
	}
	if !sawServiceCreate {
		t.Errorf("plan should emit ActionCreate for declared sidecar; got items: %+v", plan.Items)
	}
}

func TestServices_ApplyEmitsServicesJSONInCrewBody(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  services:
    - {name: redis, image: redis:7-alpine, ports: ["6379"]}
    - name: postgres
      image: postgres:16
      env: {POSTGRES_DB: app}
      volumes:
        - {name: pg-data, mount: /var/lib/postgresql/data}
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`)
	b, _ := Load(body)
	fake := newFakeAPI(t)
	client := NewClient(fake)
	_, err := Apply(context.Background(), client, b, Options{Mode: ApplyUpsert, Yes: true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var sawServicesJSON string
	for _, call := range fake.Calls {
		if call.Method == "POST" && call.Path == "/api/v1/crews" {
			if v, _ := call.Body["services_json"].(string); v != "" {
				sawServicesJSON = v
			}
		}
	}
	if sawServicesJSON == "" {
		t.Fatal("expected POST /crews body to include services_json")
	}
	if !strings.Contains(sawServicesJSON, "redis:7-alpine") {
		t.Errorf("services_json missing redis image; got %q", sawServicesJSON)
	}
	if !strings.Contains(sawServicesJSON, "postgres:16") {
		t.Errorf("services_json missing postgres image; got %q", sawServicesJSON)
	}
}
