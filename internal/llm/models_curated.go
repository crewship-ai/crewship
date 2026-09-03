package llm

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/config"
)

// The curated model set — the single source of truth for "what models exist"
// when a provider can't be reached live (no credential, network down, or the
// provider doesn't implement ModelLister) — is config/models.json, shared with
// the web bundle. This file only decodes it. Keyed by provider identifier; the
// lookup is case-insensitive so both the API enum form ("ANTHROPIC") and the
// lowercase Provider.Name() form ("anthropic") resolve.
//
// The ids are bare aliases, no date suffixes (date-suffixed aliases 404
// against the Messages API), ordered most-capable-first so a UI that renders
// the list top-to-bottom presents the recommended default first.

// ModelRole is the catalog's sizing hint for a model: what the Guide reaches
// for when it has to pick a cheap, an everyday, or a top model for a crew.
type ModelRole string

const (
	ModelRoleCheap   ModelRole = "cheap"
	ModelRoleDefault ModelRole = "default"
	ModelRoleTop     ModelRole = "top"
)

type catalogModel struct {
	ID       string    `json:"id"`
	Label    string    `json:"label"`
	Category string    `json:"category"`
	Role     ModelRole `json:"role"`
}

type catalogProvider struct {
	Label    string         `json:"label"`
	Default  string         `json:"default"`
	LiveOnly bool           `json:"live_only"`
	Models   []catalogModel `json:"models"`
}

type catalogAdapter struct {
	Provider string `json:"provider"`
	Default  string `json:"default"`
}

type modelCatalogFile struct {
	Version   int                        `json:"version"`
	Providers map[string]catalogProvider `json:"providers"`
	Adapters  map[string]catalogAdapter  `json:"adapters"`
}

// catalog is decoded once at package init. A malformed file is a build
// defect, not a runtime condition, so it panics here rather than returning
// an empty set that every picker would silently render as "no models".
var catalog = mustParseModelCatalog(config.ModelsJSON)

func mustParseModelCatalog(raw []byte) modelCatalogFile {
	c, err := parseModelCatalog(raw)
	if err != nil {
		panic(fmt.Sprintf("config/models.json: %v", err))
	}
	return c
}

func parseModelCatalog(raw []byte) (modelCatalogFile, error) {
	var c modelCatalogFile
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("decode: %w", err)
	}
	if len(c.Providers) == 0 {
		return c, fmt.Errorf("no providers")
	}
	normalised := make(map[string]catalogProvider, len(c.Providers))
	for key, p := range c.Providers {
		k := strings.ToLower(strings.TrimSpace(key))
		seen := map[string]bool{}
		for _, m := range p.Models {
			if strings.TrimSpace(m.ID) == "" {
				return c, fmt.Errorf("provider %q: model with empty id", k)
			}
			if seen[m.ID] {
				return c, fmt.Errorf("provider %q: duplicate model id %q", k, m.ID)
			}
			seen[m.ID] = true
		}
		if p.Default != "" && !seen[p.Default] {
			return c, fmt.Errorf("provider %q: default %q is not one of its models", k, p.Default)
		}
		normalised[k] = p
	}
	c.Providers = normalised
	adapters := make(map[string]catalogAdapter, len(c.Adapters))
	for key, a := range c.Adapters {
		k := strings.ToUpper(strings.TrimSpace(key))
		if k == "" {
			return c, fmt.Errorf("adapter with empty key")
		}
		if _, dup := adapters[k]; dup {
			return c, fmt.Errorf("adapter %q: duplicate key", k)
		}
		a.Provider = strings.ToLower(strings.TrimSpace(a.Provider))
		a.Default = strings.TrimSpace(a.Default)
		if a.Provider == "" {
			return c, fmt.Errorf("adapter %q: no provider", k)
		}
		if a.Default == "" {
			return c, fmt.Errorf("adapter %q: no default model", k)
		}
		// When the adapter's provider is one the server curates, the default
		// must be one of that provider's own rows — unless it is a
		// provider-prefixed id ("anthropic/claude-sonnet-5"), which is the
		// adapter's own vocabulary (OpenCode). Adapters on providers the server
		// does not curate (Cursor, Factory) carry their own ids and are not
		// checked here; validateCrewModel passes those through unchanged.
		if p, ok := normalised[a.Provider]; ok && !p.LiveOnly && !strings.Contains(a.Default, "/") {
			found := false
			for _, m := range p.Models {
				if m.ID == a.Default {
					found = true
					break
				}
			}
			if !found {
				return c, fmt.Errorf("adapter %q: default %q is not a curated %s model", k, a.Default, a.Provider)
			}
		}
		adapters[k] = a
	}
	c.Adapters = adapters
	return c, nil
}

// AdapterDefaultModel is the catalog's default model id for a CLI adapter key
// ("CLAUDE_CODE", "CODEX_CLI", …), or "" for an unknown adapter. It is the
// model `crewship setup` and the web wizard both start a new workspace on.
func AdapterDefaultModel(adapterKey string) string {
	return catalog.Adapters[strings.ToUpper(strings.TrimSpace(adapterKey))].Default
}

// HousekeepingModel is the model Crewship's own housekeeping uses for a provider —
// summarising memory, drafting a skill, probing a token, governance
// decisions: the catalog's `cheap` role, falling back to the provider default
// for a provider that marks none. These calls are billed to the customer and
// short, so the cheapest curated model is the right one by construction.
func HousekeepingModel(provider string) string {
	if id, ok := CuratedModelForRole(provider, ModelRoleCheap); ok {
		return id
	}
	return DefaultModel(provider)
}

func providerEntry(provider string) (catalogProvider, bool) {
	p, ok := catalog.Providers[strings.ToLower(strings.TrimSpace(provider))]
	return p, ok
}

// CuratedModels returns the fallback model set for a provider, or nil when the
// provider has no curated list (e.g. OLLAMA, whose model set is purely local
// and must be discovered live via /api/tags — the catalog marks it live_only
// and its rows are picker suggestions, never a served list). The returned
// slice is freshly built so callers can sort/append freely.
func CuratedModels(provider string) []ModelInfo {
	p, ok := providerEntry(provider)
	if !ok || p.LiveOnly {
		return nil
	}
	key := strings.ToLower(strings.TrimSpace(provider))
	out := make([]ModelInfo, 0, len(p.Models))
	for _, m := range p.Models {
		out = append(out, ModelInfo{ID: m.ID, DisplayName: m.Label, Provider: key})
	}
	return out
}

// DefaultModel is the catalog's default id for a provider ("" when the
// provider is unknown or declares none). It is the id both the Guide's own
// agent and a newly created agent start on for that provider.
func DefaultModel(provider string) string {
	p, ok := providerEntry(provider)
	if !ok {
		return ""
	}
	return p.Default
}

// CuratedModelForRole returns the curated model the catalog marks with the
// given role for a provider, e.g. ("openai", ModelRoleCheap). ok is false when
// the provider has no curated list or no model carries that role.
func CuratedModelForRole(provider string, role ModelRole) (string, bool) {
	p, ok := providerEntry(provider)
	if !ok || p.LiveOnly {
		return "", false
	}
	for _, m := range p.Models {
		if m.Role == role {
			return m.ID, true
		}
	}
	return "", false
}
