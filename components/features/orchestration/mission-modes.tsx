"use client"

import { useMemo, useState } from "react"
import Link from "next/link"
import { ArrowLeft, FileText, GitFork, ListTodo } from "lucide-react"
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs"
import type { Mission, MissionTask, MissionTaskStatus } from "@/lib/types/mission"
import { MissionSpecMode } from "./mission-spec-mode"
import { MissionDocumentMode } from "./mission-document-mode"
import { MissionGraphMode } from "./mission-graph-mode"
import { MissionWatchRoster } from "./mission-watch-roster"

interface MissionModesProps {
  mission: Mission
}

/**
 * MissionModes is the three-view shell rendered at /orchestration/missions/[id].
 * Wireframe reference: wireframes/orchestration-modes.html. The same Mission
 * record drives every tab — Spec breaks it down by phase, Document narrates it
 * as prose, Graph visualises task dependencies. The right rail (WatchRoster)
 * is shared across all three so the operator's "what's waiting on me" view
 * stays anchored regardless of which mode they're in.
 */
export function MissionModes({ mission }: MissionModesProps) {
  const [view, setView] = useState<"spec" | "doc" | "graph">("spec")
  const phases = useMemo(() => derivePhases(mission), [mission])

  return (
    <div className="flex flex-col h-full">
      {/* Top bar — workspace context + view tabs. Mirrors the wireframe
          header but uses the dashboard chrome's color tokens. */}
      <header className="flex items-center gap-4 px-6 py-3 border-b bg-background">
        <Link
          href="/orchestration"
          className="flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="h-4 w-4" />
          Orchestration
        </Link>
        <span className="text-muted-foreground/50">/</span>
        <span className="text-sm font-medium truncate max-w-[40ch]">{mission.title}</span>

        <div className="ml-auto">
          <Tabs value={view} onValueChange={(v) => setView(v as typeof view)}>
            <TabsList>
              <TabsTrigger value="spec" className="gap-1.5">
                <ListTodo className="h-4 w-4" />
                Spec Mode
              </TabsTrigger>
              <TabsTrigger value="doc" className="gap-1.5">
                <FileText className="h-4 w-4" />
                Document
              </TabsTrigger>
              <TabsTrigger value="graph" className="gap-1.5">
                <GitFork className="h-4 w-4" />
                Graph
              </TabsTrigger>
            </TabsList>
          </Tabs>
        </div>
      </header>

      <main className="grid grid-cols-[1fr_340px] gap-4 p-6 max-w-[1400px] mx-auto w-full flex-1 overflow-hidden">
        <section className="bg-card border rounded-lg p-7 overflow-y-auto">
          {/* Mission heading is shared across all three modes — repeating
              it inside every tab would scroll out of sync between
              switches and waste vertical real estate. */}
          <MissionHeading mission={mission} />

          <Tabs value={view} className="mt-6">
            <TabsContent value="spec" className="m-0">
              <MissionSpecMode mission={mission} phases={phases} />
            </TabsContent>
            <TabsContent value="doc" className="m-0">
              <MissionDocumentMode mission={mission} />
            </TabsContent>
            <TabsContent value="graph" className="m-0">
              <MissionGraphMode tasks={mission.tasks ?? []} />
            </TabsContent>
          </Tabs>
        </section>

        <aside className="overflow-y-auto">
          <MissionWatchRoster mission={mission} />
        </aside>
      </main>
    </div>
  )
}

function MissionHeading({ mission }: { mission: Mission }) {
  const meta = [
    mission.identifier ?? null,
    mission.crew_name ? `Crew ${mission.crew_name}` : null,
    mission.lead_agent_name ? `Lead @${mission.lead_agent_slug || mission.lead_agent_name}` : null,
    mission.pattern ? `Pattern ${mission.pattern}` : null,
    mission.due_date ? new Date(mission.due_date).toLocaleDateString() : null,
  ].filter(Boolean) as string[]

  return (
    <div>
      <h1 className="text-2xl font-bold leading-tight mb-1">{mission.title}</h1>
      <div className="flex flex-wrap gap-3 text-xs text-muted-foreground">
        {meta.map((m) => (
          <span key={m}>{m}</span>
        ))}
      </div>
    </div>
  )
}

/** Phase descriptor used by the Spec view's progress bar. */
export interface MissionPhase {
  id: "specify" | "plan" | "tasks" | "implement"
  label: string
  status: "done" | "active" | "pending"
}

/**
 * derivePhases maps a mission's lifecycle into the four-step Spec model
 * the wireframe assumes. The mapping is intentionally lenient: a mission
 * that skips ahead (e.g. created already IN_PROGRESS) still resolves to
 * a sensible "active" cursor so the bar never sits empty.
 *
 * - Specify is always considered done: a mission that exists has been
 *   specified at least to the title-and-description level.
 * - Plan is done if a plan blob is attached or status >= IN_PROGRESS;
 *   active otherwise (caller is still drafting the plan).
 * - Tasks is active when at least one task exists and not all are
 *   terminal; done once every task is COMPLETED/SKIPPED.
 * - Implement is done only when the mission itself reached a terminal
 *   state, otherwise pending.
 */
export function derivePhases(mission: Mission): MissionPhase[] {
  const tasks = mission.tasks ?? []
  const hasPlan = !!mission.plan && mission.plan.trim().length > 0
  const movedPastPlan =
    mission.status === "IN_PROGRESS" ||
    mission.status === "REVIEW" ||
    mission.status === "COMPLETED" ||
    mission.status === "DONE"
  const tasksTerminal = tasks.length > 0 &&
    tasks.every((t) => isTerminalTaskStatus(t.status))
  const missionTerminal =
    mission.status === "COMPLETED" ||
    mission.status === "DONE" ||
    mission.status === "FAILED" ||
    mission.status === "CANCELLED"

  const phases: MissionPhase[] = [
    { id: "specify", label: "Specify", status: "done" },
    {
      id: "plan",
      label: "Plan",
      status: hasPlan || movedPastPlan ? "done" : "active",
    },
    {
      id: "tasks",
      label: "Tasks",
      status: tasksTerminal
        ? "done"
        : tasks.length > 0
        ? "active"
        : hasPlan || movedPastPlan
        ? "active"
        : "pending",
    },
    {
      id: "implement",
      label: "Implement",
      status: missionTerminal ? "done" : tasksTerminal ? "active" : "pending",
    },
  ]

  // Promote the first non-done phase to "active" if nothing is currently
  // marked active — covers the gap where Plan is done but no tasks exist
  // yet, so the operator can see the cursor sitting on Tasks.
  if (!phases.some((p) => p.status === "active")) {
    const firstPending = phases.find((p) => p.status === "pending")
    if (firstPending) firstPending.status = "active"
  }

  return phases
}

function isTerminalTaskStatus(status: MissionTaskStatus): boolean {
  return status === "COMPLETED" || status === "SKIPPED" || status === "FAILED"
}

/**
 * groupTasksByStatusBucket bins tasks into the four UI buckets the
 * wireframe shows on the Tasks phase header (running / done / waiting /
 * blocked). Exported so SpecMode and the Tasks panel can share the
 * derivation without duplicating the BLOCKED-vs-PENDING heuristic.
 */
export function groupTasksByStatusBucket(tasks: MissionTask[]) {
  const running: MissionTask[] = []
  const done: MissionTask[] = []
  const waiting: MissionTask[] = []
  const blocked: MissionTask[] = []
  for (const t of tasks) {
    switch (t.status) {
      case "IN_PROGRESS":
        running.push(t)
        break
      case "COMPLETED":
      case "SKIPPED":
        done.push(t)
        break
      case "AWAITING_APPROVAL":
        waiting.push(t)
        break
      case "BLOCKED":
        blocked.push(t)
        break
      case "FAILED":
        // Failures sit visually with done — they are terminal — but the
        // status icon distinguishes them; keeping them in `done` keeps
        // the four-bucket count accurate without a fifth category.
        done.push(t)
        break
      default:
        // PENDING tasks with a non-empty depends_on string are blocked
        // pending an upstream completion; otherwise they are queued and
        // count as blocked for the wireframe's "🔒 N" indicator.
        if (t.depends_on && t.depends_on.length > 0) blocked.push(t)
        else blocked.push(t)
    }
  }
  return { running, done, waiting, blocked }
}
