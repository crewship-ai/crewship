package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/notifyroute"
)

func TestNotifyDeliveriesHandler_List_RequiresManage(t *testing.T) {
	db := setupTestDB(t)
	h := NewNotifyDeliveriesHandler(db, newTestLogger())

	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/notification-deliveries", nil), "u1", "ws1", "MEMBER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != 403 {
		t.Fatalf("a MEMBER should not read the delivery log, got %d", rr.Code)
	}

	adminReq := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/notification-deliveries", nil), "u1", "ws1", "ADMIN")
	adminRR := httptest.NewRecorder()
	h.List(adminRR, adminReq)
	if adminRR.Code != 200 {
		t.Fatalf("ADMIN should read the delivery log, got %d: %s", adminRR.Code, adminRR.Body.String())
	}
}

func TestNotifyDeliveriesHandler_List_ReturnsSeededRows(t *testing.T) {
	db := setupTestDB(t)
	store := notifyroute.NewDeliveryStore(db)
	if _, _, err := store.InsertPending(context.Background(), notifyroute.Delivery{
		WorkspaceID: "ws1", ChannelID: "nch_1", Category: "security", DedupKey: "security:x",
	}); err != nil {
		t.Fatal(err)
	}

	h := NewNotifyDeliveriesHandler(db, newTestLogger())
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/notification-deliveries", nil), "u1", "ws1", "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	var resp struct {
		Deliveries []notifyroute.Delivery `json:"deliveries"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Deliveries) != 1 {
		t.Fatalf("expected 1 delivery, got %d", len(resp.Deliveries))
	}
}
