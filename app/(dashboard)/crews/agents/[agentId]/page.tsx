import { AgentRedirect } from "./redirect-client"

// Static export requires a placeholder param. The real agent id is
// resolved at runtime by the client redirector, not at build.
export function generateStaticParams() {
  return [{ agentId: "_" }]
}

export default function AgentLegacyRedirectPage() {
  return <AgentRedirect />
}
