package resourcelifecycle

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/serviceconfig"
)

// ImageReference distinguishes an exact selected environment from an optional
// base or future build alias. Missing outputs must never reject a cache miss.
type ImageReference struct {
	Ref        string
	Required   bool
	BuildInput bool
	// ConfiguredBaseOnly means every occurrence is a configured runtime or devcontainer base.
	// A service or optional rollback selection takes precedence over this provenance.
	ConfiguredBaseOnly bool
}

type ImageReferenceProtector interface {
	Preserve(context.Context, []ImageReference) (map[string]string, error)
}

// SetReferenceProtector is wired before serving, including live restore routes.
// Runtimes without Docker image collection leave protection unsupported.
func (g *ImageUse) SetReferenceProtector(p ImageReferenceProtector) {
	g.protectorMu.Lock()
	defer g.protectorMu.Unlock()
	g.protector = p
}

func (g *ImageUse) ReferenceProtectionEnabled() bool {
	g.protectorMu.RLock()
	defer g.protectorMu.RUnlock()
	return g.protector != nil
}

func (g *ImageUse) Preserve(ctx context.Context, refs []ImageReference) (map[string]string, error) {
	g.protectorMu.RLock()
	p := g.protector
	g.protectorMu.RUnlock()
	if p == nil {
		// Pure database/offline restore does not execute environments. Admission
		// is deferred until a server with its runtime starts and enrolls them.
		return nil, nil
	}
	return p.Preserve(ctx, refs)
}

type ImageSelectionQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// SelectedImageReferences reads transaction-local selections during restore,
// or committed selections during startup/reconciliation. Previous images and
// configured bases are retained when present; current environments are exact.
func SelectedImageReferences(ctx context.Context, q ImageSelectionQuery, crewIDs ...string) ([]ImageReference, error) {
	return SelectedImageReferencesAt(ctx, q, time.Now(), crewIDs...)
}

// SelectedImageReferencesAt evaluates tombstones at the collection pass time.
func SelectedImageReferencesAt(ctx context.Context, q ImageSelectionQuery, now time.Time, crewIDs ...string) ([]ImageReference, error) {
	query := `SELECT COALESCE(cached_image,''),COALESCE(last_selected_image,''),COALESCE(previous_image,''),COALESCE(runtime_image,''),COALESCE(devcontainer_config,''),COALESCE(deleted_at,''),COALESCE(services_json,'') FROM crews`
	var args []any
	if len(crewIDs) > 0 {
		query += ` WHERE id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(crewIDs)), ",") + `)`
		for _, id := range crewIDs {
			args = append(args, id)
		}
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	wanted := map[string]ImageReference{}
	add := func(ref ImageReference) {
		if ref.Ref == "" {
			return
		}
		if existing, ok := wanted[ref.Ref]; ok {
			ref.Required = existing.Required || ref.Required
			ref.ConfiguredBaseOnly = existing.ConfiguredBaseOnly && ref.ConfiguredBaseOnly
		}
		wanted[ref.Ref] = ref
	}
	for rows.Next() {
		var current, last, previous, base, config, deleted, services string
		if err := rows.Scan(&current, &last, &previous, &base, &config, &deleted, &services); err != nil {
			return nil, err
		}
		if deleted != "" {
			at, err := time.Parse(time.RFC3339Nano, deleted) // tsformat:allow: parse persisted deletion timestamp, never emit one
			if err == nil && now.Sub(at) > 7*24*time.Hour {
				continue
			}
		}
		add(ImageReference{Ref: current, Required: true})
		add(ImageReference{Ref: last})
		add(ImageReference{Ref: previous})
		inputs, err := ConfiguredImageReferences(base, config, services)
		if err != nil {
			return nil, err
		}
		for _, input := range inputs {
			add(input)
		}

	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	refs := make([]ImageReference, 0, len(wanted))
	for _, ref := range wanted {
		refs = append(refs, ref)
	}
	return refs, nil
}

// ConfiguredImageReferences includes sidecar images even before any runtime
// exists. Unreadable configuration is incomplete evidence, never permission to
// release references that could belong to it.
func ConfiguredImageReferences(base, config, services string) ([]ImageReference, error) {
	refs := []ImageReference{{Ref: base, ConfiguredBaseOnly: true}}
	if config != "" {
		cfg, err := devcontainer.ParseBytes([]byte(config))
		if err != nil {
			return nil, fmt.Errorf("environment references unavailable: unreadable crew configuration: %w", err)
		}
		refs = append(refs, ImageReference{Ref: cfg.Image, ConfiguredBaseOnly: true})
	}
	if strings.TrimSpace(services) != "" {
		plain, err := serviceconfig.Open(services)
		if err != nil {
			return nil, fmt.Errorf("environment references unavailable: unreadable services")
		}
		var declarations []struct {
			Image string `json:"image"`
		}
		if err := json.Unmarshal([]byte(plain), &declarations); err != nil {
			return nil, fmt.Errorf("environment references unavailable: invalid services")
		}
		for _, service := range declarations {
			refs = append(refs, ImageReference{Ref: service.Image})
		}
	}
	return refs, nil
}

// Acquire protects resolution until immutable-ID leases exist, then returns a
// scoped lease the consumer holds through its actual container creation.
func (g *ImageUse) Acquire(ctx context.Context, refs []ImageReference) (map[string]string, func(), error) {
	releaseSelection := g.Use()
	aliases := make([]string, 0, len(refs))
	for _, ref := range refs {
		aliases = append(aliases, ref.Ref)
	}
	releaseAliases, err := g.UseImages(ctx, aliases...)
	if err != nil {
		releaseSelection()
		return nil, nil, err
	}
	resolved, err := g.Preserve(ctx, refs)
	if err != nil {
		releaseAliases()
		releaseSelection()
		return nil, nil, err
	}
	ids := make([]string, 0, len(resolved))
	for _, id := range resolved {
		ids = append(ids, id)
	}
	releaseIDs, err := g.UseImages(ctx, ids...)
	releaseSelection()
	if err != nil {
		releaseAliases()
		return nil, nil, err
	}
	return resolved, func() { releaseIDs(); releaseAliases() }, nil
}
