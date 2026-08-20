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
	"sort"
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

	// CapabilityRoutineRun gates INVOKING an existing routine —
	// POST /workspaces/{ws}/pipelines/{slug}/run, and the per-routine
	// entries in the slash palette that post to it.
	//
	// Separate from routine.create because the two are different powers.
	// Creating a routine writes a definition somebody still has to
	// approve; running one executes inside the author crew's container
	// with its credentials and its connected integrations, right now.
	// A workspace that wants a bookkeeper to trigger the monthly
	// accounting pack has no reason to also let them author routines.
	//
	// It grants no exemption from anything else the run path enforces:
	// governance status (proposed / disabled never run), the
	// integrations, resources and credentials gates, and the routine's
	// own spend caps all still apply. This capability decides who may
	// ask, not what the platform will agree to.
	CapabilityRoutineRun = "routine.run"

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

	// CapabilityCredentialReveal gates disclosure of a stored secret's
	// plaintext to a human (PRD-CREDENTIALS-V2-2026 §2.6 L2).
	//
	// Two things about it are deliberate and load-bearing:
	//
	//  1. The wire string is colon-separated, unlike every other
	//     capability here. §2.6 names it `credentials:reveal` and is
	//     the standard the rest of Crewship is meant to cite; a
	//     capability string is a stable wire identifier that cannot be
	//     renamed later without a data migration, so it is spelled the
	//     way the security document spells it rather than the way the
	//     local convention would.
	//  2. It appears in NO bundle — not even BundleAdmin, which is the
	//     role-derived fallback for OWNER and ADMIN. That is the whole
	//     point of the layer: role is a necessary condition for reveal
	//     and never a sufficient one, so an OWNER whose membership row
	//     has never been touched must NOT come out holding it. If you
	//     add it to a bundle you have deleted L2.
	CapabilityCredentialReveal = "credentials:reveal"

	// CapabilityIssueCreate gates issue.Create. Issues are the
	// lowest-stakes write action — even chat-only members commonly
	// want to file a ticket from a conversation — so a default-grant
	// bundle includes this above the MEMBER tier.
	CapabilityIssueCreate = "issue.create"

	// CapabilityPageCreate gates authoring a Page (docs/prd/pages.md
	// §11, POST /api/v1/pages). A page is a workspace-visible surface
	// that names crews as panel owners and routines as producers, so
	// creating one is a MANAGER+ action by default — this capability
	// is how an admin lets a specific MEMBER author pages without
	// promoting them.
	//
	// It appears in no bundle, like credentials:reveal: the v109
	// migration's stored capability sets predate it, and adding a
	// string to a bundle that the backfill never wrote would make the
	// role-derived fallback disagree with what is in the column.
	CapabilityPageCreate = "page.create"

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
	CapabilityRoutineRun:       {},
	CapabilitySkillCreate:      {},
	CapabilityCredentialCreate: {},
	CapabilityCredentialRotate: {},
	CapabilityCredentialReveal: {},
	CapabilityIssueCreate:      {},
	CapabilityMemoryWrite:      {},
	CapabilityPageCreate:       {},
}

// IsValidCapability reports whether the string is a known capability
// the server will accept. Admin commands call this before persisting
// a grant so a typo (`routine.creat` ← missing 'e') produces a
// rejection instead of a row that silently never matches the runtime
// constant.
func IsValidCapability(capability string) bool {
	_, ok := allCapabilities[capability]
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
	sort.Strings(out)
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
			CapabilityRoutineRun,
			CapabilityIssueCreate,
			CapabilityMemoryWrite,
		}
	case BundleAdmin:
		return []string{
			CapabilityChat,
			CapabilityRoutineCreate,
			CapabilityRoutineRun,
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
	sort.Strings(out)
	b, _ := json.Marshal(out)
	return string(b)
}

// HasCapability reports whether the parsed set grants the named
// capability. Treats CapabilityChat as implied — every membership
// can chat, even if the stored set somehow omits it (defensive: an
// admin couldn't mean to revoke chat without ejecting the user
// entirely, so the runtime never enforces deny on chat).
//
// Parameter is named `capability` (not `cap`) to avoid shadowing the
// Go built-in. Linters flag the shadow; humans reading the code see
// "capability" as the obvious intent.
func HasCapability(caps map[string]struct{}, capability string) bool {
	if capability == CapabilityChat {
		return true
	}
	_, ok := caps[capability]
	return ok
}

// v109BackfillCapabilities is what migration v109 WROTE into
// workspace_members.capabilities, per role, frozen.
//
// It is a transcription of the migration's `CASE role WHEN … THEN …`
// literals and it must stay one. TestMigrationBundleDriftV109 compares
// the two and fails on any difference, which is the point: a row whose
// column the migration filled and a row whose column is still NULL have
// to resolve to the same capability set, and the migration's half of
// that pair already ran in databases nobody can go back and edit.
//
// It used to be expressed as "whatever BundleCapabilities returns for
// the matching bundle", which read as economical until the first
// capability was added to a preset — BundleAdmin grew routine.run and
// every NULL-column OWNER silently grew it too, while every OWNER the
// backfill had already written did not. The two concepts had never been
// the same thing; they had only had the same contents. So they are two
// tables now.
//
// Adding a capability here is a data-migration decision, not a code
// decision. Add it to a BUNDLE instead (BundleCapabilities), which is
// what an admin applies today and is free to grow.
var v109BackfillCapabilities = map[string][]string{
	"OWNER": {
		CapabilityChat,
		CapabilityRoutineCreate,
		CapabilitySkillCreate,
		CapabilityCredentialCreate,
		CapabilityCredentialRotate,
		CapabilityIssueCreate,
		CapabilityMemoryWrite,
	},
	"ADMIN": {
		CapabilityChat,
		CapabilityRoutineCreate,
		CapabilitySkillCreate,
		CapabilityCredentialCreate,
		CapabilityCredentialRotate,
		CapabilityIssueCreate,
		CapabilityMemoryWrite,
	},
	"MANAGER": {
		CapabilityChat,
		CapabilityRoutineCreate,
		CapabilityIssueCreate,
		CapabilityMemoryWrite,
	},
	"MEMBER": {CapabilityChat},
	"VIEWER": {CapabilityChat},
}

// FallbackCapabilitiesForRole returns the role-derived default set
// when a workspace_members row has NULL capabilities. The v109
// backfill populates these into the column directly, so in practice
// this fallback fires only when (a) a new row was inserted between
// migration apply and the application-layer write that should fill
// capabilities, or (b) an older sidecar binary still runs against a
// post-v109 schema and didn't write the column. Both cases degrade
// to what the migration wrote, so behaviour matches it exactly.
//
// An unknown role gets the chat baseline — fail-closed, since a role
// string the runtime doesn't recognise is not a licence to guess
// upward.
func FallbackCapabilitiesForRole(role string) map[string]struct{} {
	caps, ok := v109BackfillCapabilities[role]
	if !ok {
		caps = []string{CapabilityChat}
	}
	out := make(map[string]struct{}, len(caps))
	for _, c := range caps {
		out[c] = struct{}{}
	}
	return out
}
