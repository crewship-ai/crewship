package pipeline

import (
	"net/url"
	"sort"
	"strings"
)

// Manifest is the full "what this routine touches" blast radius — the union of
// declared resources and what's derivable from the step graph. Rendered by the
// UI (data-flow diagram) and usable by governance.
//
// Every slice is non-nil (empty, not null) so the JSON the UI consumes is
// stable regardless of which capabilities a routine exercises.
type Manifest struct {
	Integrations []string       `json:"integrations"` // from IntegrationsRequired
	Egress       []string       `json:"egress"`       // from EgressTargets + http step hosts
	Credentials  []CredReq      `json:"credentials"`  // from CredsRequired
	Agents       []string       `json:"agents"`       // from agent_run steps (AgentSlug), deduped+sorted
	Routines     []string       `json:"routines"`     // from call_pipeline steps (PipelineSlug)
	Datastores   []DatastoreRef `json:"datastores"`   // declared Resources.Datastores
	Tools        []ToolRef      `json:"tools"`        // declared Resources.Tools + code-step runtimes
	HasHTTP      bool           `json:"has_http"`     // any http step
	HasCode      bool           `json:"has_code"`     // any code step
}

// ExtractManifest computes the routine's full capability manifest from the DSL
// alone (no DB, no crew context). It mirrors StaticRiskReasons' walk — the same
// recursion over top-level steps plus routine-level and per-step lifecycle hooks
// — so a capability hidden in an on_failure hook is part of the blast radius the
// UI renders and governance reasons about.
//
// Derivation rules:
//   - Integrations = NormalizedIntegrationsRequired (lowercased, deduped).
//   - Credentials  = CredsRequired (passthrough).
//   - Egress       = EgressTargets PLUS any host parseable from http step URLs
//     (best-effort url.Parse; templated `{{ }}` URLs that don't parse are
//     skipped).
//   - Agents       = AgentSlug from every agent_run step.
//   - Routines     = PipelineSlug from every call_pipeline step.
//   - Tools        = declared Resources.Tools PLUS a ToolRef{Type: runtime} per
//     code step (the runtime is the only statically-knowable "tool").
//   - Datastores   = declared Resources.Datastores.
//
// All string/ref slices are deduped + sorted deterministically and never nil.
func (d *DSL) ExtractManifest() *Manifest {
	m := &Manifest{
		Integrations: []string{},
		Egress:       []string{},
		Credentials:  []CredReq{},
		Agents:       []string{},
		Routines:     []string{},
		Datastores:   []DatastoreRef{},
		Tools:        []ToolRef{},
	}
	if d == nil {
		return m
	}

	// Integrations + credentials are passthroughs, deduped + sorted so the
	// manifest JSON the UI/governance consumes is deterministic regardless of
	// how the author ordered (or repeated) the declarations.
	m.Integrations = dedupeSorted(d.NormalizedIntegrationsRequired())
	if len(d.CredsRequired) > 0 {
		m.Credentials = dedupeSortedCreds(d.CredsRequired)
	}

	// Accumulators walked across steps + hooks. Egress entries are DNS
	// hostnames (case-insensitive), so canonicalize to lowercase before they
	// reach dedupeSorted — otherwise EXAMPLE.com and example.com survive as two.
	egress := make([]string, 0, len(d.EgressTargets))
	for _, e := range d.EgressTargets {
		egress = append(egress, strings.ToLower(strings.TrimSpace(e)))
	}
	var agents, routines []string
	var tools []ToolRef

	var scan func(st *Step)
	scan = func(st *Step) {
		if st == nil {
			return
		}
		switch st.Type {
		case StepAgentRun:
			if st.AgentSlug != "" {
				agents = append(agents, st.AgentSlug)
			}
		case StepCallPipeline:
			if st.PipelineSlug != "" {
				routines = append(routines, st.PipelineSlug)
			}
		case StepHTTP:
			m.HasHTTP = true
			if st.HTTP != nil {
				if host := hostFromURL(st.HTTP.URL); host != "" {
					egress = append(egress, host)
				}
			}
		case StepCode:
			m.HasCode = true
			if st.Code != nil && st.Code.Runtime != "" {
				tools = append(tools, ToolRef{Type: st.Code.Runtime})
			}
		}
		if st.Hooks != nil {
			scan(st.Hooks.Before)
			scan(st.Hooks.After)
		}
	}
	for i := range d.Steps {
		scan(&d.Steps[i])
	}
	if d.Hooks != nil {
		scan(d.Hooks.BeforeAll)
		scan(d.Hooks.AfterAll)
		scan(d.Hooks.OnFailure)
	}

	// Merge declared resources.
	if d.Resources != nil {
		if len(d.Resources.Datastores) > 0 {
			m.Datastores = dedupeSortedDatastores(d.Resources.Datastores)
		}
		tools = append(tools, d.Resources.Tools...)
	}

	m.Egress = dedupeSorted(egress)
	m.Agents = dedupeSorted(agents)
	m.Routines = dedupeSorted(routines)
	if t := dedupeSortedTools(tools); len(t) > 0 {
		m.Tools = t
	}
	return m
}

// hostFromURL extracts the hostname from an http step URL. Returns "" for
// templated or unparseable URLs (a `{{ inputs.host }}` URL has no static host).
func hostFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "{{") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	// Lowercase: DNS hostnames are case-insensitive, so the manifest dedupes
	// http hosts against declared egress targets consistently.
	return strings.ToLower(u.Hostname())
}

// dedupeSorted returns the input with empties dropped, duplicates removed, and
// the result sorted. Never returns nil — callers rely on a non-nil slice so the
// manifest JSON renders [] rather than null.
func dedupeSorted(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// dedupeSortedTools dedupes ToolRefs by (Type, Name) and sorts by Type then
// Name. Entries with an empty Type are dropped (a tool with no family is
// meaningless in the manifest).
func dedupeSortedCreds(in []CredReq) []CredReq {
	seen := make(map[string]struct{}, len(in))
	out := make([]CredReq, 0, len(in))
	for _, c := range in {
		c.Type = strings.TrimSpace(c.Type)
		c.Scope = strings.TrimSpace(c.Scope)
		if c.Type == "" {
			continue
		}
		key := c.Type + "\x00" + c.Scope
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Scope < out[j].Scope
	})
	return out
}

func dedupeSortedTools(in []ToolRef) []ToolRef {
	seen := make(map[string]struct{}, len(in))
	out := make([]ToolRef, 0, len(in))
	for _, t := range in {
		t.Type = strings.TrimSpace(t.Type)
		t.Name = strings.TrimSpace(t.Name)
		if t.Type == "" {
			continue
		}
		key := t.Type + "\x00" + t.Name
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// dedupeSortedDatastores dedupes DatastoreRefs by the full (Type, Name, Note)
// tuple and sorts by Type, Name, Note. Entries with an empty Type are dropped.
func dedupeSortedDatastores(in []DatastoreRef) []DatastoreRef {
	seen := make(map[string]struct{}, len(in))
	out := make([]DatastoreRef, 0, len(in))
	for _, ds := range in {
		ds.Type = strings.TrimSpace(ds.Type)
		ds.Name = strings.TrimSpace(ds.Name)
		ds.Note = strings.TrimSpace(ds.Note)
		if ds.Type == "" {
			continue
		}
		key := ds.Type + "\x00" + ds.Name + "\x00" + ds.Note
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, ds)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Note < out[j].Note
	})
	return out
}
