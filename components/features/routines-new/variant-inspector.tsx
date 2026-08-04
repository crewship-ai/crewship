"use client"

// Variant C — "Inspektor". Graph on the left, and a right rail whose
// content follows the selection: nothing selected → the whole
// definition; a node selected → that step's fragment, what it waits
// on, what it unblocks, and what its kind guarantees.
//
// Thesis: the question an operator actually has is never "show me the
// whole YAML", it is "what does THIS box do, and what breaks if it
// fails". This is the n8n / Trigger.dev reading, and it is the variant
// that gets better the more the routine is granulated — which is the
// direction we want the routines to go anyway.
//
// Cost: the full definition is one click away rather than always
// present, so "just show me the file" costs a deselect.

import * as React from "react"

import { DSL_BY_FIDELITY, type Fidelity } from "@/lib/routines-preview/fixtures"
import {
  DefinitionCanvas,
  DependencySummary,
  OpacityMeter,
  RunHistoryCard,
  StepInspector,
} from "./shared"

export function VariantInspector({ fidelity }: { fidelity: Fidelity }) {
  const dsl = DSL_BY_FIDELITY[fidelity]
  const [selected, setSelected] = React.useState<string | null>(null)

  // Selecting a step id that the other fidelity does not have would
  // leave the rail stuck on "nothing selected" with a stale id; clear
  // it whenever the routine under inspection changes.
  React.useEffect(() => {
    setSelected(null)
  }, [fidelity])

  return (
    <div className="flex h-full flex-col overflow-auto">
      <div className="grid h-[64vh] min-h-[440px] shrink-0 grid-cols-1 divide-y divide-border/60 border-b border-border/60 lg:grid-cols-[1.7fr_1fr] lg:divide-x lg:divide-y-0">
        <div className="relative min-h-[300px]">
          <DefinitionCanvas
            dsl={dsl}
            selectedStepId={selected}
            onStepSelect={setSelected}
          />
        </div>
        <div className="min-h-[300px] bg-card/30">
          <StepInspector dsl={dsl} stepId={selected} />
        </div>
      </div>

      <div className="space-y-4 p-4">
        <OpacityMeter dsl={dsl} />
        <div className="grid gap-4 lg:grid-cols-[1.6fr_1fr]">
          <div>
            <h3 className="mb-2 text-[12px] font-semibold uppercase tracking-wider text-muted-foreground">
              Na čem to visí
            </h3>
            <DependencySummary columns={2} />
          </div>
          <RunHistoryCard compact />
        </div>
      </div>
    </div>
  )
}
