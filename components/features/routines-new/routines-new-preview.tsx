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
import { Columns2, Maximize2, PanelRight, Workflow } from "lucide-react"

import { cn } from "@/lib/utils"
import { SubBar } from "@/components/layout/sub-bar"
import { DSL_BY_FIDELITY, opacityOf, type Fidelity } from "@/lib/routines-preview/fixtures"
import { VariantSplit } from "./variant-split"
import { VariantCanvasFirst } from "./variant-canvas-first"
import { VariantInspector } from "./variant-inspector"

const VARIANTS = [
  {
    id: "split" as const,
    label: "A · Split",
    icon: Columns2,
    pitch: "Graf a kód vedle sebe, oba pořád vidět.",
  },
  {
    id: "canvas" as const,
    label: "B · Plátno napřed",
    icon: Maximize2,
    pitch: "Graf přes celou šířku, kód se vysune až když editujete.",
  },
  {
    id: "inspector" as const,
    label: "C · Inspektor",
    icon: PanelRight,
    pitch: "Klik na krok → jen jeho fragment, závislosti a záruky.",
  },
] as const

type VariantId = (typeof VARIANTS)[number]["id"]

const FIDELITIES: { id: Fidelity; label: string; hint: string }[] = [
  { id: "today", label: "Dnes", hint: "7 kroků, tak jak recept běžel v produkci" },
  { id: "granular", label: "Granulárně", hint: "14 kroků, stejná práce rozsekaná na buňky" },
]

export function RoutinesNewPreview() {
  const [variant, setVariant] = React.useState<VariantId>("split")
  const [fidelity, setFidelity] = React.useState<Fidelity>("granular")

  const active = VARIANTS.find((v) => v.id === variant) ?? VARIANTS[0]
  const dsl = DSL_BY_FIDELITY[fidelity]
  const stepCount = (dsl.steps ?? []).length

  return (
    <div className="flex h-[calc(100vh-48px)] flex-col bg-background">
      <SubBar<VariantId>
        icon={Workflow}
        title="Routines"
        section="Návrh detailu"
        description={
          <>
            náhled · {stepCount} kroků · {opacityOf(dsl)}% agentních
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
        {variant === "split" && <VariantSplit fidelity={fidelity} />}
        {variant === "canvas" && <VariantCanvasFirst fidelity={fidelity} />}
        {variant === "inspector" && <VariantInspector fidelity={fidelity} />}
      </div>
    </div>
  )
}
