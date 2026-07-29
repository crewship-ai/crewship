package api

import (
	"strings"
	"testing"
)

// The posture panel only ever read the environment: six warnings, all derived
// from env vars set at deploy. That answers "how was this instance started"
// and nothing about what has happened to it since — so an instance could be
// running with its isolation boundary switched off, its documented demo
// account still live, and no backup ever taken, and the panel would report a
// clean bill of health.
//
// These checks read state instead. Each one names a consequence, not a flag.

func warningFor(p securityPostureResponse, key string) *postureWarning {
	for i := range p.Warnings {
		if p.Warnings[i].Key == key {
			return &p.Warnings[i]
		}
	}
	return nil
}

func TestPostureState_GeneratedEncryptionKeyIsAFinding(t *testing.T) {
	p := buildSecurityPosture(false, false, false, false, postureState{EncryptionKeySource: "generated"})
	w := warningFor(p, "encryption_key_generated")
	if w == nil {
		t.Fatal("a self-generated master key produced no warning")
	}
	// The word "generated" is not the finding — where the file sits is.
	if !strings.Contains(strings.ToLower(w.Message), "beside the database") {
		t.Errorf("warning does not say what it costs:\n%s", w.Message)
	}
	// An operator-supplied key is the good case and must stay quiet.
	if warningFor(buildSecurityPosture(false, false, false, false,
		postureState{EncryptionKeySource: "external"}), "encryption_key_generated") != nil {
		t.Error("an externally supplied key was flagged")
	}
}

func TestPostureState_PrivilegedCredentialsBoundaryOff(t *testing.T) {
	p := buildSecurityPosture(false, false, false, false, postureState{PrivilegedCredentialWorkspaces: 2})
	w := warningFor(p, "privileged_credentials_enabled")
	if w == nil {
		t.Fatal("no warning for workspaces that dropped the fail-closed boundary")
	}
	if !strings.Contains(w.Message, "2") {
		t.Errorf("warning does not say how many workspaces:\n%s", w.Message)
	}
	if warningFor(buildSecurityPosture(false, false, false, false, postureState{}), "privileged_credentials_enabled") != nil {
		t.Error("flagged with no workspace opted in")
	}
}

// The existing ceiling warning is theoretical — "crews that ALSO set
// allow_private_endpoints can reach RFC1918". Whether any crew actually does
// is the difference between a note and a finding.
func TestPostureState_PrivateEgressCeilingCountsRealUsers(t *testing.T) {
	open := buildSecurityPosture(false, false, false, false, postureState{PrivateEndpointCrews: 3})
	// The instance ceiling is closed here, so nothing can cross it.
	if w := warningFor(open, "private_endpoints_in_use"); w != nil {
		t.Errorf("flagged crews while the instance ceiling is closed: %s", w.Message)
	}
}

func TestPostureState_SeedAccountStillUsesTheDocumentedPassword(t *testing.T) {
	p := buildSecurityPosture(false, false, false, false, postureState{SeedAccountDefaultPassword: true})
	w := warningFor(p, "seed_account_default_password")
	if w == nil {
		t.Fatal("the documented demo credentials were not flagged")
	}
	if w.Severity != "high" {
		t.Errorf("severity = %q, want high — the password is in the public docs", w.Severity)
	}
}

func TestPostureState_NoBackupEverTaken(t *testing.T) {
	p := buildSecurityPosture(false, false, false, false, postureState{BackupsRecorded: 0})
	if warningFor(p, "no_backup_recorded") == nil {
		t.Fatal("an instance with no backup at all was not flagged")
	}
	if warningFor(buildSecurityPosture(false, false, false, false,
		postureState{BackupsRecorded: 1}), "no_backup_recorded") != nil {
		t.Error("flagged despite a backup having been taken")
	}
}

// Worst first, so the row an operator must act on is not below three notes.
func TestPostureState_WarningsAreOrderedBySeverity(t *testing.T) {
	p := buildSecurityPosture(true, false, false, true, postureState{
		EncryptionKeySource:        "generated",
		SeedAccountDefaultPassword: true,
		BackupsRecorded:            0,
	})
	rank := map[string]int{"high": 0, "medium": 1, "low": 2, "info": 3}
	for i := 1; i < len(p.Warnings); i++ {
		if rank[p.Warnings[i-1].Severity] > rank[p.Warnings[i].Severity] {
			t.Fatalf("warning %d (%s) outranks the one before it (%s)",
				i, p.Warnings[i].Severity, p.Warnings[i-1].Severity)
		}
	}
}

// The probes read live tables, so they have to survive the tables being
// empty, the columns being absent on an old schema, and the demo account
// simply not existing. A posture panel that 500s because one COUNT failed is
// worse than one reporting five findings out of six.
func TestReadPostureState_ProbesRealTables(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	if _, err := db.Exec(`UPDATE workspaces SET allow_privileged_credentials = 1 WHERE id = ?`, wsID); err != nil {
		t.Fatalf("enable privileged credentials: %v", err)
	}
	crewID := seedCrewRow(t, db, "crew-posture", wsID, "Ops", "ops")
	if _, err := db.Exec(`UPDATE crews SET allow_private_endpoints = 1 WHERE id = ?`, crewID); err != nil {
		t.Fatalf("enable private endpoints: %v", err)
	}

	st := readPostureState(t.Context(), db, newTestLogger())
	if st.PrivilegedCredentialWorkspaces != 1 {
		t.Errorf("privileged workspaces = %d, want 1", st.PrivilegedCredentialWorkspaces)
	}
	if st.PrivateEndpointCrews != 1 {
		t.Errorf("private-endpoint crews = %d, want 1", st.PrivateEndpointCrews)
	}
	if st.BackupsRecorded != 0 {
		t.Errorf("backups = %d, want 0 on a fresh instance", st.BackupsRecorded)
	}
	// No demo account in this fixture, so nothing to flag — and crucially no
	// false positive from a NULL hash.
	if st.SeedAccountDefaultPassword {
		t.Error("flagged the seed password with no demo account present")
	}
}

func TestReadPostureState_NilDBStillReportsTheKeySource(t *testing.T) {
	st := readPostureState(t.Context(), nil, newTestLogger())
	if st.EncryptionKeySource == "" {
		t.Error("the env-derived half was lost when the DB was absent")
	}
}
