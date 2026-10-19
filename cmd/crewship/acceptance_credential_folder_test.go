package main

// Acceptance for Vault folders (#3479), driven through the BUILT BINARY against
// the real router: the folder commands reach /api/v1/credential-folders, --folder
// on create/update/list reaches the credential routes, and a refusal exits
// non-zero with the server's sentence.

import (
	"strings"
	"testing"
	"time"
)

func TestAcceptance_CredentialFolders_Lifecycle(t *testing.T) {
	rig := startCredAdminServer(t)
	ownerJSON := rig.config(credAdminOwnerToken, "json")
	ownerTable := rig.config(credAdminOwnerToken, "table")
	memberTable := rig.config(credAdminMemberToken, "table")

	var prod, pay credFolderOut
	rig.mustJSON(ownerJSON, &prod, "credential", "folder", "create", "Production", "--reveal", "admins", "--min-security-level", "2", "--default-security-level", "3", "--remind-after-days", "90")
	if prod.ID == "" || prod.RevealPolicy != "admins" || prod.MinSecurityLevel != 2 || prod.RemindAfterDays != 90 {
		t.Fatalf("folder create = %+v", prod)
	}
	rig.mustJSON(ownerJSON, &pay, "credential", "folder", "create", "Payments", "--parent", "Production")
	if pay.ParentID == nil || *pay.ParentID != prod.ID {
		t.Fatalf("child folder = %+v, want parent %s", pay, prod.ID)
	}

	// A third level and a duplicate name are refused with the server's sentence.
	if out, err := rig.run(ownerTable, "", "credential", "folder", "create", "Deep", "--parent", "Production/Payments"); err == nil || !strings.Contains(out, "two levels") {
		t.Errorf("third level should be refused; err=%v\n%s", err, out)
	}
	if out, err := rig.run(ownerTable, "", "credential", "folder", "create", "production"); err == nil || !strings.Contains(out, "already exists") {
		t.Errorf("duplicate name should be refused; err=%v\n%s", err, out)
	}

	// A secret filed through --folder inherits the folder's default tier.
	var cred struct {
		ID            string  `json:"id"`
		FolderID      *string `json:"folder_id"`
		SecurityLevel int     `json:"security_level"`
	}
	rig.must(ownerJSON, "credential", "create", "--name", "stripe-key", "--type", "SECRET", "--provider", "STRIPE", "--value", "sk_live_x", "--folder", "Production/Payments")
	rig.mustJSON(ownerJSON, &cred, "credential", "get", "stripe-key")
	if cred.FolderID == nil || *cred.FolderID != pay.ID || cred.SecurityLevel != 3 {
		t.Fatalf("credential create = %+v, want folder %s at the folder's default tier L3", cred, pay.ID)
	}
	if strings.Contains(rig.must(ownerJSON, "credential", "folder", "list"), "sk_live_x") {
		t.Fatal("folder list leaked a secret value")
	}

	// list --folder filters; the table names the folder.
	if out := rig.must(ownerTable, "credential", "list", "--folder", "Payments"); !strings.Contains(out, "stripe-key") {
		t.Errorf("list --folder lacks the secret:\n%s", out)
	}
	if out := rig.must(ownerTable, "credential", "list", "--folder", "none"); strings.Contains(out, "stripe-key") {
		t.Errorf("list --folder none should not show a filed secret:\n%s", out)
	}
	out := rig.must(ownerTable, "credential", "folder", "list")
	for _, want := range []string{"Production/Payments", "admins", "L2", "90d"} {
		if !strings.Contains(out, want) {
			t.Errorf("folder list lacks %q:\n%s", want, out)
		}
	}

	// Policy changes and non-empty deletes.
	if out, err := rig.run(ownerTable, "", "credential", "folder", "delete", "Payments"); err == nil || !strings.Contains(out, "not empty") {
		t.Errorf("deleting a non-empty folder should be refused; err=%v\n%s", err, out)
	}
	if out, err := rig.run(memberTable, "", "credential", "folder", "create", "Mine"); err == nil {
		t.Errorf("a MEMBER should not create folders:\n%s", out)
	}
	if out, err := rig.run(ownerTable, "", "credential", "update", "stripe-key", "--security-level", "1"); err == nil || !strings.Contains(out, "requires Keeper tier") {
		t.Errorf("lowering below the folder floor should be refused; err=%v\n%s", err, out)
	}
	rig.must(ownerTable, "credential", "update", "stripe-key", "--folder", "")
	rig.must(ownerTable, "credential", "folder", "delete", "Payments")
	var after credRow
	rig.mustJSON(ownerJSON, &after, "credential", "get", "stripe-key")
	if after.FolderID != nil {
		t.Errorf("credential should be unfiled after --folder \"\": %+v", after.FolderID)
	}
}

// #3706: raising a folder's floor through the CLI lifts the secrets already in
// it (Keeper reads the tier from the secret), and filing a secret into a
// stricter folder without a tier raises it to the floor.
func TestAcceptance_CredentialFolders_FloorRaiseLiftsSecrets(t *testing.T) {
	rig := startCredAdminServer(t)
	ownerJSON := rig.config(credAdminOwnerToken, "json")

	rig.must(ownerJSON, "credential", "folder", "create", "Ops")
	rig.must(ownerJSON, "credential", "folder", "create", "Locked", "--min-security-level", "3")
	rig.must(ownerJSON, "credential", "create", "--name", "ops-key", "--type", "SECRET", "--provider", "GITHUB", "--value", "v1", "--folder", "Ops")
	rig.must(ownerJSON, "credential", "create", "--name", "loose-key", "--type", "SECRET", "--provider", "GITHUB", "--value", "v2")

	rig.must(ownerJSON, "credential", "folder", "update", "Ops", "--min-security-level", "4")
	var c credRow
	rig.mustJSON(ownerJSON, &c, "credential", "get", "ops-key")
	if c.SecurityLevel != 4 {
		t.Errorf("after raising the folder floor to L4 the secret is L%d", c.SecurityLevel)
	}

	rig.must(ownerJSON, "credential", "update", "loose-key", "--folder", "Locked")
	rig.mustJSON(ownerJSON, &c, "credential", "get", "loose-key")
	if c.SecurityLevel != 3 {
		t.Errorf("filed into an L3 folder the secret is L%d, want L3", c.SecurityLevel)
	}
}

// `paymaster credential-spend` reaches /paymaster/credential-spend: the calendar
// month's metered $ per key for OWNER/ADMIN, a refusal for everyone below.
func TestAcceptance_PaymasterCredentialSpend(t *testing.T) {
	rig := startCredAdminServer(t)
	ownerJSON := rig.config(credAdminOwnerToken, "json")
	ownerTable := rig.config(credAdminOwnerToken, "table")
	memberTable := rig.config(credAdminMemberToken, "table")

	now := time.Now().UTC()
	thisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 1, 0, time.UTC).Format("2006-01-02T15:04:05.000Z")
	lastMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Add(-time.Hour).Format("2006-01-02T15:04:05.000Z")
	for _, r := range []struct {
		id, ts string
		cost   float64
	}{{"sp1", thisMonth, 2.5}, {"sp2", thisMonth, 1.0}, {"sp3", lastMonth, 30}} {
		if _, err := rig.db.DB.Exec(`INSERT INTO cost_ledger (id, workspace_id, ts, provider, model, input_tokens, output_tokens, cost_usd, credential_id)
			VALUES (?, ?, ?, 'anthropic', 'm', 100, 50, ?, 'key-abc')`, r.id, credAdminWorkspaceID, r.ts, r.cost); err != nil {
			t.Fatal(err)
		}
	}
	var rows []struct {
		CredentialID string  `json:"credential_id"`
		CostUSD      float64 `json:"cost_usd"`
		CallCount    int64   `json:"call_count"`
	}
	rig.mustJSON(ownerJSON, &rows, "paymaster", "credential-spend")
	if len(rows) != 1 || rows[0].CredentialID != "key-abc" || rows[0].CostUSD != 3.5 || rows[0].CallCount != 2 {
		t.Fatalf("credential-spend = %+v, want key-abc $3.50 over 2 calls (last month excluded)", rows)
	}
	if out := rig.must(ownerTable, "paymaster", "credential-spend"); !strings.Contains(out, "$3.50") {
		t.Errorf("table lacks the figure:\n%s", out)
	}
	if out, err := rig.run(memberTable, "", "paymaster", "credential-spend"); err == nil || !strings.Contains(out, "OWNER or ADMIN") {
		t.Errorf("a MEMBER should be refused; err=%v\n%s", err, out)
	}
}
