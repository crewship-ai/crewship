"use client"

import { CrCard } from "./cr-card"
import { CliCommandRows } from "./cli-command-rows"

/**
 * Agent Settings › Hooks: a static card, no controls. Hooks are set in the
 * CLI. The registry is paused on the server (it answers 503 and nothing
 * fires), so the card says so instead of promising a run hook.
 */
export function AgentHooksCard() {
  return (
    <CrCard title="Hooks" concept="settings">
      <p className="px-4 pb-1 text-label text-muted-foreground">Commands around each run · set in the CLI</p>
      <CliCommandRows
        rows={[{ cmd: "crewship hooks create --event pre_agent_start --handler shell --command ./check.sh", desc: "Run a command before the agent starts" }]}
      />
      <p className="border-t border-border px-4 py-3 text-label text-muted-foreground">
        Hooks are paused: the registry is unfinished, so a hook you register is kept but does not fire yet.
      </p>
    </CrCard>
  )
}
