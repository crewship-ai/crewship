"use client"

import {
  ArrowDownToLine,
  ArrowUpFromLine,
  Braces,
  Hash,
  ListChecks,
  ToggleLeft,
  Type,
  type LucideIcon,
} from "lucide-react"
import { DetailCard } from "@/components/ui/detail"
import { isRecord } from "@/lib/routine-step-describe"
import { routineInputSpecs } from "@/lib/routine-inputs"
import { RoutineStepSpine, type RoutineStepSpineProps } from "./routine-step-spine"

// routine-work-overview — the saved recipe, read the way a client reads it:
// what it wants, what it returns, and what it does in between.
//
// The two halves used to be a card each, both carrying a paragraph explaining
// what the card was. Inputs and results are one question — the contract — and
// answering it takes two columns of rows, not two cards of prose. The steps
// then get the width they need, with the graph a toggle on the same block
// rather than a second card appearing below it.

const readableName = (value: unknown) =>
  String(value ?? "")
    .replace(/[_-]+/g, " ")
    .replace(/^./, (c) => c.toUpperCase())

const fieldVisual = (type?: string, choices?: string[]): { icon: LucideIcon; label: string } =>
  choices?.length
    ? { icon: ListChecks, label: "Choice" }
    : type === "integer" || type === "number"
      ? { icon: Hash, label: "Number" }
      : type === "boolean"
        ? { icon: ToggleLeft, label: "Yes / no" }
        : type === "array"
          ? { icon: ListChecks, label: "List" }
          : type === "object"
            ? { icon: Braces, label: "Structured data" }
            : { icon: Type, label: "Text" }

/** One row of the contract: a field the reader supplies, or one they get back. */
function ContractRow({
  icon: Icon,
  tone,
  name,
  meta,
  description,
}: {
  icon: LucideIcon
  tone: string
  name: string
  meta: string
  description?: string
}) {
  return (
    <li className="flex items-start gap-2.5 py-1.5">
      <span
        className={`mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-lg ${tone}`}
      >
        <Icon className="h-3.5 w-3.5" />
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-baseline gap-x-2">
          <span className="text-[13px] font-medium text-foreground">{name}</span>
          <span className="text-[11px] text-muted-foreground">{meta}</span>
        </div>
        {description && (
          <p className="mt-0.5 text-xs leading-relaxed text-muted-foreground">{description}</p>
        )}
      </div>
    </li>
  )
}

interface RoutineWorkOverviewProps {
  workspaceId?: string
  definition: unknown
  /** The recipe graph, shown when the reader switches the step block to Map. */
  map?: RoutineStepSpineProps["map"]
  onEdit?: () => void
}

/** Read the saved definition without inventing execution order, checks or effects. */
export function RoutineWorkOverview({
  definition,
  map,
  onEdit,
  workspaceId,
}: RoutineWorkOverviewProps) {
  const dsl = isRecord(definition) ? definition : {}
  const inputs = routineInputSpecs(dsl)
  const outputs = Array.isArray(dsl.outputs) ? dsl.outputs.filter(isRecord) : []

  return (
    <div className="space-y-4">
      {inputs.length || outputs.length ? (
        <DetailCard title="Inputs and results">
          <div className="grid gap-x-8 gap-y-5 md:grid-cols-2">
            <section className="min-w-0">
              <div className="mb-2 flex items-center gap-2 border-b border-border/50 pb-2">
                <ArrowDownToLine className="h-3.5 w-3.5 text-primary" />
                <h3 className="text-[13px] font-medium">Inputs</h3>
                <span className="ml-auto text-[11px] text-muted-foreground">
                  {inputs.length}
                </span>
              </div>
              {inputs.length ? (
                <ul>
                  {inputs.map((input) => {
                    const visual = fieldVisual(input.type, input.options)
                    return (
                      <ContractRow
                        key={input.name}
                        icon={visual.icon}
                        tone="bg-primary/10 text-primary"
                        name={input.label || readableName(input.name)}
                        meta={`${visual.label} · ${input.required ? "required" : "optional"}${input.default !== undefined ? " · has a default" : ""}`}
                        description={input.description}
                      />
                    )
                  })}
                </ul>
              ) : (
                <p className="py-1.5 text-xs text-muted-foreground">
                  Nothing to answer before starting.
                </p>
              )}
            </section>
            {outputs.length ? (
              <section className="min-w-0">
                <div className="mb-2 flex items-center gap-2 border-b border-border/50 pb-2">
                  <ArrowUpFromLine className="h-3.5 w-3.5 text-success" />
                  <h3 className="text-[13px] font-medium">Results</h3>
                  <span className="ml-auto text-[11px] text-muted-foreground">
                    {outputs.length}
                  </span>
                </div>
                {outputs.length ? (
                  <ul>
                    {outputs.map((output, i) => {
                      const visual = fieldVisual(
                        typeof output.type === "string" ? output.type : undefined,
                      )
                      return (
                        <ContractRow
                          key={i}
                          icon={visual.icon}
                          tone="bg-success/10 text-success"
                          name={
                            typeof output.label === "string"
                              ? output.label
                              : readableName(output.name || "Result")
                          }
                          meta={visual.label}
                          description={
                            typeof output.description === "string"
                              ? output.description
                              : undefined
                          }
                        />
                      )
                    })}
                  </ul>
                ) : (
                  <p className="py-1.5 text-xs text-muted-foreground">
                    Nothing is declared. Each run still keeps its recorded response and any
                    files.
                  </p>
                )}
              </section>
            ) : (
              <details className="self-start py-2 text-xs text-muted-foreground">
                <summary className="cursor-pointer">Results · 0 declared</summary>
                <p className="mt-2">Each run still keeps its response and saved files.</p>
              </details>
            )}
          </div>
        </DetailCard>
      ) : (
        <div className="flex flex-wrap gap-4 rounded-lg border border-border px-4 py-3 text-xs text-muted-foreground">
          <details>
            <summary className="cursor-pointer">Inputs · 0</summary>
            <p className="mt-2">Nothing to fill in before running.</p>
          </details>
          <details>
            <summary className="cursor-pointer">Results · 0 declared</summary>
            <p className="mt-2">Each run still keeps its response and saved files.</p>
          </details>
        </div>
      )}
      <RoutineStepSpine
        workspaceId={workspaceId}
        definition={definition}
        map={map}
        onEdit={onEdit}
      />
    </div>
  )
}
