package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// A job-specific creation authorization outlives its source Board. Only the
// admitted exact run and current human create authority can consume it.
func (h *PageHandler) checkBoardJobCreation(ctx context.Context, a *artifactRunAuthority, allowSucceeded bool) error {
	var id string
	if err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT COALESCE(board_request_id,'') FROM artifact_creation_authorizations WHERE run_id=?`, a.RunID).Scan(&id); err != nil {
		return err
	}
	if id == "" {
		return nil
	}
	var status string
	if err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT status FROM page_board_requests WHERE id=? AND kind='artifact' AND workspace_id=? AND human_user_id=? AND agent_id=? AND chat_id=? AND run_id=?`, id, a.WorkspaceID, a.HumanUserID, a.AgentID, a.ChatID, a.RunID).Scan(&status); err != nil {
		return errors.New("artifact job authorization changed")
	}
	if status != "running" && !(allowSucceeded && status == "succeeded") {
		return errors.New("artifact job cancelled or not running")
	}
	return nil
}
func (h *PageHandler) completeBoardJobCreation(r *http.Request, tx *sql.Tx, a *artifactRunAuthority, page string) error {
	var id, board, target string
	var base int64
	err := tx.QueryRowContext(r.Context(), `SELECT r.id,COALESCE(r.board_page_id,''),r.base_seq,r.target_json FROM artifact_creation_authorizations c JOIN page_board_requests r ON r.id=c.board_request_id WHERE c.run_id=?`, a.RunID).Scan(&id, &board, &base, &target)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = h.checkBoardJobCreation(r.Context(), a, false); err != nil {
		return err
	}
	pinStatus, pinReason, placement := "created_not_pinned", "source board unavailable", ""
	if board != "" && h.boardRequestMayWrite(r.Context(), board, a.HumanUserID) {
		var seq int64
		if err = tx.QueryRowContext(r.Context(), `SELECT seq FROM page_boards WHERE page_id=?`, board).Scan(&seq); err != nil {
			return err
		}
		pinReason = "board changed after request; pin the private result explicitly"
		if seq == base {
			var g boardPlacementGeometry
			if json.Unmarshal([]byte(target), &g) != nil || !g.valid() {
				return errors.New("invalid stored job target")
			}
			var live, total int
			if err = tx.QueryRowContext(r.Context(), `SELECT COALESCE(SUM(deleted_at IS NULL),0),COUNT(*) FROM page_board_artifact_placements WHERE board_page_id=?`, board).Scan(&live, &total); err != nil {
				return err
			}
			pinReason = "board placement limit reached"
			var mutationCount, mutationHour int
			if err = tx.QueryRowContext(r.Context(), `SELECT COUNT(*),COALESCE(SUM(created_at>=?),0) FROM page_board_mutations WHERE board_page_id=?`, tsformat.Format(h.evaluator().Now().Add(-time.Hour)), board).Scan(&mutationCount, &mutationHour); err != nil {
				return err
			}
			if mutationCount >= 10000 || mutationHour >= 200 {
				pinReason = "board mutation quota reached"
			}
			if live < 100 && total < 1000 && mutationCount < 10000 && mutationHour < 200 {
				// Never overlap another artifact placement; bounded search can leave a
				// usable private result instead of forcing an unsafe/overwriting pin.
				available := false
				for attempt := 0; attempt < 20; attempt++ {
					var occupied int
					if err = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM page_board_artifact_placements WHERE board_page_id=? AND deleted_at IS NULL AND x<? AND x+width>? AND y<? AND y+height>?`, board, *g.X+*g.Width, *g.X, *g.Y+*g.Height, *g.Y).Scan(&occupied); err != nil {
						return err
					}
					if occupied == 0 {
						available = true
						break
					}
					*g.X += *g.Width + 24
					if !g.valid() {
						break
					}
				}
				pinReason = "target area occupied"
				if available {
					placement = generateCUID()
					if _, err = tx.ExecContext(r.Context(), `INSERT INTO page_board_artifact_placements(id,board_page_id,source_page_id,source_revision,x,y,width,height,updated_seq,author_user_id) VALUES(?,?,?,1,?,?,?,?,?,?)`, placement, board, page, *g.X, *g.Y, *g.Width, *g.Height, seq+1, a.HumanUserID); err != nil {
						return err
					}
					if _, err = tx.ExecContext(r.Context(), `UPDATE page_boards SET seq=seq+1 WHERE page_id=?`, board); err != nil {
						return err
					}
					if err = h.boardReceipt(r, tx, board, "board-job:"+id, boardFingerprint("board-job-pin", id), seq+1, placement); err != nil {
						return err
					}
					pinStatus, pinReason = "pinned", ""
				}
			}
		}
	}
	_, err = tx.ExecContext(r.Context(), `UPDATE page_board_requests SET result_page_id=?,result_revision=1,status='succeeded',pin_status=?,pin_reason=?,result_placement_id=NULLIF(?,''),updated_at=? WHERE id=? AND status='running'`, page, pinStatus, pinReason, placement, tsformat.Format(time.Now()), id)
	return err
}
