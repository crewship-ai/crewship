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

import { type Fidelity } from "@/lib/routines-preview/fixtures"
import {
  CodePane,
  DefinitionCanvas,
  DependencySummary,
  OpacityMeter,
  RunHistoryCard,
  type Workbench,
} from "./shared"

export function VariantSplit({ fidelity, wb }: { fidelity: Fidelity; wb: Workbench }) {

  return (
    <div className="flex h-full flex-col overflow-auto">
      {/* Top half — the split itself. Fixed height so the summary
          below is discoverable by scroll, not hidden past the fold. */}
      {/* Below lg the two halves stack, and the container grows to fit
          both floors instead of squeezing each into half a viewport —
          two 210px panes are two unusable panes. */}
      <div className="grid min-h-[420px] shrink-0 grid-cols-1 divide-y divide-border/60 border-b border-border/60 lg:h-[62vh] lg:grid-cols-2 lg:divide-x lg:divide-y-0">
        <div className="relative h-[45vh] min-h-[280px] lg:h-auto">
          <DefinitionCanvas
            dsl={wb.dsl}
            selectedStepId={wb.selected}
            onStepSelect={wb.onSelect}
            focusStepId={wb.focus}
            recenterOnResize
            viewportRef={wb.viewportRef}
          />
        </div>
        <div className="h-[45vh] min-h-[280px] lg:h-auto">
          <CodePane
            fidelity={fidelity}
            onStepAtCaret={wb.onCaret}
            follow={wb.follow}
            onFollowChange={wb.setFollow}
            onApply={wb.setDsl}
          />
        </div>
      </div>

      {/* Bottom — everything a reviewer asks after "what shape is it". */}
      <div className="space-y-4 p-4">
        <OpacityMeter dsl={wb.dsl} />
        <div>
          <h3 className="mb-2 text-[12px] font-semibold uppercase tracking-wider text-muted-foreground">
            Dependencies
          </h3>
          <DependencySummary columns={3} />
        </div>
        <RunHistoryCard />
      </div>
    </div>
  )
}
