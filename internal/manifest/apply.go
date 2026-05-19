package manifest

import (
	"context"
	"fmt"
)

// ApplyMode controls what apply does when a slug already exists in
// the target workspace. Default is Upsert (k8s-style with prune).
type ApplyMode int

const (
	// ApplyUpsert is the default: create missing, update drifted,
	// AND delete resources that exist in the workspace but aren't
	// declared in the manifest (manifest is source of truth).
	// Destructive deletes prompt for confirmation unless --yes was
	// passed.
	ApplyUpsert ApplyMode = iota

	// ApplyStrict refuses to update or delete; if any resource in
	// the manifest already exists, apply stops with a clear error.
	// Use in CI when "this manifest must create fresh resources" is
	// the requirement.
	ApplyStrict

	// ApplyReplace deletes every existing resource that matches a
	// manifest slug before creating it fresh. Destructive; prompts
	// for confirmation unless --yes was passed.
	ApplyReplace
)

// CredentialSource is the strategy for filling pending credential
// values. Callers wire it up from CLI flags; the manifest package
// itself stays unaware of how the user actually wants to supply
// secrets.
type CredentialSource interface {
	ValueFor(envVar string) (string, bool)
}

// NoSecretsSource is a CredentialSource that never supplies values;
// every declared credential becomes a pending slot.
type NoSecretsSource struct{}

func (NoSecretsSource) ValueFor(string) (string, bool) { return "", false }

// EnvSecretsSource looks up values from the process environment.
type EnvSecretsSource struct {
	Lookup func(string) (string, bool)
}

func (s EnvSecretsSource) ValueFor(envVar string) (string, bool) {
	if s.Lookup == nil {
		return "", false
	}
	v, ok := s.Lookup(envVar)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

// MapSecretsSource resolves from a pre-loaded map.
type MapSecretsSource map[string]string

func (m MapSecretsSource) ValueFor(envVar string) (string, bool) {
	v, ok := m[envVar]
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

// ChainSecretsSource queries each child in order.
type ChainSecretsSource []CredentialSource

func (c ChainSecretsSource) ValueFor(envVar string) (string, bool) {
	for _, src := range c {
		if v, ok := src.ValueFor(envVar); ok {
			return v, true
		}
	}
	return "", false
}

// Options configure a single apply or plan run.
type Options struct {
	Mode    ApplyMode
	DryRun  bool
	Secrets CredentialSource
	// Yes skips the destructive-action confirmation prompt. The
	// manifest package never reads stdin itself; the CLI sets this
	// after running its own prompt. Calling Apply() with Yes=false
	// and HasDestructive()=true returns ErrConfirmationRequired so
	// the caller can prompt and retry.
	Yes      bool
	OnReport func(line string)
}

// ErrConfirmationRequired is returned by Apply when the plan
// includes destructive operations and Options.Yes is false. CLI
// callers prompt the user and re-invoke with Yes=true.
var ErrConfirmationRequired = fmt.Errorf("destructive plan requires confirmation (set Options.Yes or pass --yes)")

// Apply runs BuildPlan and then executes each plan item in order.
// Destructive plans without Options.Yes return
// ErrConfirmationRequired without performing any mutations — the CLI
// is expected to prompt the user and retry with Yes=true.
//
// DryRun returns the plan but skips every exec closure; the result
// counts what would have changed.
func Apply(ctx context.Context, c *Client, b *Bundle, opts Options) (*Result, error) {
	if c == nil {
		return nil, fmt.Errorf("manifest.Apply: client is nil")
	}
	if b == nil {
		return nil, fmt.Errorf("manifest.Apply: bundle is nil")
	}
	if opts.Secrets == nil {
		opts.Secrets = NoSecretsSource{}
	}

	plan, err := BuildPlan(ctx, c, b, opts)
	if err != nil {
		return nil, err
	}

	res := &Result{Plan: plan, PendingCredentials: plan.PendingCredentials}

	// Surface the plan to the caller via OnReport before any
	// mutation runs. The CLI prints this as a Terraform-style
	// "Plan:" block.
	if opts.OnReport != nil {
		for _, it := range plan.Items {
			opts.OnReport(fmt.Sprintf("  %s %s %s", it.Action.String(), it.Kind, it.Description))
		}
	}

	if opts.DryRun {
		c, u, n, d := plan.Summary()
		res.Created, res.Updated, res.Unchanged, res.Deleted = c, u, n, d
		return res, nil
	}

	if plan.HasDestructive() && !opts.Yes {
		return res, ErrConfirmationRequired
	}

	for _, it := range plan.Items {
		if it.exec == nil {
			// Unchanged entries have no exec; nothing to do.
			switch it.Action {
			case ActionUnchanged:
				res.Unchanged++
			}
			continue
		}
		if err := it.exec(ctx, c, opts); err != nil {
			res.LastError = err
			return res, fmt.Errorf("%s %s %s: %w", it.Action, it.Kind, it.Description, err)
		}
		switch it.Action {
		case ActionCreate:
			res.Created++
		case ActionUpdate:
			res.Updated++
		case ActionDelete:
			res.Deleted++
		}
	}
	return res, nil
}

// Result captures the outcome of a single apply run.
type Result struct {
	Plan               *Plan
	Created            int
	Updated            int
	Unchanged          int
	Deleted            int
	PendingCredentials []string
	LastError          error
}

// buildCrewBody packs a CrewSpec into the body the /crews POST
// endpoint expects.
func buildCrewBody(name, slug string, spec *CrewSpec) map[string]any {
	body := map[string]any{
		"name": name,
		"slug": slug,
	}
	if spec.Description != "" {
		body["description"] = spec.Description
	}
	if spec.Color != "" {
		body["color"] = spec.Color
	}
	if spec.Icon != "" {
		body["icon"] = spec.Icon
	}
	if len(spec.Services) > 0 {
		// Serialise as JSON text — the server stores the body
		// verbatim in crews.services_json and re-parses at agent
		// run time. Mirrors how devcontainer_config is shipped.
		if data, err := jsonMarshal(spec.Services); err == nil {
			body["services_json"] = string(data)
		}
	}
	if spec.Devcontainer != nil {
		dc := spec.Devcontainer
		if dc.MemoryMB != nil {
			body["container_memory_mb"] = *dc.MemoryMB
		}
		if dc.CPUs != nil {
			body["container_cpus"] = *dc.CPUs
		}
		if dc.TTLHours != nil {
			body["container_ttl_hours"] = *dc.TTLHours
		}
		if dc.NetworkMode != "" {
			body["network_mode"] = dc.NetworkMode
		}
		if len(dc.AllowedDomains) > 0 {
			body["allowed_domains"] = dc.AllowedDomains
		}
		if dc.RuntimeImage != "" {
			body["runtime_image"] = dc.RuntimeImage
		} else if dc.Image != "" {
			body["runtime_image"] = dc.Image
		}
		if dc.Mise != "" {
			body["mise_config"] = dc.Mise
		}
		if cfg := buildDevcontainerJSON(dc); cfg != "" {
			body["devcontainer_config"] = cfg
		}
	}
	return body
}

func buildDevcontainerJSON(dc *Devcontainer) string {
	if dc == nil {
		return ""
	}
	obj := map[string]any{}
	for k, v := range dc.Raw {
		obj[k] = v
	}
	if dc.Image != "" {
		obj["image"] = dc.Image
	}
	if len(dc.Features) > 0 {
		obj["features"] = dc.Features
	}
	if len(dc.Env) > 0 {
		obj["containerEnv"] = dc.Env
	}
	if len(obj) == 0 {
		return ""
	}
	return mustJSON(obj)
}

func mustJSON(v any) string {
	out, err := jsonMarshal(v)
	if err != nil {
		panic(fmt.Sprintf("manifest: build devcontainer json: %v", err))
	}
	return string(out)
}

func copyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func defaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func defaultInt(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}
