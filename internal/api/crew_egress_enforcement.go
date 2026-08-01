package api

// Effective egress state on the crew read paths.
//
// crews.network_mode is the operator's INTENT and is never rewritten here. A
// crew configured "restricted" on a provider that cannot enforce it stays
// "restricted" in the row, so moving it to a provider that can enforce it
// restores the fence without anyone having to remember what it used to say.
//
// What was wrong (#1648) is that intent was the only thing any surface
// reported. The crew list, the crew detail, `crewship crew get` and the
// dashboard's Network Policy card all printed "restricted" while the Apple
// provider applied nothing, so three independent surfaces agreed on a
// containment control that did not exist. That is worse than the missing
// feature: an operator can plan around a limitation they can see.
//
// So every crew response now carries the effective state next to the
// configured one, derived from the provider's own capability report rather
// than from a rule restated here. If a provider changes what it supports, the
// read surfaces change with it; there is no second copy to forget.

import (
	"strings"

	"github.com/crewship-ai/crewship/internal/provider"
)

// crewConfigNetworkModeField is the CrewConfig field name the provider report
// keys egress on. Named once so a rename shows up as a compile-adjacent grep
// hit rather than as a silently-always-enforced response.
const crewConfigNetworkModeField = "NetworkMode"

// crewEgressEnforcement is one crew's effective egress state.
type crewEgressEnforcement struct {
	Enforced bool
	// Reason is the provider's own words for what is missing. Empty when
	// Enforced.
	Reason string
}

// egressEnforcementFor asks the container provider what it would do with a
// crew's egress settings. It is the ONLY place the question is asked, because
// every surface has to give the same answer — the human-facing ones
// (crew list/get, the dashboard) and the agent-facing one (the agent-config
// resolve response and the system prompt built from it).
//
// Enforced is true unless the provider POSITIVELY reports that it drops the
// field. That is the correct polarity for the two unknowns:
//
//   - no container provider wired (tests, --no-docker): no crew runs at all,
//     so there is no unenforced container to warn about.
//   - a provider that does not implement CrewConfigReporter (docker): it
//     honours what it is given, which is the whole meaning of not answering.
//
// "free" is enforced everywhere by construction — there is nothing to apply —
// so it never produces a reason.
func egressEnforcementFor(cp provider.ContainerProvider, crewID, crewSlug, networkMode string, allowedDomains []string) crewEgressEnforcement {
	if cp == nil {
		return crewEgressEnforcement{Enforced: true}
	}
	support := provider.InspectCrewConfigSupport(cp, provider.CrewConfig{
		ID:             crewID,
		Slug:           crewSlug,
		NetworkMode:    networkMode,
		AllowedDomains: allowedDomains,
	})
	if drop, ok := support.Drop(crewConfigNetworkModeField); ok {
		return crewEgressEnforcement{Enforced: false, Reason: drop.Detail}
	}
	return crewEgressEnforcement{Enforced: true}
}

// annotateEgressEnforcement fills NetworkModeEnforced /
// NetworkModeUnenforcedReason on a scanned crew row.
func (h *CrewHandler) annotateEgressEnforcement(c *crewResponse) {
	if c == nil {
		return
	}
	if h == nil {
		c.NetworkModeEnforced = true
		c.NetworkModeUnenforcedReason = ""
		return
	}
	enf := egressEnforcementFor(h.container, c.ID, c.Slug, c.NetworkMode, c.AllowedDomains)
	c.NetworkModeEnforced = enf.Enforced
	c.NetworkModeUnenforcedReason = enf.Reason
}

// buildNetworkPolicyBlock renders the system-prompt section that tells an
// AGENT its egress fence is not real — and returns "" when it is.
//
// This is the surface that differs in kind from the others. Every other read
// path tells a person something; a person reading a stale badge makes a worse
// decision. This one is read by the model at every run, and a model that
// believes it is fenced takes worse ACTIONS: it treats an outbound call as
// safely contained, or skips sanitising something because the allowlist will
// catch it. So the block is written as instructions about what to do, not as
// a status line about what is true — an agent that reads
// "restricted (not enforced)" has been informed; an agent told to assume every
// request leaves the machine behaves differently.
//
// The empty return is load-bearing. There is no network block in the prompt
// today, so emitting nothing whenever the mode IS enforced leaves the prompt
// byte-identical for every crew on a provider that applies it — this change
// must not cost Docker users a single token to describe an Apple problem.
// TestAgentConfig_FencedCrewPromptIsByteIdentical pins that.
func buildNetworkPolicyBlock(networkMode string, enf crewEgressEnforcement) string {
	if enf.Enforced {
		return ""
	}
	reason := strings.TrimSpace(enf.Reason)
	if reason == "" {
		reason = "the container runtime this crew is running on cannot apply it."
	}
	return "[NETWORK POLICY — NOT ENFORCED]\n" +
		"This crew is configured for " + networkMode + " egress, but the container runtime it is " +
		"running on cannot apply that setting: " + reason + "\n" +
		"Work as if you are on an open, unmonitored network:\n" +
		"- Assume every outbound request succeeds and leaves this machine. No allowlist stands " +
		"between you and the internet, so \"the fence will catch it\" is not true here.\n" +
		"- Redact secrets, tokens and customer data before sending anything, exactly as you would " +
		"on an open network. The configured allow-list is not filtering what you send.\n" +
		"- Do not describe this crew to the user as network-restricted, sandboxed or isolated, and " +
		"do not let \"egress is contained\" carry any part of a safety judgement. It is not contained.\n" +
		"If a task only makes sense with egress actually enforced, say so instead of proceeding as " +
		"though it were.\n" +
		"[END NETWORK POLICY]"
}

// egressEnforcementAdvisory returns the create/update `warnings` line for a
// crew whose egress mode this instance cannot enforce, or "" when it can.
//
// It rides the array #1641 added rather than being a new response shape,
// because the moment an operator sets `--network-mode restricted` is the
// moment they should hear that this instance will not apply it — not on a
// later GET they may never make.
func (h *CrewHandler) egressEnforcementAdvisory(c *crewResponse) string {
	if c == nil || c.NetworkModeEnforced {
		return ""
	}
	return "network_mode " + c.NetworkMode + " is not enforced by this instance's container provider: " +
		c.NetworkModeUnenforcedReason +
		" The setting is stored as configured; `crewship crew get " + c.Slug +
		"` reports whether it is in effect."
}
