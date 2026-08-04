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
  const [follow, setFollow] = React.useState(true)
  // Separate from `selected` on purpose. Selection is a persistent
  // choice; focus is a one-shot "bring this into view". Merging them
  // would mean turning follow off could not leave the last selection
  // highlighted, and that a re-render could re-centre the viewport
  // after the user had panned away from it.
  const [focus, setFocus] = React.useState<string | null>(null)

  const handleCaret = React.useCallback(
    (stepId: string | null) => {
      if (!follow || !stepId) return
      setSelected(stepId)
      setFocus(stepId)
    },
    [follow],
  )

  // A click is its own focus request; clear the caret-driven one so a
  // later caret move to the SAME step still re-centres.
  const handleSelect = React.useCallback((id: string | null) => {
    setSelected(id)
    setFocus(null)
  }, [])

  return (
    <div className="flex h-full flex-col overflow-auto">
      {/* Top half — the split itself. Fixed height so the summary
          below is discoverable by scroll, not hidden past the fold. */}
      <div className="grid h-[62vh] min-h-[420px] shrink-0 grid-cols-1 divide-y divide-border/60 border-b border-border/60 lg:grid-cols-2 lg:divide-x lg:divide-y-0">
        <div className="relative min-h-[280px]">
          <DefinitionCanvas
            dsl={dsl}
            selectedStepId={selected}
            onStepSelect={handleSelect}
            focusStepId={focus}
          />
        </div>
        <div className="min-h-[280px]">
          <CodePane
            fidelity={fidelity}
            onStepAtCaret={handleCaret}
            follow={follow}
            onFollowChange={setFollow}
          />
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
