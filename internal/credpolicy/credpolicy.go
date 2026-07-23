// Package credpolicy is the single source of truth for how each credential
// TYPE is delivered to an agent and whether Keeper gates it. Both the API
// resolver (which withholds plaintext before it leaves the server) and the
// orchestrator (which delivers values as env vars / files / proxy injection)
// consult this one table, so the security posture of a credential type — or of
// a new service credential among the thousands a workspace might hold — is
// governed by one ROW here, not by a "SECRET"-shaped special case duplicated
// across every delivery path.
//
// Design intent (fail-safe by construction): a type with no explicit row is
// treated as the MOST sensitive kind — not delivered to the agent and gated
// behind Keeper. Adding a new credential type that must reach agents is a
// deliberate act (add a row); forgetting to classify one leaks nothing.
//
// Keep the policy map in sync with the credential type enum in
// internal/api/credentials_types.go.
package credpolicy

// DeliveryMode describes the primary channel by which a credential value
// reaches the agent when it IS delivered (i.e. Keeper off, or the type is not
// Keeper-gated). It is descriptive: a couple of types ride more than one
// channel (a CLI_TOKEN is written as a file AND injected as an env var), and
// DeliveryMode names the file/secret-material channel that the /secrets
// tmpfs + cleanup accounting cares about.
type DeliveryMode string

const (
	// DeliveryFile: written as a 0400 file under /secrets/<agent-slug>/ (plus
	// an env var pointing at the path). The channel for opaque secret material.
	DeliveryFile DeliveryMode = "file"
	// DeliveryEnv: injected directly as an environment-variable value the agent
	// process can read.
	DeliveryEnv DeliveryMode = "env"
	// DeliveryProxy: never enters the agent; the sidecar reverse-proxy swaps a
	// dummy placeholder for the real value mid-flight (LLM provider API keys).
	DeliveryProxy DeliveryMode = "proxy"
	// DeliveryNone: not delivered to the agent at all — the fail-safe posture
	// for an unknown/unclassified type.
	DeliveryNone DeliveryMode = "none"
)

// TypePolicy declares one credential type's delivery posture.
type TypePolicy struct {
	// Delivery is the primary channel the value reaches the agent through when
	// it is delivered.
	Delivery DeliveryMode
	// KeeperGated: when Keeper is enabled, the value is WITHHELD from every
	// delivery path (env, file, and MCP env injection) and the agent must fetch
	// it via /keeper/request. The resolver blanks the plaintext so it never
	// leaves the API process; the orchestrator delivery gates are
	// defense-in-depth on top of that.
	KeeperGated bool
}

// FileMounted reports whether this type's primary channel is a /secrets file.
func (p TypePolicy) FileMounted() bool { return p.Delivery == DeliveryFile }

// policies maps every KNOWN credential type to its delivery posture. Only
// SECRET is Keeper-gated today; GENERIC_SECRET and the vault types are explicit
// rows (not comments) so their NON-gating is a deliberate, reviewable decision
// rather than an omission.
var policies = map[string]TypePolicy{
	"SECRET":         {Delivery: DeliveryFile, KeeperGated: true},
	"GENERIC_SECRET": {Delivery: DeliveryFile, KeeperGated: false},
	"CLI_TOKEN":      {Delivery: DeliveryFile, KeeperGated: false},
	"USERPASS":       {Delivery: DeliveryFile, KeeperGated: false},
	"SSH_KEY":        {Delivery: DeliveryFile, KeeperGated: false},
	"CERTIFICATE":    {Delivery: DeliveryFile, KeeperGated: false},
	"API_KEY":        {Delivery: DeliveryProxy, KeeperGated: false},
	"AI_CLI_TOKEN":   {Delivery: DeliveryEnv, KeeperGated: false},
	"OAUTH2":         {Delivery: DeliveryEnv, KeeperGated: false},
	"ENDPOINT_URL":   {Delivery: DeliveryEnv, KeeperGated: false},
}

// fallback is the posture for an UNKNOWN type: withheld (Keeper-gated) and not
// delivered. Fail safe — an unclassified type leaks nothing to the agent.
var fallback = TypePolicy{Delivery: DeliveryNone, KeeperGated: true}

// For returns the delivery policy for a credential type, or the fail-safe
// fallback for an unrecognised type.
func For(credType string) TypePolicy {
	if p, ok := policies[credType]; ok {
		return p
	}
	return fallback
}

// IsKeeperGated reports whether Keeper withholds this credential type from
// every delivery path when Keeper is enabled.
func IsKeeperGated(credType string) bool { return For(credType).KeeperGated }

// Known reports whether the type has an explicit policy row (vs the fail-safe
// fallback). Used by the cross-package test that keeps this table in lockstep
// with the credential type enum.
func Known(credType string) bool { _, ok := policies[credType]; return ok }
