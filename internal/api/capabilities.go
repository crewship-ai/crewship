package api

// Per-workspace_membership capability strings — the layer above
// canRole that lets a workspace admin grant individual MEMBER users
// reach into specific MANAGER+ actions (create a routine, create a
// skill, ...) without promoting them to the MANAGER tier.
//
// See PRD-SLASH-CAPABILITIES-2026.md for the full design. v109
// migration adds the storage column; the requireCapabilityOrForbid
// helper that enforces these strings lives in capabilities_check.go
// (commit 2). This file is constants + parse + bundle helpers only,
// shared between the migration backfill, the admin CLI, the slash
// command catalog, and the enforcement helper.

import (
	"encoding/json"
	"strings"
)

// Capability strings. Stable wire identifiers — once a capability
// appears in a customer database its name cannot change without a
// data migration. Add new ones; rename none.
const (
	// CapabilityChat is the baseline every member needs to talk to
	// agents at all. Default for new MEMBER + VIEWER rows.
	CapabilityChat = "chat"

	// CapabilityRoutineCreate gates creation of pipeline-schedule
	// rows (cron-driven routines). Matches the public MANAGER+
	// pipeline_schedules.go:91 gate; layered on top via the slash
	// command surface and the new sidecar /routines/schedules route.
	CapabilityRoutineCreate = "routine.create"

	// CapabilitySkillCreate gates skills.Generate + skills.Import.
	// Distinct from routine.create because skill authoring is the
	// higher-trust action (skills run inside agent prompts; routines
	// just schedule existing pipelines).
	CapabilitySkillCreate = "skill.create"

	// CapabilityCredentialCreate gates credential row creation —
	// fresh secret material entering the workspace vault. Should be
	// granted parsimoniously; most end users never need it.
	CapabilityCredentialCreate = "credential.create"

	// CapabilityCredentialRotate gates rotation of an existing
	// credential's value. Distinct from create so an admin can let
	// an oncall user rotate a leaked token without giving them
	// blanket "add anything to the vault" reach.
	CapabilityCredentialRotate = "credential.rotate"

	// CapabilityIssueCreate gates issue.Create. Issues are the
	// lowest-stakes write action — even chat-only members commonly
	// want to file a ticket from a conversation — so a default-grant
	// bundle includes this above the MEMBER tier.
	CapabilityIssueCreate = "issue.create"

	// CapabilityMemoryWrite gates writes to agent / crew / workspace
	// memory via the slash /remember surface. The HITL verifier
	// (PR #3 of MEMORY-ROADMAP-2026) still gates persistence; this
	// capability is the entry-point gate.
	CapabilityMemoryWrite = "memory.write"
)

// allCapabilities is the closed set of valid capability strings.
// Used by admin grant/revoke validators to reject typos before they
// reach the database, and by the slash-command catalog to filter the
// per-user list down to actions the platform actually understands.
//
// Adding a capability: append the constant + add the entry here.
// Removing a capability: don't — keep the constant for backwards
// compatibility with existing rows; mark it deprecated in the
// docstring and stop emitting it from new bundle defaults.
var allCapabilities = map[string]struct{}{
	CapabilityChat:             {},
	CapabilityRoutineCreate:    {},
	CapabilitySkillCreate:      {},
	CapabilityCredentialCreate: {},
	CapabilityCredentialRotate: {},
	CapabilityIssueCreate:      {},
	CapabilityMemoryWrite:      {},
}

// IsValidCapability reports whether the string is a known capability
// the server will accept. Admin commands call this before persisting
// a grant so a typo (`routine.creat` ← missing 'e') produces a
// rejection instead of a row that silently never matches the runtime
// constant.
func IsValidCapability(cap string) bool {
	_, ok := allCapabilities[cap]
	return ok
}

// AllCapabilities returns every capability the server understands.
// Used by the admin CLI's `crewship member capabilities --help` and
// by the Members grid in the dashboard to render the full checkbox
// list. Order is stable (alphabetical) so the UI doesn't reshuffle
// between calls.
func AllCapabilities() []string {
	out := make([]string, 0, len(allCapabilities))
	for c := range allCapabilities {
		out = append(out, c)
	}
	// Sort by lexical order so render output is stable across calls
	// and across Go map-iteration nondeterminism.
	sortStringsInPlace(out)
	return out
}

// CapabilityBundle is a named preset — "Chat User", "Power User",
// "Workspace Admin" — that maps to a fixed capability set. Admin CLI
// uses these so an operator can grant a common combination without
// listing every capability individually; SCIM/IdP integration in the
// future maps IdP group names to bundle names.
type CapabilityBundle string

const (
	// BundleChat = chat-only. Default for new MEMBER + VIEWER
	// memberships. Matches the post-migration v109 backfill for
	// MEMBER/VIEWER rows.
	BundleChat CapabilityBundle = "chat"

	// BundlePower adds the high-value end-user actions that don't
	// touch the credential vault. Suitable for trusted team members
	// who run their own routines and file their own issues.
	BundlePower CapabilityBundle = "power"

	// BundleAdmin grants the full capability set, including
	// credential mutation. Matches the post-migration v109 backfill
	// for OWNER + ADMIN rows.
	BundleAdmin CapabilityBundle = "admin"
)

// BundleCapabilities returns the capability strings the named
// bundle grants. Unknown bundle returns nil (caller treats as "do
// not change" or "invalid", per its own semantics).
//
// The MANAGER-equivalent bundle (the v109 backfill for MANAGER rows)
// is intentionally not exposed as a named bundle — MANAGERs get
// their capabilities via role-inheritance during the migration; the
// admin CLI doesn't surface it as a separate preset to avoid the
// "grant MANAGER bundle to a MEMBER" confusion.
func BundleCapabilities(b CapabilityBundle) []string {
	switch b {
	case BundleChat:
		return []string{CapabilityChat}
	case BundlePower:
		return []string{
			CapabilityChat,
			CapabilityRoutineCreate,
			CapabilityIssueCreate,
			CapabilityMemoryWrite,
		}
	case BundleAdmin:
		return []string{
			CapabilityChat,
			CapabilityRoutineCreate,
			CapabilitySkillCreate,
			CapabilityCredentialCreate,
			CapabilityCredentialRotate,
			CapabilityIssueCreate,
			CapabilityMemoryWrite,
		}
	default:
		return nil
	}
}

// AllBundles returns the ordered list of bundle names — used by the
// admin CLI to populate `crewship member preset <user> <bundle>`
// completions and by the dashboard Members UI to render the bundle
// quick-pick dropdown.
func AllBundles() []CapabilityBundle {
	return []CapabilityBundle{BundleChat, BundlePower, BundleAdmin}
}

// ParseCapabilities decodes the JSON-array TEXT shape stored in
// workspace_members.capabilities into a deduplicated set. Empty
// string, NULL-equivalent, or invalid JSON returns nil — callers
// treat nil as "no explicit capability set" and fall back to role-
// derived defaults via FallbackCapabilitiesForRole.
//
// Unknown capability strings in the stored JSON are dropped (not
// errored) so a downgrade-and-reupgrade across a v109+1 release that
// adds a capability doesn't lock a user out — we forget the
// future-version capability silently rather than reject the whole row.
func ParseCapabilities(jsonValue string) map[string]struct{} {
	jsonValue = strings.TrimSpace(jsonValue)
	if jsonValue == "" || jsonValue == "null" {
		return nil
	}
	var arr []string
	if err := json.Unmarshal([]byte(jsonValue), &arr); err != nil {
		return nil
	}
	if len(arr) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(arr))
	for _, s := range arr {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := allCapabilities[s]; !ok {
			continue // forward-compat drop
		}
		out[s] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SerializeCapabilities encodes a capability set back into the JSON-
// array TEXT shape for storage. Output is stable-ordered
// (alphabetical) so equal sets produce equal JSON regardless of map
// iteration order — that keeps diff-based audit logging meaningful.
// Empty / nil input returns the chat-only baseline so a row never
// regresses below the minimum needed to talk to an agent.
func SerializeCapabilities(caps map[string]struct{}) string {
	if len(caps) == 0 {
		caps = map[string]struct{}{CapabilityChat: {}}
	}
	out := make([]string, 0, len(caps))
	for c := range caps {
		out = append(out, c)
	}
	sortStringsInPlace(out)
	b, _ := json.Marshal(out)
	return string(b)
}

// HasCapability reports whether the parsed set grants the named
// capability. Treats CapabilityChat as implied — every membership
// can chat, even if the stored set somehow omits it (defensive: an
// admin couldn't mean to revoke chat without ejecting the user
// entirely, so the runtime never enforces deny on chat).
func HasCapability(caps map[string]struct{}, cap string) bool {
	if cap == CapabilityChat {
		return true
	}
	_, ok := caps[cap]
	return ok
}

// FallbackCapabilitiesForRole returns the role-derived default set
// when a workspace_members row has NULL capabilities. The v109
// backfill populates these into the column directly, so in practice
// this fallback fires only when (a) a new row was inserted between
// migration apply and the application-layer write that should fill
// capabilities, or (b) an older sidecar binary still runs against a
// post-v109 schema and didn't write the column. Both cases degrade
// to the role-mapped bundle so behaviour matches the migration.
func FallbackCapabilitiesForRole(role string) map[string]struct{} {
	bundle := BundleChat
	switch role {
	case "OWNER", "ADMIN":
		bundle = BundleAdmin
	case "MANAGER":
		// MANAGER-equivalent: chat + routine + issue + memory.
		// Same set BundlePower exposes, used internally rather than
		// as a named preset for the reason noted on BundleCapabilities.
		return map[string]struct{}{
			CapabilityChat:          {},
			CapabilityRoutineCreate: {},
			CapabilityIssueCreate:   {},
			CapabilityMemoryWrite:   {},
		}
	}
	caps := BundleCapabilities(bundle)
	out := make(map[string]struct{}, len(caps))
	for _, c := range caps {
		out[c] = struct{}{}
	}
	return out
}

// sortStringsInPlace is a tiny insertion sort so we don't pull
// sort.Strings into the import list just for the stable-output
// requirement. n is bounded by the capability count (≤10 for the
// foreseeable future), so O(n²) is irrelevant.
func sortStringsInPlace(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
