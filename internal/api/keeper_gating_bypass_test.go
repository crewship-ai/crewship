package api

// Explicit bypass tests for the Keeper SECRET-gating invariant at the API
// resolver chokepoint (#1486). Sibling of
// internal/orchestrator/secret_gating_bypass_test.go, which covers the four
// delivery gates; this file covers the three sites in agent_config.go:
//
//	:292   the withhold log + the withholdKeeperSecretValues call in resolve
//	:1576  withholdKeeperSecretValues — blanks the plaintext
//	:1618  buildKeeperBlock — tells the agent which credentials to ask for
//
// This tier matters more than the orchestrator's, not less: the comment on
// withholdKeeperSecretValues says withholding here "keeps the plaintext from
// ever leaving the API process, which is what makes the orchestrator env/file/
// MCP gates defense-in-depth rather than the sole line". If this chokepoint
// leaks, the value is already on the wire.
//
// The existing tests (agent_config_keeper_withhold_unit_test.go,
// keeper_block_alignment_test.go) all use the literal type "SECRET". Every case
// below attacks the SPELLING or the CLASSIFICATION of the type instead, which
// is the only part of the decision an attacker or an accident can move.
//
// Each test states an attacker's plan and proves it fails. Teeth verified by
// mutation — matrix in the PR body.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/credpolicy"
)

const (
	keeperBypassValue = "api-bypass-canary-77b210"
	keeperBypassPart  = "api-bypass-part-canary-31da"
)

// keeperBypassTypes are the type spellings that miss the credpolicy map key and
// therefore land on the fail-safe fallback. Two families:
//
//   - case/whitespace variants of SECRET — reachable because the credentials
//     type column was unvalidated plain TEXT for most of the product's life
//     (see internal/api/credentials_types.go), so pre-enum rows can hold them;
//   - types nobody classified — a connector, a migration or a future feature
//     that adds a type and forgets the credpolicy row, plus the empty string a
//     row carries when nothing set it.
var keeperBypassTypes = []string{
	"secret", "Secret", "sEcReT", "SECRET ", " SECRET", "SECRET\n",
	"VAULT_HANDLE", "HSM_KEY", "GENERIC_SECRET_V2", "",
}

func keeperBypassCaseName(ty string) string {
	if ty == "" {
		return "(empty type column)"
	}
	return strings.ReplaceAll(strings.ReplaceAll(ty, "\n", "\\n"), "\t", "\\t")
}

// gatedEntry is the credential each attack ships: secret plaintext, one SECRET
// part, one identifier part.
func gatedEntry(ty string) mcpCredEntry {
	return mcpCredEntry{
		ID:     "cred-bypass",
		EnvVar: "PROD_DB_PASSWORD",
		Value:  keeperBypassValue,
		Type:   ty,
		Fields: []mcpCredFieldEntry{
			{Key: "passphrase", EnvVar: "PROD_DB_PASSWORD_PASSPHRASE", Value: keeperBypassPart, IsSecret: true},
			{Key: "region", EnvVar: "PROD_DB_PASSWORD_REGION", Value: "eu-central-1", IsSecret: false},
		},
	}
}

// ATTACKER'S PLAN: the resolver blanks gated plaintext by looking my credential
// type up in a map, with an exact string match. I control how that column is
// SPELLED — or I get an unclassified type into it — so the lookup misses and my
// plaintext is serialized into the resolved agent config and shipped out of the
// API process. From there every orchestrator gate is moot: the value is already
// on the wire.
//
// Why it fails: a map miss lands on credpolicy's fallback, which is gated.
//
// The assertion is made on the SERIALIZED config, not on the struct fields,
// because "never leaves the API process" is a statement about what goes on the
// wire. A future field that carries the plaintext under another name would pass
// a field-by-field check and fail this one.
func TestBypass_MisspelledOrUnclassifiedTypeIsStillWithheldByTheResolver(t *testing.T) {
	t.Parallel()
	for _, ty := range keeperBypassTypes {
		t.Run("type="+keeperBypassCaseName(ty), func(t *testing.T) {
			t.Parallel()
			if credpolicy.Known(ty) {
				t.Fatalf("credpolicy.Known(%q) = true — this type now has an explicit row, "+
					"so it is no longer exercising the fail-safe fallback", ty)
			}
			creds := []mcpCredEntry{gatedEntry(ty)}

			withholdKeeperSecretValues(creds)

			wire, err := json.Marshal(creds)
			if err != nil {
				t.Fatalf("marshal resolved creds: %v", err)
			}
			if strings.Contains(string(wire), keeperBypassValue) {
				t.Errorf("type %q: the gated plaintext is still in the serialized agent "+
					"config after withholdKeeperSecretValues — it leaves the API process "+
					"(agent_config.go:1576). Payload: %s", ty, wire)
			}
			if strings.Contains(string(wire), keeperBypassPart) {
				t.Errorf("type %q: the gated credential's SECRET PART is still in the "+
					"serialized agent config. A passphrase delivered under a derived name "+
					"routes around /keeper/request just as well as the value does", ty)
			}
			// The identifier part must survive: Keeper gates secrets, not the
			// shape of the account. Asserting it keeps the fix from being
			// "blank everything", which would pass the two checks above while
			// breaking every multi-part credential in the install base.
			if !strings.Contains(string(wire), "eu-central-1") {
				t.Errorf("type %q: the NON-secret identifier part was withheld too. Keeper "+
					"gates credential material, not region/account-id/host — this is the "+
					"same footing credentials.username has always had", ty)
			}
		})
	}
}

// ATTACKER'S PLAN: I cannot get the value, so I settle for making it
// unreachable and unexplained. If withholdKeeperSecretValues blanks my
// credential but buildKeeperBlock does not LIST it, the agent is handed an
// empty variable with no [CREDENTIAL ACCESS CONTROL] entry naming it: it reads
// the absence as "not provisioned", gives up, and never opens a /keeper/request
// — so the request/judge/audit path that would have recorded my activity is
// never entered. The mirror plan is worse: a credential LISTED as withheld but
// not actually blanked makes the prompt's "You do NOT have these credentials in
// your environment" a lie the agent acts on.
//
// Why both fail: the two functions read the same credpolicy predicate. Asserted
// DIFFERENTIALLY — the blanked set and the listed set are computed from the
// same input and compared — rather than against a hand-written list of type
// names, which is what drifted in the first place.
func TestBypass_KeeperBlockAndWithholdingCannotDisagreeAboutWhatIsGated(t *testing.T) {
	t.Parallel()
	h := &InternalHandler{logger: newTestLogger()}

	types := append([]string{
		"SECRET", "GENERIC_SECRET", "CLI_TOKEN", "USERPASS", "SSH_KEY",
		"CERTIFICATE", "API_KEY", "AI_CLI_TOKEN", "OAUTH2", "ENDPOINT_URL",
	}, keeperBypassTypes...)

	for _, ty := range types {
		t.Run("type="+keeperBypassCaseName(ty), func(t *testing.T) {
			t.Parallel()
			entry := gatedEntry(ty)
			entry.EnvVar = "PROD_DB_PASSWORD"

			// The block is built BEFORE the withholding, exactly as
			// agent_config.go:280-297 orders it — build the prompt while the
			// plaintext still exists to name, then blank.
			block := h.buildKeeperBlock("riley", []mcpCredEntry{entry})
			listed := strings.Contains(block, "PROD_DB_PASSWORD")

			creds := []mcpCredEntry{entry}
			withholdKeeperSecretValues(creds)
			blanked := creds[0].Value == ""

			switch {
			case blanked && !listed:
				t.Errorf("type %q: the value was withheld but the credential is not named "+
					"in the [CREDENTIAL ACCESS CONTROL] block. The agent sees an empty "+
					"variable, reads it as 'not provisioned', and never opens a "+
					"/keeper/request — so nothing is audited (agent_config.go:1618)", ty)
			case listed && !blanked:
				t.Errorf("type %q: the block tells the agent it does NOT have this "+
					"credential in its environment, and the value was delivered anyway. "+
					"The prompt is a lie and the audit gate is bypassed by simply reading "+
					"the env (agent_config.go:1576)", ty)
			}
			// And the set really is the gated set, not some third thing.
			if blanked != credpolicy.IsKeeperGated(ty) {
				t.Errorf("type %q: withheld=%v but credpolicy.IsKeeperGated=%v — the "+
					"resolver is no longer deciding from the one table", ty, blanked, credpolicy.IsKeeperGated(ty))
			}
		})
	}
}

// ATTACKER'S PLAN, run end to end over the real HTTP resolver
// (agent_config.go:292): everything above is a unit test on a helper, and a
// helper that withholds correctly is worthless if the resolve path stops
// calling it, or calls it before the value is re-populated, or serialises the
// plaintext through some other field on the way out. So: I write my credential
// straight into the `credentials` table with type 'secret' — no API call, no
// enum validation, the shape a pre-enum row already has — turn Keeper on, and
// read what /internal/agents/{id}/resolve hands the orchestrator.
//
// Why it fails: the fallback is gated, the resolver blanks it, and the body
// carries no plaintext. Asserted on the whole response body rather than on the
// credential's "value" key, because a leak through any other field is the same
// leak.
//
// The existing end-to-end tests in agent_config_keeper_withhold_sec_test.go
// cover exactly one type spelling: the literal "SECRET".
func TestBypass_MisspelledSecretTypeIsWithheldByTheLiveResolver(t *testing.T) {
	for _, ty := range []string{"secret", "Secret", "SECRET ", "VAULT_HANDLE", ""} {
		t.Run("type="+keeperBypassCaseName(ty), func(t *testing.T) {
			if credpolicy.Known(ty) {
				t.Fatalf("credpolicy.Known(%q) = true — no longer the fallback path", ty)
			}
			h, wsID, _, agentID := covCfg2Rig(t)
			h.SetKeeperEnabled(true)
			seedAgentSecret(t, h, wsID, agentID, "ProdKey", "PROD_DB_PASSWORD", ty)

			rr := covCfg2Resolve(t, h, agentID)
			if rr.Code != http.StatusOK {
				t.Fatalf("resolve status = %d; body=%s", rr.Code, rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), withholdSecretPlaintext) {
				t.Errorf("type %q: the resolver serialized the plaintext of a Keeper-gated "+
					"credential into the agent config with Keeper ON. It has left the API "+
					"process; every orchestrator gate downstream is now moot "+
					"(agent_config.go:292). body=%s", ty, rr.Body.String())
			}
			// The entry itself must survive, blanked — a credential dropped
			// from the payload entirely tells the agent nothing exists to ask
			// for, and would also make the assertion above pass vacuously.
			val, ok := credValueFor(t, rr.Body.Bytes(), "PROD_DB_PASSWORD")
			if !ok {
				t.Fatalf("type %q: the credential vanished from the resolved config; the "+
					"agent is never told it can request it, and the leak assertion above "+
					"had nothing to catch", ty)
			}
			if val != "" {
				t.Errorf("type %q: resolved value = %q, want blank", ty, val)
			}
		})
	}
}
