package database

import (
	"testing"
)

// TestLoadBuiltinCrewTemplates_LoadsAll pins the count to 12.
// A future contributor adding or removing a builtin crew template
// must update this assertion deliberately — the catalogue is the
// first thing a new operator sees in the workspace UI, so silent
// changes are a UX surprise.
func TestLoadBuiltinCrewTemplates_LoadsAll(t *testing.T) {
	t.Parallel()
	docs, err := loadBuiltinCrewTemplates()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got, want := len(docs), 12; got != want {
		t.Fatalf("loaded %d templates, want %d", got, want)
	}
	want := map[string]bool{
		"software-development":   false,
		"devops-sre":             false,
		"content-marketing":      false,
		"accounting-finance":     false,
		"customer-support":       false,
		"research-analysis":      false,
		"saas-product-squad":     false,
		"data-engineering":       false,
		"security-audit":         false,
		"mobile-app-development": false,
		"api-integrations":       false,
		"documentation-team":     false,
	}
	for _, d := range docs {
		if _, ok := want[d.Slug]; !ok {
			t.Errorf("unexpected template slug %q", d.Slug)
			continue
		}
		want[d.Slug] = true
	}
	for slug, seen := range want {
		if !seen {
			t.Errorf("missing template slug %q from embedded set", slug)
		}
	}
}

// TestLoadBuiltinCrewTemplates_ShapeIntegrity asserts the per-
// template shape: non-empty name/slug/description/icon/color/category
// and at least one agent with required fields populated. Catches a
// YAML edit that breaks the downstream INSERT / agents_json marshal.
func TestLoadBuiltinCrewTemplates_ShapeIntegrity(t *testing.T) {
	t.Parallel()
	docs, err := loadBuiltinCrewTemplates()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, d := range docs {
		if d.Name == "" {
			t.Errorf("%s: name is empty", d.Slug)
		}
		if d.Slug == "" {
			t.Errorf("template missing slug (file content: %+v)", d)
			continue
		}
		if d.Description == "" {
			t.Errorf("%s: description is empty", d.Slug)
		}
		if d.Icon == "" {
			t.Errorf("%s: icon is empty", d.Slug)
		}
		if d.Color == "" {
			t.Errorf("%s: color is empty", d.Slug)
		}
		if d.Category == "" {
			t.Errorf("%s: category is empty", d.Slug)
		}
		if len(d.Agents) == 0 {
			t.Errorf("%s: zero agents", d.Slug)
		}
		seenLead := false
		seenSlug := map[string]bool{}
		for i, a := range d.Agents {
			if a.Slug == "" {
				t.Errorf("%s: agent %d missing slug", d.Slug, i)
			}
			if seenSlug[a.Slug] {
				t.Errorf("%s: agent slug %q is duplicated", d.Slug, a.Slug)
			}
			seenSlug[a.Slug] = true
			if a.Name == "" {
				t.Errorf("%s: agent %d (%q) missing name", d.Slug, i, a.Slug)
			}
			if a.AgentRole == "" {
				t.Errorf("%s: agent %q missing agent_role", d.Slug, a.Slug)
			}
			if a.SystemPrompt == "" {
				t.Errorf("%s: agent %q missing system_prompt", d.Slug, a.Slug)
			}
			// Routing / model fields — empty values here would produce
			// a template that the deploy handler accepts but the
			// orchestrator can't actually instantiate (the agent_role
			// row would land with empty CLI adapter / provider / model
			// / tool profile). Earlier shape check only covered
			// agent_role + system_prompt, which let routing drift
			// through silently.
			if a.CLIAdapter == "" {
				t.Errorf("%s: agent %q missing cli_adapter", d.Slug, a.Slug)
			}
			if a.LLMProvider == "" {
				t.Errorf("%s: agent %q missing llm_provider", d.Slug, a.Slug)
			}
			if a.LLMModel == "" {
				t.Errorf("%s: agent %q missing llm_model", d.Slug, a.Slug)
			}
			if a.ToolProfile == "" {
				t.Errorf("%s: agent %q missing tool_profile", d.Slug, a.Slug)
			}
			if a.AgentRole == "LEAD" {
				seenLead = true
			}
		}
		if !seenLead {
			t.Errorf("%s: no agent with role LEAD (every crew needs a lead)", d.Slug)
		}
	}
}

// TestLoadBuiltinCrewTemplates_DeterministicOrder confirms the loader
// returns templates in lexicographic filename order across repeated
// calls so SeedBuiltinCrewTemplates is deterministic — a CREATE
// race would otherwise depend on map iteration order.
func TestLoadBuiltinCrewTemplates_DeterministicOrder(t *testing.T) {
	t.Parallel()
	first, err := loadBuiltinCrewTemplates()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for i := 0; i < 4; i++ {
		next, err := loadBuiltinCrewTemplates()
		if err != nil {
			t.Fatalf("load iter %d: %v", i, err)
		}
		if len(next) != len(first) {
			t.Fatalf("iter %d: len mismatch: %d vs %d", i, len(next), len(first))
		}
		for j := range next {
			if next[j].Slug != first[j].Slug {
				t.Errorf("iter %d, docs[%d].Slug = %q, want %q (order drift)", i, j, next[j].Slug, first[j].Slug)
			}
		}
	}
}
