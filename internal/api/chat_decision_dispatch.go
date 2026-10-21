package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatentities"
)

func (r *Router) escalationDecisionAdapter() decisionSourceAdapter {
	return decisionSourceAdapter{SourceType: "escalation", Read: r.escalationDecision, Upper: func(ctx context.Context, a chataudience.Receipt) (int64, error) {
		var upper int64
		err := r.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(e.rowid),0) FROM escalations e WHERE `+textDecisionVisibilitySQL, a.WorkspaceID, a.ChatID, a.AgentID).Scan(&upper)
		return upper, err
	}, Scan: func(ctx context.Context, a chataudience.Receipt, after, upper int64, limit int) ([]decisionSourceRow, error) {
		rows, err := r.db.QueryContext(ctx, `SELECT e.id,e.rowid FROM escalations e WHERE `+textDecisionVisibilitySQL+` AND e.rowid>? AND e.rowid<=? ORDER BY e.rowid LIMIT ?`, a.WorkspaceID, a.ChatID, a.AgentID, after, upper, limit)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		selected := []decisionSourceRow{}
		for rows.Next() {
			var item decisionSourceRow
			if err = rows.Scan(&item.ID, &item.Position); err != nil {
				return nil, err
			}
			selected = append(selected, item)
		}
		return selected, rows.Err()
	}}
}

// Every candidate is projected before it may influence has_more or a public
// page boundary. Unsupported historical forms and hidden sources are skipped.
func (r *Router) chatDecisionList(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	a, err := r.entityRefAudience(req, req.PathValue("chatId"))
	if err != nil || a == nil {
		replyError(w, 404, "Card not found")
		return
	}
	var trusted bool
	if err = r.db.QueryRowContext(req.Context(), `SELECT EXISTS(SELECT 1 FROM workspace_members WHERE id=? AND access_mode='trusted')`, a.MemberID).Scan(&trusted); err != nil {
		replyError(w, 503, "Could not read cards")
		return
	}
	if !trusted {
		replyError(w, 404, "Card not found")
		return
	}
	if r.queryHandler != nil {
		r.queryHandler.sweepExpiredEscalationsBestEffort(req.Context(), a.WorkspaceID)
	}
	limit := 100
	if raw := req.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			replyError(w, 400, "Invalid limit")
			return
		}
	}
	adapters := r.decisionSourceAdapters()
	cursor := decisionPageCursor{Audience: *a, Snapshot: generateCUID(), Expires: time.Now().Add(15 * time.Minute).Unix()}
	reset := true
	if raw := req.URL.Query().Get("cursor"); raw != "" {
		cursor, err = r.decodeDecisionCursor(raw)
		if err != nil || cursor.Audience != *a || len(cursor.Sources) != len(adapters) {
			replyError(w, 400, "cursor_invalid")
			return
		}
		for i, adapter := range adapters {
			if cursor.Sources[i].Type != adapter.SourceType {
				replyError(w, 400, "cursor_invalid")
				return
			}
		}
		reset = false
	} else {
		for _, adapter := range adapters {
			upper, readErr := adapter.Upper(req.Context(), *a)
			if readErr != nil {
				replyError(w, 503, "Could not read cards")
				return
			}
			cursor.Sources = append(cursor.Sources, decisionSourcePageCursor{Type: adapter.SourceType, Upper: upper})
		}
	}
	refs := []ChatCardResponse{}
	hasMore := false
	for cursor.Index < len(adapters) && !hasMore {
		adapter := adapters[cursor.Index]
		source := &cursor.Sources[cursor.Index]
		rows, scanErr := adapter.Scan(req.Context(), *a, source.After, source.Upper, 100)
		if scanErr != nil {
			replyError(w, 503, "Could not read cards")
			return
		}
		if len(rows) == 0 {
			cursor.Index++
			continue
		}
		for _, row := range rows {
			if row.Position <= source.After || row.Position > source.Upper {
				replyError(w, 503, "Could not read cards")
				return
			}
			projection, readErr := adapter.Read(req.Context(), *a, row.ID)
			if errors.Is(readErr, sql.ErrNoRows) {
				source.After = row.Position
				continue
			}
			if readErr != nil {
				replyError(w, 503, "Could not read cards")
				return
			}
			if projection.ChatID != a.ChatID || projection.Ref.Entity.WorkspaceID != a.WorkspaceID {
				replyError(w, 503, "Could not read cards")
				return
			}
			if len(refs) == limit {
				hasMore = true
				break
			}
			refs = append(refs, projection.Ref)
			source.After = row.Position
		}
		if !hasMore && len(rows) < 100 {
			cursor.Index++
		}
	}
	current, accessErr := r.entityRefAudience(req, a.ChatID)
	if accessErr != nil || current == nil || *current != *a {
		replyError(w, 404, "Card not found")
		return
	}
	next := ""
	if hasMore {
		next, err = r.encodeDecisionCursor(cursor)
		if err != nil {
			replyError(w, 503, "Could not read cards")
			return
		}
	}
	writeJSON(w, 200, ChatCardPage{ContractVersion: 1, Reset: reset, SnapshotID: cursor.Snapshot, SnapshotComplete: !hasMore, HasMore: hasMore, NextCursor: next, Refs: refs})
}

func (r *Router) chatDecisionAnswer(w http.ResponseWriter, req *http.Request) {
	if !canScope(req.Context(), "workspace:admin") {
		replyError(w, 403, "Forbidden")
		return
	}
	if req.PathValue("sourceType") == "escalation" {
		r.chatEscalationDecisionAnswer(w, req)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	adapter, ok := r.decisionSourceAdapterFor(req.PathValue("sourceType"))
	if !ok || adapter.Resolve == nil {
		replyError(w, 404, "Card not found")
		return
	}
	a, err := r.entityRefAudience(req, req.PathValue("chatId"))
	if err != nil || a == nil {
		replyError(w, 404, "Card not found")
		return
	}
	if _, err = adapter.Read(req.Context(), *a, req.PathValue("sourceId")); err != nil {
		replyError(w, 404, "Card not found")
		return
	}
	var body chatentities.DecisionAnswer
	decoder := json.NewDecoder(io.LimitReader(req.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&body); err != nil {
		replyError(w, 400, "Invalid JSON")
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		replyError(w, 400, "Invalid JSON")
		return
	}
	key := req.Header.Get("Idempotency-Key")
	if !cardText(key, 1, 200) || strings.TrimSpace(key) != key || body.SourceID != req.PathValue("sourceId") || !cardText(body.ExpectedVersion, 1, 128) {
		replyError(w, 400, errDecisionInput.Error())
		return
	}
	intent, _ := json.Marshal(struct {
		User   string
		Answer chatentities.DecisionAnswer
	}{a.UserID, body})
	hash := sha256.Sum256(intent)
	op := &decisionOperation{Audience: *a, Version: body.ExpectedVersion, Key: key, Hash: fmt.Sprintf("%x", hash)}
	err = adapter.Resolve(req, op, body)
	if err == nil {
		r.chatDecisionGet(w, req)
		return
	}
	if errors.Is(err, errDecisionInput) {
		replyError(w, 400, errDecisionInput.Error())
		return
	}
	if errors.Is(err, errDecisionKeyCollision) {
		replyError(w, 409, errDecisionKeyCollision.Error())
		return
	}
	current, accessErr := r.entityRefAudience(req, a.ChatID)
	if accessErr != nil || current == nil || *current != *a {
		replyError(w, 404, "Card not found")
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "Card not found")
		return
	}
	if errors.Is(err, errDecisionDenied) {
		if _, readErr := adapter.Read(req.Context(), *a, body.SourceID); readErr != nil {
			replyError(w, 404, "Card not found")
			return
		}
		replyError(w, 403, "Forbidden")
		return
	}
	if errors.Is(err, errDecisionVersion) {
		projection, readErr := adapter.Read(req.Context(), *a, body.SourceID)
		if readErr != nil {
			replyError(w, 404, "Card not found")
			return
		}
		latest, latestErr := r.entityRefAudience(req, a.ChatID)
		if latestErr != nil || latest == nil || *latest != *a {
			replyError(w, 404, "Card not found")
			return
		}
		writeJSON(w, 409, ChatCardVersionConflict{Error: "decision_version_conflict", Current: projection.Ref})
		return
	}
	replyError(w, 500, "Could not answer decision")
}
