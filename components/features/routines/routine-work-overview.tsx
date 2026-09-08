"use client"

import { useState } from "react"
import { Bot, GitBranch, ListOrdered, Network, PackageCheck } from "lucide-react"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { DetailCard, Pill } from "@/components/ui/detail"
import { Button } from "@/components/ui/button"
import { describeStep, isRecord } from "@/lib/routine-step-describe"
import { routineInputSpecs } from "@/lib/routine-inputs"
import { RoutineStepDefinition } from "./routine-step-definition"

/** Read the saved definition without inventing execution order, checks or effects. */
export function RoutineWorkOverview({ definition, onMap, onEdit }: { definition: unknown; onMap?: () => void; onEdit?: () => void }) {
  const [expanded, setExpanded] = useState(false)
  const dsl = isRecord(definition) ? definition : {}
  const steps = Array.isArray(dsl.steps) ? dsl.steps.filter(isRecord) : []
  const inputs = routineInputSpecs(dsl)
  const outputs = Array.isArray(dsl.outputs) ? dsl.outputs.filter(isRecord) : []
  return <div className="space-y-4">
    <DetailCard title="Before you start" icon={PackageCheck}>
      <div className="grid gap-5 sm:grid-cols-2">
        <div><h3 className="text-sm font-medium">What you provide</h3>{inputs.length ? <ul className="mt-2 space-y-2 text-xs text-muted-foreground">{inputs.map(input => <li key={input.name}><span className="text-foreground">{input.label || input.name}</span> · {input.required ? "required" : "optional"}{input.default !== undefined && " · prepared default"}{input.description && <p className="mt-1">{input.description}</p>}</li>)}</ul> : <p className="mt-2 text-xs text-muted-foreground">No start-form answers are declared.</p>}</div>
        <div><h3 className="text-sm font-medium">Expected result</h3>{outputs.length ? <ul className="mt-2 space-y-2 text-xs text-muted-foreground">{outputs.map((output, i) => <li key={i}><span className="text-foreground">{String(output.label || output.name || "Result")}</span>{typeof output.description === "string" && <p className="mt-1">{output.description}</p>}</li>)}</ul> : <p className="mt-2 text-xs text-muted-foreground">No result is declared. Recorded responses and available files will appear in the run.</p>}<p className="mt-2 text-xs text-muted-foreground">Declared outputs describe the recipe; the run shows what actually became available.</p></div>
      </div>
    </DetailCard>
    <DetailCard title="How the work happens" icon={ListOrdered} action={onMap && <Button size="sm" variant="ghost" onClick={onMap}><Network className="mr-1.5 h-3.5 w-3.5" />Workflow map</Button>}>
      <p className="mb-3 text-xs text-muted-foreground">Steps from the saved recipe. Dependencies and conditions determine which work runs.</p>
      {!steps.length && <p className="text-sm text-muted-foreground">No steps are defined yet.</p>}
      {(expanded ? steps : steps.slice(0, 5)).map((step, i) => {
        const description = describeStep(step, i + 1)
        const name = typeof step.name === "string" ? step.name : String(step.id || description.title).replaceAll("_", " ")
        return <details key={String(step.id || i)} className="group border-t border-border/60 py-3 first:border-t-0">
          <summary className="flex cursor-pointer list-none items-start gap-3 rounded-lg focus-visible:outline focus-visible:outline-primary">
            <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-muted text-xs text-muted-foreground">{typeof step.agent_slug === "string" ? <AgentAvatar seed={step.agent_slug} className="h-7 w-7" alt={step.agent_slug} /> : i + 1}</span>
            <span className="min-w-0 flex-1"><span className="block text-sm font-medium capitalize">{name}</span><span className="mt-1 block text-xs text-muted-foreground">{description.title}</span><span className="mt-2 flex flex-wrap gap-1.5">{Boolean(step.if) && <Pill tone="warn"><GitBranch className="h-3 w-3" />Conditional</Pill>}{typeof step.agent_slug === "string" && <Pill tone="purple"><Bot className="h-3 w-3" />{step.agent_slug}</Pill>}{Array.isArray(step.needs) && step.needs.length > 0 && <span className="text-xs text-muted-foreground">After: {step.needs.map(String).join(", ")}</span>}</span></span><span className="text-xs text-primary group-open:hidden">Details</span>
          </summary>
          <div className="ml-11 mt-4 space-y-3"><RoutineStepDefinition step={step} />{Boolean(step.if) && <p className="break-words text-xs text-muted-foreground">Condition: {String(step.if)}</p>}{onEdit && <button onClick={onEdit} className="text-xs text-primary">Edit recipe →</button>}</div>
        </details>
      })}
      {steps.length > 5 && <Button variant="ghost" size="sm" onClick={() => setExpanded(v => !v)}>{expanded ? "Show fewer steps" : `Show all ${steps.length} steps`}</Button>}
    </DetailCard>
  </div>
}
