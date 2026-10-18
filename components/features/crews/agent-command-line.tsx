"use client"

import { CrCard } from "./cr-card"
import { CliCommandRows, type CliCommandRow } from "./cli-command-rows"

/** Commands the CLI has for one agent (`agent update` takes --llm-model; logs is top level). */
export function agentCommandRows(agent: { slug: string; llm_model?: string | null }): CliCommandRow[] {
  return [
    { cmd: `crewship agent get ${agent.slug}`, desc: "Everything about this agent" },
    { cmd: `crewship agent update ${agent.slug} --llm-model "${agent.llm_model || "<model>"}"`, desc: "Change the model" },
    { cmd: `crewship logs ${agent.slug} --follow`, desc: "Live log of its runs" },
  ]
}

/** Agent Settings › Command line. */
export function AgentCommandLine({ agent }: { agent: { slug: string; llm_model?: string | null } }) {
  return (
    <CrCard title="Command line" concept="settings">
      <CliCommandRows rows={agentCommandRows(agent)} />
    </CrCard>
  )
}
