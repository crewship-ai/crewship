"use client"

import { useState } from "react"
import { Bot, GitBranch, ListOrdered, Network, PackageCheck, FileCode2, ArrowLeftRight, Globe, Clock, Braces, Bell, Database, Repeat2, ScrollText, PenSquare, Cog, Type, Hash, ToggleLeft, ListChecks, FileText, ArrowDownToLine, ArrowUpFromLine, type LucideIcon } from "lucide-react"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { DetailCard, Pill } from "@/components/ui/detail"
import { Button } from "@/components/ui/button"
import { describeStep, isRecord } from "@/lib/routine-step-describe"
import { routineInputSpecs } from "@/lib/routine-inputs"
import { cn } from "@/lib/utils"
import { useWorkspaceAgentDirectory } from "@/hooks/use-workspace-agent-directory"
import { RoutineAgentLink } from "./routine-agent-link"
import { RoutineStepDefinition } from "./routine-step-definition"

const STEP_VISUALS: Record<string, { icon: LucideIcon; label: string; tone: string }> = {
  agent_run: { icon: Bot, label: "Agent task", tone: "bg-purple/10 text-purple" },
  script: { icon: FileCode2, label: "Run a script", tone: "bg-info/10 text-info" },
  transform: { icon: ArrowLeftRight, label: "Prepare data", tone: "bg-success/10 text-success" },
  http: { icon: Globe, label: "Call a service", tone: "bg-info/10 text-info" },
  wait: { icon: Clock, label: "Wait", tone: "bg-warn/10 text-warn" },
  code: { icon: Braces, label: "Run code", tone: "bg-warn/10 text-warn" },
  notify: { icon: Bell, label: "Send a notification", tone: "bg-purple/10 text-purple" },
  query: { icon: Database, label: "Read stored data", tone: "bg-info/10 text-info" },
  foreach: { icon: Repeat2, label: "For each item", tone: "bg-purple/10 text-purple" },
  call_pipeline: { icon: ScrollText, label: "Run another routine", tone: "bg-purple/10 text-purple" },
  crewship: { icon: PenSquare, label: "Crewship action", tone: "bg-warn/10 text-warn" },
}
const readableName = (value: unknown) => String(value ?? "").replace(/[_-]+/g, " ").replace(/^./, c => c.toUpperCase())
const fieldVisual = (type?: string, choices?: string[]) => choices?.length ? { icon: ListChecks, label: "Choice" } : type === "integer" || type === "number" ? { icon: Hash, label: "Number" } : type === "boolean" ? { icon: ToggleLeft, label: "Yes / no" } : type === "array" ? { icon: ListChecks, label: "List" } : type === "object" ? { icon: Braces, label: "Structured data" } : { icon: Type, label: "Text" }

/** Read the saved definition without inventing execution order, checks or effects. */
export function RoutineWorkOverview({ definition, onMap, onEdit, workspaceId }: { workspaceId?: string; definition: unknown; onMap?: () => void; onEdit?: () => void }) {
  const { agents } = useWorkspaceAgentDirectory(workspaceId)
  const [expanded, setExpanded] = useState(false)
  const dsl = isRecord(definition) ? definition : {}
  const steps = Array.isArray(dsl.steps) ? dsl.steps.filter(isRecord) : []
  const inputs = routineInputSpecs(dsl)
  const outputs = Array.isArray(dsl.outputs) ? dsl.outputs.filter(isRecord) : []
  return <div className="space-y-4">
    <DetailCard title="Before you start" icon={PackageCheck}>
      <div className="grid gap-5 md:grid-cols-2">
        <section className="min-w-0"><div className="mb-3 flex items-center gap-2"><ArrowDownToLine className="h-4 w-4 text-primary" /><h3 className="text-sm font-medium">What you provide</h3><span className="ml-auto text-xs text-muted-foreground">{inputs.length} {inputs.length === 1 ? "question" : "questions"}</span></div>
          {inputs.length ? <ul className="space-y-2.5">{inputs.map(input => { const visual = fieldVisual(input.type, input.options); const Icon = visual.icon; return <li key={input.name} className="rounded-xl border border-border/50 bg-muted/20 p-3.5"><div className="flex items-start gap-3"><span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary"><Icon className="h-4 w-4" /></span><div className="min-w-0 flex-1"><div className="flex flex-wrap items-center gap-2"><span className="text-[13px] font-medium">{input.label || readableName(input.name)}</span><span className="rounded-full bg-muted px-2 py-0.5 text-[10px] text-muted-foreground">{input.required ? "Required" : "Optional"}</span></div><p className="mt-1 text-[11px] text-muted-foreground">{visual.label}{input.default !== undefined && " · Default set"}</p>{input.description && <p className="mt-2 text-xs leading-relaxed text-foreground/70">{input.description}</p>}</div></div></li> })}</ul> : <p className="rounded-xl border border-border/50 bg-muted/20 p-4 text-xs text-muted-foreground">No questions to answer before starting.</p>}
          {inputs.length > 0 && <p className="mt-3 text-[11px] text-muted-foreground">Review these answers when you start. Defaults can be changed for each run.</p>}
        </section>
        <section className="min-w-0"><div className="mb-3 flex items-center gap-2"><ArrowUpFromLine className="h-4 w-4 text-success" /><h3 className="text-sm font-medium">Expected result</h3><span className="ml-auto text-xs text-muted-foreground">{outputs.length} {outputs.length === 1 ? "output" : "outputs"}</span></div>
          {outputs.length ? <ul className="space-y-2.5">{outputs.map((output, i) => <li key={i} className="flex items-start gap-3 rounded-xl border border-border/50 bg-muted/20 p-3.5"><span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-success/10 text-success"><FileText className="h-4 w-4" /></span><div className="min-w-0"><h4 className="text-[13px] font-medium">{typeof output.label === "string" ? output.label : readableName(output.name || "Result")}</h4><p className="mt-1 text-[11px] text-muted-foreground">{fieldVisual(typeof output.type === "string" ? output.type : undefined).label}</p>{typeof output.description === "string" && <p className="mt-2 text-xs leading-relaxed text-foreground/70">{output.description}</p>}</div></li>)}</ul> : <p className="rounded-xl border border-border/50 bg-muted/20 p-4 text-xs text-muted-foreground">No output is declared. Recorded responses and available files will appear in the run.</p>}
          <p className="mt-3 text-[11px] text-muted-foreground">Expected outputs from the recipe. Each run shows what was actually produced.</p>
        </section>
      </div>
    </DetailCard>
    <DetailCard title="How the work happens" icon={ListOrdered} action={onMap && <Button size="sm" variant="ghost" onClick={onMap}><Network className="mr-1.5 h-3.5 w-3.5" />Workflow map</Button>}>
      <p className="mb-3 text-xs text-muted-foreground">Steps from the saved recipe. Dependencies and conditions determine which work runs.</p>
      {!steps.length && <p className="text-sm text-muted-foreground">No steps are defined yet.</p>}
      {(expanded ? steps : steps.slice(0, 5)).map((step, i) => {
        const description = describeStep(step, i + 1)
        const visual = STEP_VISUALS[String(step.type)] ?? { icon: Cog, label: "Step", tone: "bg-muted text-muted-foreground" }
        const Icon = visual.icon
        const agent = agents?.find(a => a.slug === step.agent_slug)
        const name = typeof step.name === "string" ? step.name : String(step.id || description.title).replaceAll("_", " ")
        return <details key={String(step.id || i)} className="group border-t border-border/60 py-3 first:border-t-0">
          <summary className="flex cursor-pointer list-none items-start gap-3 rounded-lg focus-visible:outline focus-visible:outline-primary">
            <span className={cn("relative flex h-10 w-10 shrink-0 items-center justify-center rounded-xl", visual.tone)} title={visual.label} data-step-kind={String(step.type)}>{agent ? <AgentAvatar seed={agent.avatar_seed || agent.name} style={agent.avatar_style || agent.crew?.avatar_style || undefined} agentId={agent.id} avatarUrl={agent.avatar_url} workspaceId={workspaceId} className="h-8 w-8" alt="" /> : <Icon className="h-5 w-5" aria-hidden="true" />}<span className="absolute -bottom-1 -right-1 flex h-4 min-w-4 items-center justify-center rounded-full border border-border bg-card px-0.5 text-[9px] tabular-nums text-muted-foreground">{i + 1}</span></span>
            <span className="min-w-0 flex-1"><span className="block text-sm font-medium capitalize">{name}</span><span className="mt-1 block text-xs text-muted-foreground">{description.kind === "unknown" ? visual.label : description.title}</span><span className="mt-2 flex flex-wrap gap-1.5">{Boolean(step.if) && <Pill tone="warn"><GitBranch className="h-3 w-3" />Conditional</Pill>}{Array.isArray(step.needs) && step.needs.length > 0 && <span className="text-xs text-muted-foreground">After: {step.needs.map(String).join(", ")}</span>}</span></span><span className="text-xs text-primary group-open:hidden">Details</span>
          </summary>
          <div className="ml-[52px] mt-4 space-y-3">{typeof step.agent_slug === "string" && <RoutineAgentLink slug={step.agent_slug} agent={agent} workspaceId={workspaceId} />}<RoutineStepDefinition step={step} />{Boolean(step.if) && <p className="break-words text-xs text-muted-foreground">Condition: {String(step.if)}</p>}{onEdit && <button onClick={onEdit} className="text-xs text-primary">Edit recipe →</button>}</div>
        </details>
      })}
      {steps.length > 5 && <Button variant="ghost" size="sm" onClick={() => setExpanded(v => !v)}>{expanded ? "Show fewer steps" : `Show all ${steps.length} steps`}</Button>}
    </DetailCard>
  </div>
}
