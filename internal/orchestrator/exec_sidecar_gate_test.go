package orchestrator

import (
	"strings"
	"testing"
)

// What "Keeper off" means for credentials, pinned where it is decided.
//
// The API path fails closed — an unconfigured instance answers DENY "Keeper not
// configured" to every credential request. Boot delivery does the opposite:
// buildCredFileScript withholds Keeper-gated types ONLY when Keeper is on, so
// with it off a SECRET is written into the container at start. No judgement, no
// escalation, no audit row.
//
// That is a deliberate trade rather than an oversight — an instance with no
// judge cannot gate anything, and refusing every credential would make it
// useless. But it lived in a comment, so it was one refactor away from
// inverting silently, and nothing would have failed.
func TestKeeperOn_WithholdsSecretsFromBootDelivery(t *testing.T) {
	creds := []Credential{{EnvVarName: "PROD_DB_ADMIN", Type: "SECRET", PlainValue: "s3cr3t"}}

	script, written, _, err := buildCredFileScript(creds, "/secrets/riley", true)
	if err != nil {
		t.Fatalf("buildCredFileScript: %v", err)
	}
	if written != 0 {
		t.Errorf("wrote %d credential files with Keeper ON; a SECRET must be fetched "+
			"through the judge, not handed over at boot", written)
	}
	if strings.Contains(script, "s3cr3t") {
		t.Error("the secret value is in the boot script with Keeper ON")
	}
}

// And with it off the secret IS delivered — the behaviour the admin screen now
// states out loud. Asserting it keeps the UI copy and the code honest with each
// other: if this ever changes, the sentence on the Keeper tab becomes a lie and
// this test is what says so.
func TestKeeperOff_DeliversSecretsAtBoot(t *testing.T) {
	creds := []Credential{{EnvVarName: "PROD_DB_ADMIN", Type: "SECRET", PlainValue: "s3cr3t"}}

	_, written, _, err := buildCredFileScript(creds, "/secrets/riley", false)
	if err != nil {
		t.Fatalf("buildCredFileScript: %v", err)
	}
	if written == 0 {
		t.Error("nothing delivered with Keeper OFF — either the gate inverted or " +
			"the admin screen's warning is now wrong")
	}
}
