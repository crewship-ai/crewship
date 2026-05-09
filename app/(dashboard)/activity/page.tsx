"use client"

import { ActivityTracePage } from "@/components/features/activity/activity-trace-page"

// /activity — single-canvas trace view. Picks a run from the left
// rail and renders the full execution chain (issue → routine → step
// nodes with data-flow edges) on a ReactFlow canvas, with a step
// detail panel on the right.
//
// The legacy 4-tab layout (Runs / Graph / Timeline / Feed via
// OrchestrationPageShell) was retired here on the IA refactor — too
// much fragmentation. The /orchestration route still uses the old
// layout for backwards compat.
export default function ActivityPage() {
  return <ActivityTracePage />
}
