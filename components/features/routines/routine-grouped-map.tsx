"use client"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import type { WorkspaceAgentIdentity } from "@/hooks/use-workspace-agent-directory"
import {
  stepDisplayName,
  stepRoleLabel,
  type RoutineStepsLayout,
  type Step,
} from "@/lib/routine-steps-layout"

/**
 * The map of a large recipe: one column per phase, and in a phase with many
 * steps one node per group with a count. Clicking a node opens that phase
 * (and group) in the list.
 *
 * `RoutineDefinitionCanvas` draws every step as a React Flow node, which is
 * right up to a dozen and unreadable at a hundred — 96 parallel nodes are a
 * wall, not a map. Above BIG_RECIPE_STEPS the spine renders this instead.
 * Columns are phases from declared dependencies; nothing is laid out by
 * guesswork. Kept apart from the canvas so the step list does not pull the
 * graph library in.
 */
export function RoutineGroupedMap({
  layout,
  agents,
  workspaceId,
  onOpenPhase,
}: {
  layout: RoutineStepsLayout
  agents: WorkspaceAgentIdentity[] | null
  workspaceId?: string
  onOpenPhase: (level: number, groupKey?: string) => void
}) {
  const node = (step: Step, label: string, count: number, level: number, groupKey?: string) => {
    const agent = agents?.find((a) => a.slug === step.agent_slug)
    return (
      <button
        key={`${level}:${groupKey ?? String(step.id)}`}
        type="button"
        data-testid={`routine-map-node-${groupKey ?? String(step.id)}`}
        onClick={() => onOpenPhase(level, groupKey)}
        className="grid w-full grid-cols-[22px_1fr_auto] items-center gap-1.5 rounded-lg border border-border/60 bg-card px-2 py-1.5 text-left text-xs hover:border-muted-foreground"
      >
        {agent ? (
          <AgentAvatar
            seed={agent.avatar_seed || agent.name}
            style={agent.avatar_style || agent.crew?.avatar_style || undefined}
            agentId={agent.id}
            avatarUrl={agent.avatar_url}
            workspaceId={workspaceId}
            className="h-5 w-5"
            alt=""
          />
        ) : (
          <span className="flex h-5 w-5 items-center justify-center rounded bg-muted text-[10px] text-muted-foreground">
            {stepRoleLabel(step).slice(0, 1)}
          </span>
        )}
        <span className="truncate" title={label}>
          {label}
        </span>
        {count > 1 && <span className="font-mono text-[10px] text-primary">×{count}</span>}
      </button>
    )
  }
  return (
    <div className="overflow-auto p-3" data-testid="routine-grouped-map">
      <div className="grid min-w-max auto-cols-[190px] grid-flow-col items-start gap-6">
        {layout.phases.map((phase) => (
          <div key={phase.level} className="flex flex-col gap-1.5">
            <div className="px-0.5 pb-1 text-[10.5px] font-semibold uppercase tracking-wider text-muted-foreground-soft">
              {phase.level === 1 ? "First" : "Then"}
              {phase.steps.length > 1 ? ` · ${phase.steps.length}` : ""}
            </div>
            {phase.collapsed
              ? phase.groups.map((group) =>
                  node(group.steps[0], group.name, group.steps.length, phase.level, group.key),
                )
              : phase.steps.map((step, i) => node(step, stepDisplayName(step, i + 1), 1, phase.level))}
          </div>
        ))}
      </div>
      <p className="mt-3 text-[11px] text-muted-foreground">
        Columns are phases from declared dependencies. A node with ×N stands for N steps of one
        group; click it to read them in the list.
      </p>
    </div>
  )
}
