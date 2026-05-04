package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// auditLogger returns a logger that suppresses warn-and-below — keeps
// test output readable while RecordCredentialEvent's tx-rollback path
// emits its diagnostics.
func auditLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// TestRecordCredentialEvent_USEUpdatesSnapshot verifies the central
// invariant of EPIC 1.4: a USE event refreshes both credential_audit
// (timeline) and credentials.last_used_at + last_used_ips
// (denormalised snapshot for fast list-row rendering). Other event
// types touch only the timeline.
func TestRecordCredentialEvent_USEUpdatesSnapshot(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	credID := "cred-audit-1"
	seedCredentialEnc(t, db, wsID, userID, credID, "TEST_KEY", "v")

	ctx := context.Background()
	if err := RecordCredentialEvent(ctx, db, auditLogger(), credID, AuditEventUse, "", "1.2.3.4", nil); err != nil {
		t.Fatalf("USE: %v", err)
	}

	var lastUsed sql.NullString
	var ipsRaw sql.NullString
	if err := db.QueryRow(`SELECT last_used_at, last_used_ips FROM credentials WHERE id = ?`, credID).Scan(&lastUsed, &ipsRaw); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if !lastUsed.Valid {
		t.Error("last_used_at not set after USE")
	}
	if !ipsRaw.Valid || !strings.Contains(ipsRaw.String, "1.2.3.4") {
		t.Errorf("last_used_ips missing IP: %v", ipsRaw)
	}

	// ROTATE event must NOT update the snapshot — it's a lifecycle
	// event, not actual usage. The Stale check (last_used_at < now-90d)
	// only works if we don't conflate the two.
	prevLastUsed := lastUsed.String
	if err := RecordCredentialEvent(ctx, db, auditLogger(), credID, AuditEventRotate, "", "1.2.3.4", nil); err != nil {
		t.Fatalf("ROTATE: %v", err)
	}
	var lastUsed2 string
	_ = db.QueryRow(`SELECT last_used_at FROM credentials WHERE id = ?`, credID).Scan(&lastUsed2)
	if lastUsed2 != prevLastUsed {
		t.Errorf("ROTATE bumped last_used_at: was %q, now %q", prevLastUsed, lastUsed2)
	}

	// Both events should have landed in credential_audit.
	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM credential_audit WHERE credential_id = ?`, credID).Scan(&count)
	if count != 2 {
		t.Errorf("audit row count = %d, want 2 (USE + ROTATE)", count)
	}
}

// TestRecordCredentialEvent_IPRingbuffer is the regression guard for
// the move-to-front semantics. Five distinct IPs fill the cap; a
// repeat IP must be promoted to the front, not duplicated.
func TestRecordCredentialEvent_IPRingbuffer(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	credID := "cred-audit-ring"
	seedCredentialEnc(t, db, wsID, userID, credID, "TEST_KEY", "v")
	ctx := context.Background()

	for _, ip := range []string{"1.1.1.1", "2.2.2.2", "3.3.3.3", "4.4.4.4", "5.5.5.5", "6.6.6.6"} {
		if err := RecordCredentialEvent(ctx, db, auditLogger(), credID, AuditEventUse, "", ip, nil); err != nil {
			t.Fatalf("USE %s: %v", ip, err)
		}
	}

	// After 6 inserts, ringbuffer should hold 5 most-recent: 6,5,4,3,2.
	var raw string
	_ = db.QueryRow(`SELECT last_used_ips FROM credentials WHERE id = ?`, credID).Scan(&raw)
	var ips []string
	_ = json.Unmarshal([]byte(raw), &ips)
	want := []string{"6.6.6.6", "5.5.5.5", "4.4.4.4", "3.3.3.3", "2.2.2.2"}
	if len(ips) != len(want) {
		t.Fatalf("ips length = %d, want %d (got %v)", len(ips), len(want), ips)
	}
	for i, w := range want {
		if ips[i] != w {
			t.Errorf("ips[%d] = %q, want %q", i, ips[i], w)
		}
	}

	// Repeat the front IP — should move it back to position 0
	// without duplicating; the now-evicted "2.2.2.2" should come back
	// from the cap and 3.3.3.3 stays at the tail.
	if err := RecordCredentialEvent(ctx, db, auditLogger(), credID, AuditEventUse, "", "3.3.3.3", nil); err != nil {
		t.Fatalf("USE 3.3.3.3 repeat: %v", err)
	}
	_ = db.QueryRow(`SELECT last_used_ips FROM credentials WHERE id = ?`, credID).Scan(&raw)
	_ = json.Unmarshal([]byte(raw), &ips)
	if ips[0] != "3.3.3.3" {
		t.Errorf("repeat IP not promoted to front: %v", ips)
	}
	// Duplicate check: 3.3.3.3 must not appear twice
	dup := 0
	for _, ip := range ips {
		if ip == "3.3.3.3" {
			dup++
		}
	}
	if dup != 1 {
		t.Errorf("3.3.3.3 appears %d times, want 1: %v", dup, ips)
	}
}

// TestRecordCredentialEvent_InvalidEventRejects guards the Go-layer
// enum check — schemas without CHECK constraints rely on this.
func TestRecordCredentialEvent_InvalidEventRejects(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	credID := "cred-audit-invalid"
	seedCredentialEnc(t, db, wsID, userID, credID, "TEST_KEY", "v")

	err := RecordCredentialEvent(context.Background(), db, auditLogger(), credID, CredentialAuditEvent("BOGUS"), "", "", nil)
	if err == nil {
		t.Fatal("expected error for invalid event type")
	}
	if !strings.Contains(err.Error(), "invalid audit event") {
		t.Errorf("unexpected error: %v", err)
	}

	// And nothing should have been written to either table.
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM credential_audit`).Scan(&n)
	if n != 0 {
		t.Errorf("rows written despite invalid event: %d", n)
	}
}

// TestAuditTimeline_Endpoint covers the GET handler: workspace
// isolation 404, returns most-recent-first, parses metadata JSON.
func TestAuditTimeline_Endpoint(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	credID := "cred-audit-ep"
	seedCredentialEnc(t, db, wsID, userID, credID, "TEST_KEY", "v")
	ctx := context.Background()

	// Seed a few events
	_ = RecordCredentialEvent(ctx, db, auditLogger(), credID, AuditEventCreated, "", "", map[string]any{"by": "u1"})
	_ = RecordCredentialEvent(ctx, db, auditLogger(), credID, AuditEventTest, "", "10.0.0.1", map[string]any{"result": "valid"})
	_ = RecordCredentialEvent(ctx, db, auditLogger(), credID, AuditEventUse, "", "10.0.0.1", nil)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	h := NewCredentialHandler(db, logger)

	req := httptest.NewRequest("GET", "/api/v1/credentials/"+credID+"/audit", nil)
	req.SetPathValue("credentialId", credID)
	rctx := withUser(req.Context(), &AuthUser{ID: userID})
	rctx = withWorkspace(rctx, wsID, "OWNER")
	req = req.WithContext(rctx)
	rr := httptest.NewRecorder()
	h.AuditTimeline(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var events []auditEventResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &events); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	// Most-recent-first ordering — last seeded event is USE, so it's first.
	if events[0].EventType != "USE" {
		t.Errorf("first event = %q, want USE", events[0].EventType)
	}
	// Metadata round-trips as parsed map, not embedded JSON string.
	var testEvent auditEventResponse
	for _, e := range events {
		if e.EventType == "TEST" {
			testEvent = e
			break
		}
	}
	if testEvent.Metadata["result"] != "valid" {
		t.Errorf("metadata not parsed: %v", testEvent.Metadata)
	}

	// Cross-workspace must 404.
	otherWS := "test-other-workspace"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	req2 := httptest.NewRequest("GET", "/api/v1/credentials/"+credID+"/audit", nil)
	req2.SetPathValue("credentialId", credID)
	rctx2 := withUser(req2.Context(), &AuthUser{ID: userID})
	rctx2 = withWorkspace(rctx2, otherWS, "OWNER")
	req2 = req2.WithContext(rctx2)
	rr2 := httptest.NewRecorder()
	h.AuditTimeline(rr2, req2)
	if rr2.Code != http.StatusNotFound {
		t.Errorf("cross-workspace status = %d, want 404", rr2.Code)
	}
}
