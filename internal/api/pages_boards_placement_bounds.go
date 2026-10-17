package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"

	"github.com/crewship-ai/crewship/internal/pages"
)

var errBoardDrawingBounds = errors.New("drawing bounds unavailable")

type boardOccupiedBounds struct{ left, top, right, bottom float64 }

// Use conservative rotated bounds, including stroke and linear/freehand points.
// The result is placement advice, never an authorization or a scene mutation.
func boardDrawingBounds(raw []byte) (boardOccupiedBounds, error) {
	var e pages.BoardElement
	if err := json.Unmarshal(raw, &e); err != nil {
		return boardOccupiedBounds{}, err
	}
	if e.X == nil || e.Y == nil || e.Width == nil || e.Height == nil || *e.Width < 0 || *e.Height < 0 {
		return boardOccupiedBounds{}, errors.New("missing drawing bounds")
	}
	x, y, w, h := *e.X, *e.Y, *e.Width, *e.Height
	for _, v := range []float64{x, y, w, h, e.Angle, e.StrokeWidth} {
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 1_000_000 {
			return boardOccupiedBounds{}, errors.New("invalid drawing bounds")
		}
	}
	cx, cy := x+w/2, y+h/2
	c, s := math.Cos(e.Angle), math.Sin(e.Angle)
	b := boardOccupiedBounds{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	include := func(px, py float64) {
		rx, ry := cx+(px-cx)*c-(py-cy)*s, cy+(px-cx)*s+(py-cy)*c
		b.left = math.Min(b.left, rx)
		b.right = math.Max(b.right, rx)
		b.top = math.Min(b.top, ry)
		b.bottom = math.Max(b.bottom, ry)
	}
	include(x, y)
	include(x+w, y)
	include(x, y+h)
	include(x+w, y+h)
	for _, p := range e.Points {
		if math.IsNaN(p[0]) || math.IsInf(p[0], 0) || math.IsNaN(p[1]) || math.IsInf(p[1], 0) || math.Abs(p[0]) > 1_000_000 || math.Abs(p[1]) > 1_000_000 {
			return boardOccupiedBounds{}, errors.New("invalid drawing point")
		}
		include(x+p[0], y+p[1])
	}
	// Excalidraw freehand rotation uses the point bounds center, which may
	// differ from width/height metadata. Keep both envelopes conservatively.
	if len(e.Points) > 0 {
		minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
		for _, p := range e.Points {
			minX = math.Min(minX, x+p[0])
			maxX = math.Max(maxX, x+p[0])
			minY = math.Min(minY, y+p[1])
			maxY = math.Max(maxY, y+p[1])
		}
		cx, cy = (minX+maxX)/2, (minY+maxY)/2
		for _, p := range e.Points {
			include(x+p[0], y+p[1])
		}
		// Linear paths may use curve extrema for their center. A conservative
		// envelope covers any center inside their control-point bounds.
		if e.Angle != 0 && e.Type != "freedraw" {
			radius := math.Hypot(maxX-minX, maxY-minY)
			b.left = math.Min(b.left, minX-radius)
			b.right = math.Max(b.right, maxX+radius)
			b.top = math.Min(b.top, minY-radius)
			b.bottom = math.Max(b.bottom, maxY+radius)
		}
	}
	padding := math.Max(0, e.StrokeWidth)/2 + 32
	if e.Type == "arrow" {
		// Circle arrowheads grow with stroke width and have their own outline.
		padding = math.Max(0, e.StrokeWidth) + 32
	}
	if e.Type == "freedraw" {
		// Pinned Excalidraw uses a pressure-sensitive stroke sized at 4.25x
		// strokeWidth. Use the full size as a conservative placement margin.
		padding = math.Max(0, e.StrokeWidth)*4.25 + 32
	}
	b.left -= padding
	b.right += padding
	b.top -= padding
	b.bottom += padding
	return b, nil
}

func loadBoardDrawingBounds(ctx context.Context, tx *sql.Tx, board string) ([]boardOccupiedBounds, error) {
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN length(CAST(element_json AS BLOB))<=? THEN element_json ELSE NULL END FROM page_board_elements WHERE board_page_id=? AND deleted_at IS NULL AND element_json IS NOT NULL LIMIT ?`, pages.MaxBoardBatchBytes, board, pages.MaxBoardElements+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []boardOccupiedBounds{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		bounds, err := boardDrawingBounds(raw)
		if err != nil {
			return nil, errors.Join(errBoardDrawingBounds, err)
		}
		result = append(result, bounds)
		if len(result) > pages.MaxBoardElements {
			return nil, errBoardDrawingBounds
		}
	}
	return result, rows.Err()
}

func boardDrawingsOverlap(bounds []boardOccupiedBounds, g boardPlacementGeometry) bool {
	for _, b := range bounds {
		if b.left < *g.X+*g.Width && b.right > *g.X && b.top < *g.Y+*g.Height && b.bottom > *g.Y {
			return true
		}
	}
	return false
}
