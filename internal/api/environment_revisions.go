package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/dockerutil"
	"github.com/crewship-ai/crewship/internal/toolchain"
)

var errBuildDefinitionChanged = errors.New("environment definition changed during build; rebuild the current definition")

type provisionDefinition struct {
	Config, Mise, Runtime string
	Adapters              []string
}

func (d provisionDefinition) hash() string {
	b, _ := json.Marshal(d)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// EnvironmentDefinitionHash shares the immutable revision binding with runtime
// admission; edits to selectors/lock/config cannot reuse old build authority.
func EnvironmentDefinitionHash(cfg, mise, runtime string, adapters []string) string {
	return (provisionDefinition{Config: cfg, Mise: mise, Runtime: runtime, Adapters: adapters}).hash()
}

type EnvironmentRevision struct {
	ID             string                           `json:"id" yaml:"id"`
	DefinitionHash string                           `json:"definition_hash" yaml:"definition_hash"`
	BuildHash      string                           `json:"build_hash" yaml:"build_hash"`
	ImageID        string                           `json:"image_id" yaml:"image_id"`
	Toolchain      *devcontainer.ToolchainInventory `json:"toolchain" yaml:"toolchain"`
	CreatedAt      string                           `json:"created_at" yaml:"created_at"`
}

// saveProvisionResult atomically verifies the definition, records immutable
// build evidence and publishes the cached result. It never changes a running
// container. Raw configuration/env values are deliberately absent from history.
func (h *ProvisioningHandler) saveProvisionResult(ctx context.Context, crewID, workspaceID string, expected provisionDefinition, result *devcontainer.ProvisionResult) (string, error) {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var cfg, mise, runtime sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT devcontainer_config,mise_config,runtime_image FROM crews WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, crewID, workspaceID).Scan(&cfg, &mise, &runtime); err != nil {
		return "", err
	}
	adapters, err := crewAgentAdapters(ctx, tx, crewID)
	if err != nil {
		return "", err
	}
	if database.EffectiveCrewDevcontainerConfig(cfg.String, cfg.Valid) != expected.Config || mise.String != expected.Mise || runtime.String != expected.Runtime || !slices.Equal(adapters, expected.Adapters) {
		return "", errBuildDefinitionChanged
	}
	// Supersession wins over validation of obsolete build evidence. Keep both
	// checks in the publication transaction so a concurrent edit cannot slip
	// between the comparison and the selected image update.
	if expected.Mise != "" {
		mise, err := devcontainer.ParseMiseConfig(expected.Mise)
		if err != nil {
			return "", err
		}
		if err := mise.Validate(); err != nil {
			return "", err
		}
		if mise.AICLICheck == "required" {
			var binaries []string
			for _, cli := range devcontainer.RequiredAdapterCLIs(expected.Adapters) {
				binaries = append(binaries, cli.Binary)
			}
			if err := toolchain.RequirePassing(result.CachedImage, result.Requirements.Toolchain, binaries); err != nil {
				return "", err
			}
			requested, err := devcontainer.RequestedToolchain(nil, expected.Mise, expected.Adapters)
			if err != nil {
				return "", err
			}
			if err := toolchain.RequireExactPins(result.Requirements.Toolchain, requested); err != nil {
				return "", err
			}
		}
	}
	var requirements sql.NullString
	if !isEmptyRequirements(result.Requirements) {
		raw, err := json.Marshal(result.Requirements)
		if err != nil {
			return "", err
		}
		requirements = sql.NullString{String: string(raw), Valid: true}
	}
	var features sql.NullString
	if result.Features != nil {
		raw, err := json.Marshal(result.Features)
		if err != nil {
			return "", err
		}
		features = sql.NullString{String: string(raw), Valid: true}
	}
	revisionID := ""
	inventory := result.Requirements.Toolchain
	imageID := ""
	if dockerutil.IsLocalImageID(result.CachedImage) {
		imageID = result.CachedImage
	} else if inventory != nil {
		imageID = inventory.ImageID
	}
	if imageID != "" {
		raw, err := json.Marshal(inventory)
		if err != nil {
			return "", err
		}
		definitionHash := expected.hash()
		revisionID = generateCUID()
		_, err = tx.ExecContext(ctx, `INSERT INTO environment_revisions(id,workspace_id,crew_id,definition_hash,build_hash,image_id,toolchain_json,created_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(workspace_id,crew_id,definition_hash,image_id) DO NOTHING`, revisionID, workspaceID, crewID, definitionHash, result.ConfigHash, imageID, string(raw), time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z"))
		if err != nil {
			return "", err
		}
		if err = tx.QueryRowContext(ctx, `SELECT id FROM environment_revisions WHERE workspace_id=? AND crew_id=? AND definition_hash=? AND image_id=?`, workspaceID, crewID, definitionHash, imageID).Scan(&revisionID); err != nil {
			return "", err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE crews SET cached_image=?,config_hash=?,cached_requirements=?,resolved_features=?,updated_at=datetime('now') WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, result.CachedImage, result.ConfigHash, requirements, features, crewID, workspaceID)
	if err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return revisionID, nil
}

// EnvironmentRevisions returns bounded build provenance for one live crew in
// the caller's workspace. Image presence/current execution is not inferred.
func (h *ProvisioningHandler) EnvironmentRevisions(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "read") {
		return
	}
	workspaceID, crewID := WorkspaceIDFromContext(r.Context()), r.PathValue("crewId")
	var exists int
	err := h.db.QueryRowContext(r.Context(), `SELECT 1 FROM crews WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, crewID, workspaceID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "crew not found")
		return
	}
	if err != nil {
		replyError(w, http.StatusInternalServerError, "failed to read crew")
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT id,definition_hash,build_hash,image_id,toolchain_json,created_at FROM environment_revisions WHERE workspace_id=? AND crew_id=? ORDER BY created_at DESC,id DESC LIMIT 100`, workspaceID, crewID)
	if err != nil {
		replyError(w, http.StatusInternalServerError, "failed to read environment revisions")
		return
	}
	defer rows.Close()
	revisions := []EnvironmentRevision{}
	for rows.Next() {
		var revision EnvironmentRevision
		var raw string
		if err := rows.Scan(&revision.ID, &revision.DefinitionHash, &revision.BuildHash, &revision.ImageID, &raw, &revision.CreatedAt); err != nil {
			replyError(w, http.StatusInternalServerError, "failed to read environment revision")
			return
		}
		if err := json.Unmarshal([]byte(raw), &revision.Toolchain); err != nil {
			replyError(w, http.StatusInternalServerError, "invalid environment revision evidence")
			return
		}
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		replyError(w, http.StatusInternalServerError, "failed to read environment revisions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revisions": revisions, "limit": 100})
}
