package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/devcontainer"
)

// The crew image must be able to run every agent in the crew. Provisioning
// derives the adapter CLIs from the agents it finds (crewAgentAdapters →
// devcontainer.EnsureAdapterCLIs), and the agent handlers close the other
// direction: an agent created on, or moved to, an adapter the crew's cached
// image was not verified for triggers a rebuild — the chat then waits for
// the build (chatbridge pending messages) instead of answering "No such file
// or directory".

// agentProvisionEnqueuer is the one method the agent handlers need from the
// provisioning handler. An interface so the handler stays testable.
type agentProvisionEnqueuer interface {
	EnqueueForCrew(ctx context.Context, crewID, workspaceID string) (EnqueueResult, error)
}

// SetProvisioner wires the rebuild-on-adapter-change hook.
func (h *AgentHandler) SetProvisioner(p agentProvisionEnqueuer) { h.provisioner = p }

// crewAgentAdapters returns the distinct cli_adapter values of a crew's live
// agents, the input of devcontainer.RequiredAdapterCLIs.
func crewAgentAdapters(ctx context.Context, db *sql.DB, crewID string) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT DISTINCT cli_adapter FROM agents
		 WHERE crew_id = ? AND deleted_at IS NULL AND cli_adapter IS NOT NULL AND cli_adapter != ''
		 ORDER BY cli_adapter`, crewID)
	if err != nil {
		return nil, fmt.Errorf("query crew adapters: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, fmt.Errorf("scan crew adapter: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// crewImageCoversAdapter reports whether the crew's cached image was verified
// to run the adapter's CLI. built is false when the crew has no cached image
// yet — the first build reads the agents itself, so there is nothing to do.
// An image built before adapter verification existed records no binaries and
// therefore never covers anything: one rebuild brings it under the guarantee.
func crewImageCoversAdapter(ctx context.Context, db *sql.DB, crewID, workspaceID, adapter string) (built, covered bool, err error) {
	if _, ok := devcontainer.AdapterCLIFor(adapter); !ok {
		return false, true, nil // unknown adapter: validated elsewhere, nothing to install
	}
	var cachedImage, reqJSON sql.NullString
	err = db.QueryRowContext(ctx,
		`SELECT cached_image, cached_requirements FROM crews
		 WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		crewID, workspaceID).Scan(&cachedImage, &reqJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return false, true, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("query crew image: %w", err)
	}
	if !cachedImage.Valid || cachedImage.String == "" {
		return false, false, nil
	}
	return true, devcontainer.ImageCoversAdapter(parseCachedRequirements(reqJSON), adapter), nil
}

// parseCachedRequirements decodes the crews.cached_requirements column; nil
// when absent or unreadable — which ImageCoversAdapter reads as "verified for
// nothing", so a rebuild rewrites it.
func parseCachedRequirements(reqJSON sql.NullString) *devcontainer.AggregatedRequirements {
	if !reqJSON.Valid || strings.TrimSpace(reqJSON.String) == "" {
		return nil
	}
	var req devcontainer.AggregatedRequirements
	if err := json.Unmarshal([]byte(reqJSON.String), &req); err != nil {
		return nil
	}
	return &req
}

// crewImageReady is the dispatch-time truth about a crew's image: whether a
// build is needed at all (config customizations, or agents whose adapter CLI
// must be installed), whether the cached image is present, and whether it
// was verified for every live agent's adapter. It is what EnsureProvisioned
// and the agent-create/update hooks consult, so an agent added by any path —
// wizard, hire, template, onboarding, manifest — is covered before it runs.
func crewImageReady(ctx context.Context, db *sql.DB, crewID, workspaceID string, imagePresent func(string) bool) (needsBuild bool, ready bool, reason string, err error) {
	var devcontainerCfg, miseCfg, cachedImage, reqJSON sql.NullString
	err = db.QueryRowContext(ctx,
		`SELECT devcontainer_config, mise_config, cached_image, cached_requirements
		 FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		crewID, workspaceID).Scan(&devcontainerCfg, &miseCfg, &cachedImage, &reqJSON)
	if err != nil {
		return false, false, "", fmt.Errorf("load crew for provisioning check: %w", err)
	}
	adapters, err := crewAgentAdapters(ctx, db, crewID)
	if err != nil {
		return false, false, "", err
	}
	effectiveCfg := database.EffectiveCrewDevcontainerConfig(devcontainerCfg.String, devcontainerCfg.Valid)
	needsBuild = crewNeedsProvision(effectiveCfg, miseCfg.String) || len(devcontainer.RequiredAdapterCLIs(adapters)) > 0
	if !needsBuild {
		return false, true, "no build needed", nil
	}
	if !cachedImage.Valid || cachedImage.String == "" {
		return true, false, "no image built yet", nil
	}
	if imagePresent != nil && !imagePresent(cachedImage.String) {
		return true, false, "cached image " + cachedImage.String + " is not present locally", nil
	}
	if !devcontainer.ImageCoversAdapters(parseCachedRequirements(reqJSON), adapters) {
		return true, false, "image " + cachedImage.String + " was not verified for the crew's adapter CLIs", nil
	}
	return true, true, "", nil
}

// ensureCrewImageHasAdapter enqueues a rebuild of the crew when its cached
// image is not verified for the adapter. Best-effort and logged: a failure to
// enqueue must not fail the agent write that triggered it — the next chat
// message enqueues the same build through the bridge, and the image is what
// the operator sees on the crew page.
func (h *AgentHandler) ensureCrewImageHasAdapter(ctx context.Context, crewID, workspaceID, adapter string) {
	if h.provisioner == nil || crewID == "" || adapter == "" {
		return
	}
	built, covered, err := crewImageCoversAdapter(ctx, h.db, crewID, workspaceID, adapter)
	if err != nil {
		h.logger.Warn("adapter coverage check failed; not rebuilding", "crew_id", crewID, "adapter", adapter, "error", err)
		return
	}
	if !built || covered {
		return
	}
	cli, _ := devcontainer.AdapterCLIFor(adapter)
	// Detached from the request: the build outlives the response.
	res, err := h.provisioner.EnqueueForCrew(context.Background(), crewID, workspaceID)
	if err != nil {
		h.logger.Warn("rebuild for adapter CLI could not be enqueued", "crew_id", crewID, "adapter", adapter, "binary", cli.Binary, "error", err)
		return
	}
	h.logger.Info("crew image not verified for adapter CLI; rebuild enqueued",
		slog.String("crew_id", crewID), slog.String("adapter", adapter), slog.String("binary", cli.Binary),
		slog.Any("enqueue", res))
}
