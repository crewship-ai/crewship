package api

// Delivering the PARTS of a multi-part credential — PRD-CREDENTIALS-V2 §2.2, P4.
//
// credential_fields (P4's storage half) gave a credential named parts: AWS =
// access key id + secret + region, a service account = blob + filename. Nothing
// read them. The credential reached the container as its single legacy value and
// the parts were invisible, which made the table a place to record facts rather
// than a way to deliver them.
//
// This file is the reading half, and it lives next to credential_delivery.go
// deliberately: the SET of credentials an agent receives is derived in ONE
// place, and so is the NAME each part arrives under. #1373's first increment is
// the standing cautionary tale — the lease gate was written into /keeper/execute
// and three other resolvers kept reading agent_credentials with no expiry filter
// at all. A second place that derives part names would drift the same way.
//
// THE NAMING RULE
//
//	<SLOT>_<KEY UPCASED>
//
// SLOT is whatever the CREDENTIAL resolved to — the explicit grant's
// env_var_name, the binding's slot, or the legacy credentials.name — never
// anything the field itself decides. That is the whole point: §2.5b makes the
// slot the property that distinguishes ten GitHub accounts in one workspace, so
// a part that derived its own prefix would collapse them back into one. Bind
// GH_TOKEN to account A and GH_ACME to account B and their `account_id` parts
// arrive as GH_TOKEN_ACCOUNT_ID and GH_ACME_ACCOUNT_ID, matching the values
// they belong to.
//
// The key is already lower_snake_case (credentialFieldKeyRe enforces it at
// write time, precisely so `Region` and `region` cannot both exist and then
// fight over REGION), so upcasing is total and reversible-looking to a reader.
//
// WHAT HAPPENS WHEN THE NAME IS NOT FREE
//
// The part is DROPPED and the conflict is reported to the caller's logger. Not
// renamed, not suffixed, not allowed to win. Four ways a derived name is
// refused:
//
//	no slot            — the credential has no delivery name at all, so "_REGION"
//	                     is the only thing derivable, and that is a variable
//	                     nobody asked for rather than a part of anything.
//	not an env var     — the legacy crew-link source delivers under
//	                     credentials.name, a NAME, which may hold a dash:
//	                     "github-acme_REGION" is not a legal identifier. This one
//	                     is load-bearing beyond tidiness — buildCredFileScript
//	                     returns an error for the first bad name it sees and that
//	                     error aborts the WHOLE credential-file script, so one
//	                     malformed part would leave the agent with no secrets.
//	reserved           — the agent runtime sets these itself (identity, the proxy
//	                     fence, the OAuth destination). A part landing on one is
//	                     not "a variable got clobbered", it is an agent lying
//	                     about which agent it is, or leaving the egress allowlist.
//	already claimed    — by a delivered credential's own slot, or by an earlier
//	                     part. First claim holds.
//
// Dropping is the fail-closed choice and matches the doctrine the binding
// resolver already records: a MISSING AWS_REGION is diagnosable from inside the
// container in one command; an AWS_REGION that plausibly holds another
// credential's value is not diagnosable at all, and the agent will act on it.

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

// deliveryEnvVarRE is the shape a name must have to be exported into a
// container. Deliberately the same expression as the orchestrator's
// envVarNameRE (internal/orchestrator/exec.go), which is the gate that actually
// rejects a name at mount time — duplicated rather than exported because the
// point is to refuse HERE, where a refusal costs one dropped part, instead of
// there, where it costs the whole script.
var deliveryEnvVarRE = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// reservedDeliveryEnvVarPrefix marks the namespace the server issues to the
// agent about itself (CREWSHIP_AGENT_ID, CREWSHIP_CREW_ID, CREWSHIP_CHAT_ID,
// CREWSHIP_CREW_SHARED — baseAgentEnv). A prefix rule rather than a list, so a
// variable added to that block later is protected the day it is added rather
// than the day someone remembers this file.
const reservedDeliveryEnvVarPrefix = "CREWSHIP_"

// reservedDeliveryEnvVars are the names the container runtime owns. Two groups,
// both chosen because a credential part winning them changes behaviour rather
// than data:
//
//   - the process environment every tool trusts (HOME, PATH, the loader and
//     shell hooks). Note that the <SLOT>_<KEY> rule can never DERIVE a bare
//     PATH — there is always a prefix — so these are belt to that braces, kept
//     because the rule is one refactor away from someone "simplifying" the
//     prefix out for single-field credentials.
//   - the delivery machinery itself: the sidecar proxy fence (an agent that
//     overwrites HTTP_PROXY leaves the crew's egress allowlist), the provider
//     base URLs the reverse proxy publishes, the OAuth destination resolveEnvVar
//     redirects AI_CLI_TOKEN to, and the OpenCode config blob.
var reservedDeliveryEnvVars = map[string]struct{}{
	"HOME": {}, "PATH": {}, "SHELL": {}, "IFS": {}, "ENV": {}, "BASH_ENV": {},
	"LD_PRELOAD": {}, "LD_LIBRARY_PATH": {},
	"HTTP_PROXY": {}, "HTTPS_PROXY": {}, "NO_PROXY": {},
	"CLAUDE_CODE_OAUTH_TOKEN": {}, "CLAUDE_CODE_DISABLE_AUTOUPDATE": {},
	"ANTHROPIC_BASE_URL": {}, "OPENAI_BASE_URL": {}, "GOOGLE_GEMINI_BASE_URL": {},
	"OPENCODE_CONFIG_CONTENT": {},
}

// deliveredFieldEnvVar is THE naming rule. One function, so the docs, the tests
// and the delivery paths cannot each hold a slightly different version of it.
func deliveredFieldEnvVar(slot, key string) string {
	return slot + "_" + strings.ToUpper(key)
}

func isReservedDeliveryEnvVar(name string) bool {
	if strings.HasPrefix(name, reservedDeliveryEnvVarPrefix) {
		return true
	}
	_, reserved := reservedDeliveryEnvVars[name]
	return reserved
}

// deliveredCredentialField is one part of a delivered credential, still in
// storage form. Exactly one of Value / EncryptedValue is populated, decided by
// IsSecret — the same split the table's CHECK constraint enforces, carried all
// the way to the consumer so no reader has to guess which column it is holding.
type deliveredCredentialField struct {
	Key            string
	EnvVar         string
	Value          string // cleartext, non-secret parts only
	EncryptedValue string // AEAD, secret parts only
	IsSecret       bool
}

// deliveredFieldConflict records a part that was refused a name. Returned rather
// than logged here because loadDeliveredCredentials has no logger and should not
// grow one: a silent drop is the failure mode this whole design exists to avoid,
// so the caller — which has both a logger and the agent's identity — reports it.
type deliveredFieldConflict struct {
	CredentialID string
	Key          string
	EnvVar       string
	Reason       string
}

// plainCredentialField is a part with its value resolved to cleartext, ready to
// hand to a delivery consumer.
type plainCredentialField struct {
	Key      string
	EnvVar   string
	Value    string
	IsSecret bool
}

// attachDeliveredCredentialFields loads every delivered credential's parts,
// derives their names and resolves collisions, in one pass over the whole
// delivered set.
//
// One pass over the WHOLE set is what makes collision handling meaningful. Doing
// it per credential would let a part claim a name that another credential is
// about to be delivered under, and which of the two won would depend on the
// order the consumer happened to iterate in.
//
// The claim table is seeded with every delivered credential's own slot BEFORE
// any part is considered, so a part can never take a name a credential holds,
// regardless of ordering. Parts are then considered in delivery order (priority,
// then source rank — the order the consumers already use) and claim as they go.
func attachDeliveredCredentialFields(ctx context.Context, db *sql.DB, delivered []deliveredCredential) error {
	if len(delivered) == 0 {
		return nil
	}

	ids := make([]any, 0, len(delivered))
	seenID := make(map[string]bool, len(delivered))
	for _, d := range delivered {
		if !seenID[d.ID] {
			seenID[d.ID] = true
			ids = append(ids, d.ID)
		}
	}

	// The credential ids come from agentDeliveredCredentialsSQL, which is already
	// workspace- and status-filtered, so this query needs no tenancy predicate of
	// its own — and must never be handed a set from anywhere else. A soft-deleted
	// or revoked credential contributes no id here, so its parts are unreachable
	// by construction rather than by a second filter that could be forgotten.
	query := `SELECT credential_id, key, COALESCE(value, ''), COALESCE(encrypted_value, ''), is_secret
		FROM credential_fields
		WHERE credential_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `)
		ORDER BY credential_id, ordinal ASC, key ASC`

	rows, err := db.QueryContext(ctx, query, ids...)
	if err != nil {
		return fmt.Errorf("query credential fields: %w", err)
	}
	defer rows.Close()

	byCred := make(map[string][]deliveredCredentialField)
	for rows.Next() {
		var credID string
		var f deliveredCredentialField
		var isSecret int
		if err := rows.Scan(&credID, &f.Key, &f.Value, &f.EncryptedValue, &isSecret); err != nil {
			return fmt.Errorf("scan credential field: %w", err)
		}
		f.IsSecret = isSecret != 0
		byCred[credID] = append(byCred[credID], f)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate credential fields: %w", err)
	}
	if len(byCred) == 0 {
		// The overwhelmingly common case: no credential in this delivery has
		// parts. Return with every Fields slice still nil, so the delivered set
		// is byte-identical to what it was before this file existed.
		return nil
	}

	claimedBy := make(map[string]string, len(delivered))
	for _, d := range delivered {
		if d.EnvVar != "" {
			claimedBy[d.EnvVar] = "credential " + d.ID
		}
	}

	for i := range delivered {
		fields := byCred[delivered[i].ID]
		if len(fields) == 0 {
			continue
		}
		for _, f := range fields {
			name := deliveredFieldEnvVar(delivered[i].EnvVar, f.Key)
			var reason string
			switch {
			case delivered[i].EnvVar == "":
				reason = "the credential has no delivery slot to hang the field off"
			case !deliveryEnvVarRE.MatchString(name):
				reason = "derived name is not a valid environment variable name"
			case isReservedDeliveryEnvVar(name):
				reason = "derived name is reserved by the agent runtime"
			case claimedBy[name] != "":
				reason = "derived name is already claimed by " + claimedBy[name]
			}
			if reason != "" {
				delivered[i].FieldConflicts = append(delivered[i].FieldConflicts, deliveredFieldConflict{
					CredentialID: delivered[i].ID, Key: f.Key, EnvVar: name, Reason: reason,
				})
				continue
			}
			claimedBy[name] = "field " + f.Key + " of credential " + delivered[i].ID
			f.EnvVar = name
			delivered[i].Fields = append(delivered[i].Fields, f)
		}
	}
	return nil
}

// decryptDeliveredFields resolves a credential's parts to cleartext.
//
// The decrypt function is the caller's, so a secret part goes through EXACTLY
// the helper that opened the credential's own value on that path — the boot
// resolver's decryptCredential, the run loaders' encryption.Decrypt. Two
// different openers for two halves of one credential is how a key-rotation bug
// ends up affecting parts and not values, or the reverse.
//
// A non-secret part is passed through untouched. It is cleartext at rest by
// design (identifiers, not secrets — the reasoning credentials.username already
// records), and running it through an AEAD open would either error or, far
// worse, succeed on some future path that treats the plaintext as ciphertext.
//
// An error aborts the whole credential rather than dropping one part: an AWS key
// delivered without its secret is not a smaller credential, it is a credential
// that will fail at the point of use with an error about the wrong thing.
func decryptDeliveredFields(d deliveredCredential, decrypt func(string) (string, error)) ([]plainCredentialField, error) {
	if len(d.Fields) == 0 {
		return nil, nil
	}
	out := make([]plainCredentialField, 0, len(d.Fields))
	for _, f := range d.Fields {
		value := f.Value
		if f.IsSecret {
			dec, err := decrypt(f.EncryptedValue)
			if err != nil {
				return nil, fmt.Errorf("decrypt field %q of credential %s: %w", f.Key, d.ID, err)
			}
			value = dec
		}
		out = append(out, plainCredentialField{
			Key: f.Key, EnvVar: f.EnvVar, Value: value, IsSecret: f.IsSecret,
		})
	}
	return out, nil
}

// logDeliveredFieldConflicts reports every part that was refused a name.
//
// Called by each delivery consumer, once per resolution. A dropped part is
// invisible from inside the container — the agent simply finds no AWS_REGION —
// so this log line is the only place an operator can learn that the vault holds
// a part the runtime declined to deliver, and why. It names the env var, never
// the value.
func logDeliveredFieldConflicts(logger *slog.Logger, agentID string, delivered []deliveredCredential) {
	if logger == nil {
		return
	}
	for _, d := range delivered {
		for _, c := range d.FieldConflicts {
			logger.Warn("credential field not delivered — its derived environment variable name is unavailable",
				"agent_id", agentID, "credential_id", c.CredentialID,
				"field_key", c.Key, "env_var", c.EnvVar, "reason", c.Reason)
		}
	}
}
