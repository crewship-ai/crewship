package access

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"slices"
)

// Presets are immutable explicit bundles, never operation inheritance.
// Append a new version rather than changing shipped definitions.
type PresetDefinition struct {
	ID                string        `json:"preset_id"`
	Version           int           `json:"version"`
	Kind              string        `json:"kind"`
	ManagedOperations []string      `json:"managed_operations"`
	Levels            []PresetLevel `json:"levels"`
}
type PresetLevel struct {
	Level      string   `json:"level"`
	Operations []string `json:"operations"`
}

// Fresh slices prevent consumers from mutating the canonical catalog.
func PresetCatalog() []PresetDefinition {
	return []PresetDefinition{
		{"routine-levels", 1, "routine", []string{"view", "results", "run", "edit"}, []PresetLevel{
			{"view", []string{"view", "results"}}, {"run", []string{"view", "results", "run"}}, {"edit", []string{"view", "results", "run", "edit"}},
		}},
		{"project-levels", 1, "project", []string{"list", "read", "contribute", "manage"}, []PresetLevel{
			{"view", []string{"list", "read"}}, {"contribute", []string{"list", "read", "contribute"}}, {"manage", []string{"list", "read", "contribute", "manage"}},
		}},
	}
}
func ResolvePreset(id string, version int) (PresetDefinition, bool) {
	for _, definition := range PresetCatalog() {
		if definition.ID == id && definition.Version == version {
			return definition, true
		}
	}
	return PresetDefinition{}, false
}
func (d PresetDefinition) Hash() string {
	encoded, _ := json.Marshal(d) // Definition contains only serializable values.
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
func (d PresetDefinition) Operations(level string) ([]string, bool) {
	for _, candidate := range d.Levels {
		if candidate.Level == level {
			return slices.Clone(candidate.Operations), true
		}
	}
	return nil, false
}

// Selection records saved intent, not live/effective authority. Inactive grant
// issuers remain represented by Policy.GrantDetails; selection never reissues.
type PresetSelection struct {
	ID             string  `json:"id" yaml:"id"`
	Kind           string  `json:"kind" yaml:"kind"`
	ResourceID     string  `json:"resource_id" yaml:"resource_id"`
	PresetID       string  `json:"preset_id" yaml:"preset_id"`
	Version        int     `json:"version" yaml:"version"`
	Level          string  `json:"level" yaml:"level"`
	DefinitionHash string  `json:"definition_hash" yaml:"definition_hash"`
	SelectedBy     *string `json:"selected_by,omitempty" yaml:"selected_by,omitempty"`
	SelectedAt     string  `json:"selected_at" yaml:"selected_at"`
	State          string  `json:"state" yaml:"state"` // selected saved intent, or custom/unsupported metadata
}

// PresetSelectionsIn is administrative, inside the caller's policy snapshot.
// No public application endpoint is enabled by this foundation.
func (s Store) PresetSelectionsIn(ctx context.Context, tx *sql.Tx, actor, user, workspace string) ([]PresetSelection, error) {
	if tx == nil {
		return nil, ErrDenied
	}
	policy, err := s.PolicyIn(ctx, tx, actor, user, workspace)
	if err != nil {
		return nil, err
	}
	return *policy.PresetSelections, nil
}

// Caller has already checked live administrative authority and read canonical
// grants in this same transaction. This helper cannot recursively read Policy.
func presetSelectionsForPolicyIn(ctx context.Context, tx *sql.Tx, policy Policy, workspace string) ([]PresetSelection, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,resource_kind,COALESCE(project_id,routine_id),preset_id,preset_version,level,definition_hash,managed_operations,selected_by,selected_at FROM access_preset_selections WHERE workspace_id=? AND member_id=? ORDER BY resource_kind,COALESCE(project_id,routine_id)`, workspace, policy.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []PresetSelection{}
	for rows.Next() {
		var selection PresetSelection
		var raw string
		var actorID sql.NullString
		if err := rows.Scan(&selection.ID, &selection.Kind, &selection.ResourceID, &selection.PresetID, &selection.Version, &selection.Level, &selection.DefinitionHash, &raw, &actorID, &selection.SelectedAt); err != nil {
			return nil, err
		}
		if actorID.Valid {
			selection.SelectedBy = &actorID.String
		}
		definition, known := ResolvePreset(selection.PresetID, selection.Version)
		operations, levelKnown := definition.Operations(selection.Level)
		var managed []string
		selection.State = "custom"
		if known && levelKnown && definition.Kind == selection.Kind && definition.Hash() == selection.DefinitionHash && json.Unmarshal([]byte(raw), &managed) == nil && slices.Equal(managed, definition.ManagedOperations) && policy.Mode == "restricted" {
			actual := []string{}
			for _, right := range policy.Rights {
				if right.Kind == selection.Kind && right.ID == selection.ResourceID && slices.Contains(managed, right.Operation) {
					actual = append(actual, right.Operation)
				}
			}
			slices.Sort(actual)
			slices.Sort(operations)
			if slices.Equal(actual, operations) {
				selection.State = "selected"
			}
		}
		result = append(result, selection)
	}
	return result, rows.Err()
}
