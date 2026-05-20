// Package kinds contains the per-kind implementations for the
// declarative manifest system. Each file in this package owns ONE
// manifest kind end-to-end: the YAML shape (`*Spec` + `*Document`),
// validation, the remote-state shape used during planning
// (`*Remote`), the diff/plan logic, and the export-back-to-YAML
// helper. The top-level `internal/manifest` package wires these
// kinds together (dispatch, ordering, apply loop) and supplies the
// `Client` interface they call through.
package kinds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// ----- YAML schema types ---------------------------------------------------

// FeatureFlagSpec is the body of a `kind: FeatureFlag` document.
//
// A FeatureFlag has TWO distinct concerns that share one manifest entry:
//
//  1. The instance-global flag DEFINITION — `description`,
//     `default_enabled`, `default_percentage`. This row lives in the
//     `feature_flags` table and is keyed by `metadata.slug` (which the
//     server stores as the `key` column).
//
//  2. The per-workspace OVERRIDE — `workspace_override`. Optional and
//     pointer-typed so we can distinguish "field omitted" (no override
//     desired) from "field set to false" (force-disabled for this
//     workspace). When absent, the workspace inherits the instance
//     default; when present, an override row is written for the
//     current workspace.
//
// One Plan() call can therefore emit 0, 1, or 2 PlanItems: the flag
// definition + the override are diffed independently.
type FeatureFlagSpec struct {
	Description       string `yaml:"description,omitempty"        json:"description,omitempty"`
	DefaultEnabled    bool   `yaml:"default_enabled"              json:"default_enabled"`
	DefaultPercentage int    `yaml:"default_percentage"           json:"default_percentage"`

	// WorkspaceOverride is *bool so we can tell an unset field
	// (inherit the instance default) from an explicit `false`
	// (force the flag OFF for this workspace). Marshalling with
	// `omitempty` ensures Export only emits the key when an
	// override row actually exists server-side.
	WorkspaceOverride *bool `yaml:"workspace_override,omitempty" json:"workspace_override,omitempty"`
}

// FeatureFlagDocument is the full top-level shape parsed out of YAML.
// `metadata.slug` is the idempotency key and maps to the server's
// `feature_flags.key` column.
type FeatureFlagDocument struct {
	APIVersion string               `yaml:"apiVersion" json:"apiVersion"`
	Kind       string               `yaml:"kind"       json:"kind"`
	Metadata   internalapi.Metadata `yaml:"metadata"   json:"metadata"`
	Spec       FeatureFlagSpec      `yaml:"spec"       json:"spec"`
}

// FeatureFlagRemote captures the relevant fields of a server-side
// flag as returned by `GET /api/v1/feature-flags`. The list endpoint
// returns one entry per flag with the current workspace's override
// already resolved into `WorkspaceOverride` (nil pointer = no
// override row exists). Plan() takes a *FeatureFlagRemote so a nil
// pointer cleanly represents "flag does not exist on the server".
type FeatureFlagRemote struct {
	Key               string `json:"key"`
	Description       string `json:"description"`
	DefaultEnabled    bool   `json:"default_enabled"`
	DefaultPercentage int    `json:"default_percentage"`
	WorkspaceOverride *bool  `json:"workspace_override,omitempty"`
}

// ----- Validate ------------------------------------------------------------

// Validate enforces structural rules that don't require talking to
// the server. The percentage range is the only constraint here;
// description and the override field are free-form.
//
// workspaceCtx is accepted to keep the per-kind Validate signature
// uniform across the codebase even though FeatureFlag has no
// cross-kind FK references.
func (d *FeatureFlagDocument) Validate(_ internalapi.WorkspaceContext) error {
	if d.Metadata.Slug == "" {
		return fmt.Errorf("FeatureFlag %q: metadata.slug is required (used as flag key)", d.Metadata.Name)
	}
	if d.Spec.DefaultPercentage < 0 || d.Spec.DefaultPercentage > 100 {
		return fmt.Errorf("FeatureFlag %q: default_percentage must be in [0,100], got %d",
			d.Metadata.Slug, d.Spec.DefaultPercentage)
	}
	return nil
}

// ----- Plan ----------------------------------------------------------------

// Plan compares the declared FeatureFlagDocument against the remote
// state and returns the plan items required to reconcile them.
//
// The two concerns are diffed independently:
//
//   - Definition (description / default_enabled): if the flag is
//     missing remotely → 1× POST create; if present but drifted →
//     1× PATCH update; otherwise no item.
//   - Workspace override: if the declared spec omits the override
//     and a remote override row exists → 1× DELETE override; if the
//     declared override differs from the remote value → 1× PUT
//     override; otherwise no item.
//
// Net result: 0, 1, or 2 PlanItems per flag. When the flag is being
// created AND the manifest sets an override, the POST runs first and
// the override PUT runs second (declaration order matters because
// the override endpoint requires the flag to already exist).
//
// default_percentage drift is intentionally NOT included in the
// definition diff: percentage rollouts are typically tuned at
// runtime by ops, and re-applying a manifest that hard-codes the
// original 0% would constantly fight the live system. We keep the
// field in the document for create-time bootstrap and for export
// round-tripping, but skip it on update.
func (d *FeatureFlagDocument) Plan(_ context.Context, c internalapi.Client, remote *FeatureFlagRemote) ([]internalapi.PlanItem, error) {
	if c == nil {
		// Defensive: every kind's Plan is contractually called with a
		// non-nil client. Returning an error here turns a nil-deref
		// panic at exec time into a structured plan failure.
		return nil, fmt.Errorf("FeatureFlag %q: nil client passed to Plan", d.Metadata.Slug)
	}

	var items []internalapi.PlanItem
	key := d.Metadata.Slug

	// ----- definition diff -------------------------------------------------
	if remote == nil {
		// Flag doesn't exist: emit a single POST that creates the
		// definition. The override (if any) is emitted as a second
		// item below; both run in declaration order, so the
		// definition lands first.
		body := map[string]any{
			"key":                key,
			"description":        d.Spec.Description,
			"default_enabled":    d.Spec.DefaultEnabled,
			"default_percentage": d.Spec.DefaultPercentage,
		}
		items = append(items, internalapi.PlanItem{
			Kind:        "feature_flag",
			Slug:        key,
			Action:      internalapi.ActionCreate,
			Description: fmt.Sprintf("create feature flag %q", key),
			Exec: func(ctx context.Context, c internalapi.Client) error {
				resp, err := c.Post(ctx, "/api/v1/feature-flags", body)
				return ffCheckStatus(resp, err, http.StatusCreated, http.StatusOK)
			},
		})
	} else if definitionDrifted(d.Spec, *remote) {
		// Flag exists but description or default_enabled differ.
		// Skip default_percentage on purpose (see Plan doc-comment).
		body := map[string]any{
			"description":     d.Spec.Description,
			"default_enabled": d.Spec.DefaultEnabled,
		}
		items = append(items, internalapi.PlanItem{
			Kind:        "feature_flag",
			Slug:        key,
			Action:      internalapi.ActionUpdate,
			Description: fmt.Sprintf("update feature flag %q definition", key),
			Exec: func(ctx context.Context, c internalapi.Client) error {
				resp, err := c.Patch(ctx, "/api/v1/feature-flags/"+key, body)
				return ffCheckStatus(resp, err, http.StatusOK)
			},
		})
	}

	// ----- override diff ---------------------------------------------------
	overrideItem := planOverrideDiff(key, d.Spec.WorkspaceOverride, remote)
	if overrideItem != nil {
		items = append(items, *overrideItem)
	}

	return items, nil
}

// definitionDrifted returns true when the user-meaningful definition
// fields disagree with the remote row. default_percentage is
// deliberately excluded (see Plan doc-comment).
func definitionDrifted(spec FeatureFlagSpec, remote FeatureFlagRemote) bool {
	return spec.Description != remote.Description ||
		spec.DefaultEnabled != remote.DefaultEnabled
}

// planOverrideDiff returns a single PlanItem when the workspace
// override needs to converge, or nil when no work is needed.
//
// The full truth table:
//
//	declared=nil, remote=nil  → no-op (inherit; nothing on server)
//	declared=nil, remote=set  → DELETE override (clear stale override)
//	declared=set, remote=nil  → PUT override
//	declared=set, remote=set, same → no-op
//	declared=set, remote=set, diff → PUT override (upsert)
//
// remoteFlag may itself be nil when the flag doesn't exist yet; in
// that case the remote override is treated as absent too.
func planOverrideDiff(key string, declared *bool, remoteFlag *FeatureFlagRemote) *internalapi.PlanItem {
	var remoteOverride *bool
	if remoteFlag != nil {
		remoteOverride = remoteFlag.WorkspaceOverride
	}

	// declared absent
	if declared == nil {
		if remoteOverride == nil {
			return nil // nothing on server, nothing declared → no-op
		}
		// Stale override on server: clear it so the workspace
		// inherits the instance default again.
		return &internalapi.PlanItem{
			Kind:        "feature_flag",
			Slug:        key,
			Action:      internalapi.ActionDelete,
			Description: fmt.Sprintf("remove workspace override for feature flag %q", key),
			Exec: func(ctx context.Context, c internalapi.Client) error {
				resp, err := c.Delete(ctx, "/api/v1/feature-flags/"+key+"/override")
				return ffCheckStatus(resp, err, http.StatusOK, http.StatusNoContent)
			},
		}
	}

	// declared set
	if remoteOverride != nil && *remoteOverride == *declared {
		return nil // already at the declared value
	}
	val := *declared
	return &internalapi.PlanItem{
		Kind:        "feature_flag",
		Slug:        key,
		Action:      internalapi.ActionUpdate,
		Description: fmt.Sprintf("set workspace override for feature flag %q to %t", key, val),
		Exec: func(ctx context.Context, c internalapi.Client) error {
			body := map[string]any{"enabled": val}
			resp, err := c.Put(ctx, "/api/v1/feature-flags/"+key+"/override", body)
			return ffCheckStatus(resp, err, http.StatusOK, http.StatusCreated, http.StatusNoContent)
		},
	}
}

// ----- Export --------------------------------------------------------------

// ExportFeatureFlags converts the server's current feature-flag
// state back into a slice of FeatureFlagDocuments suitable for
// emitting as YAML. One document per flag definition; the
// `workspace_override` field is set only when the server reports a
// real override row for the current workspace (nil pointer ==
// inherit instance default).
//
// The function performs a single GET; it does not page because the
// flag set is small (instance-global) and the existing API returns
// the full list in one call.
func ExportFeatureFlags(ctx context.Context, c internalapi.Client) ([]*FeatureFlagDocument, error) {
	if c == nil {
		return nil, fmt.Errorf("ExportFeatureFlags: nil client")
	}
	resp, err := c.Get(ctx, "/api/v1/feature-flags")
	if err != nil {
		return nil, fmt.Errorf("ExportFeatureFlags: GET: %w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("ExportFeatureFlags: nil response")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ExportFeatureFlags: unexpected status %d", resp.StatusCode)
	}

	data, err := ffReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ExportFeatureFlags: read body: %w", err)
	}

	// The list endpoint may return either a bare array (preferred) or
	// a `{"items": [...]}` envelope (some handlers in this codebase
	// use that shape). Try the array form first, then fall back.
	var arr []FeatureFlagRemote
	if jerr := json.Unmarshal(data, &arr); jerr != nil {
		var env struct {
			Items []FeatureFlagRemote `json:"items"`
		}
		if eerr := json.Unmarshal(data, &env); eerr != nil {
			return nil, fmt.Errorf("ExportFeatureFlags: decode body: %w", jerr)
		}
		arr = env.Items
	}

	out := make([]*FeatureFlagDocument, 0, len(arr))
	for _, row := range arr {
		doc := &FeatureFlagDocument{
			APIVersion: "crewship/v1",
			Kind:       "FeatureFlag",
			Metadata: internalapi.Metadata{
				Name: row.Key,
				Slug: row.Key,
			},
			Spec: FeatureFlagSpec{
				Description:       row.Description,
				DefaultEnabled:    row.DefaultEnabled,
				DefaultPercentage: row.DefaultPercentage,
			},
		}
		// Copy the override pointer only when the server actually
		// reported one. Aliasing the loop variable directly would
		// leak the iteration pointer; allocate a fresh bool instead.
		if row.WorkspaceOverride != nil {
			v := *row.WorkspaceOverride
			doc.Spec.WorkspaceOverride = &v
		}
		out = append(out, doc)
	}
	return out, nil
}

// ----- helpers -------------------------------------------------------------
//
// These helpers are deliberately namespaced with an `ff` prefix to
// avoid colliding with same-named helpers other kinds in this
// package may have introduced — multiple parallel agents own files
// in `internal/manifest/kinds/` and any unprefixed `checkStatus` /
// `readAll` would clash at link time.

// ffCheckStatus folds a (response, err) pair plus a list of accepted
// status codes into a single error. Used by every Exec closure so
// the per-item happy path is just `return ffCheckStatus(...)`.
func ffCheckStatus(resp *internalapi.Response, err error, accepted ...int) error {
	if err != nil {
		return err
	}
	if resp == nil {
		return fmt.Errorf("nil response")
	}
	for _, want := range accepted {
		if resp.StatusCode == want {
			return nil
		}
	}
	return fmt.Errorf("unexpected status %d", resp.StatusCode)
}

// ffReadAll drains a response body, tolerating a nil reader (some
// fakes return Response{Body: nil} for 204s).
func ffReadAll(r io.Reader) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	return io.ReadAll(r)
}
