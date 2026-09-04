package llm

import "testing"

// config/models.json is the one list every picker and validator reads, so
// what these pin is the CONTRACT the file has to keep, not any particular
// model: every served provider has a default, that default is one of its own
// rows, the three roles the Guide sizes crews with are present, and a
// malformed file fails loudly instead of emptying every picker.
func TestModelCatalog_Contract(t *testing.T) {
	for _, provider := range []string{"anthropic", "openai", "google"} {
		t.Run(provider, func(t *testing.T) {
			models := CuratedModels(provider)
			if len(models) == 0 {
				t.Fatalf("no curated models for %s", provider)
			}
			def := DefaultModel(provider)
			if def == "" {
				t.Fatalf("no default for %s", provider)
			}
			found := false
			for _, m := range models {
				if m.ID == def {
					found = true
				}
				if m.DisplayName == "" {
					t.Errorf("%s: model %q has no label", provider, m.ID)
				}
			}
			if !found {
				t.Errorf("%s: default %q is not a curated model", provider, def)
			}
			for _, role := range []ModelRole{ModelRoleCheap, ModelRoleDefault, ModelRoleTop} {
				if _, ok := CuratedModelForRole(provider, role); !ok {
					t.Errorf("%s: no model carries role %q", provider, role)
				}
			}
			if got, _ := CuratedModelForRole(provider, ModelRoleDefault); got != def {
				t.Errorf("%s: role default is %q but provider default is %q", provider, got, def)
			}
		})
	}
}

func TestModelCatalog_OllamaIsLiveOnly(t *testing.T) {
	if got := CuratedModels("ollama"); got != nil {
		t.Fatalf("ollama must not be served as curated, got %+v", got)
	}
	if _, ok := CuratedModelForRole("ollama", ModelRoleDefault); ok {
		t.Fatal("ollama must not answer a role")
	}
}

func TestModelCatalog_RejectsBrokenFile(t *testing.T) {
	cases := map[string]string{
		"not json":              `{`,
		"no providers":          `{"providers":{}}`,
		"empty id":              `{"providers":{"x":{"models":[{"id":""}]}}}`,
		"duplicate id":          `{"providers":{"x":{"models":[{"id":"a"},{"id":"a"}]}}}`,
		"default not a row":     `{"providers":{"x":{"default":"b","models":[{"id":"a"}]}}}`,
		"adapter no provider":   `{"providers":{"x":{"models":[{"id":"a"}]}},"adapters":{"A":{"default":"a"}}}`,
		"adapter no default":    `{"providers":{"x":{"models":[{"id":"a"}]}},"adapters":{"A":{"provider":"x"}}}`,
		"adapter default drift": `{"providers":{"x":{"models":[{"id":"a"}]}},"adapters":{"A":{"provider":"x","default":"zzz"}}}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseModelCatalog([]byte(raw)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestModelCatalog_AdaptersAndAux(t *testing.T) {
	for _, key := range []string{"CLAUDE_CODE", "CODEX_CLI", "GEMINI_CLI", "CURSOR_CLI", "FACTORY_DROID", "OPENCODE"} {
		if AdapterDefaultModel(key) == "" {
			t.Errorf("adapter %s has no default model", key)
		}
	}
	if AdapterDefaultModel("NOPE") != "" {
		t.Error("unknown adapter must answer empty")
	}
	for _, provider := range []string{"anthropic", "openai", "google"} {
		aux := HousekeepingModel(provider)
		cheap, _ := CuratedModelForRole(provider, ModelRoleCheap)
		if aux == "" || aux != cheap {
			t.Errorf("%s: HousekeepingModel = %q, want the cheap role %q", provider, aux, cheap)
		}
	}
	if HousekeepingModel("nope") != "" {
		t.Error("unknown provider must answer empty")
	}
}

func TestModelCatalog_AdapterKeysAreNormalised(t *testing.T) {
	c, err := parseModelCatalog([]byte(`{"providers":{"x":{"models":[{"id":"a"}]}},"adapters":{" claude_code ":{"provider":"X","default":"a"}}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := c.Adapters["CLAUDE_CODE"]; !ok {
		t.Fatalf("adapter key not normalised: %v", c.Adapters)
	}
}
