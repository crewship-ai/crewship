"use client"

import type { ReactNode } from "react"
import { useState } from "react"
import type { Edge, Node } from "@xyflow/react"
import { ChainCanvas } from "@/components/features/activity-stream/chain-canvas"
import { TraceCanvas } from "@/components/features/activity/trace-canvas"
import { WorkflowGraph } from "@/components/features/orchestration/workflow-graph"
import { RoutineDefinitionCanvas } from "@/components/features/routines/routine-definition-canvas"
import type { Mission, MissionTask } from "@/lib/types/mission"

const nodes: Node[] = [
  { id: "issue:fixture", type: "default", position: { x: 0, y: 0 }, data: { label: "Review request" } },
  { id: "run:fixture", type: "default", position: { x: 190, y: 125 }, data: { label: "Run checks" } },
]
const edges: Edge[] = [{ id: "fixture-edge", source: nodes[0].id, target: nodes[1].id }]
const definition = { steps: [{ id: "prepare", type: "transform", expression: "input" }, { id: "review", type: "waitpoint", depends_on: ["prepare"] }] }
const task = (id: string, order: number, deps: string[]) => ({
  id, title: id, status: "PENDING", task_order: order, depends_on: JSON.stringify(deps), agent_name: "Fixture operator", agent_slug: "fixture",
} as MissionTask)
const missions = [{
  id: "fixture", title: "Confirm the plan", status: "IN_PROGRESS", updated_at: "2026-11-24T00:00:00Z",
  tasks: [task("prepare", 1, []), task("review", 2, ["prepare"])],
} as Mission]

function Sample({ name, children }: { name: string; children: ReactNode }) {
  return <section aria-label={name} className="h-[430px] w-full overflow-hidden rounded-md border border-border bg-card p-2"><h1>{name}</h1><div className="h-[390px] w-full">{children}</div></section>
}

export default function GraphFixture() {
  const [selected, setSelected] = useState<string | null>(null)
  return <main className="mx-auto grid max-w-6xl gap-3 p-4">
    <Sample name="Activity chain"><ChainCanvas nodes={nodes} edges={edges} onOpenNode={() => {}} /></Sample>
    <Sample name="Activity overview"><TraceCanvas run={null} dsl={null} selectedStepId={null} onStepSelect={() => {}} workspaceId=""
      waitpointTokensByStepId={new Map()} heatmapBuckets={new Map()} stepMetrics={new Map()} overview={{ nodes, edges }} /></Sample>
    <Sample name="Routine step trace"><RoutineDefinitionCanvas definition={definition} slug="fixture" name="Fixture routine"
      selectedStepId={selected} onStepSelect={setSelected} /></Sample>
    <Sample name="Workflow graph"><WorkflowGraph missions={missions} onTaskClick={() => {}} /></Sample>
  </main>
}
