package access

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// RecordPresetSelectionIn records informational intent only after canonical
// grant replacement and its audit in the caller's transaction. It never commits.
// The expected membership is the post-replacement state, not a client revision.
func (s Store) RecordPresetSelectionIn(ctx context.Context, tx *sql.Tx, actor, user, workspace string, expected Membership, req PresetRequest) error {
	if tx == nil {
		return ErrDenied
	}
	policy, err := s.PolicyIn(ctx, tx, actor, user, workspace)
	if err != nil {
		return err
	}
	if policy.Membership != expected || req.MembershipID != policy.ID {
		return ErrConflict
	}
	definition, known := ResolvePreset(req.PresetID, req.Version)
	operations, levelKnown := definition.Operations(req.Level)
	if !known || !levelKnown || definition.Kind != req.Resource.Kind || req.Resource.ID == "" || policy.Mode != "restricted" || len(operations) == 0 {
		return ErrPresetInvalid
	}
	if err := resourceExists(ctx, tx, workspace, Right{req.Resource.Kind, req.Resource.ID, operations[0]}); err != nil {
		return err
	}
	actual := []string{}
	for _, right := range policy.Rights {
		if right.Kind == req.Resource.Kind && right.ID == req.Resource.ID && slices.Contains(definition.ManagedOperations, right.Operation) {
			actual = append(actual, right.Operation)
		}
	}
	slices.Sort(actual)
	slices.Sort(operations)
	if !slices.Equal(actual, operations) {
		return ErrPresetInvalid
	}
	managed, err := json.Marshal(definition.ManagedOperations)
	if err != nil {
		return err
	}
	selectedAt := tsformat.Format(time.Now())
	result, err := tx.ExecContext(ctx, `UPDATE access_preset_selections SET preset_id=?,preset_version=?,level=?,definition_hash=?,managed_operations=?,selected_by=?,selected_at=? WHERE workspace_id=? AND member_id=? AND resource_kind=? AND COALESCE(project_id,routine_id)=?`, definition.ID, definition.Version, req.Level, definition.Hash(), string(managed), actor, selectedAt, workspace, policy.ID, req.Resource.Kind, req.Resource.ID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 0 {
		return nil
	}
	var project, routine any
	if req.Resource.Kind == "project" {
		project = req.Resource.ID
	} else {
		routine = req.Resource.ID
	}
	result, err = tx.ExecContext(ctx, `INSERT INTO access_preset_selections(id,workspace_id,member_id,resource_kind,project_id,routine_id,preset_id,preset_version,level,definition_hash,managed_operations,selected_by,selected_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, randomID(), workspace, policy.ID, req.Resource.Kind, project, routine, definition.ID, definition.Version, req.Level, definition.Hash(), string(managed), actor, selectedAt)
	if err != nil {
		return err
	}
	changed, err = result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrConflict
	}
	return nil
}
