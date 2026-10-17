"use client"

import { cn } from "@/lib/utils"

import { CrCard } from "./cr-card"

/**
 * "What it reads before every run" (A-PERS, K20). The order is the
 * backend's, checked against the code rather than copied from the mockup:
 *
 *   1–6  internal/api/agent_config.go loadAgentSystemPrompt — ethos (:719),
 *        workspace language (:722), identity (:737), the Instructions field
 *        as [CUSTOM SYSTEM PROMPT] (:756), skills (:759), routines (:779).
 *        Chat and issue runs both resolve through it (chatbridge resolver,
 *        assignments #810).
 *   7    internal/orchestrator/orchestrator_run.go assembleSystemPrompt —
 *        lead context / peer context (:1383, :1391).
 *   8–10 only `if req.MemoryEnabled` (:1455) → memory.go buildMemoryContext:
 *        AGENT.md, CREW.md, workspace memory, pins (:223–232), then
 *        [PERSONA] (:243; agent PERSONA.md wins, else the crew default —
 *        memory_persona.go:110, :114), then the user model and peer card
 *        (:249, :252).
 *
 * Change this list only together with that code.
 */
export const READ_ORDER: { title: string; sub: (crewName: string | null) => string; memory?: boolean }[] = [
  { title: "Crewship’s rules", sub: () => "The same for every agent; they cannot be turned off" },
  { title: "The workspace language", sub: () => "When the workspace has one set" },
  { title: "Who it is", sub: () => "Its name, role and crew" },
  { title: "Its Instructions", sub: () => "The Instructions field above" },
  { title: "Its skills", sub: () => "Instructions only; a skill never runs anything by itself" },
  { title: "The workspace’s routines", sub: () => "So it reaches for a routine before doing the steps itself" },
  { title: "Its crew", sub: () => "Who leads and who its teammates are; a lead also sees connected crews" },
  { title: "Its memory", sub: () => "AGENT.md, CREW.md, workspace notes and pinned notes", memory: true },
  {
    title: "PERSONA.md",
    sub: (crewName) => `Its own, or the ${crewName ? `${crewName} default` : "crew default"} when it has none`,
    memory: true,
  },
  { title: "What it knows about you", sub: () => "About the person who started the run", memory: true },
]

export function AgentReadOrder({ crewName, memoryEnabled }: { crewName: string | null; memoryEnabled: boolean }) {
  return (
    <CrCard title="What it reads before every run" concept="crews">
      <ol className="border-t border-border py-1">
        {READ_ORDER.map((step, i) => {
          const skipped = step.memory && !memoryEnabled
          return (
            <li
              key={step.title}
              data-skipped={skipped || undefined}
              className={cn("flex min-w-0 items-start gap-3 px-4 py-2", skipped && "opacity-60")}
            >
              <span className="mt-0.5 grid size-5 shrink-0 place-items-center rounded-full bg-muted font-mono text-micro tabular-nums text-muted-foreground" aria-hidden>
                {i + 1}
              </span>
              <span className="min-w-0 flex-1">
                <b className="block text-body font-medium text-foreground">{step.title}</b>
                <span className="block text-label text-muted-foreground">{step.sub(crewName)}</span>
                {skipped && <span className="block text-label text-muted-foreground">Skipped: Remember between runs is off</span>}
              </span>
            </li>
          )
        })}
      </ol>
    </CrCard>
  )
}
