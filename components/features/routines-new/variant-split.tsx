"use client"

// Variant A — "Split". Graph and code side by side, both always
// visible, dependency summary and run history stacked underneath.
//
// Thesis: the definition and its picture are one artifact, so never
// make the user choose which one to look at. Editing the JSON and
// watching the graph agree is the fastest way to trust it.
//
// Cost: a DAG that fans out wide gets half the width. The accounting
// routine already fans in three ways, and it is not a big routine.

import * as React from "react"

import { DSL_BY_FIDELITY, type Fidelity } from "@/lib/routines-preview/fixtures"
import {
  CodePane,
  DefinitionCanvas,
  DependencySummary,
  OpacityMeter,
  RunHistoryCard,
} from "./shared"

export function VariantSplit({ fidelity }: { fidelity: Fidelity }) {
  const dsl = DSL_BY_FIDELITY[fidelity]
  const [selected, setSelected] = React.useState<string | null>(null)

  return (
    <div className="flex h-full flex-col overflow-auto">
      {/* Top half — the split itself. Fixed height so the summary
          below is discoverable by scroll, not hidden past the fold. */}
      <div className="grid h-[62vh] min-h-[420px] shrink-0 grid-cols-1 divide-y divide-border/60 border-b border-border/60 lg:grid-cols-2 lg:divide-x lg:divide-y-0">
        <div className="relative min-h-[280px]">
          <DefinitionCanvas
            dsl={dsl}
            selectedStepId={selected}
            onStepSelect={setSelected}
          />
        </div>
        <div className="min-h-[280px]">
          <CodePane fidelity={fidelity} />
        </div>
      </div>

      {/* Bottom — everything a reviewer asks after "what shape is it". */}
      <div className="space-y-4 p-4">
        <OpacityMeter dsl={dsl} />
        <div>
          <h3 className="mb-2 text-[12px] font-semibold uppercase tracking-wider text-muted-foreground">
            Na čem to visí
          </h3>
          <DependencySummary columns={3} />
        </div>
        <RunHistoryCard />
      </div>
    </div>
  )
}
