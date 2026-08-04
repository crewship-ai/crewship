"use client"

// /routines-new — a design preview, deliberately unlisted in the
// sidebar and wired to nothing.
//
// It exists so a layout argument can be had against the real renderer
// instead of a picture: every variant below feeds Activity's own
// TraceCanvas, so what you see here is what the routine detail would
// actually look like. Nothing on this page writes; there is no server
// call, no workspace lookup and no seed dependency.
//
// Two axes:
//   variant  — three ways to lay out graph + code + dependencies
//   fidelity — the same production routine before and after granulation
//
// The fidelity toggle is the more important of the two. It is the
// argument that the backend should grow more, smaller step kinds: the
// left column of every variant gets dramatically more useful when the
// routine stops being six agent turns in a trench coat.

import * as React from "react"
import { Columns2, LayoutList, Maximize2, Workflow } from "lucide-react"

import { cn } from "@/lib/utils"
import { SubBar } from "@/components/layout/sub-bar"
import { opacityOf, type Fidelity } from "@/lib/routines-preview/fixtures"
import { useWorkbench } from "./shared"
import { VariantCanvasFirst } from "./variant-canvas-first"
import { VariantCard } from "./variant-card"
import { VariantSplit } from "./variant-split"

// Two, not three. The inspector variant is gone: its right rail
// answered "what does THIS box do", which the hover card already
// answers for a glance and the code panel answers in full — a third
// surface for the same question is a third place to keep in sync.
//
// Canvas-first leads because reading a routine is the common act and
// editing is the occasional one. Split is one click away for the times
// the job really is watching the code and the graph agree.
const VARIANTS = [
  {
    id: "canvas" as const,
    label: "Plátno napřed",
    icon: Maximize2,
    pitch: "Graf přes celou šířku; kód si vezme část plochy, až o něj řekneš.",
  },
  {
    id: "split" as const,
    label: "Split",
    icon: Columns2,
    pitch: "Graf a kód vedle sebe, oba pořád vidět.",
  },
  {
    id: "card" as const,
    label: "Karta",
    icon: LayoutList,
    pitch:
      "Návrh CELÉ karty routiny — bez tabů, kartami jako Inbox. Preview i Advanced pryč.",
  },
] as const

type VariantId = (typeof VARIANTS)[number]["id"]

const FIDELITIES: { id: Fidelity; label: string; hint: string }[] = [
  { id: "today", label: "Dnes", hint: "7 kroků, tak jak recept běžel v produkci" },
  { id: "granular", label: "Granulárně", hint: "14 kroků, stejná práce rozsekaná na buňky" },
]

export function RoutinesNewPreview() {
  const [variant, setVariant] = React.useState<VariantId>("canvas")
  const [fidelity, setFidelity] = React.useState<Fidelity>("granular")
  // Owned here so switching layout rearranges the same work rather than
  // restarting it — selection, edits and viewport all survive the tab.
  const wb = useWorkbench(fidelity)

  const active = VARIANTS.find((v) => v.id === variant) ?? VARIANTS[0]
  const stepCount = (wb.dsl.steps ?? []).length

  return (
    <div className="flex h-[calc(100vh-48px)] flex-col bg-background">
      <SubBar<VariantId>
        icon={Workflow}
        title="Routines"
        section="Návrh detailu"
        description={
          <>
            náhled · {stepCount} kroků · {opacityOf(wb.dsl)}% agentních
          </>
        }
        ariaLabel="Náhled návrhu detailu routiny"
        tabs={VARIANTS.map((v) => ({ id: v.id, label: v.label, icon: v.icon }))}
        activeTab={variant}
        onTabChange={setVariant}
        tools={
          <div className="flex items-center gap-1 rounded-lg border border-border/60 bg-card p-0.5">
            {FIDELITIES.map((f) => (
              <button
                key={f.id}
                type="button"
                onClick={() => setFidelity(f.id)}
                title={f.hint}
                className={cn(
                  "rounded-md px-2.5 py-1 text-[11px] font-medium transition-colors",
                  fidelity === f.id
                    ? "bg-primary/15 text-primary"
                    : "text-muted-foreground hover:text-foreground",
                )}
              >
                {f.label}
              </button>
            ))}
          </div>
        }
      />

      {/* One line of orientation. The variants differ in layout, not in
          content, so saying what THIS one argues saves comparing by eye. */}
      <div className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1 border-b border-border/60 bg-card/40 px-4 py-2">
        <span className="rounded border border-primary/30 bg-primary/10 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wider text-primary">
          Náhled
        </span>
        <span className="text-[12px] text-foreground/85">{active.pitch}</span>
        <span className="text-[11px] text-muted-foreground">
          Recept: Měsíční účetní podklady — skutečná šablona, která běžela v produkci. Nic se
          neukládá.
        </span>
      </div>

      <div className="min-h-0 flex-1">
        {variant === "canvas" && <VariantCanvasFirst fidelity={fidelity} wb={wb} />}
        {variant === "split" && <VariantSplit fidelity={fidelity} wb={wb} />}
        {variant === "card" && <VariantCard fidelity={fidelity} wb={wb} />}
      </div>
    </div>
  )
}
