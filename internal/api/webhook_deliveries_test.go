package api

// Tests for the delivery-ledger read surface.
//
// The load-bearing assertion is the negative one: the raw body is held in the
// ledger so a replay can reproduce it, and it must never come back out of a
// read endpoint. Everything else here is the ledger answering the two
// questions the four columns on the endpoint row could not — "did you receive
// this one" and "what happened to it".

import (
	"database/sql"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

type seededDelivery struct {
	ID               string
	WorkspaceID      string
	EndpointID       string
	EndpointKind     string
	Profile          string
	SourceDeliveryID string
	ContentKey       string
	EventType        string
	FilterDecision   string
	FilterReason     string
	WorkID           string
	RawBody          []byte
	RawBodyExpiresAt string
}

func seedDelivery(t *testing.T, db *sql.DB, d seededDelivery) string {
	t.Helper()
	if d.EndpointKind == "" {
		d.EndpointKind = "agent"
	}
	if d.Profile == "" {
		d.Profile = "github"
	}
	if d.FilterDecision == "" {
		d.FilterDecision = "accepted"
	}
	now := time.Now().UTC()
	var workID, rawExpires any
	if d.WorkID != "" {
		workID = d.WorkID
	}
	if d.RawBodyExpiresAt != "" {
		rawExpires = d.RawBodyExpiresAt
	}
	_, err := db.Exec(`
		INSERT INTO webhook_deliveries (id, workspace_id, endpoint_id, endpoint_kind, profile,
			source_delivery_id, content_key, body_sha256, body_bytes, raw_body, raw_body_expires_at,
			event_type, event_action, signing_key_id, filter_decision, filter_reason, target_revision,
			work_id, received_at, dedup_expires_at)
		VALUES (?,?,?,?,?,?,?,'sha-body',42,?,?,?,'opened','key-1',?,?,'rev-1',?,?,?)`,
		d.ID, d.WorkspaceID, d.EndpointID, d.EndpointKind, d.Profile,
		d.SourceDeliveryID, d.ContentKey, d.RawBody, rawExpires,
		d.EventType, d.FilterDecision, d.FilterReason, workID,
		tsformat.Format(now), tsformat.Format(now.Add(30*24*time.Hour)))
	if err != nil {
		t.Fatalf("seed delivery %s: %v", d.ID, err)
	}
	return d.ID
}

// TestWebhookDeliveryDetail_NeverReturnsTheRawBody is the whole reason the
// view has a raw_body_available boolean instead of the bytes. The ledger keeps
// a signed third-party payload so a REPLAY can reproduce it; a read endpoint
// handing it back publishes whatever the sender put in it.
func TestWebhookDeliveryDetail_NeverReturnsTheRawBody(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	h := NewWebhookDeliveriesHandler(db, quietLogger())
	const secret = "ghp_do_not_publish_this_token"
	seedDelivery(t, db, seededDelivery{ID: "dlv-raw", WorkspaceID: ws, EndpointID: "ep-1",
		SourceDeliveryID: "src-1", EventType: "issues", RawBody: []byte(`{"token":"` + secret + `"}`)})

	req := workReq(t, "GET", "/webhook-deliveries/dlv-raw", "", user, ws, "VIEWER")
	req.SetPathValue("deliveryId", "dlv-raw")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 200 {
		t.Fatalf("get: %d %s", rr.Code, rr.Body.String())
	}
	for _, forbidden := range []string{secret, "raw_body\":", "\"body\""} {
		if strings.Contains(rr.Body.String(), forbidden) {
			t.Fatalf("the delivery response leaks %q: %s", forbidden, rr.Body.String())
		}
	}
	var view webhookDeliveryView
	decodeJSON(t, rr, &view)
	if !view.RawBodyAvailable {
		t.Fatal("a delivery that still holds its payload must say a replay is available")
	}
	if view.BodySHA256 == "" || view.BodyBytes != 42 {
		t.Fatalf("the fingerprint is what replaces the payload: %+v", view)
	}
}

// A delivery whose payload was dropped says so, so the UI can grey out the
// replay button rather than offering one that answers 409.
func TestWebhookDeliveryDetail_ReportsAnExpiredPayload(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	h := NewWebhookDeliveriesHandler(db, quietLogger())
	expires := tsformat.Format(time.Now().UTC().Add(-time.Hour))
	seedDelivery(t, db, seededDelivery{ID: "dlv-gone", WorkspaceID: ws, EndpointID: "ep-1",
		SourceDeliveryID: "src-gone", RawBody: nil, RawBodyExpiresAt: expires})

	req := workReq(t, "GET", "/webhook-deliveries/dlv-gone", "", user, ws, "MEMBER")
	req.SetPathValue("deliveryId", "dlv-gone")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	var view webhookDeliveryView
	decodeJSON(t, rr, &view)
	if view.RawBodyAvailable {
		t.Fatal("a dropped payload must not be reported as available")
	}
	if view.RawBodyExpiresAt == nil || *view.RawBodyExpiresAt != expires {
		t.Fatalf("raw_body_expires_at %v, want %q", view.RawBodyExpiresAt, expires)
	}
}

func TestWebhookDeliveriesList_FiltersAndFences(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	if _, err := db.Exec(`INSERT INTO workspaces (id,name,slug) VALUES ('dlv-foreign','F','dlv-foreign')`); err != nil {
		t.Fatal(err)
	}
	h := NewWebhookDeliveriesHandler(db, quietLogger())
	seedWorkItem(t, db, seededWork{ID: "wk-for-dlv", WorkspaceID: ws, State: "queued", Source: "webhook"})
	seedDelivery(t, db, seededDelivery{ID: "dlv-a", WorkspaceID: ws, EndpointID: "ep-1", SourceDeliveryID: "s-a",
		EventType: "issues", FilterDecision: "accepted", WorkID: "wk-for-dlv"})
	seedDelivery(t, db, seededDelivery{ID: "dlv-b", WorkspaceID: ws, EndpointID: "ep-2", SourceDeliveryID: "s-b",
		EventType: "ping", FilterDecision: "ignored", FilterReason: "ping is not an event"})
	seedDelivery(t, db, seededDelivery{ID: "dlv-far", WorkspaceID: "dlv-foreign", EndpointID: "ep-1", SourceDeliveryID: "s-far"})

	cases := []struct {
		name  string
		query string
		code  int
		want  []string
	}{
		{"all in the workspace", "", 200, []string{"dlv-a", "dlv-b"}},
		{"endpoint", "?endpoint_id=ep-2", 200, []string{"dlv-b"}},
		{"decision", "?decision=ignored", 200, []string{"dlv-b"}},
		{"event type", "?event_type=issues", 200, []string{"dlv-a"}},
		{"cursor", "?after=dlv-a", 200, []string{"dlv-b"}},
		{"identity lookup", "?endpoint_id=ep-1&source_delivery_id=s-a", 200, []string{"dlv-a"}},
		{"identity lookup that misses", "?endpoint_id=ep-1&source_delivery_id=never", 200, nil},
		{"a source id without an endpoint is refused", "?source_delivery_id=s-a", 400, nil},
		{"an unknown decision is refused", "?decision=maybe", 400, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.List(rr, workReq(t, "GET", "/webhook-deliveries"+tc.query, "", user, ws, "MEMBER"))
			if rr.Code != tc.code {
				t.Fatalf("status %d, want %d: %s", rr.Code, tc.code, rr.Body.String())
			}
			if tc.code != 200 {
				return
			}
			var page webhookDeliveryPage
			decodeJSON(t, rr, &page)
			var got []string
			for _, item := range page.Items {
				got = append(got, item.ID)
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("items %v, want %v", got, tc.want)
			}
			if strings.Contains(rr.Body.String(), "dlv-far") {
				t.Fatal("a foreign workspace's delivery leaked into the page")
			}
		})
	}
}

// An ignored delivery keeps its reason and carries a null work id — §5 wants
// the filter decision recoverable, and an empty string that reads like an id
// is not the same answer as "nothing was dispatched".
func TestWebhookDeliveriesList_IgnoredDeliveryIsAuditedNotDropped(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	h := NewWebhookDeliveriesHandler(db, quietLogger())
	seedDelivery(t, db, seededDelivery{ID: "dlv-ignored", WorkspaceID: ws, EndpointID: "ep-1",
		SourceDeliveryID: "s-ignored", FilterDecision: "ignored", FilterReason: "branch filter did not match"})

	req := workReq(t, "GET", "/webhook-deliveries/dlv-ignored", "", user, ws, "MEMBER")
	req.SetPathValue("deliveryId", "dlv-ignored")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	var view webhookDeliveryView
	decodeJSON(t, rr, &view)
	if view.FilterDecision != "ignored" || view.FilterReason != "branch filter did not match" {
		t.Fatalf("the filter decision must be recoverable: %+v", view)
	}
	if view.WorkID != nil {
		t.Fatalf("work_id %v, want null — nothing was dispatched", *view.WorkID)
	}
}

// The identity lookup goes through the ledger's own rule: a delivery id is
// only unique WITHIN an endpoint, so the same source id on two endpoints is
// two deliveries. A WHERE clause that dropped the endpoint term — the obvious
// shortcut, since the id looks unique — would answer with whichever row came
// first.
func TestWebhookDeliveriesList_IdentityIsPerEndpoint(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	h := NewWebhookDeliveriesHandler(db, quietLogger())
	seedDelivery(t, db, seededDelivery{ID: "dlv-ep1", WorkspaceID: ws, EndpointID: "ep-1", SourceDeliveryID: "shared-id"})
	seedDelivery(t, db, seededDelivery{ID: "dlv-ep2", WorkspaceID: ws, EndpointID: "ep-2", SourceDeliveryID: "shared-id"})

	for endpoint, want := range map[string]string{"ep-1": "dlv-ep1", "ep-2": "dlv-ep2"} {
		rr := httptest.NewRecorder()
		h.List(rr, workReq(t, "GET", "/webhook-deliveries?endpoint_id="+endpoint+"&source_delivery_id=shared-id", "", user, ws, "MEMBER"))
		if rr.Code != 200 {
			t.Fatalf("lookup %s: %d %s", endpoint, rr.Code, rr.Body.String())
		}
		var page webhookDeliveryPage
		decodeJSON(t, rr, &page)
		if len(page.Items) != 1 || page.Items[0].ID != want {
			t.Fatalf("endpoint %s resolved to %+v, want %s", endpoint, page.Items, want)
		}
	}

	// And a delivery id that belongs to another workspace is not found, even
	// with the right endpoint: a receipt is an identifier, not a capability.
	if _, err := db.Exec(`INSERT INTO workspaces (id,name,slug) VALUES ('dlv-far-ws','FW','dlv-far-ws')`); err != nil {
		t.Fatal(err)
	}
	seedDelivery(t, db, seededDelivery{ID: "dlv-far2", WorkspaceID: "dlv-far-ws", EndpointID: "ep-1", SourceDeliveryID: "far-id"})
	rr := httptest.NewRecorder()
	h.List(rr, workReq(t, "GET", "/webhook-deliveries?endpoint_id=ep-1&source_delivery_id=far-id", "", user, ws, "MEMBER"))
	var page webhookDeliveryPage
	decodeJSON(t, rr, &page)
	if len(page.Items) != 0 {
		t.Fatalf("a receipt from another workspace resolved: %+v", page.Items)
	}
}

func TestWebhookDeliveryGet_ForeignWorkspaceIs404(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	if _, err := db.Exec(`INSERT INTO workspaces (id,name,slug) VALUES ('dlv-other','O','dlv-other')`); err != nil {
		t.Fatal(err)
	}
	seedDelivery(t, db, seededDelivery{ID: "dlv-hidden", WorkspaceID: "dlv-other", EndpointID: "ep-1", SourceDeliveryID: "s-h"})
	h := NewWebhookDeliveriesHandler(db, quietLogger())
	for _, id := range []string{"dlv-hidden", "dlv-nope", ""} {
		req := workReq(t, "GET", "/webhook-deliveries/"+id, "", user, ws, "OWNER")
		req.SetPathValue("deliveryId", id)
		rr := httptest.NewRecorder()
		h.Get(rr, req)
		if rr.Code != 404 {
			t.Fatalf("id %q: status %d, want 404", id, rr.Code)
		}
	}
}
