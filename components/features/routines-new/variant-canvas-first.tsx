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
import { DSL_BY_FIDELITY, type Fidelity } from "@/lib/routines-preview/fixtures"
import {
  CodePane,
  DefinitionCanvas,
  DependencySummary,
  OpacityMeter,
  RunHistoryCard,
} from "./shared"

export function VariantCanvasFirst({ fidelity }: { fidelity: Fidelity }) {
  // The definition is local state, not a constant read: saving in the
  // code pane replaces it and the canvas redraws from it. That loop IS
  // the design under review, so the preview has to actually close it.
  const [dsl, setDsl] = React.useState(DSL_BY_FIDELITY[fidelity])
  React.useEffect(() => {
    setDsl(DSL_BY_FIDELITY[fidelity])
  }, [fidelity])
  const [selected, setSelected] = React.useState<string | null>(null)
  const [editing, setEditing] = React.useState(false)
  const [follow, setFollow] = React.useState(true)
  const [focus, setFocus] = React.useState<string | null>(null)

  const handleCaret = React.useCallback(
    (stepId: string | null) => {
      if (!follow || !stepId) return
      setSelected(stepId)
      setFocus(stepId)
    },
    [follow],
  )

  const handleSelect = React.useCallback((id: string | null) => {
    setSelected(id)
    setFocus(null)
  }, [])

  return (
    <div className="flex h-full flex-col overflow-auto">
      {/* Hero canvas. The code panel is a SIBLING, not an overlay: as a
          sibling it takes real width, the canvas shrinks, and the graph
          slides left into what is still visible. As an overlay it
          covered the half of the graph the reader was looking at. */}
      <div className="flex h-[68vh] min-h-[440px] shrink-0 border-b border-border/60">
        <div className="relative min-w-0 flex-1">
          <DefinitionCanvas
            dsl={dsl}
            selectedStepId={selected}
            onStepSelect={handleSelect}
            focusStepId={focus}
            recenterOnResize
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
          <aside className="w-full shrink-0 border-l border-border/60 md:w-[46%] lg:w-[38%]">
            <CodePane
              fidelity={fidelity}
              footnote="Zavři panel a graf se překreslí z uloženého DSL."
              onStepAtCaret={handleCaret}
              follow={follow}
              onFollowChange={setFollow}
              onApply={setDsl}
            />
          </aside>
        )}
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
          <RunHistoryCard />
        </div>
      </div>
    </div>
  )
}
