"use client"

import Link from "next/link"
import { CalendarClock, Copy } from "lucide-react"

import { Button } from "@/components/ui/button"
import { toast } from "@/lib/toast"

import { CrCard, CrLine } from "./cr-card"

/** The commands of an agent that exist in the CLI today, each with what it does (v67 Command line). */
export function agentCommandRows(agent: { slug: string; llm_model?: string | null }): { cmd: string; note: string }[] {
  return [
    { cmd: `crewship agent get ${agent.slug}`, note: "Everything about this agent" },
    { cmd: `crewship agent update ${agent.slug} --llm-model "${agent.llm_model || "<model>"}"`, note: "Change the model" },
    { cmd: `crewship logs ${agent.slug} --follow`, note: "Live log of its runs" },
  ]
}

export function AgentCommandLine({ agent }: { agent: { slug: string; llm_model?: string | null } }) {
  const copy = (text: string) => {
    void navigator.clipboard.writeText(text).then(() => toast.success("Command copied"), () => toast.error("Could not copy"))
  }
  return (
    <CrCard title="Command line" concept="settings">
      {agentCommandRows(agent).map(({ cmd, note }) => (
        <CrLine
          key={cmd}
          title={<code className="font-mono text-label">{cmd}</code>}
          sub={note}
          right={
            <Button variant="ghost" size="icon-sm" aria-label={`Copy ${cmd}`} onClick={() => copy(cmd)}>
              <Copy className="size-3.5" />
            </Button>
          }
        />
      ))}
    </CrCard>
  )
}

/** The note on Model & limits: where schedules and webhooks live now. */
export function RoutinesMovedNote() {
  return (
    <div className="flex items-start gap-2.5 rounded-card border border-border bg-card px-4 py-3 text-body text-muted-foreground">
      <CalendarClock className="mt-0.5 size-4 shrink-0" aria-hidden />
      <span className="min-w-0 flex-1">
        <b className="block font-medium text-foreground">Schedules and webhooks moved to Routines</b>
        <span>A routine runs this agent on a schedule, a webhook or an issue, with a test run and history.</span>
      </span>
      <Button asChild variant="ghost" size="sm"><Link href="/routines">Routines</Link></Button>
    </div>
  )
}
