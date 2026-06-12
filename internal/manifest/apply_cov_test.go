package manifest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildDevcontainerJSON(t *testing.T) {
	t.Run("nil devcontainer", func(t *testing.T) {
		out, err := buildDevcontainerJSON(nil)
		if err != nil || out != "" {
			t.Fatalf("want empty, got (%q, %v)", out, err)
		}
	})
	t.Run("empty devcontainer", func(t *testing.T) {
		out, err := buildDevcontainerJSON(&Devcontainer{})
		if err != nil || out != "" {
			t.Fatalf("want empty for no serialisable fields, got (%q, %v)", out, err)
		}
	})
	t.Run("structured fields override raw", func(t *testing.T) {
		dc := &Devcontainer{
			Image:    "structured-img",
			Features: map[string]any{"ghcr.io/x/node:1": map[string]any{}},
			Env:      map[string]string{"FOO": "bar"},
			Raw: map[string]any{
				"image":      "raw-img",
				"postCreate": "echo hi",
			},
		}
		out, err := buildDevcontainerJSON(dc)
		if err != nil {
			t.Fatalf("buildDevcontainerJSON: %v", err)
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(out), &obj); err != nil {
			t.Fatalf("output is not JSON: %v\n%s", err, out)
		}
		if obj["image"] != "structured-img" {
			t.Errorf("structured image must win over raw: %v", obj["image"])
		}
		if obj["postCreate"] != "echo hi" {
			t.Errorf("raw keys must survive: %v", obj)
		}
		env, _ := obj["containerEnv"].(map[string]any)
		if env == nil || env["FOO"] != "bar" {
			t.Errorf("env must land under containerEnv: %v", obj)
		}
		if _, ok := obj["features"]; !ok {
			t.Errorf("features missing: %v", obj)
		}
	})
}

func TestBuildCrewBody_FullSpec(t *testing.T) {
	mem, cpus, ttl := 2048, 1.5, 8
	spec := &CrewSpec{
		Description: "desc",
		Color:       "#fff",
		Icon:        "gear",
		Services:    []Service{{Name: "redis", Image: "redis:7"}},
		Devcontainer: &Devcontainer{
			MemoryMB:       &mem,
			CPUs:           &cpus,
			TTLHours:       &ttl,
			NetworkMode:    "restricted",
			AllowedDomains: []string{"example.com"},
			Image:          "fallback-img", // no RuntimeImage → Image is used
			Mise:           "[tools]",
			Env:            map[string]string{"A": "b"},
		},
	}
	body, err := buildCrewBody("T", "t", spec)
	if err != nil {
		t.Fatalf("buildCrewBody: %v", err)
	}
	if body["name"] != "T" || body["slug"] != "t" {
		t.Errorf("name/slug: %v", body)
	}
	if body["description"] != "desc" || body["color"] != "#fff" || body["icon"] != "gear" {
		t.Errorf("metadata fields lost: %v", body)
	}
	svc, _ := body["services_json"].(string)
	if !strings.Contains(svc, `"redis"`) {
		t.Errorf("services_json lost: %q", svc)
	}
	if body["container_memory_mb"] != 2048 || body["container_cpus"] != 1.5 || body["container_ttl_hours"] != 8 {
		t.Errorf("container shape lost: %v", body)
	}
	if body["network_mode"] != "restricted" {
		t.Errorf("network mode lost: %v", body)
	}
	if body["runtime_image"] != "fallback-img" {
		t.Errorf("Image fallback for runtime_image broken: %v", body["runtime_image"])
	}
	if body["mise_config"] != "[tools]" {
		t.Errorf("mise lost: %v", body)
	}
	cfg, _ := body["devcontainer_config"].(string)
	if !strings.Contains(cfg, "containerEnv") {
		t.Errorf("devcontainer_config lost: %q", cfg)
	}

	// RuntimeImage wins over Image when both set.
	spec.Devcontainer.RuntimeImage = "primary-img"
	body2, err := buildCrewBody("T", "t", spec)
	if err != nil {
		t.Fatalf("buildCrewBody: %v", err)
	}
	if body2["runtime_image"] != "primary-img" {
		t.Errorf("RuntimeImage should win: %v", body2["runtime_image"])
	}
}

func TestSecretsSources(t *testing.T) {
	t.Run("EnvSecretsSource nil lookup", func(t *testing.T) {
		s := EnvSecretsSource{}
		if _, ok := s.ValueFor("X"); ok {
			t.Error("nil lookup must return false")
		}
	})
	t.Run("EnvSecretsSource found", func(t *testing.T) {
		s := EnvSecretsSource{Lookup: func(k string) (string, bool) {
			if k == "X" {
				return "val", true
			}
			return "", false
		}}
		if v, ok := s.ValueFor("X"); !ok || v != "val" {
			t.Errorf("got (%q, %v)", v, ok)
		}
		if _, ok := s.ValueFor("Y"); ok {
			t.Error("missing key must return false")
		}
	})
	t.Run("EnvSecretsSource empty value treated missing", func(t *testing.T) {
		s := EnvSecretsSource{Lookup: func(string) (string, bool) { return "", true }}
		if _, ok := s.ValueFor("X"); ok {
			t.Error("empty value must return false")
		}
	})
	t.Run("MapSecretsSource empty value treated missing", func(t *testing.T) {
		s := MapSecretsSource{"X": ""}
		if _, ok := s.ValueFor("X"); ok {
			t.Error("empty value must return false")
		}
	})
	t.Run("ChainSecretsSource ordering", func(t *testing.T) {
		chain := ChainSecretsSource{
			MapSecretsSource{"A": "first"},
			MapSecretsSource{"A": "second", "B": "from-second"},
		}
		if v, _ := chain.ValueFor("A"); v != "first" {
			t.Errorf("first source must win, got %q", v)
		}
		if v, _ := chain.ValueFor("B"); v != "from-second" {
			t.Errorf("fallthrough broken, got %q", v)
		}
		if _, ok := chain.ValueFor("C"); ok {
			t.Error("unknown key must return false")
		}
	})
}

func TestDefaultInt_NonZero(t *testing.T) {
	if got := defaultInt(5, 9); got != 5 {
		t.Errorf("defaultInt(5,9) = %d, want 5", got)
	}
	if got := defaultInt(0, 9); got != 9 {
		t.Errorf("defaultInt(0,9) = %d, want 9", got)
	}
}

func TestApply_NilGuards(t *testing.T) {
	ctx := context.Background()
	if _, err := Apply(ctx, nil, &Bundle{}, Options{}); err == nil || !strings.Contains(err.Error(), "client is nil") {
		t.Errorf("want nil-client error, got %v", err)
	}
	if _, err := Apply(ctx, NewClient(newCovStub()), nil, Options{}); err == nil || !strings.Contains(err.Error(), "bundle is nil") {
		t.Errorf("want nil-bundle error, got %v", err)
	}
}

func TestApply_ExecFailureStopsAndWraps(t *testing.T) {
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
	stub := newCovStub()
	stub.on("GET", "/api/v1/crews", 200, `[]`)
	stub.on("GET", "/api/v1/credentials", 200, `[]`)
	stub.on("POST", "/api/v1/crews", 500, `{"error":"db locked"}`)

	res, err := Apply(context.Background(), NewClient(stub), b, Options{Mode: ApplyUpsert})
	if err == nil {
		t.Fatal("want apply error when crew create fails")
	}
	if !strings.Contains(err.Error(), "+ crew t") {
		t.Errorf("error should carry the plan-item prefix, got %v", err)
	}
	if res == nil || res.LastError == nil {
		t.Errorf("result should carry LastError, got %+v", res)
	}
	if res.Created != 0 {
		t.Errorf("nothing should be counted created, got %d", res.Created)
	}
	// The agent create must never have been attempted after the
	// failed crew create.
	if stub.countCalls("POST", "/api/v1/agents") != 0 {
		t.Error("apply must stop at the first failing item")
	}
}
