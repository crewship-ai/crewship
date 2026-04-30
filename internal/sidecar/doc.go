// Package sidecar implements the per-agent HTTP server that runs inside
// every Crewship agent container.
//
// The sidecar is the only network surface the agent process is permitted
// to see. It listens on 127.0.0.1:9119 (DefaultAddr) and runs as UID 1002
// while the agent itself runs as UID 1001 — the UID split is the security
// boundary, so the agent can speak to the sidecar over the loopback
// socket but cannot read the sidecar's process memory or files.
//
// Responsibilities:
//
//   - HTTP forward proxy for agent egress, with per-domain allowlisting
//     in restricted network mode and credential injection for permitted
//     destinations. Outbound responses pass through the scrubber so
//     secrets that leak back from upstream services are redacted before
//     the agent sees them.
//
//   - Credential bridge to crewshipd: the agent never holds a credential
//     directly. When a tool call needs one, the sidecar forwards the
//     Keeper Request to crewshipd over a Unix socket and either injects
//     the resolved value into the proxied request (ALLOW) or returns
//     a structured error (DENY/ESCALATE).
//
//   - Memory search API (when configured): exposes the agent's private
//     memory and, for lead agents, the crew's shared memory under
//     ".memory/" so the agent can recall context without filesystem
//     access to the journal.
//
//   - Assignment routing for lead agents: lead-role agents POST work
//     items through the sidecar to crewshipd, which then dispatches
//     them to the right crew-member chat.
//
//   - MCP gateway: connects to workspace- and crew-scoped MCP servers
//     on the agent's behalf, attaching credentials handed out by the
//     credential bridge so MCP server URLs and tokens never appear in
//     the agent's environment.
//
// The sidecar is configured via ServerConfig at construction time and
// driven by NewServer. It exposes a readiness channel so containers can
// wait for the loopback listener to be bound before the agent starts.
//
// All journal entries emitted by the sidecar carry ActorType=keeper or
// ActorType=sidecar so SREs can distinguish in-container guard activity
// from crewshipd-side decisions.
package sidecar
