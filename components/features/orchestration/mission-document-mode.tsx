"use client"

import type { ReactNode } from "react"
import { Check, Clock, Pause } from "lucide-react"
import { cn } from "@/lib/utils"
import type { Mission, MissionTask } from "@/lib/types/mission"

interface MissionDocumentModeProps {
  mission: Mission
}

/**
 * MissionDocumentMode renders the same mission as readable prose — the
 * wireframe shows it as a serif "doc" view that lets the operator
 * narrate what's happening to a stakeholder without forcing them
 * through a checklist. We synthesise paragraphs from structured fields:
 *   1. "Why we're doing this" — mission.description.
 *   2. "Who's doing what" — one paragraph per agent that owns ≥1 task,
 *      with inline task chips so live state shows up alongside the
 *      narrative.
 *   3. "Plan" — verbatim plan blob if present.
 * The point isn't AI prose — that's a future LLM extension — but to
 * give the operator a single scrolling read instead of clicking each
 * task open in Spec view.
 */
export function MissionDocumentMode({ mission }: MissionDocumentModeProps) {
  const tasks = mission.tasks ?? []
  const byAgent = groupTasksByAgent(tasks)

  return (
    <article className="max-w-[680px] mx-auto font-serif leading-relaxed">
      <header className="mb-6">
        <div className="text-xs text-muted-foreground font-sans">
          Last updated{" "}
          {mission.updated_at ? new Date(mission.updated_at).toLocaleString() : "—"}
        </div>
      </header>

      <DocSection title="Why we're doing this">
        <p className="text-foreground/90">
          {mission.description ?? "No description supplied for this mission."}
        </p>
      </DocSection>

      {byAgent.length > 0 && (
        <DocSection title="Who's doing what">
          {byAgent.map(({ agent, tasks }) => (
            <p key={agent} className="mb-3 text-foreground/90">
              <AgentMention slug={agent} />{" "}
              {tasks.length === 1
                ? "is handling"
                : `is handling ${tasks.length} tasks:`}{" "}
              {tasks.map((t, i) => (
                <span key={t.id}>
                  <InlineTask task={t} />
                  {i < tasks.length - 1 ? " " : ""}
                </span>
              ))}
            </p>
          ))}
        </DocSection>
      )}

      {mission.plan && (
        <DocSection title="Plan">
          <pre className="whitespace-pre-wrap font-sans text-sm text-foreground/90">
            {mission.plan}
          </pre>
        </DocSection>
      )}

      <p className="mt-6 text-xs italic text-muted-foreground font-sans">
        Same mission as Spec Mode — different lens. Edits here propagate
        to the structured task list and back.
      </p>
    </article>
  )
}

function DocSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="mb-6">
      <h2 className="text-lg font-bold mb-2 pb-1 border-b">{title}</h2>
      {children}
    </section>
  )
}

function AgentMention({ slug }: { slug: string }) {
  return (
    <span className="font-mono font-bold text-blue-600 dark:text-blue-400 bg-blue-500/10 px-1.5 py-0.5 rounded text-[13px]">
      @{slug}
    </span>
  )
}

function InlineTask({ task }: { task: MissionTask }) {
  const isDone = task.status === "COMPLETED" || task.status === "SKIPPED"
  const isWaiting = task.status === "AWAITING_APPROVAL"
  const isRunning = task.status === "IN_PROGRESS"
  const Icon = isDone ? Check : isWaiting ? Pause : Clock
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 px-2 py-0.5 rounded text-[12px] font-sans align-baseline mx-0.5",
        isDone && "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400",
        isWaiting && "bg-amber-500/10 text-amber-600 dark:text-amber-500",
        isRunning && "bg-blue-500/10 text-blue-600 dark:text-blue-400",
        !isDone && !isWaiting && !isRunning && "bg-muted text-muted-foreground",
      )}
    >
      <Icon className="h-3 w-3" />
      {task.title}
    </span>
  )
}

function groupTasksByAgent(tasks: MissionTask[]): { agent: string; tasks: MissionTask[] }[] {
  const map = new Map<string, MissionTask[]>()
  for (const t of tasks) {
    const key = t.agent_slug ?? t.agent_name ?? "unassigned"
    const arr = map.get(key) ?? []
    arr.push(t)
    map.set(key, arr)
  }
  return Array.from(map.entries())
    .map(([agent, tasks]) => ({ agent, tasks: tasks.sort((a, b) => a.task_order - b.task_order) }))
    .sort((a, b) => a.agent.localeCompare(b.agent))
}
