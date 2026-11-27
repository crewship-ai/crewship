package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/craftsession"
	"github.com/crewship-ai/crewship/internal/mooring"
)

const craftRealtimeRetries = 3

func (d *craftNativeDocumentsHTTP) mutateRealtimeWorkbook(w http.ResponseWriter, r *http.Request, entry *craftNativeSession, s *mooring.NativeStore, lease mooring.EditLease, base int64, body craftNativeSnapshotBody, operation string) {
	if !entry.realtimeWrite || body.Generation != craftRealtimeGeneration || !s.Workbook() {
		craftNativeDocumentError(w, errCraftNativeAccess)
		return
	}
	a, err := newCraftRealtimeAttempt(entry, body, base)
	if err != nil {
		replyError(w, 400, "invalid realtime Sheet attempt")
		return
	}
	ctx := context.WithValue(r.Context(), craftRealtimeWriteKey{}, true)
	if operation == "reconcile" {
		out, err := a.lookup(ctx, s, lease, d.human.clock, true)
		if err != nil {
			craftNativeDocumentError(w, err)
		} else {
			writeJSON(w, 200, out)
		}
		return
	}
	if craftContentHash(body.Bytes) != body.ContentHash {
		replyError(w, 400, "the workbook bytes do not match contentHash")
		return
	}
	touched, ok := craftWorkbookTouched(r.Header.Values("X-Craft-Touched"))
	if !ok {
		replyError(w, 400, "invalid X-Craft-Touched")
		return
	}
	out, err := d.saveRealtimeWorkbook(ctx, s, lease, a, body.Bytes, touched)
	var loss *craftsession.FidelityLossError
	switch {
	case errors.As(err, &loss):
		craftWorkbookFidelityRefusal(w, loss.Parts)
	case errors.Is(err, errCraftRealtimeUnproved):
		replyCode(w, 409, "realtime_unproved", "the changes could not be safely combined; keep the local draft")
	case errors.Is(err, ErrCraftNativeSheetBusy):
		replyNativeSheetBusy(w)
	case err != nil:
		craftNativeDocumentError(w, err)
	default:
		writeJSON(w, 200, out)
	}
}

func (d *craftNativeDocumentsHTTP) saveRealtimeWorkbook(ctx context.Context, s *mooring.NativeStore, l mooring.EditLease, a craftRealtimeAttempt, submitted []byte, touched json.RawMessage) (any, error) {
	clock, scope, entry := d.human.clock, a.entry.scope, a.entry
	if out, err := a.lookup(ctx, s, l, clock, false); out != nil || err != nil {
		return out, err
	}
	manifest, _, err := s.ReadVersion(ctx, scope, a.base)
	if errors.Is(err, artifacts.ErrVersionNotFound) {
		return nil, errCraftRealtimeUnproved
	}
	if err != nil {
		return nil, err
	}
	baseFile, err := s.DocumentFile(manifest)
	if err != nil {
		return nil, err
	}
	if _, err = mooring.DetectWorkbook(submitted); err != nil {
		return nil, err
	}
	if err = craftWorkbookValidate(ctx, submitted); err != nil {
		return nil, err
	}
	touched = craftWorkbookTouchedSheets(touched, baseFile.Content, submitted)
	edits := craftWorkbookHumanEdits(touched)
	if lost := craftWorkbookPartsLost(ctx, baseFile.Content, submitted, edits); lost != nil {
		return nil, &craftsession.FidelityLossError{Parts: lost}
	}

	var changed []craftRealtimeCell
	for retry := 0; retry < craftRealtimeRetries; retry++ {
		state, err := s.Read(ctx, scope)
		if err != nil {
			return nil, err
		}
		head, err := s.DocumentFile(state.Manifest)
		if err != nil {
			return nil, err
		}
		base := state.LatestVersion
		candidate := submitted
		ops := touched
		if ops == nil {
			ops = craftWorkbookTouchedUnknown()
		}
		meta := craftRealtimeAttemptMetadata{Signed: craftWorkbookSigned(head.Content), Merged: base != a.base}
		if meta.Merged {
			if changed == nil {
				changed, err = craftRealtimeChangedCells(baseFile.Content, submitted, edits)
				if err != nil {
					return nil, err
				}
			}
			candidate, ops, meta.Overlap, meta.AgentRanges, err = d.mergeRealtimeWorkbook(ctx, s, a, base, head.Content, submitted, changed)
			if err != nil {
				return nil, err
			}
		}
		if len(candidate) > 32<<20 {
			return nil, artifacts.ErrVersionQuota
		}
		hash := craftContentHash(candidate)
		audit := craftSheetRecord(scope, entry.application, entry.source.UserID, craftSheetSavedByEditor)
		record := func(ctx context.Context, tx *sql.Tx, r artifacts.VersionReceipt, raw []byte) error {
			if err := audit(ctx, tx, r, raw); err != nil {
				return err
			}
			if meta.Signed {
				if err := recordCraftSignatureBreak(ctx, tx, scope.WorkspaceID, scope.ArtifactID, r.Version); err != nil {
					return err
				}
			}
			return recordCraftVersionOps(ctx, tx, craftVersionOps{
				WorkspaceID: scope.WorkspaceID, PageID: scope.ArtifactID, Version: r.Version, BaseVersion: base,
				ActorKind: "human", ActorID: entry.source.UserID, ChatID: entry.chatID, OperationID: "realtime:" + strconv.FormatInt(r.Version, 10),
				EnginePin: s.EnginePin(), Calculate: "saved", Ops: ops,
			})
		}
		unchangedMeta := meta
		unchangedMeta.Signed = false
		receipt, err := s.SaveConcurrentWorkbook(ctx, l, base, a.submitted.EditorRevision, hash, candidate,
			a.record(base, hash, meta, record), a.record(base, hash, unchangedMeta, nil))
		if errors.Is(err, artifacts.ErrVersionConflict) {
			if out, err := a.lookup(ctx, s, l, clock, false); out != nil || err != nil {
				return out, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		if receipt.Unchanged {
			meta = unchangedMeta
		}
		return a.response(receipt, candidate, meta), nil
	}
	return nil, errCraftRealtimeUnproved
}

func (d *craftNativeDocumentsHTTP) mergeRealtimeWorkbook(ctx context.Context, s *mooring.NativeStore, a craftRealtimeAttempt, base int64, head, submitted []byte, changed []craftRealtimeCell) ([]byte, json.RawMessage, []craftSheetRange, []craftSheetRange, error) {
	entry := a.entry
	db := d.human.pages.db
	var agent string
	if err := db.QueryRowContext(ctx, `SELECT agent_id FROM chats WHERE id=? AND workspace_id=? AND created_by=? AND visibility='private'`, entry.chatID, entry.scope.WorkspaceID, entry.source.UserID).Scan(&agent); err != nil {
		return nil, nil, nil, nil, errCraftRealtimeUnproved
	}
	history, err := craftReadRealtimeChanges(ctx, db, entry.scope, entry.source.UserID, agent, entry.chatID, s.EnginePin(), a.base, base)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	profile, ok := d.profile(entry.application)
	agents := d.human.agents
	if !ok || agents == nil || agents.config.AgentApply == nil || agents.config.AgentInspect == nil {
		return nil, nil, nil, nil, errCraftRealtimeUnproved
	}
	kept := make([]craftRealtimeCell, 0, len(changed))
	overlap := make([]craftSheetRange, 0, len(changed))
	for _, cell := range changed {
		sheet := strings.ToLower(cell.sheet)
		if history.agent.includes(sheet, cell.col, cell.row) {
			overlap = append(overlap, cell.wireRange())
			continue
		}
		if history.human.includes(sheet, cell.col, cell.row) {
			return nil, nil, nil, nil, errCraftRealtimeUnproved
		}
		kept = append(kept, cell)
	}
	candidate, err := craftRealtimeReplay(ctx, agents, profile, head, submitted, kept)
	return candidate, craftRealtimeOps(kept), overlap, history.agentRanges, err
}
