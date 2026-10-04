package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/managedlaunch"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/toolchain"
)

func managedLaunchResolver(db *sql.DB) orchestrator.ManagedLaunchResolver {
	return func(ctx context.Context, workspace, crew, adapter string) (*managedlaunch.Descriptor, error) {
		denied := errors.New("managed launch: qualified exact pin and current image revision required")
		cli, ok := devcontainer.AdapterCLIFor(adapter)
		if !ok || (cli.Binary != "claude" && cli.Binary != "codex") {
			return nil, denied
		}
		var miseRaw, requirements, buildHash, runtime string
		var cfg sql.NullString
		// Scope must be captured by dispatch. Empty workspace cannot look up
		// authority from a different tenant, even if a crew ID is known.
		if err := db.QueryRowContext(ctx, `SELECT COALESCE(mise_config,''),COALESCE(cached_requirements,''),COALESCE(config_hash,''),COALESCE(runtime_image,''),devcontainer_config FROM crews WHERE workspace_id=? AND id=? AND deleted_at IS NULL`, workspace, crew).Scan(&miseRaw, &requirements, &buildHash, &runtime, &cfg); err != nil {
			return nil, denied
		}
		rows, err := db.QueryContext(ctx, `SELECT DISTINCT cli_adapter FROM agents WHERE crew_id=? AND deleted_at IS NULL AND cli_adapter IS NOT NULL AND cli_adapter != '' ORDER BY cli_adapter`, crew)
		if err != nil {
			return nil, denied
		}
		adapters := []string{}
		for rows.Next() {
			var value string
			if rows.Scan(&value) != nil {
				rows.Close()
				return nil, denied
			}
			adapters = append(adapters, value)
		}
		rowsErr := rows.Err()
		rows.Close()
		if rowsErr != nil {
			return nil, denied
		}
		definitionHash := api.EnvironmentDefinitionHash(database.EffectiveCrewDevcontainerConfig(cfg.String, cfg.Valid), miseRaw, runtime, adapters)
		mise, err := devcontainer.ParseMiseConfig(miseRaw)
		if err != nil || mise.Lock == nil || mise.Validate() != nil {
			return nil, denied
		}
		requested, err := devcontainer.RequestedToolchain(nil, miseRaw, []string{adapter})
		if err != nil || len(requested) != 1 || !requested[0].Exact {
			return nil, denied
		}
		locked, err := devcontainer.LockedToolVersion(mise.Lock, cli.MiseTool)
		if err != nil || locked != strings.TrimPrefix(requested[0].Selector, "v") {
			return nil, denied
		}
		var reqs devcontainer.AggregatedRequirements
		if json.Unmarshal([]byte(requirements), &reqs) != nil || reqs.Toolchain == nil {
			return nil, denied
		}
		inventory := reqs.Toolchain
		if toolchain.RequirePassing(inventory.ImageID, inventory, []string{cli.Binary}) != nil || toolchain.RequireExactPins(inventory, requested) != nil {
			return nil, denied
		}
		var revisionID, revisionRaw string
		if db.QueryRowContext(ctx, `SELECT id,toolchain_json FROM environment_revisions WHERE workspace_id=? AND crew_id=? AND image_id=? AND build_hash=? AND definition_hash=? ORDER BY created_at DESC,id DESC LIMIT 1`, workspace, crew, inventory.ImageID, buildHash, definitionHash).Scan(&revisionID, &revisionRaw) != nil {
			return nil, denied
		}
		// Use immutable revision evidence, not mutable cached metadata.
		var revision devcontainer.ToolchainInventory
		if json.Unmarshal([]byte(revisionRaw), &revision) != nil || toolchain.RequirePassing(inventory.ImageID, &revision, []string{cli.Binary}) != nil || toolchain.RequireExactPins(&revision, requested) != nil {
			return nil, denied
		}
		lockRaw, _ := json.Marshal(mise.Lock)
		sum := sha256.Sum256(lockRaw)
		for _, tool := range revision.Tools {
			if tool.Binary != cli.Binary {
				continue
			}
			if tool.LaunchArtifact == nil || tool.LaunchArtifact.Path != tool.Path {
				return nil, denied
			}
			d := &managedlaunch.Descriptor{Artifact: *tool.LaunchArtifact, ImageID: inventory.ImageID, RevisionID: revisionID, LockSHA256: hex.EncodeToString(sum[:]), Binary: cli.Binary, Version: locked}
			if d.Validate() != nil {
				return nil, denied
			}
			return d, nil
		}
		return nil, denied
	}
}
