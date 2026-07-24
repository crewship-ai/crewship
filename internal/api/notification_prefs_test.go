package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/notify"
	"github.com/crewship-ai/crewship/internal/notifyroute"
)

func TestNotifyPrefsHandler_PutThenGet_RoundTrips(t *testing.T) {
	db := setupTestDB(t)
	t.Setenv("ENCRYPTION_KEY", testNotifyEncKey)
	ch, err := notify.NewChannelStore(db).Create(context.Background(), notify.ChannelInput{
		WorkspaceID: "ws1", Type: notify.ChannelWebhook, URL: "https://hooks.example.com/x",
	})
	if err != nil {
		t.Fatalf("seed channel: %v", err)
	}

	h := NewNotifyPrefsHandler(db, newTestLogger())

	putBody, _ := json.Marshal(map[string]any{
		"cells": []notifyroute.PrefCell{{Category: notify.CategoryApprovals, ChannelID: ch.ID, State: "immediate"}},
	})
	putReq := withWorkspaceUser(httptest.NewRequest("PUT", "/api/v1/me/notification-prefs", strings.NewReader(string(putBody))), "u1", "ws1", "MEMBER")
	putRR := httptest.NewRecorder()
	h.Put(putRR, putReq)
	if putRR.Code != 200 {
		t.Fatalf("put: got %d, body=%s", putRR.Code, putRR.Body.String())
	}

	getReq := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/me/notification-prefs", nil), "u1", "ws1", "MEMBER")
	getRR := httptest.NewRecorder()
	h.Get(getRR, getReq)
	var resp struct {
		Cells []notifyroute.PrefCell `json:"cells"`
	}
	if err := json.Unmarshal(getRR.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, getRR.Body.String())
	}
	if len(resp.Cells) != 1 || resp.Cells[0].State != "immediate" {
		t.Fatalf("expected the set cell to round-trip, got %+v", resp.Cells)
	}
}

func TestNotifyPrefsHandler_Get_IsolatedPerUser(t *testing.T) {
	db := setupTestDB(t)
	t.Setenv("ENCRYPTION_KEY", testNotifyEncKey)
	ch, _ := notify.NewChannelStore(db).Create(context.Background(), notify.ChannelInput{
		WorkspaceID: "ws1", Type: notify.ChannelWebhook, URL: "https://hooks.example.com/x",
	})
	h := NewNotifyPrefsHandler(db, newTestLogger())

	body, _ := json.Marshal(map[string]any{
		"cells": []notifyroute.PrefCell{{Category: notify.CategoryBudget, ChannelID: ch.ID, State: "immediate"}},
	})
	putReq := withWorkspaceUser(httptest.NewRequest("PUT", "/api/v1/me/notification-prefs", strings.NewReader(string(body))), "u1", "ws1", "MEMBER")
	h.Put(httptest.NewRecorder(), putReq)

	// A DIFFERENT user in the same workspace sees an empty matrix.
	getReq := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/me/notification-prefs", nil), "u2", "ws1", "MEMBER")
	getRR := httptest.NewRecorder()
	h.Get(getRR, getReq)
	var resp struct {
		Cells []notifyroute.PrefCell `json:"cells"`
	}
	_ = json.Unmarshal(getRR.Body.Bytes(), &resp)
	if len(resp.Cells) != 0 {
		t.Fatalf("a different user's matrix must not leak u1's cells, got %+v", resp.Cells)
	}
}

func TestNotifyPrefsHandler_Put_RejectsUnauthenticated(t *testing.T) {
	db := setupTestDB(t)
	h := NewNotifyPrefsHandler(db, newTestLogger())
	req := httptest.NewRequest("PUT", "/api/v1/me/notification-prefs", strings.NewReader(`{"cells":[]}`))
	rr := httptest.NewRecorder()
	h.Put(rr, req)
	if rr.Code != 401 {
		t.Fatalf("expected 401 without auth context, got %d", rr.Code)
	}
}
