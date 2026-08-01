package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// ---- fakes -----------------------------------------------------------------

// egressFakeProvider is a bare ContainerProvider. It does NOT implement
// CrewConfigReporter, which is exactly the docker provider's position: it
// honours what it is given, so it has nothing to report.
type egressFakeProvider struct{}

func (egressFakeProvider) EnsureCrewRuntime(context.Context, provider.CrewConfig) (string, error) {
	return "c1", nil
}
func (egressFakeProvider) StopCrewRuntime(context.Context, string) error   { return nil }
func (egressFakeProvider) RemoveCrewRuntime(context.Context, string) error { return nil }
func (egressFakeProvider) ContainerStatus(context.Context, string) (*provider.ContainerStatus, error) {
	return nil, nil
}
func (egressFakeProvider) ContainerStats(context.Context, string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (egressFakeProvider) Exec(context.Context, provider.ExecConfig) (*provider.ExecResult, error) {
	return nil, nil
}
func (egressFakeProvider) ExecInspect(context.Context, string) (bool, int, error) {
	return false, 0, nil
}
func (egressFakeProvider) CrewContainerName(_ string, slug string) string { return "crew-" + slug }
func (egressFakeProvider) CopyToContainer(context.Context, string, string, io.Reader) error {
	return nil
}

// noEgressProvider is the Apple-shaped provider: it runs the crew but cannot
// apply restricted egress, and says so through the capability report.
type noEgressProvider struct{ egressFakeProvider }

const noEgressReason = "egress is enforced by the in-container crewship-sidecar proxy, " +
	"whose binary this provider does not mount"

func (noEgressProvider) UnsupportedCrewConfig(cfg provider.CrewConfig) provider.CrewConfigSupport {
	if !strings.EqualFold(cfg.NetworkMode, "restricted") {
		return provider.CrewConfigSupport{}
	}
	return provider.CrewConfigSupport{Degraded: []provider.DroppedField{{
		Field: "NetworkMode", Value: cfg.NetworkMode, Detail: noEgressReason,
	}}}
}

var (
	_ provider.ContainerProvider  = (*egressFakeProvider)(nil)
	_ provider.CrewConfigReporter = (*noEgressProvider)(nil)
)

// ---- helpers ---------------------------------------------------------------

func seedRestrictedCrew(t *testing.T, db *sql.DB, id, wsID, slug string) {
	t.Helper()
	seedCrewRow(t, db, id, wsID, slug, slug)
	if _, err := db.Exec(`UPDATE crews SET network_mode = 'restricted' WHERE id = ?`, id); err != nil {
		t.Fatalf("set restricted: %v", err)
	}
}

func getCrewJSON(t *testing.T, h *CrewHandler, wsID, crewID string) map[string]any {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/crews/"+crewID, nil)
	req.SetPathValue("crewId", crewID)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rr.Body.String())
	}
	return out
}

// ---- the assertion this whole change exists for ----------------------------

// TestCrewGet_RestrictedEgressReportsUnenforcedOnAProviderThatCannotApplyIt is
// the property #1648 turns on. A crew configured "restricted" on a provider
// with no egress proxy used to be reported as "restricted" by the crew record,
// the dashboard and `crewship crew get` alike, while nothing restricted
// anything. Now the crew still runs — but no read surface presents the
// configured value as the effective one.
func TestCrewGet_RestrictedEgressReportsUnenforcedOnAProviderThatCannotApplyIt(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedRestrictedCrew(t, db, "crew-1", wsID, "ops")

	h := NewCrewHandler(db, newTestLogger())
	h.SetContainer(noEgressProvider{})

	body := getCrewJSON(t, h, wsID, "crew-1")

	if body["network_mode"] != "restricted" {
		t.Fatalf("network_mode = %v, want the configured value to survive untouched", body["network_mode"])
	}
	enforced, ok := body["network_mode_enforced"].(bool)
	if !ok {
		t.Fatalf("network_mode_enforced missing from the crew response: %#v", body)
	}
	if enforced {
		t.Fatal("a crew whose provider drops NetworkMode must not be reported as enforced")
	}
	reason, _ := body["network_mode_unenforced_reason"].(string)
	if !strings.Contains(reason, "crewship-sidecar") {
		t.Errorf("the response must carry the provider's own reason, got %q", reason)
	}
}

// TestCrewGet_ConfiguredIntentIsNeverRewritten guards the other half: the fix
// is a read-time annotation, not a write. Rewriting the row to "free" would
// make the surfaces agree — and would silently discard the operator's intent,
// so moving the crew to a provider that CAN enforce it would leave it open.
func TestCrewGet_ConfiguredIntentIsNeverRewritten(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedRestrictedCrew(t, db, "crew-1", wsID, "ops")

	h := NewCrewHandler(db, newTestLogger())
	h.SetContainer(noEgressProvider{})

	_ = getCrewJSON(t, h, wsID, "crew-1")
	// Read it twice — a rewrite would most plausibly land on the read path.
	_ = getCrewJSON(t, h, wsID, "crew-1")

	var stored string
	if err := db.QueryRow(`SELECT network_mode FROM crews WHERE id = 'crew-1'`).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != "restricted" {
		t.Fatalf("crews.network_mode = %q, want it left as the operator's intent %q", stored, "restricted")
	}
}

// TestCrewGet_EnforcedWhenTheProviderHonoursIt: the docker-shaped provider
// answers nothing, which means it honours what it is given. A crew there must
// not be marked unenforced, or the marking becomes noise nobody reads.
func TestCrewGet_EnforcedWhenTheProviderHonoursIt(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedRestrictedCrew(t, db, "crew-1", wsID, "ops")

	h := NewCrewHandler(db, newTestLogger())
	h.SetContainer(egressFakeProvider{})

	body := getCrewJSON(t, h, wsID, "crew-1")
	if enforced, _ := body["network_mode_enforced"].(bool); !enforced {
		t.Fatal("a provider that reports no drop honours the mode")
	}
	if _, present := body["network_mode_unenforced_reason"]; present {
		t.Error("no reason should be emitted when the mode is enforced")
	}
}

// TestCrewGet_FreeEgressIsAlwaysEnforced: "free" asks for nothing, so there is
// nothing any provider can fail to apply.
func TestCrewGet_FreeEgressIsAlwaysEnforced(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-1", wsID, "ops", "ops") // seeds network_mode = 'free'

	h := NewCrewHandler(db, newTestLogger())
	h.SetContainer(noEgressProvider{})

	body := getCrewJSON(t, h, wsID, "crew-1")
	if enforced, _ := body["network_mode_enforced"].(bool); !enforced {
		t.Fatalf("free egress is enforced everywhere; body = %#v", body)
	}
}

// TestCrewGet_NoProviderWiredReportsEnforced covers --no-docker / tests: no
// container runs at all, so there is no unenforced crew to warn about, and a
// blanket "not enforced" would fire on every install that has not wired a
// runtime yet.
func TestCrewGet_NoProviderWiredReportsEnforced(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedRestrictedCrew(t, db, "crew-1", wsID, "ops")

	h := NewCrewHandler(db, newTestLogger()) // no SetContainer

	body := getCrewJSON(t, h, wsID, "crew-1")
	if enforced, _ := body["network_mode_enforced"].(bool); !enforced {
		t.Fatal("with no container provider there is nothing running to be unfenced")
	}
}

// TestCrewList_ReportsUnenforcedEgressPerRow: the list is where an operator
// scans a fleet for "which of these is actually fenced". Answering only on the
// detail page would cost one request per crew to find out.
func TestCrewList_ReportsUnenforcedEgressPerRow(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedRestrictedCrew(t, db, "crew-fenced", wsID, "fenced")
	seedCrewRow(t, db, "crew-open", wsID, "open", "open") // free

	h := NewCrewHandler(db, newTestLogger())
	h.SetContainer(noEgressProvider{})

	req := httptest.NewRequest("GET", "/api/v1/crews", nil)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := map[string]bool{}
	for _, r := range rows {
		enforced, ok := r["network_mode_enforced"].(bool)
		if !ok {
			t.Fatalf("crew %v: network_mode_enforced missing from the list row", r["id"])
		}
		got[r["id"].(string)] = enforced
	}
	if got["crew-fenced"] {
		t.Error("the restricted crew must be listed as unenforced")
	}
	if !got["crew-open"] {
		t.Error("the free crew has nothing unenforced about it")
	}
}

// TestCrewUpdate_SwitchingToRestrictedWarnsOnTheResponseThatDidIt: the moment
// an operator turns the fence on is the moment to say this instance will not
// apply it — not on some later GET they may never make. It rides the same
// `warnings` array #1641 added rather than inventing a second shape.
func TestCrewUpdate_SwitchingToRestrictedWarnsOnTheResponseThatDidIt(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-1", wsID, "ops", "ops") // starts free

	h := NewCrewHandler(db, newTestLogger())
	h.SetContainer(noEgressProvider{})

	req := httptest.NewRequest("PATCH", "/api/v1/crews/crew-1",
		strings.NewReader(`{"network_mode":"restricted"}`))
	req.SetPathValue("crewId", "crew-1")
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var body struct {
		NetworkMode         string   `json:"network_mode"`
		NetworkModeEnforced bool     `json:"network_mode_enforced"`
		Warnings            []string `json:"warnings"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rr.Body.String())
	}
	if body.NetworkMode != "restricted" {
		t.Fatalf("the request must still take effect on the record, got %q", body.NetworkMode)
	}
	if body.NetworkModeEnforced {
		t.Error("the update response must report the mode as unenforced")
	}
	joined := strings.Join(body.Warnings, "\n")
	if !strings.Contains(joined, "not enforced") || !strings.Contains(joined, "crewship-sidecar") {
		t.Errorf("warnings must name the problem and the provider's reason; got %v", body.Warnings)
	}

	var stored string
	if err := db.QueryRow(`SELECT network_mode FROM crews WHERE id = 'crew-1'`).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != "restricted" {
		t.Fatalf("crews.network_mode = %q, want the intent stored as asked", stored)
	}
}
