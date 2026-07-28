package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/notifyroute"
)

// TestDeliveriesWireIsSnakeCase pins the field NAMES on the wire, not just
// their values.
//
// Why this needs its own test: TestNotifyDeliveriesHandler_List_ReturnsSeededRows
// decodes the response back into notifyroute.Delivery, so it agrees with the
// server no matter what the server emits — it cannot see a casing mismatch.
// The two real clients do not have that luxury.
// cmd/crewship/cmd_notifychannel.go decodes `json:"channel_id"`,
// `json:"user_id"` and `json:"created_at"`, and the Deliveries view in the
// dashboard reads the same keys. Delivery carried NO json tags, so the API
// emitted Go field names (ChannelID, CreatedAt, …). Go's unmarshal falls back
// to a case-insensitive match, which rescues `ID`->`id` and
// `Status`->`status` but never `ChannelID`->`channel_id`: the underscore
// makes them different strings. The CLI's `notifychannel deliveries` table
// therefore printed blank CHANNEL, USER and CREATED columns for every row.
//
// Decoding into a map is the whole point — decode into the struct and the
// bug is invisible again.
func TestDeliveriesWireIsSnakeCase(t *testing.T) {
	db := setupTestDB(t)
	store := notifyroute.NewDeliveryStore(db)
	if _, _, err := store.InsertPending(context.Background(), notifyroute.Delivery{
		WorkspaceID: "ws1", ChannelID: "nch_1", UserID: "u1",
		Category: "security", DedupKey: "security:wire", SourceKind: "journal",
		SourceID: "j1", Title: "t",
	}); err != nil {
		t.Fatal(err)
	}

	h := NewNotifyDeliveriesHandler(db, newTestLogger())
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/notification-deliveries", nil), "u1", "ws1", "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	var body struct {
		Deliveries []map[string]json.RawMessage `json:"deliveries"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Deliveries) != 1 {
		t.Fatalf("got %d deliveries, want 1", len(body.Deliveries))
	}
	row := body.Deliveries[0]

	// Every key a client reads. Listing them explicitly (rather than
	// lower-casing whatever arrives) is what makes this a contract.
	for _, key := range []string{
		"id", "workspace_id", "channel_id", "user_id", "category",
		"dedup_key", "source_kind", "source_id", "title", "status",
		"attempts", "created_at", "updated_at",
	} {
		if _, ok := row[key]; !ok {
			t.Errorf("delivery JSON is missing %q; keys present: %v", key, deliveryKeys(row))
		}
	}
}

func deliveryKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
