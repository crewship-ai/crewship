"use client"

// Variant B — "Plátno napřed". The graph is the page; code is a mode
// you enter, not a permanent neighbour.
//
// Thesis: reading a routine and editing a routine are different jobs
// done at different moments, and reading is the far more common one.
// Give reading the full width — wide DAGs, foreach bodies and fan-in
// all need it — and let the editor take a share when you actually mean
// to change something.
//
// The panel is a sibling rather than an overlay, so opening it shrinks
// the canvas and the graph slides left instead of hiding under it.
//
// Cost: opening the editor is a deliberate act. If your job is to watch
// the code and the graph agree line by line, Split starts you there.

import * as React from "react"
import { Code2, X } from "lucide-react"

import { cn } from "@/lib/utils"
import { type Fidelity } from "@/lib/routines-preview/fixtures"
import {
  CodePane,
  DefinitionCanvas,
  DependencySummary,
  OpacityMeter,
  RunHistoryCard,
  type Workbench,
} from "./shared"

export function VariantCanvasFirst({ fidelity, wb }: { fidelity: Fidelity; wb: Workbench }) {
  // Local to this layout on purpose: "is the editor open" is a property
  // of THIS arrangement, not of the routine. Split has no such state,
  // and hoisting it would make one layout carry the other's concerns.
  const [editing, setEditing] = React.useState(false)

  return (
    <div className="flex h-full flex-col overflow-auto">
      {/* Hero canvas. The code panel is a SIBLING, not an overlay: as a
          sibling it takes real width, the canvas shrinks, and the graph
          slides left into what is still visible. As an overlay it
          covered the half of the graph the reader was looking at. */}
      {/* Stacks below md, splits from md up. `w-full` on a flex ROW
          would starve the canvas to zero width on a phone, so the axis
          flips with the breakpoint rather than only the widths. */}
      <div className="flex h-[68vh] min-h-[420px] shrink-0 flex-col border-b border-border/60 md:h-[68vh] md:flex-row">
        <div className="relative min-h-[240px] min-w-0 flex-1">
          <DefinitionCanvas
            dsl={wb.dsl}
            selectedStepId={wb.selected}
            onStepSelect={wb.onSelect}
            focusStepId={wb.focus}
            recenterOnResize
            viewportRef={wb.viewportRef}
          />

          <button
            type="button"
            onClick={() => setEditing((v) => !v)}
            className={cn(
              "absolute right-3 top-3 z-10 inline-flex items-center gap-1.5 rounded-lg border px-2.5 py-1.5 text-[11px] font-medium backdrop-blur transition-colors",
              editing
                ? "border-primary/40 bg-primary/15 text-primary"
                : "border-border/60 bg-card/85 text-muted-foreground hover:text-foreground",
            )}
          >
            {editing ? <X className="h-3.5 w-3.5" /> : <Code2 className="h-3.5 w-3.5" />}
            {editing ? "Zavřít kód" : "Upravit kód"}
          </button>
        </div>

        {/* Rendered only while open so CodeMirror is not mounted, and
            re-measuring, behind a panel nobody asked for. */}
        {editing && (
          <aside className="h-[45%] w-full shrink-0 border-t border-border/60 md:h-auto md:w-[46%] md:border-l md:border-t-0 lg:w-[38%]">
            <CodePane
              fidelity={fidelity}
              footnote="Zavři panel a graf se překreslí z uloženého DSL."
              onStepAtCaret={wb.onCaret}
              follow={wb.follow}
              onFollowChange={wb.setFollow}
              onApply={wb.setDsl}
            />
          </aside>
        )}
      </div>

      <div className="space-y-4 p-4">
        <OpacityMeter dsl={wb.dsl} />
        <div className="grid gap-4 lg:grid-cols-[1.6fr_1fr]">
          <div>
            <h3 className="mb-2 text-[12px] font-semibold uppercase tracking-wider text-muted-foreground">
              Na čem to visí
            </h3>
            <DependencySummary columns={2} />
          </div>
          <RunHistoryCard />
        </div>
      </div>
    </div>
  )
}
