"use client"

import { ConfirmDialog } from "@/components/ui/confirm-dialog"

import type { ContainerStatus } from "./bottom-panel/types"
import type { AgentSummary } from "./crew-canvas-tabs/types"
import { useWorkspaceResource } from "./memory-workspace"

const plural = (n: number, one: string) => `${n} ${one}${n === 1 ? "" : "s"}`

/**
 * What the crew holds, in one line: "3 agents · 2 routines · container
 * running". A count that could not be read is left out, never shown as 0.
 */
export function crewDeleteSummary({ agents, routines, containers }: {
  agents: number
  routines: number | null
  containers: ContainerStatus[] | null
}): string {
  const runtime = containers?.find((c) => c.kind === "crew") ?? null
  return [
    plural(agents, "agent"),
    routines == null ? null : plural(routines, "routine"),
    runtime ? `container ${runtime.status}` : null,
  ].filter(Boolean).join(" · ")
}

/** Delete crew: the summary line, what is lost, what is kept, the slug typed back. */
export function CrewDeleteDialog({ open, onOpenChange, workspaceId, crew, agents, containers, onConfirm }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  workspaceId: string
  crew: { id: string; name: string; slug: string }
  agents: AgentSummary[]
  containers: ContainerStatus[] | null
  onConfirm: () => void | Promise<void>
}) {
  const ws = encodeURIComponent(workspaceId)
  // Read only while the dialog is open.
  const routines = useWorkspaceResource<unknown[]>(open ? `/api/v1/workspaces/${ws}/pipelines?author_crew_id=${encodeURIComponent(crew.id)}&order=recent` : "")
  const routineCount = Array.isArray(routines.data) ? routines.data.length : null
  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={`Delete crew ${crew.name}?`}
      summary={crewDeleteSummary({ agents: agents.length, routines: routineCount, containers })}
      description="This deletes the crew and its agents. Background cleanup removes runtimes whose ownership is verified; older runtimes may need manual cleanup. Removing a runtime stops its processes and discards files stored only in its container writable layer."
      consequences={[
        { tone: "lost", text: `${agents.length === 1 ? "1 agent is" : `${agents.length} agents are`} deleted along with the crew` },
        { tone: "lost", text: "Declared sidecar containers and their data volumes are also deleted" },
        { tone: "lost", text: "Crew issues are deleted" },
        { tone: "kept", text: "Background runtime cleanup preserves mounted volumes, host data and shared images" },
      ]}
      confirmLabel="Delete crew"
      destructive
      typeToConfirm={crew.slug}
      onConfirm={onConfirm}
    />
  )
}
