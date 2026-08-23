package orchestrator

import (
	"sort"
	"strings"
	"sync"
)

// The egress allowlist a crew's shared sidecar enforces is assembled from three
// inputs, and only one of them is a property of the crew:
//
//	req.AllowedDomains            crews.allowed_domains — the same value for every member
//	mcpStdioDomains(MCPServers)   agent_mcp_bindings is a per-agent opt-out, so
//	                              two members resolve different stdio servers
//	proxiedEndpointDomains(req)   the member's OWN assigned OPENAI_COMPAT
//	                              credential names the host the sidecar dials
//
// sidecarNeedsRestart compares the running sidecar's domains_hash against the
// set the current exec computed, so the moment the last two inputs differ
// between members, every member's exec sees a hash it does not recognise and
// kills a healthy sidecar to install its own view. Two members alternating
// turns restart the sidecar on EVERY exec — #1160's unconditional restart,
// reached from the other direction and without the comment that warns about it.
//
// The fix is not to drop the differing inputs from the hash. The sidecar dials
// the upstream on the agent's behalf and checks this list before it does, so a
// member whose host is missing gets a 403 on every model call with a perfectly
// valid credential — a failure this area has already produced twice. What the
// members need is the UNION: one allowlist that covers every member the shared
// sidecar might have to answer for, which is also, being the same set for all
// of them, a hash they all agree on.
//
// Scope is the container, not the crew, because the container is what the
// sidecar and its CredStore actually belong to. That also bounds how long a
// contribution outlives its reason: a member that stops needing a host drops it
// on its own next exec, and a member that never runs again keeps it only for as
// long as the sidecar that might have served it. A recreated container gets a
// new ID and starts empty.
//
// One *crewEgressExtras per container for the process lifetime, same footprint
// rationale as sidecarLifecycleLocks: containers are few and long-lived, and a
// stale entry for a recreated container is unreachable rather than wrong.
type crewEgressExtras struct {
	mu      sync.Mutex
	byAgent map[string][]string
}

// crewDesiredDomains returns the crew-level allowlist widened by every member's
// per-agent contribution seen in this container, after recording agentExtras as
// this member's current contribution (replacing, not merging, so a member that
// loses an MCP server or an endpoint credential narrows the set on its next
// exec rather than pinning the old host forever).
//
// It deliberately runs OUTSIDE lockSidecarLifecycle. Two execs racing into the
// same container can each read the union before the other has recorded its
// own, and the loser then starts the sidecar one host short; the lifecycle lock
// serializes the start itself, and the next exec — which does see both — widens
// it with a single restart. Converging in one extra restart is the cost of not
// extending a lock whose documented posture forbids most of what RunAgent does
// on either side of it.
func (o *Orchestrator) crewDesiredDomains(containerID, agentID string, crewDomains, agentExtras []string) []string {
	domains := append([]string{}, crewDomains...)
	if containerID == "" {
		return append(domains, agentExtras...)
	}

	stateAny, _ := o.crewEgressExtras.LoadOrStore(containerID, &crewEgressExtras{})
	state := stateAny.(*crewEgressExtras)

	state.mu.Lock()
	defer state.mu.Unlock()

	if len(agentExtras) == 0 {
		delete(state.byAgent, agentID)
	} else {
		if state.byAgent == nil {
			state.byAgent = make(map[string][]string, 2)
		}
		state.byAgent[agentID] = append([]string(nil), agentExtras...)
	}

	// DomainsHash normalizes and sorts, so neither ordering nor duplication here
	// changes any decision — but a stable, deduplicated list keeps the allowlist
	// in the sidecar boot payload (and therefore in every log and golden file
	// that quotes it) from reshuffling on map iteration alone, and from showing
	// the same host twice when a crew domain is also one member's MCP or
	// endpoint host. A doubled entry in an egress allowlist reads as a bug to
	// whoever is looking at the log, which costs someone a diagnosis.
	ids := make([]string, 0, len(state.byAgent))
	for id := range state.byAgent {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		domains = append(domains, state.byAgent[id]...)
	}
	return dedupeHosts(domains)
}

// dedupeHosts drops repeats while preserving first-seen order. Case-insensitive
// to match DomainsHash, so "API.Example" and "api.example" collapse the way the
// hash already treats them.
func dedupeHosts(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, d := range in {
		k := strings.ToLower(d)
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, d)
	}
	return out
}
