package api

// Which VARIABLE does a delivered credential arrive under? (#1657)
//
// The delivery query resolves a slot from three different places — an explicit
// grant's env_var_name, a binding's slot, or, when neither exists, the
// credential's own display NAME. That last arm is the whole problem. A display
// name is a human identity for an account (`github-acme` is this codebase's own
// example), and nothing ever held it to the charset a container can export. So
// a user could name a credential `github-token` in the UI, and the name would
// travel unchanged all the way to the orchestrator's file writer, fail the
// uppercase-only gate there, and — because that function returned a hard error
// rather than skipping — abandon the agent's WHOLE credential batch and abort
// the run, with an error blaming the Docker daemon's version.
//
// The orchestrator now skips instead of aborting, which is the floor. This file
// is the part that makes the user get what they meant: the slot is normalised
// onto a legal variable name once, here, at the single delivery chokepoint,
// before any consumer sees the set.
//
// Why here and not in the orchestrator. Normalisation can collide — `gh-token`
// and `GH_TOKEN` are two legal, distinct credential names in one workspace, and
// they fold onto the same variable. Resolving a collision needs the agent's
// WHOLE delivery set in hand, and this is the only place that has it. The
// orchestrator sees whatever slice it was handed; a rename invented there could
// silently point two credentials at one file, which is the failure the binding
// resolver already refuses to make ("a missing GH_TOKEN is diagnosable, the
// wrong GH_TOKEN is not").
//
// Why normalise at all, rather than only skipping. Skipping alone is a quieter
// version of the same harm: the run survives, the agent finds no token, and
// nothing connects that to the name someone typed. Folding case and `-` gives
// the operator the variable they obviously meant, and every fold is reported —
// it is not silent, it is just not fatal.
//
// Why not tighten the display name at creation instead. The name is not a
// variable and must not become one: PRD-CREDENTIALS-V2 §2.5b exists precisely
// to separate "which account" (`github-acme`) from "which variable"
// (`GH_TOKEN`), and forcing accounts to be spelled GITHUB_ACME would undo that
// and break every existing row besides. The two fields that ARE variables —
// a binding's slot and a grant's env_var_name — are held to the rule at write
// time (credential_bindings.go, agent_credentials.go); only the display name,
// which nobody chose as a variable, is normalised on read.

import (
	"log/slog"

	"github.com/crewship-ai/crewship/internal/credname"
)

// resolveDeliverySlots settles every delivered credential's slot in one pass
// over the whole set, in place.
//
// Two passes, and the order is the load-bearing part:
//
//  1. Names that are ALREADY legal variables claim themselves. Nothing about
//     them changes — not the string, not their relative order, not the existing
//     "first row wins a duplicate slot" behaviour the orchestrator and the
//     resolution view both rely on. A duplicate exact name is left exactly as it
//     was; it is not this function's business and never was.
//
//  2. Names that are not legal variables are folded, and may only take a name
//     nobody has claimed. That ordering is the guarantee that makes folding safe
//     to do at all: **a credential that literally asked for GH_TOKEN never loses
//     it to one we renamed.** Were this one pass in row order, a crew-linked
//     `gh-token` could outrank the workspace's actual GH_TOKEN binding purely
//     because of where it landed in the ORDER BY, and the agent would push as
//     the wrong bot with nothing having said so.
//
// A credential that cannot be folded at all, or whose folded name is taken, is
// REMOVED from the set rather than left in it with a blank slot. It has no
// variable, so there is nothing for a consumer to deliver it under — and the
// row still carries the credential's ciphertext, which would otherwise be
// handed to the sidecar's credential store and to the delegation payload under
// no name at all. It leaves as a notice instead, and the notice never carries a
// value.
//
// The returned slice preserves the input's order and is the same backing array;
// callers must use the return value rather than the argument.
func resolveDeliverySlots(delivered []deliveredCredential) ([]deliveredCredential, []deliveredSlotNotice) {
	// A second fold onto an already-claimed name must be able to name the
	// holder, so the table stores the credential id rather than a bare bool.
	claimedBy := make(map[string]string, len(delivered))

	for i := range delivered {
		if credname.Valid(delivered[i].EnvVar) {
			if _, taken := claimedBy[delivered[i].EnvVar]; !taken {
				claimedBy[delivered[i].EnvVar] = delivered[i].ID
			}
		}
	}

	var notices []deliveredSlotNotice
	kept := delivered[:0]
	for _, d := range delivered {
		requested := d.EnvVar
		if credname.Valid(requested) {
			kept = append(kept, d)
			continue
		}
		folded, ok := credname.Canonical(requested)
		if !ok {
			notices = append(notices, deliveredSlotNotice{
				CredentialID: d.ID, Requested: requested,
				Reason: "the name cannot be an environment variable, and no variable can be derived from it",
			})
			continue
		}
		if holder, taken := claimedBy[folded]; taken {
			notices = append(notices, deliveredSlotNotice{
				CredentialID: d.ID, Requested: requested,
				Reason: "it would be delivered as " + folded + ", which credential " + holder + " already occupies",
			})
			continue
		}
		claimedBy[folded] = d.ID
		d.EnvVar = folded
		notices = append(notices, deliveredSlotNotice{
			CredentialID: d.ID, Requested: requested, Delivered: folded,
			Reason: "the name is not a legal environment variable, so it was delivered as " + folded,
		})
		kept = append(kept, d)
	}
	return kept, notices
}

// logDeliveredCredentialNotices reports everything a resolution decided to
// rename or not deliver — both the credentials (this file) and their parts
// (credential_field_delivery.go).
//
// One call per delivery path, replacing the field-only call the three paths
// already made. A credential that was renamed or dropped is invisible from
// inside the container in exactly the way a dropped part is: the agent simply
// finds no GITHUB_TOKEN. The two belong in one report or the second one will be
// added to one path and missed on the other two, which is the drift the shared
// delivery query exists to end.
func logDeliveredCredentialNotices(logger *slog.Logger, agentID string, delivered []deliveredCredential, notices []deliveredSlotNotice) {
	if logger == nil {
		return
	}
	for _, n := range notices {
		if n.Delivered != "" {
			logger.Warn("credential delivered under a normalised environment variable name",
				"agent_id", agentID, "credential_id", n.CredentialID,
				"requested", n.Requested, "delivered_as", n.Delivered, "reason", n.Reason)
			continue
		}
		logger.Warn("credential not delivered — its name cannot be an environment variable",
			"agent_id", agentID, "credential_id", n.CredentialID,
			"requested", n.Requested, "reason", n.Reason)
	}
	logDeliveredFieldConflicts(logger, agentID, delivered)
}

// deliveredSlotWarnings renders the notices as sentences for an API response.
//
// names maps credential id → display name; a caller without it passes nil and
// gets the id, which is worse to read but never wrong. The value is never
// mentioned — these are names, and this is a map, not a reveal.
func deliveredSlotWarnings(notices []deliveredSlotNotice, names map[string]string) []string {
	var out []string
	for _, n := range notices {
		label := names[n.CredentialID]
		if label == "" {
			label = n.CredentialID
		}
		if n.Delivered != "" {
			out = append(out, "Credential "+label+" is delivered as "+n.Delivered+
				" — "+n.Reason+".")
			continue
		}
		out = append(out, "Credential "+label+" is NOT delivered to this agent — "+n.Reason+".")
	}
	return out
}
