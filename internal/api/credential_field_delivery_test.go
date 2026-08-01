package api

import (
	"database/sql"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// Delivering the PARTS of a multi-part credential — PRD-CREDENTIALS-V2 §2.2, P4.
//
// P4's storage half shipped credential_fields and nothing that reads it: a
// credential with an access key id, a secret and a region reached the container
// as one opaque value and the parts were invisible. These tests pin the reading
// half from all three value-carrying delivery consumers at once, because a
// fanout that lands in one resolver and not the others is exactly the defect
// credentials_crew_delivery_test.go was written to end — three near-misses, one
// shared definition.
//
// The naming rule under test is <SLOT>_<KEY_UPCASED>, where SLOT is whatever
// the credential itself resolved to (explicit grant env_var_name, binding slot,
// or the legacy credentials.name). Everything else here is about what happens
// when that derived name is NOT free.

// seedCredentialField writes one part of a multi-part credential, honouring the
// table's CHECK: a secret part populates encrypted_value only, a non-secret part
// populates value only. Written directly rather than through the HTTP handler so
// a delivery test cannot be made to pass by a change in the CRUD surface.
func seedCredentialField(t *testing.T, db *sql.DB, credID, key, value string, isSecret bool, ordinal int) {
	t.Helper()
	if isSecret {
		enc, err := encryption.Encrypt(value)
		if err != nil {
			t.Fatalf("encrypt field %q: %v", key, err)
		}
		execOrFatal(t, db, `INSERT INTO credential_fields
			(credential_id, key, value, encrypted_value, is_secret, ordinal)
			VALUES (?, ?, NULL, ?, 1, ?)`, credID, key, enc, ordinal)
		return
	}
	execOrFatal(t, db, `INSERT INTO credential_fields
		(credential_id, key, value, encrypted_value, is_secret, ordinal)
		VALUES (?, ?, ?, NULL, 0, ?)`, credID, key, value, ordinal)
}

// bootAllEnv flattens the BOOT payload to the COMPLETE set of names it puts in
// front of the agent — primary values and field parts together. The whole set,
// not just the primaries: "delivered exactly as before" is only checkable if a
// stray extra entry fails the assertion.
func bootAllEnv(creds []mcpCredEntry) map[string]string {
	out := make(map[string]string, len(creds))
	for _, c := range creds {
		out[c.EnvVar] = c.Value
		for _, f := range c.Fields {
			out[f.EnvVar] = f.Value
		}
	}
	return out
}

// delegationAllEnv is bootAllEnv for the orchestrator-shaped consumers (the
// delegation/hire boundary and the peer-query run).
func delegationAllEnv(creds []orchestrator.Credential) map[string]string {
	out := make(map[string]string, len(creds))
	for _, c := range creds {
		out[c.EnvVarName] = c.PlainValue
		for _, f := range c.Fields {
			out[f.EnvVar] = f.Value
		}
	}
	return out
}

// allConsumers runs the same assertion against every delivery path that carries
// values. Keyed by name so a failure says which one drifted.
func allConsumers(t *testing.T, db *sql.DB, agentID string) map[string]map[string]string {
	t.Helper()
	return map[string]map[string]string{
		"boot":       bootAllEnv(bootCreds(t, db, agentID)),
		"delegation": delegationAllEnv(delegationCreds(t, db, agentID)),
		"peer-query": delegationAllEnv(peerQueryCreds(t, db, agentID)),
	}
}

// TestFieldDelivery_AWSShapedCredentialReachesEveryConsumer is the headline: the
// shape the PRD names as the reason credential_fields exists (access key id +
// secret + region) must arrive whole, under names derived from the credential's
// own slot, on every path that hands values to a container.
func TestFieldDelivery_AWSShapedCredentialReachesEveryConsumer(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "fd-aws", "AWS", "unused-primary")
	assignCredToAgent(t, db, "fd-aws", e.agentA, "AWS", 0)
	seedCredentialField(t, db, "fd-aws", "access_key_id", "AKIAEXAMPLE", false, 0)
	seedCredentialField(t, db, "fd-aws", "region", "eu-central-1", false, 1)
	seedCredentialField(t, db, "fd-aws", "secret_access_key", "wJalrXUtn", true, 2)

	want := map[string]string{
		"AWS":                   "unused-primary",
		"AWS_ACCESS_KEY_ID":     "AKIAEXAMPLE",
		"AWS_REGION":            "eu-central-1",
		"AWS_SECRET_ACCESS_KEY": "wJalrXUtn",
	}
	for name, got := range allConsumers(t, db, e.agentA) {
		if len(got) != len(want) {
			t.Errorf("%s: delivered %v, want exactly %v", name, got, want)
			continue
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: %s = %q, want %q", name, k, got[k], v)
			}
		}
	}
}

// TestFieldDelivery_NoFieldsDeliveredExactlyAsBefore is the compatibility
// guarantee, and it asserts the WHOLE delivered set: every credential in every
// workspace today has zero fields, so an extra entry, a renamed one or a
// non-nil Fields slice here is a regression for effectively the entire install
// base.
func TestFieldDelivery_NoFieldsDeliveredExactlyAsBefore(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "fd-plain", "CREW_TOKEN", "crew-secret")
	linkCredToCrew(t, db, "fd-plain", e.crewA)
	seedCredentialEnc(t, db, e.wsID, e.userID, "fd-own", "OWN_CRED", "own-secret")
	assignCredToAgent(t, db, "fd-own", e.agentA, "OWN_TOKEN", 0)

	want := map[string]string{"CREW_TOKEN": "crew-secret", "OWN_TOKEN": "own-secret"}
	for name, got := range allConsumers(t, db, e.agentA) {
		if len(got) != len(want) {
			t.Errorf("%s: delivered %v, want exactly %v — a fieldless credential must be unchanged", name, got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: %s = %q, want %q", name, k, got[k], v)
			}
		}
	}

	for _, c := range bootCreds(t, db, e.agentA) {
		if c.Fields != nil {
			t.Errorf("boot entry %s carries Fields=%v; a fieldless credential must serialise without the key at all", c.EnvVar, c.Fields)
		}
	}
	for _, c := range delegationCreds(t, db, e.agentA) {
		if c.Fields != nil {
			t.Errorf("delegation entry %s carries Fields=%v", c.EnvVarName, c.Fields)
		}
	}
}

// TestFieldDelivery_FieldsHangOffTheResolvedSlot pins the "no second resolution
// path" rule. The same credential, the same field, delivered once under an
// explicit grant's chosen env var and once under the legacy crew-link name: the
// prefix must be whatever the credential itself resolved to, never the
// credential's name where a grant said otherwise. A field that derived its own
// prefix would make the ten-GitHub-accounts case (§2.5b) collapse back into one.
func TestFieldDelivery_FieldsHangOffTheResolvedSlot(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)

	// Agent A: explicit grant renames the slot to GH_ACME.
	seedCredentialEnc(t, db, e.wsID, e.userID, "fd-gh", "GH_DEFAULT", "tok")
	seedCredentialField(t, db, "fd-gh", "account_id", "12345", false, 0)
	assignCredToAgent(t, db, "fd-gh", e.agentA, "GH_ACME", 0)

	// Agent B: the same credential arrives through the legacy crew link, so the
	// prefix is credentials.name.
	linkCredToCrew(t, db, "fd-gh", e.crewB)

	for name, got := range allConsumers(t, db, e.agentA) {
		if got["GH_ACME_ACCOUNT_ID"] != "12345" {
			t.Errorf("%s: delivered %v, want the field under the explicit grant's slot GH_ACME_ACCOUNT_ID", name, got)
		}
		if _, wrong := got["GH_DEFAULT_ACCOUNT_ID"]; wrong {
			t.Errorf("%s: field derived its prefix from credentials.name instead of the resolved slot: %v", name, got)
		}
	}
	for name, got := range allConsumers(t, db, e.agentB) {
		if got["GH_DEFAULT_ACCOUNT_ID"] != "12345" {
			t.Errorf("%s: delivered %v, want the crew-link name as the prefix", name, got)
		}
	}
}

// TestFieldDelivery_DerivedNameCollisionDoesNotWin is the collision-safety
// property. A credential named AWS carrying a `region` field derives
// AWS_REGION — and another credential is already delivered under exactly that
// name. Defined behaviour: the FIELD loses. The delivered variable keeps the
// value the operator explicitly asked for, and the field is dropped rather than
// silently overwriting it.
//
// Fail closed, for the same reason binding resolution does: a missing
// AWS_REGION is diagnosable from inside the container, a plausible-looking
// AWS_REGION holding another credential's value is not.
func TestFieldDelivery_DerivedNameCollisionDoesNotWin(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)

	seedCredentialEnc(t, db, e.wsID, e.userID, "fd-aws", "AWS", "aws-primary")
	seedCredentialField(t, db, "fd-aws", "region", "eu-central-1", false, 0)
	assignCredToAgent(t, db, "fd-aws", e.agentA, "AWS", 0)

	// A wholly unrelated credential that already owns AWS_REGION.
	seedCredentialEnc(t, db, e.wsID, e.userID, "fd-other", "OTHER", "somebody-elses-value")
	assignCredToAgent(t, db, "fd-other", e.agentA, "AWS_REGION", 0)

	for name, got := range allConsumers(t, db, e.agentA) {
		if got["AWS_REGION"] != "somebody-elses-value" {
			t.Errorf("%s: AWS_REGION = %q, want the explicitly granted credential's value — a field must not shadow a claimed variable",
				name, got["AWS_REGION"])
		}
		if got["AWS"] != "aws-primary" {
			t.Errorf("%s: the credential whose field was dropped must still be delivered; got %v", name, got)
		}
	}
}

// TestFieldDelivery_ReservedRuntimeNamesAreNeverShadowed covers the variables
// the agent runtime sets for itself. A field can never derive a bare PATH — the
// <SLOT>_<KEY> rule always prefixes — but it CAN reach CREWSHIP_AGENT_ID (slot
// CREWSHIP, key agent_id), HTTP_PROXY (slot HTTP, key proxy) or
// CLAUDE_CODE_OAUTH_TOKEN (slot CLAUDE_CODE, key oauth_token), and each of those
// is either an identity the server issued or the egress fence the sidecar
// enforces. Those are not "a variable got clobbered", they are an agent lying
// about who it is or leaving the allowlist.
func TestFieldDelivery_ReservedRuntimeNamesAreNeverShadowed(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)

	for i, tc := range []struct{ slot, key, derived string }{
		{"CREWSHIP", "agent_id", "CREWSHIP_AGENT_ID"},
		{"HTTP", "proxy", "HTTP_PROXY"},
		{"CLAUDE_CODE", "oauth_token", "CLAUDE_CODE_OAUTH_TOKEN"},
	} {
		credID := "fd-reserved-" + tc.derived
		seedCredentialEnc(t, db, e.wsID, e.userID, credID, tc.slot, "primary")
		seedCredentialField(t, db, credID, tc.key, "hijacked", false, 0)
		assignCredToAgent(t, db, credID, e.agentA, tc.slot, i)
	}

	for name, got := range allConsumers(t, db, e.agentA) {
		for _, derived := range []string{"CREWSHIP_AGENT_ID", "HTTP_PROXY", "CLAUDE_CODE_OAUTH_TOKEN"} {
			if _, hijacked := got[derived]; hijacked {
				t.Errorf("%s: a credential field was delivered as %s — the agent runtime owns that name", name, derived)
			}
		}
	}
}

// TestFieldDelivery_FieldCannotShadowAnotherFieldsName is the field-vs-field
// half: two credentials whose derived names land on the same string, because
// the slot boundary is not visible in the result (slot A_B + key c_d and slot A
// + key b_c_d both spell A_B_C_D). First claim wins, in the delivery order the
// consumers already use — priority, then source rank. Never merged, never
// last-write-wins, which would make the delivered value depend on row order in
// a table nobody sorts.
func TestFieldDelivery_FieldCannotShadowAnotherFieldsName(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)

	// Slot A_B with field c_d and slot A with field b_c_d both derive A_B_C_D.
	seedCredentialEnc(t, db, e.wsID, e.userID, "fd-first", "A_B", "first-primary")
	seedCredentialField(t, db, "fd-first", "c_d", "first-field", false, 0)
	assignCredToAgent(t, db, "fd-first", e.agentA, "A_B", 0)

	seedCredentialEnc(t, db, e.wsID, e.userID, "fd-second", "A", "second-primary")
	seedCredentialField(t, db, "fd-second", "b_c_d", "second-field", false, 0)
	assignCredToAgent(t, db, "fd-second", e.agentA, "A", 1)

	for name, got := range allConsumers(t, db, e.agentA) {
		if got["A_B_C_D"] != "first-field" {
			t.Errorf("%s: A_B_C_D = %q, want the first claim (priority 0) to hold", name, got["A_B_C_D"])
		}
		// Both credentials themselves must still arrive whole.
		if got["A_B"] != "first-primary" || got["A"] != "second-primary" {
			t.Errorf("%s: a dropped field took its credential with it: %v", name, got)
		}
	}
}

// TestFieldDelivery_InvalidDerivedNameIsDropped covers the legacy crew-link
// source, which delivers under credentials.name — a NAME, not a slot, so it may
// contain characters no environment variable can.
//
// This test used to seed "github-acme" and assert that the credential arrived
// under its raw name while its part was dropped, because "github-acme_REGION"
// is not a legal identifier. #1657 removed that case: the slot is normalised to
// GITHUB_ACME before parts are attached, so the part now derives
// GITHUB_ACME_REGION and lands (see TestCrewDelivery_FieldsHangOffTheNormalisedSlot).
//
// What survives is the name from which no variable can be derived at all. The
// credential has no slot, so its parts have nothing to hang off — and the
// credential itself is not delivered either, so the agent gets neither half
// rather than a part whose prefix names nothing.
func TestFieldDelivery_InvalidDerivedNameIsDropped(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "fd-dash", "github acme", "tok")
	seedCredentialField(t, db, "fd-dash", "region", "eu-central-1", false, 0)
	linkCredToCrew(t, db, "fd-dash", e.crewA)

	for name, got := range allConsumers(t, db, e.agentA) {
		if len(got) != 0 {
			t.Errorf("%s: delivered %v, want nothing — no variable can be derived from the "+
				"credential's name, so neither it nor its part has a name to arrive under", name, got)
		}
	}
}

// TestFieldDelivery_SecretDecryptedNonSecretPassedThrough pins the two-column
// contract end to end: a secret part goes through the SAME decrypt helper the
// primary value does, a non-secret part is handed over as stored. The failure
// this catches is a reader that treats one column as the other — either
// delivering ciphertext as if it were a region, or trying to AEAD-open a
// plaintext and dropping the part.
func TestFieldDelivery_SecretDecryptedNonSecretPassedThrough(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "fd-mix", "SVC", "primary")
	seedCredentialField(t, db, "fd-mix", "account_id", "acct-42", false, 0)
	seedCredentialField(t, db, "fd-mix", "passphrase", "hunter2", true, 1)
	assignCredToAgent(t, db, "fd-mix", e.agentA, "SVC", 0)

	// The non-secret part really is cleartext at rest — the property the PRD
	// commits to, and the reason the UI can sort on it without a per-row open.
	var stored, encStored sql.NullString
	if err := db.QueryRow(`SELECT value, encrypted_value FROM credential_fields
		WHERE credential_id = 'fd-mix' AND key = 'account_id'`).Scan(&stored, &encStored); err != nil {
		t.Fatalf("read stored field: %v", err)
	}
	if stored.String != "acct-42" || encStored.Valid {
		t.Errorf("non-secret field at rest = (value=%q, encrypted=%v), want cleartext in value only", stored.String, encStored.Valid)
	}

	for name, got := range allConsumers(t, db, e.agentA) {
		if got["SVC_ACCOUNT_ID"] != "acct-42" {
			t.Errorf("%s: SVC_ACCOUNT_ID = %q, want the stored cleartext", name, got["SVC_ACCOUNT_ID"])
		}
		if got["SVC_PASSPHRASE"] != "hunter2" {
			t.Errorf("%s: SVC_PASSPHRASE = %q, want the decrypted secret", name, got["SVC_PASSPHRASE"])
		}
	}

	// And the classification survives the trip, so the orchestrator can decide
	// per part whether the sidecar's isolation applies.
	for _, c := range delegationCreds(t, db, e.agentA) {
		for _, f := range c.Fields {
			if f.EnvVar == "SVC_PASSPHRASE" && !f.IsSecret {
				t.Error("the secret part arrived flagged as non-secret; every downstream isolation decision keys off that flag")
			}
			if f.EnvVar == "SVC_ACCOUNT_ID" && f.IsSecret {
				t.Error("the non-secret part arrived flagged as secret")
			}
		}
	}
}

// TestFieldDelivery_UndeliverableCredentialDeliversNoFields keeps the parts
// under the same gate the value has always had. A revoked or soft-deleted
// credential whose parts kept flowing would be the worst kind of half-revoke:
// the operator sees the credential gone from the vault while the container
// still holds its access key id and region.
func TestFieldDelivery_UndeliverableCredentialDeliversNoFields(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)

	seedCredentialEnc(t, db, e.wsID, e.userID, "fd-deleted", "DELETED", "v")
	seedCredentialField(t, db, "fd-deleted", "region", "eu-central-1", false, 0)
	assignCredToAgent(t, db, "fd-deleted", e.agentA, "DELETED", 0)
	execOrFatal(t, db, `UPDATE credentials SET deleted_at = datetime('now') WHERE id = 'fd-deleted'`)

	seedCredentialEnc(t, db, e.wsID, e.userID, "fd-revoked", "REVOKED", "v")
	seedCredentialField(t, db, "fd-revoked", "region", "us-east-1", false, 0)
	assignCredToAgent(t, db, "fd-revoked", e.agentA, "REVOKED", 1)
	execOrFatal(t, db, `UPDATE credentials SET status = 'REVOKED' WHERE id = 'fd-revoked'`)

	for name, got := range allConsumers(t, db, e.agentA) {
		if len(got) != 0 {
			t.Errorf("%s: delivered %v, want nothing — neither value nor parts survive a revoke", name, got)
		}
	}
}

// TestFieldDelivery_CrossTenantFieldsNeverAppear carries the tenancy property to
// the parts. The fields query is keyed by credential id alone, so if it ever ran
// over a set that was not already workspace-filtered, a leaked id would be
// enough to pull another tenant's cleartext identifiers. Planted the way
// credentials_crew_delivery_test.go plants it: guards dropped, foreign row
// linked, delivery still refuses.
func TestFieldDelivery_CrossTenantFieldsNeverAppear(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)

	execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('fd-ws-other', 'Other', 'other')`)
	seedCredentialEnc(t, db, "fd-ws-other", e.userID, "fd-foreign", "FOREIGN", "other-tenant-secret")
	seedCredentialField(t, db, "fd-foreign", "account_id", "other-tenant-account", false, 0)

	execOrFatal(t, db, `DROP TRIGGER IF EXISTS trg_credential_crews_workspace_check`)
	execOrFatal(t, db, `DROP TRIGGER IF EXISTS trg_credential_crews_workspace_check_upd`)
	execOrFatal(t, db, `INSERT INTO credential_crews (credential_id, crew_id) VALUES ('fd-foreign', ?)`, e.crewA)

	for name, got := range allConsumers(t, db, e.agentA) {
		if len(got) != 0 {
			t.Errorf("%s: another tenant's credential parts were delivered: %v", name, got)
		}
	}

	// And the second isolation axis: crew B's agent sees nothing of crew A's.
	seedCredentialEnc(t, db, e.wsID, e.userID, "fd-crew-a", "CREW_A", "v")
	seedCredentialField(t, db, "fd-crew-a", "account_id", "crew-a-account", false, 0)
	linkCredToCrew(t, db, "fd-crew-a", e.crewA)
	for name, got := range allConsumers(t, db, e.agentB) {
		if _, leaked := got["CREW_A_ACCOUNT_ID"]; leaked {
			t.Errorf("%s: crew A's field crossed to a crew B agent: %v", name, got)
		}
	}
}

// TestDeliveredFieldEnvVar documents the rule as a table, so the contract the
// docs promise is readable in one place and a change to it has to be deliberate.
func TestDeliveredFieldEnvVar(t *testing.T) {
	for _, tc := range []struct{ slot, key, want string }{
		{"AWS", "access_key_id", "AWS_ACCESS_KEY_ID"},
		{"GH_TOKEN", "account_id", "GH_TOKEN_ACCOUNT_ID"},
		{"SVC", "region", "SVC_REGION"},
	} {
		if got := deliveredFieldEnvVar(tc.slot, tc.key); got != tc.want {
			t.Errorf("deliveredFieldEnvVar(%q, %q) = %q, want %q", tc.slot, tc.key, got, tc.want)
		}
	}
}
