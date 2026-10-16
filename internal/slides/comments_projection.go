package slides

import (
	"context"
	"database/sql"
	"errors"
)

type anchorMembership struct {
	slides   map[string]bool
	elements map[string]string
}

func indexAnchors(deck Deck) anchorMembership {
	index := anchorMembership{slides: map[string]bool{}, elements: map[string]string{}}
	for _, slide := range deck.Slides {
		index.slides[slide.ID] = true
		for _, element := range slide.Elements {
			index.elements[element.ID] = slide.ID
		}
	}
	return index
}
func (index anchorMembership) exists(anchor CommentAnchor) bool {
	return index.slides[anchor.SlideID] && (anchor.Kind == "slide" || index.elements[anchor.ElementID] == anchor.SlideID)
}
func anchorExists(deck Deck, anchor CommentAnchor) bool { return indexAnchors(deck).exists(anchor) }

// Projection fields are derived inside the same deck writer transaction. They
// neither mutate the original anchor nor consume human comment-history versions.
// The Page cap bounds this pass; no all-workspace scan or private body is read.
func updateCommentProjections(ctx context.Context, tx *sql.Tx, key Key, deck Deck, revision int64, kind string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,anchor_schema_version,anchor_kind,slide_id,COALESCE(element_id,''),anchor_revision,status,last_missing_revision,restored_at_revision FROM slides_comment_threads WHERE workspace_id=? AND page_id=? LIMIT ?`, key.WorkspaceID, key.PageID, MaxCommentThreads+1)
	if err != nil {
		return err
	}
	type projection struct {
		id, status        string
		anchor            CommentAnchor
		missing, restored sql.NullInt64
	}
	items := []projection{}
	for rows.Next() {
		var item projection
		if err = rows.Scan(&item.id, &item.anchor.AnchorSchemaVersion, &item.anchor.Kind, &item.anchor.SlideID, &item.anchor.ElementID, &item.anchor.Revision, &item.status, &item.missing, &item.restored); err != nil {
			rows.Close()
			return err
		}
		if err = ValidateCommentAnchor(item.anchor); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(items) > MaxCommentThreads {
		return ErrQuota
	}
	index := indexAnchors(deck)
	for _, item := range items {
		status := item.status
		missing, restored := item.missing, item.restored
		if !index.exists(item.anchor) {
			if status != "detached" {
				missing = sql.NullInt64{Int64: revision, Valid: true}
			}
			status = "detached"
		} else if status == "detached" {
			if kind != "restore" {
				return errors.New("Slides comment identity reappeared outside original-identity restore")
			}
			status = "restored"
			restored = sql.NullInt64{Int64: revision, Valid: true}
		}
		if status != "attached" && status != "detached" && status != "restored" {
			return errors.New("invalid stored Slides comment projection")
		}
		if _, err = tx.ExecContext(ctx, `UPDATE slides_comment_threads SET status=?,projection_revision=?,last_missing_revision=?,restored_at_revision=? WHERE workspace_id=? AND page_id=? AND id=?`, status, revision, missing, restored, key.WorkspaceID, key.PageID, item.id); err != nil {
			return err
		}
	}
	return nil
}

// ValidateCommentProjection is also used by backup to reject forged/stale
// projection state rather than treating imported status as current authority.
func ValidateCommentProjection(thread CommentThread, state State) error {
	return validateCommentProjection(thread, state, indexAnchors(state.Deck))
}
func ValidateCommentProjections(threads []CommentThread, state State) error {
	index := indexAnchors(state.Deck)
	for _, thread := range threads {
		if err := validateCommentProjection(thread, state, index); err != nil {
			return err
		}
	}
	return nil
}
func validateCommentProjection(thread CommentThread, state State, index anchorMembership) error {
	if err := ValidateCommentAnchor(thread.Anchor); err != nil {
		return err
	}
	if thread.PageID != state.Deck.DeckID || thread.ProjectionRevision != state.Revision || thread.Anchor.Revision > state.Revision {
		return errors.New("Slides comment projection revision/identity mismatch")
	}
	present := index.exists(thread.Anchor)
	switch thread.Status {
	case "attached":
		if !present || thread.LastMissingRevision != nil || thread.RestoredAtRevision != nil {
			return errors.New("invalid attached Slides anchor")
		}
	case "detached":
		if present || thread.LastMissingRevision == nil {
			return errors.New("invalid detached Slides anchor")
		}
	case "restored":
		if !present || thread.LastMissingRevision == nil || thread.RestoredAtRevision == nil || *thread.RestoredAtRevision <= *thread.LastMissingRevision {
			return errors.New("invalid restored Slides anchor")
		}
	default:
		return errors.New("unknown Slides anchor status")
	}
	for _, revision := range []*int64{thread.LastMissingRevision, thread.RestoredAtRevision} {
		if revision != nil && (*revision < thread.Anchor.Revision || *revision > state.Revision) {
			return errors.New("invalid Slides anchor provenance revision")
		}
	}
	return nil
}
