import { AgentRedirect } from "../redirect-client"

// Catch-all so legacy deep links like /crews/agents/<id>/chat,
// /crews/agents/<id>/settings, /crews/agents/<id>/runs/<runId> land on
// /crews?agent=<slug> instead of 404 after Phase 8 deleted the sub-
// routes. AgentRedirect reads the agentId from useParams; the [...rest]
// tail is ignored on purpose — there's no per-subpath state the new
// canvas could restore beyond the agent itself.
export function generateStaticParams() {
  return [{ agentId: "_", rest: ["_"] }]
}

export default function LegacyAgentSubrouteRedirect() {
  return <AgentRedirect />
}
