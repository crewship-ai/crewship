package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestCrewCodexOuterSandboxRequiresWorkspaceAdministration(t *testing.T) {
	h, db, user, ws := covCruNewCrew(t)
	crew := seedCrewRow(t, db, "outer-crew", ws, "Outer fixture", "outer-fixture")
	// Crew management must not confer authority to weaken the nested sandbox.
	rr := covCruDoUpdate(h, crew, user, ws, "MANAGER", `{"allow_codex_outer_sandbox":true}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("manager sandbox opt-in: got %d want403: %s", rr.Code, rr.Body.String())
	}
}

func TestCrewCodexOuterSandboxDefaultsOffAndPersistsExplicitConsent(t *testing.T) {
	h, db, user, ws := covCruNewCrew(t)
	crew := seedCrewRow(t, db, "outer-crew", ws, "Outer fixture", "outer-fixture")
	var enabled bool
	if err := db.QueryRow(`SELECT allow_codex_outer_sandbox FROM crews WHERE id=?`, crew).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("fresh crew enabled without consent")
	}
	for _, body := range []string{`{"allow_codex_outer_sandbox":true}`, `{"allow_codex_outer_sandbox":false}`} {
		rr := covCruDoUpdate(h, crew, user, ws, "OWNER", body)
		if rr.Code != http.StatusOK {
			t.Fatalf("owner update: %d %s", rr.Code, rr.Body.String())
		}
		if err := db.QueryRow(`SELECT allow_codex_outer_sandbox FROM crews WHERE id=?`, crew).Scan(&enabled); err != nil {
			t.Fatal(err)
		}
		if enabled != (body == `{"allow_codex_outer_sandbox":true}`) {
			t.Fatal("explicit consent did not persist")
		}
	}
}

func TestCrewCodexOuterConsentAuditAndRevocation(t *testing.T) {
	h, db, user, ws := covCruNewCrew(t)
	crew := seedCrewRow(t, db, "outer-audit-crew", ws, "Outer audit", "outer-audit")
	rr := covCruDoUpdate(h, crew, user, ws, "OWNER", `{"allow_codex_outer_sandbox":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("enable=%d %s", rr.Code, rr.Body.String())
	}
	var raw string
	if err := db.QueryRow(`SELECT metadata FROM audit_logs WHERE entity_id=? AND action='crew.update' ORDER BY created_at DESC LIMIT 1`, crew).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Consent struct {
			Previous bool `json:"previous"`
			Current  bool `json:"current"`
		} `json:"codex_outer_sandbox"`
	}
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil || metadata.Consent.Previous || !metadata.Consent.Current {
		t.Fatal("consent audit missing old/new values")
	}
	// Workspace membership is authoritative even when the request carries a
	// stale OWNER context. Crew-level management cannot self-grant this mode.
	if _, err := db.Exec(`UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, ws, user); err != nil {
		t.Fatal(err)
	}
	rr = covCruDoUpdate(h, crew, user, ws, "OWNER", `{"allow_codex_outer_sandbox":false}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("stale authority changed consent: %d", rr.Code)
	}
	var enabled bool
	if err := db.QueryRow(`SELECT allow_codex_outer_sandbox FROM crews WHERE id=?`, crew).Scan(&enabled); err != nil || !enabled {
		t.Fatal("rejected writer changed consent")
	}
}

func TestCrewCodexOuterConsentRechecksWriterAuthority(t *testing.T) {
	h, db, user, ws := covCruNewCrew(t)
	crew := seedCrewRow(t, db, "outer-race-crew", ws, "Outer race", "outer-race")
	if _, err := db.Exec(`CREATE TRIGGER outer_demote AFTER UPDATE OF allow_codex_outer_sandbox ON crews BEGIN UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=NEW.workspace_id; END`); err != nil {
		t.Fatal(err)
	}
	rr := covCruDoUpdate(h, crew, user, ws, "OWNER", `{"allow_codex_outer_sandbox":true}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("authority changed inside writer: %d %s", rr.Code, rr.Body.String())
	}
	var enabled bool
	if err := db.QueryRow(`SELECT allow_codex_outer_sandbox FROM crews WHERE id=?`, crew).Scan(&enabled); err != nil || enabled {
		t.Fatal("revoked writer committed consent")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='crew.update' AND entity_id=?`, crew).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed mutation retained success audit")
	}
}
