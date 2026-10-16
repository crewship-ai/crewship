package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/ws"
)

func placementTestGeometry(x, y, w, h float64) boardPlacementGeometry {
	return boardPlacementGeometry{X: &x, Y: &y, Width: &w, Height: &h}
}
func TestBoardDrawingBoundsConservativeRectangleRotationAndPoints(t *testing.T) {
	cases := []struct {
		name, raw string
		target    boardPlacementGeometry
	}{
		{"rectangle", `{"type":"rectangle","x":100,"y":100,"width":200,"height":100,"strokeWidth":4}`, placementTestGeometry(95, 95, 10, 10)},
		{"rotated rectangle", fmt.Sprintf(`{"type":"rectangle","x":0,"y":0,"width":100,"height":20,"angle":%v}`, math.Pi/2), placementTestGeometry(45, -30, 10, 10)},
		{"wide outlined arrowhead", `{"type":"arrow","x":0,"y":0,"width":1000,"height":0,"strokeWidth":100,"points":[[0,0],[1000,0]],"endArrowhead":"circle_outline"}`, placementTestGeometry(1095, 0, 5, 5)},
		{"wide pressure freehand", `{"type":"freedraw","x":0,"y":0,"width":1000,"height":0,"strokeWidth":100,"points":[[0,0],[1000,0]],"pressures":[1,1],"simulatePressure":false}`, placementTestGeometry(500, 150, 10, 10)},
		{"freehand outside metadata width", `{"type":"freedraw","x":0,"y":0,"width":10,"height":10,"points":[[100,0],[200,0]]}`, placementTestGeometry(145, 0, 10, 10)},
		// Excalidraw's freedraw rotation center comes from point extrema, not
		// metadata width/height (pinned0.18.1 element/bounds.ts source).
		{"rotated freehand outside metadata width", fmt.Sprintf(`{"type":"freedraw","x":0,"y":0,"width":10,"height":10,"angle":%v,"points":[[100,0],[200,0]]}`, math.Pi), placementTestGeometry(145, 0, 10, 10)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bounds, err := boardDrawingBounds([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			if !boardDrawingsOverlap([]boardOccupiedBounds{bounds}, tc.target) {
				t.Fatalf("missed occupied drawing %+v", bounds)
			}
			if boardDrawingsOverlap([]boardOccupiedBounds{bounds}, placementTestGeometry(10000, 10000, 100, 100)) {
				t.Fatal("far target incorrectly occupied")
			}
		})
	}
	for _, raw := range []string{`{}`, `{"x":null,"y":0,"width":1,"height":1}`, `{"x":0,"y":0,"width":-1,"height":1}`, `{"x":1e300,"y":0,"width":1,"height":1}`, `{"x":0,"y":0,"width":1,"height":1,"points":[[1e300,0]]}`} {
		if _, err := boardDrawingBounds([]byte(raw)); err == nil {
			t.Fatalf("accepted malformedbounds %s", raw)
		}
	}
}
func TestBoardArtifactCreationAvoidsDrawingsOrRetainsPrivateResult(t *testing.T) {
	for _, kind := range []string{"rectangle", "rotated", "all20occupied", "deleted", "malformed", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			h, workspace, user, board, token, _ := boardJobFixture(t)
			changes := map[string]any{"type": "rectangle", "x": 0, "y": 0, "width": 200, "height": 100, "angle": 0}
			if kind == "all20occupied" {
				changes["width"] = 10000
			}
			if kind == "rotated" {
				changes["x"] = 0
				changes["y"] = -150
				changes["width"] = 100
				changes["height"] = 100
				changes["angle"] = math.Pi / 4
			}
			raw := boardAgentElement(t, "wire", changes)
			artifactExpect(t, boardCall(t, h, workspace, user, "OWNER", "report", "batch", boardAgentBatch(t, "drawing", 0, 0, raw)), 201)
			body := strings.Replace(boardDiagramRequest, `"kind":"diagram"`, `"kind":"artifact"`, 1)
			body = strings.Replace(body, `"base_seq":0`, `"base_seq":1`, 1)
			body = strings.Replace(body, `"submission_id":"diagram-request"`, `"submission_id":"placement-job"`, 1)
			body = strings.Replace(body, `"selected_element_ids":[]`, `"selected_element_ids":["wire"]`, 1)
			response := boardJobCall(t, h, workspace, user, "", body, h.CreateBoardRequest)
			artifactExpect(t, response, 202)
			var request struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &request); err != nil {
				t.Fatal(err)
			}
			execOrFatal(t, h.db, `UPDATE page_board_requests SET status='cancelled' WHERE kind='diagram'`)
			// Exercise unavailable/restored geometry at completion, not a fabricated
			// provider response or bypass of trusted run admission.
			if kind == "deleted" {
				execOrFatal(t, h.db, `UPDATE page_board_elements SET deleted_at='2026-10-13T00:00:00Z',element_json=NULL WHERE board_page_id=?`, board)
			}
			if kind == "malformed" {
				execOrFatal(t, h.db, `UPDATE page_board_elements SET element_json='{"x":null}' WHERE board_page_id=?`, board)
			}
			if kind == "oversized" {
				execOrFatal(t, h.db, `UPDATE page_board_elements SET element_json=? WHERE board_page_id=?`, `{"padding":"`+strings.Repeat("x", 2<<20)+`"}`, board)
			}
			receipt, err := chataudience.CaptureTrusted(t.Context(), h.db, "artifact-chat", user, nil)
			if err != nil || receipt == nil {
				t.Fatalf("capture: %v", err)
			}
			called := false
			h.artifactDispatch = func(ctx context.Context, actualUser, chat, prompt string, opts ws.ChatMessageOption) error {
				called = true
				if opts.BoardRequestID != request.ID || actualUser != user {
					t.Fatal("wrong admitted request")
				}
				if err := bindBoardRequestRun(httptest.NewRequest("POST", "/", nil), h.db, request.ID, "artifact-run", receipt); err != nil {
					t.Fatal(err)
				}
				artifactExpect(t, artifactCreateAgent(t, h, workspace, token, creationBody), 201)
				artifactExpect(t, artifactCreateAgent(t, h, workspace, token, creationBody), 200)
				return nil
			}
			h.dispatchNextBoardRequest(t.Context())
			if !called {
				t.Fatal("job never dispatched")
			}
			var status, pin, reason string
			var result, placement sql.NullString
			if err := h.db.QueryRow(`SELECT status,pin_status,pin_reason,result_page_id,result_placement_id FROM page_board_requests WHERE id=?`, request.ID).Scan(&status, &pin, &reason, &result, &placement); err != nil {
				t.Fatal(err)
			}
			if status != "succeeded" || !result.Valid {
				t.Fatalf("private result lost: %s %s %s", status, pin, reason)
			}
			var revisions, pins int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=?`, result.String).Scan(&revisions); err != nil {
				t.Fatal(err)
			}
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_board_artifact_placements WHERE board_page_id=?`, board).Scan(&pins); err != nil {
				t.Fatal(err)
			}
			if revisions != 1 {
				t.Fatalf("retry duplicated output revisions=%d", revisions)
			}
			unpinned := kind == "all20occupied" || kind == "malformed" || kind == "oversized"
			if unpinned {
				if pin != "created_not_pinned" || placement.Valid || pins != 0 || reason == "" {
					t.Fatalf("unsafe automatic pin %s %v count%d reason%s", pin, placement, pins, reason)
				}
			} else {
				if pin != "pinned" || !placement.Valid || pins != 1 {
					t.Fatalf("missing unique pin %s %v count%d", pin, placement, pins)
				}
				var x float64
				if err := h.db.QueryRow(`SELECT x FROM page_board_artifact_placements WHERE id=?`, placement.String).Scan(&x); err != nil {
					t.Fatal(err)
				}
				if (kind == "rectangle" || kind == "rotated") && x <= 0 {
					t.Fatal("pin overlapped rectangle")
				}
				if kind == "deleted" && x != 0 {
					t.Fatalf("deleted drawing blocked original target x=%v", x)
				}
			}
		})
	}
}

func TestBoardDrawingBoundsLoadLimitsAndDeletedRows(t *testing.T) {
	h, _, _, workspace, user, board := artifactFixture(t)
	artifactExpect(t, boardCall(t, h, workspace, user, "OWNER", "report", "init", `{"mutation_id":"init"}`), 200)
	execOrFatal(t, h.db, `WITH RECURSIVE numbers(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM numbers WHERE n<501) INSERT INTO page_board_elements(board_page_id,element_id,element_rev,element_json,updated_seq) SELECT ?,printf('bounds-%03d',n),1,'{"x":0,"y":0,"width":10,"height":10}',0 FROM numbers`, board)
	tx, err := h.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = loadBoardDrawingBounds(t.Context(), tx, board)
	_ = tx.Rollback()
	if !errors.Is(err, errBoardDrawingBounds) {
		t.Fatalf("501drawings did not fail safe: %v", err)
	}
	execOrFatal(t, h.db, `UPDATE page_board_elements SET deleted_at='2026-10-13T00:00:00Z',element_json=NULL WHERE element_id='bounds-501'`)
	tx, err = h.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	bounds, err := loadBoardDrawingBounds(t.Context(), tx, board)
	_ = tx.Rollback()
	if err != nil || len(bounds) != 500 {
		t.Fatalf("deleted drawing counted: len=%d err=%v", len(bounds), err)
	}
}
