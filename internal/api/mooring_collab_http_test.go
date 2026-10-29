package api

import (
	"github.com/crewship-ai/crewship/internal/mooring"
	"io"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

type collaborationUnreadBody struct{ reads int }

func (b *collaborationUnreadBody) Read([]byte) (int, error) { b.reads++; return 0, io.ErrUnexpectedEOF }
func (*collaborationUnreadBody) Close() error               { return nil }

func TestMooringHTTPOriginsArePositiveExactAndNeverReused(t *testing.T) {
	m := &collaborationHTTPManager{usedOrigins: make(map[mooring.SessionKey]map[uint64]struct{})}
	key := mooring.SessionKey{WorkspaceID: "w", ArtifactID: "a", ApplicationID: "wordcraft"}
	seen := map[string]bool{}
	for range 128 {
		s, e := m.origin(key)
		if e != nil {
			t.Fatal(e)
		}
		n, e := strconv.ParseUint(s, 10, 64)
		if e != nil || n == 0 || n > 1<<53-1 || seen[s] {
			t.Fatal("invalid or reused origin")
		}
		seen[s] = true
	}
	m.closed = true
	if _, e := m.origin(key); e == nil {
		t.Fatal("closed manager issued origin")
	}
}
func TestMooringHTTPDefaultDisabledRegistersNoRoutes(t *testing.T) {
	r := &Router{mux: newRouteMux()}
	r.registerMooringRoutes()
	if r.mooringHTTP != nil || len(r.mux.methods) != 0 {
		t.Fatal("disabled controller registered")
	}
}

func TestMooringHTTPRefusesInProcessRestoreBeforeBody(t *testing.T) {
	rig := pagesTransferSetup(t)
	h := NewBackupHandler(rig.db, newTestLogger(), nil, "fixture")
	h.mooringHTTPEnabled = true
	body := &collaborationUnreadBody{}
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/admin/backups/restore", nil), rig.adminID, rig.wsID, "OWNER")
	req.Body = body
	rr := httptest.NewRecorder()
	h.Restore(rr, req)
	if rr.Code != 409 || body.reads != 0 {
		t.Fatalf("restore HTTP%d reads%d", rr.Code, body.reads)
	}
}

func TestMooringHTTPEnabledStartupMustFailClosed(t *testing.T) {
	db := setupTestDB(t)
	for _, cfg := range []MooringHTTPConfig{
		{Enabled: true, Binary: "/does-not-exist-mooring-fixture", BinarySHA256: strings.Repeat("0", 64)},
		{Enabled: true, Binary: "unused", BinarySHA256: strings.Repeat("0", 64), PublicOrigin: "https://trusted.invalid/untrusted-path"},
		{Enabled: true, Binary: "unused", BinarySHA256: strings.Repeat("0", 64), SDKPrefix: "https://foreign.invalid/sdk"},
		{Enabled: true, Binary: "unused", BinarySHA256: strings.Repeat("0", 64), SDKPrefix: "/mooring-sdk/generated/" + strings.Repeat("A", 64)},
		{Enabled: true, Binary: "unused", BinarySHA256: strings.Repeat("0", 64), SDKPrefix: "/mooring-sdk/generated/" + strings.Repeat("a", 64) + "/"},
	} {
		router, err := NewRouter(db, "mooring-startup-32-character-test-key", newTestLogger(), WithMooringHTTP(cfg), WithInternalToken("synthetic-mooring-startup-test-token"))
		if router != nil {
			router.Shutdown()
		}
		if err == nil || router != nil {
			t.Error("enabled invalid Mooring config returned a serving router")
		}
	}
}
