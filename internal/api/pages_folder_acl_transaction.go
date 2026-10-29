package api

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/crewship-ai/crewship/internal/access"
)

// Reload the existing human/owning-crew policy through the mutation's writer.
func (h *PageHandler) folderACLMutationIn(ctx context.Context, tx *sql.Tx, workspace, user, id string) (*pageFolderRecord, []pageFolderACLRecord, error) {
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	role, err := h.liveContentHumanRole(ctx, workspace, user)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, access.ErrDenied
	}
	if err != nil {
		return nil, nil, err
	}
	viewer, err := h.loadViewer(ctx, workspace, user)
	if err != nil {
		return nil, nil, err
	}
	viewer.Role = role
	folder, err := scanPageFolder(tx.QueryRowContext(ctx, pageFolderSelect+` WHERE f.id=? AND f.workspace_id=? AND c.workspace_id=f.workspace_id AND c.deleted_at IS NULL`, id, workspace))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, access.ErrDenied
	}
	if err != nil {
		return nil, nil, err
	}
	if !mayAdministerFolder(viewer, folder) {
		return nil, nil, access.ErrDenied
	}
	acl, err := h.loadFolderACL(ctx, id)
	return folder, acl, err
}

func folderACLAfter(before []pageFolderACLRecord, entry pageFolderACLRecord, remove bool) []pageFolderACLRecord {
	out := make([]pageFolderACLRecord, 0, len(before)+1)
	for _, old := range before {
		if old.SubjectType != entry.SubjectType || old.SubjectID != entry.SubjectID {
			out = append(out, old)
		}
	}
	if !remove {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SubjectType != out[j].SubjectType {
			return out[i].SubjectType < out[j].SubjectType
		}
		return out[i].SubjectID < out[j].SubjectID
	})
	return out
}

func (h *PageHandler) acknowledgeFolderACLIn(ctx context.Context, tx *sql.Tx, folder *pageFolderRecord, expected []pageFolderACLRecord) error {
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	var owner, updated string
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT owner_crew_id,acl_version,updated_at FROM page_folders WHERE id=?`, folder.ID).Scan(&owner, &version, &updated); err != nil {
		return err
	}
	if owner != folder.OwnerCrewID || version != folder.ACLVersion || updated != folder.UpdatedAt {
		return errors.New("folder ACL fence not acknowledged")
	}
	actual, err := h.loadFolderACL(ctx, folder.ID)
	if err != nil {
		return err
	}
	if len(actual) != len(expected) {
		return errors.New("folder ACL set not acknowledged")
	}
	for i := range actual {
		if actual[i] != expected[i] {
			return errors.New("folder ACL row not acknowledged")
		}
	}
	return nil
}

// Canonical audit snapshots are removed by PublicAuthorizationAudit at every
// public feed/automation boundary. Keep identity and setter provenance here.
func folderACLAuditSnapshot(rows []pageFolderACLRecord) []map[string]any {
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		result = append(result, map[string]any{
			"subject_type": row.SubjectType, "subject_id": row.SubjectID,
			"can_read": true, "can_write": row.CanWrite, "set_by_user_id": row.SetBy, "set_at": row.SetAt,
		})
	}
	return result
}

type folderACLAuditChange struct {
	Before, After []pageFolderACLRecord
	Version       int64
}
