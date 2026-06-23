package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// credential_audit_cov_test.go covers the remaining branches of
// credential_audit.go: RecordCredentialEvent argument validation +
// failure modes, the pure helpers (parseLastUsedIPs, parseTags,
// normaliseTags, encodeTagsJSON), and AuditTimeline's 403/limit/500
// branches. Helpers prefixed covCA.

func covCASeedCred(t *testing.T, db *sql.DB) (wsID, credID string) {
	t.Helper()
	ensureEncryptionKey(t)
	userID := seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	credID = "covca-cred"
	seedCredentialEnc(t, db, wsID, userID, credID, "covca-name", "covca-secret")
	return wsID, credID
}

func TestCovCA_RecordCredentialEvent_Validation(t *testing.T) {
	db := setupTestDB(t)
	logger := newTestLogger()

	if err := RecordCredentialEvent(context.Background(), db, logger, "cred", CredentialAuditEvent("BOGUS"), "", "", nil); err == nil || !strings.Contains(err.Error(), "invalid audit event") {
		t.Errorf("bogus event err = %v, want invalid audit event", err)
	}
	if err := RecordCredentialEvent(context.Background(), db, logger, "", AuditEventUse, "", "", nil); err == nil || !strings.Contains(err.Error(), "credentialID required") {
		t.Errorf("empty credentialID err = %v, want credentialID required", err)
	}
}

func TestCovCA_RecordCredentialEvent_UseUpdatesLastUsed(t *testing.T) {
	db := setupTestDB(t)
	wsID, credID := covCASeedCred(t, db)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('covca-crew', ?, 'C', 'covca-c')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('agent-1', 'covca-crew', ?, 'A', 'covca-a')`, wsID)

	// agentID + ip + metadata all set: exercises the optional-field
	// branches and the AuditEventUse -> pushLastUsedIP path.
	err := RecordCredentialEvent(context.Background(), db, newTestLogger(),
		credID, AuditEventUse, "agent-1", "10.1.2.3", map[string]any{"source": "test"})
	if err != nil {
		t.Fatalf("RecordCredentialEvent: %v", err)
	}

	var lastUsedAt sql.NullString
	var lastUsedIPs sql.NullString
	if err := db.QueryRow(`SELECT last_used_at, last_used_ips FROM credentials WHERE id = ?`, credID).Scan(&lastUsedAt, &lastUsedIPs); err != nil {
		t.Fatalf("read credential: %v", err)
	}
	if !lastUsedAt.Valid {
		t.Errorf("last_used_at not stamped")
	}
	if !lastUsedIPs.Valid || !strings.Contains(lastUsedIPs.String, "10.1.2.3") {
		t.Errorf("last_used_ips = %v, want to contain 10.1.2.3", lastUsedIPs)
	}

	var eventType string
	var agentID, ip, meta sql.NullString
	if err := db.QueryRow(`SELECT event_type, agent_id, ip_address, metadata_json FROM credential_audit WHERE credential_id = ?`, credID).Scan(&eventType, &agentID, &ip, &meta); err != nil {
		t.Fatalf("read audit row: %v", err)
	}
	if eventType != "USE" || agentID.String != "agent-1" || ip.String != "10.1.2.3" {
		t.Errorf("audit row = %s/%s/%s, want USE/agent-1/10.1.2.3", eventType, agentID.String, ip.String)
	}
	if !strings.Contains(meta.String, `"source":"test"`) {
		t.Errorf("metadata_json = %q, want source:test", meta.String)
	}
}

func TestCovCA_RecordCredentialEvent_MetadataMarshalError(t *testing.T) {
	db := setupTestDB(t)
	err := RecordCredentialEvent(context.Background(), db, newTestLogger(),
		"cred-x", AuditEventTest, "", "", map[string]any{"bad": make(chan int)})
	if err == nil || !strings.Contains(err.Error(), "marshal audit metadata") {
		t.Fatalf("err = %v, want marshal audit metadata", err)
	}
}

func TestCovCA_RecordCredentialEvent_BeginTxError(t *testing.T) {
	db := setupTestDB(t)
	db.Close()
	err := RecordCredentialEvent(context.Background(), db, newTestLogger(),
		"cred-x", AuditEventTest, "", "", nil)
	if err == nil || !strings.Contains(err.Error(), "begin audit tx") {
		t.Fatalf("err = %v, want begin audit tx", err)
	}
}

func TestCovCA_RecordCredentialEvent_InsertError(t *testing.T) {
	db := setupTestDB(t)
	execOrFatal(t, db, `DROP TABLE credential_audit`)
	err := RecordCredentialEvent(context.Background(), db, newTestLogger(),
		"cred-x", AuditEventTest, "", "", nil)
	if err == nil || !strings.Contains(err.Error(), "insert audit row") {
		t.Fatalf("err = %v, want insert audit row", err)
	}
}

func TestCovCA_RecordCredentialEvent_PushLastUsedIPReadError(t *testing.T) {
	db := setupTestDB(t)
	// Call pushLastUsedIP directly with a credential id that has no row:
	// the SELECT last_used_ips scan fails with ErrNoRows.
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	err = pushLastUsedIP(context.Background(), tx, "cred-does-not-exist", "10.0.0.1", "2026-01-01T00:00:00Z")
	if err == nil || !strings.Contains(err.Error(), "read last_used_ips") {
		t.Fatalf("err = %v, want read last_used_ips", err)
	}
}

func TestCovCA_ParseLastUsedIPs(t *testing.T) {
	cases := []struct {
		name string
		raw  sql.NullString
		want []string
	}{
		{"null", sql.NullString{}, []string{}},
		{"blank", sql.NullString{Valid: true, String: "   "}, []string{}},
		{"invalid json", sql.NullString{Valid: true, String: "{not json"}, []string{}},
		{"valid", sql.NullString{Valid: true, String: `["1.2.3.4","5.6.7.8"]`}, []string{"1.2.3.4", "5.6.7.8"}},
	}
	for _, tc := range cases {
		if got := parseLastUsedIPs(tc.raw); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: parseLastUsedIPs = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCovCA_ParseTags(t *testing.T) {
	if got := parseTags(sql.NullString{Valid: true, String: "{bad"}); len(got) != 0 {
		t.Errorf("invalid json: got %v, want empty", got)
	}
	if got := parseTags(sql.NullString{Valid: true, String: `["a","b"]`}); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("valid: got %v", got)
	}
}

func TestCovCA_NormaliseTags(t *testing.T) {
	long := strings.Repeat("x", 33)
	in := []string{" A ", "a", "", long, "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	got := normaliseTags(in)
	// dedupe "A"/"a", drop empty + over-long, cap at 8.
	want := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("normaliseTags = %v, want %v", got, want)
	}
	if normaliseTags([]string{"", long}) != nil {
		t.Errorf("all-invalid input should normalise to nil")
	}
}

func TestCovCA_EncodeTagsJSON(t *testing.T) {
	s, ok := encodeTagsJSON([]string{"Web", "api"})
	if !ok || s != `["web","api"]` {
		t.Errorf("encodeTagsJSON = %q/%v", s, ok)
	}
	if _, ok := encodeTagsJSON(nil); ok {
		t.Errorf("empty input should return ok=false")
	}
}

func covCAAuditReq(wsID, credID, role, query string) *http.Request {
	req := httptest.NewRequest("GET", "/api/v1/credentials/"+credID+"/audit"+query, nil)
	req.SetPathValue("credentialId", credID)
	return req.WithContext(withWorkspace(req.Context(), wsID, role))
}

func TestCovCA_AuditTimeline_ForbiddenForViewer(t *testing.T) {
	db := setupTestDB(t)
	h := NewCredentialHandler(db, newTestLogger())
	rr := httptest.NewRecorder()
	h.AuditTimeline(rr, covCAAuditReq("ws", "cred", "VIEWER", ""))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestCovCA_AuditTimeline_ExistsCheckDBError(t *testing.T) {
	db := setupTestDB(t)
	h := NewCredentialHandler(db, newTestLogger())
	db.Close()
	rr := httptest.NewRecorder()
	h.AuditTimeline(rr, covCAAuditReq("ws", "cred", "OWNER", ""))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestCovCA_AuditTimeline_LimitParamAndRows(t *testing.T) {
	db := setupTestDB(t)
	wsID, credID := covCASeedCred(t, db)
	for i := 0; i < 3; i++ {
		if err := RecordCredentialEvent(context.Background(), db, newTestLogger(), credID, AuditEventTest, "", "", nil); err != nil {
			t.Fatalf("seed event: %v", err)
		}
	}
	h := NewCredentialHandler(db, newTestLogger())
	rr := httptest.NewRecorder()
	h.AuditTimeline(rr, covCAAuditReq(wsID, credID, "OWNER", "?limit=2"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var out []auditEventResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2 (limit applied)", len(out))
	}
	if out[0].EventType != "TEST" {
		t.Errorf("event_type = %q, want TEST", out[0].EventType)
	}
}

func TestCovCA_AuditTimeline_QueryDBError(t *testing.T) {
	db := setupTestDB(t)
	wsID, credID := covCASeedCred(t, db)
	// credentials row exists, but the audit table is gone -> the second
	// query fails -> 500.
	execOrFatal(t, db, `DROP TABLE credential_audit`)
	h := NewCredentialHandler(db, newTestLogger())
	rr := httptest.NewRecorder()
	h.AuditTimeline(rr, covCAAuditReq(wsID, credID, "OWNER", ""))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}
