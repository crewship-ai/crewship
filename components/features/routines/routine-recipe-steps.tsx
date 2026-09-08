"use client"

import { useState } from "react"
import { AnimatePresence, motion, useReducedMotion } from "motion/react"
import { Bot, Braces, CheckCircle2, ChevronRight, Clock, GitBranch, Globe, ListOrdered, Network, Play, Repeat2, SlidersHorizontal, Terminal } from "lucide-react"
import { cn } from "@/lib/utils"
import { Input } from "@/components/ui/input"
import { RoutineDefinitionCanvas } from "./routine-definition-canvas"

type Step = Record<string, unknown>
const object = (value: unknown): Step => value && typeof value === "object" && !Array.isArray(value) ? value as Step : {}
const words = (value: unknown) => String(value ?? "").replaceAll("_", " ").replaceAll("-", " ").replace(/^./, c => c.toUpperCase())
const kinds = {
  agent_run: { label: "Agent task", icon: Bot, tone: "text-purple bg-purple/10" },
  transform: { label: "Prepare data", icon: SlidersHorizontal, tone: "text-success bg-success/10" },
  code: { label: "Run code", icon: Braces, tone: "text-info bg-info/10" },
  script: { label: "Run a script", icon: Terminal, tone: "text-info bg-info/10" },
  http: { label: "Call a service", icon: Globe, tone: "text-info bg-info/10" },
  wait: { label: "Wait", icon: Clock, tone: "text-warn bg-warn/10" },
  foreach: { label: "For each item", icon: Repeat2, tone: "text-purple bg-purple/10" },
} as const
export function recipeStepSummary(step: Step, inputs: Step[] = []) {
  const config = object(step[ String(step.type) ])
  if (step.type === "wait") return config.kind === "approval" ? String(config.approval_title || "Ask for approval") : config.kind === "event" ? `Wait for ${config.event_type || "an event"}` : "Wait until a date"
  if (step.type === "agent_run") return String(step.prompt || "Choose an agent and give it instructions")
  if (step.type === "script") return String(config.path || "Configure a script")
  if (step.type === "http") return `${config.method || "GET"} ${config.url || "Choose a service"}`
  if (step.type === "transform") {
    const source = String(config.input || "")
    const answers = [...new Set([...source.matchAll(/\{\{\s*inputs\.(\w+)/g)].map(m => m[1]))]
    const prior = [...new Set([...source.matchAll(/\{\{\s*steps\.(\w+)\.output/g)].map(m => words(m[1])))]
    if (answers.length > 2) return `Prepare data from ${answers.length} start-form answers`
    if (answers.length) return `Uses ${answers.map(key => inputs.find(i => i.name === key)?.label || words(key)).join(" and ")}`
    if (prior.length) return `Uses the result of ${prior.join(", ")}`
    return String(config.input || config.expression || "Prepare the next step’s input").slice(0, 160)
  }
  if (step.type === "code") return `${config.runtime || "Code"} · ${String(config.code || "").split("\n")[0]}`
  if (step.type === "call_pipeline") return `Run ${step.pipeline_slug || "another routine"}`
  return String(step.description || step.action || words(step.type))
}
export function recipeCondition(step: Step, inputs: Step[]) {
  if (!step.if) return null
  const match = /^inputs\.([\w]+)\s*==\s*(['"])(.*?)\2$/.exec(String(step.if))
  return match ? `${inputs.find(i => i.name === match[1])?.label || words(match[1])}: ${match[3]}` : "Conditional step"
}
export function RoutineRecipeSteps({ definition, slug, name, onChange, onOpenCode }: { definition: Step; slug: string; name: string; onChange: (definition: Step) => void; onOpenCode: () => void }) {
  const steps = Array.isArray(definition.steps) ? definition.steps.filter(s => s && typeof s === "object" && !Array.isArray(s)) as Step[] : []
  const inputs = Array.isArray(definition.inputs) ? definition.inputs.filter(i => i && typeof i === "object" && !Array.isArray(i)) as Step[] : []
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [view, setView] = useState("steps")
  const reduced = useReducedMotion()
  const selected = steps.find(s => s.id === selectedId)
  const update = (patch: Step) => onChange({ ...definition, steps: (definition.steps as unknown[]).map(s => s === selected ? { ...selected, ...patch } : s) })
  const field = (label: string, key: string, nested?: string, multiline = false) => {
    const value = nested ? object(selected?.[nested])[key] : selected?.[key]
    const change = (text: string) => update(nested ? { [nested]: { ...object(selected?.[nested]), [key]: text } } : { [key]: text })
    return <label className="block space-y-2 text-xs font-medium">{label}{multiline ? <textarea value={String(value ?? "")} onChange={e => change(e.target.value)} className="min-h-36 w-full resize-y rounded-xl border border-border/60 bg-background p-3 text-sm font-normal leading-relaxed" /> : <Input value={String(value ?? "")} onChange={e => change(e.target.value)} className="font-normal" />}</label>
  }
  return <section className="space-y-4" aria-label="Recipe workflow">
    <div className="flex flex-wrap items-center justify-between gap-3"><span className="inline-flex items-center gap-2 text-xs text-muted-foreground"><Play className="h-3.5 w-3.5" />Recipe · {steps.length} steps</span><div className="flex rounded-full bg-muted/60 p-1">{[{ id: "steps", label: "List", icon: ListOrdered }, { id: "graph", label: "Graph", icon: Network }].map(v => <button key={v.id} type="button" aria-pressed={view === v.id} onClick={() => { setView(v.id); setSelectedId(null) }} className={cn("flex items-center gap-2 rounded-full px-3 py-1.5 text-xs", view === v.id ? "bg-background text-foreground shadow-sm" : "text-muted-foreground")}><v.icon className="h-3.5 w-3.5" />{v.label}</button>)}</div></div>
    <div className={cn("grid items-start gap-4", selected && "lg:grid-cols-[minmax(240px,1fr)_minmax(280px,1fr)]")}>
      {view === "graph" ? <div className={cn("relative h-[480px] overflow-hidden rounded-2xl border border-hairline", selected && "hidden lg:block")}><RoutineDefinitionCanvas definition={definition} slug={slug} name={name} selectedStepId={selectedId} onStepSelect={setSelectedId} /></div> : <div className={cn("overflow-hidden rounded-2xl border border-hairline bg-muted/10", selected && "hidden lg:block")}>{steps.map((step, i) => {
        const kind = kinds[step.type as keyof typeof kinds] ?? { label: words(step.type), icon: Play, tone: "text-muted-foreground bg-muted" }
        const condition = recipeCondition(step, inputs)
        const active = selectedId === step.id
        return <button type="button" key={String(step.id)} aria-expanded={active} onClick={() => setSelectedId(active ? null : String(step.id))} className={cn("group flex w-full items-start gap-3 border-b border-hairline px-4 py-3 text-left transition-colors last:border-b-0 hover:bg-muted/40", active && "bg-primary/5 ring-1 ring-inset ring-primary/30")}>
          <span className={cn("relative mt-0.5 flex h-9 w-9 shrink-0 items-center justify-center rounded-xl", kind.tone)}><kind.icon className="h-4 w-4" /><span className="absolute -bottom-1 -right-1 flex h-4 w-4 items-center justify-center rounded-full border border-border bg-card text-[9px] text-muted-foreground">{i + 1}</span></span>
          <span className="min-w-0 flex-1"><span className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1"><span className="text-sm font-medium">{words(step.name || step.id) || "Unnamed step"}</span><span className="text-[11px] text-muted-foreground">{kind.label}{step.agent_slug ? ` · ${step.agent_slug}` : ""}</span></span><span className="mt-2 line-clamp-2 break-words text-xs leading-relaxed text-foreground/75">{recipeStepSummary(step, inputs)}</span>{condition && <span className="mt-2 inline-flex max-w-full items-center gap-1 rounded-md bg-warn/10 px-2 py-1 text-[11px] text-warn"><GitBranch className="h-3 w-3 shrink-0" /><span className="truncate">{condition}</span></span>}</span><ChevronRight className={cn("mt-2 h-4 w-4 shrink-0 text-muted-foreground transition-transform", active && "rotate-90 text-primary")} />
        </button>
      })}{!steps.length && <div className="p-6 text-sm text-muted-foreground">No steps yet. Add your recipe in Code.</div>}</div>}
      <AnimatePresence initial={false}>{selected && <motion.aside key={String(selected.id)} initial={reduced ? false : { opacity: 0, y: 8 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0 }} transition={{ duration: reduced ? 0 : 0.16 }} className="min-w-0 space-y-5 rounded-2xl border border-hairline bg-card p-5" aria-label="Selected step">
        <div className="flex items-start justify-between gap-2"><div><h3 className="text-sm font-medium">{words(selected.name || selected.id)}</h3><p className="mt-1 text-xs text-muted-foreground">Edit this step</p></div><button type="button" onClick={() => setSelectedId(null)} className="text-xs text-primary">Back to steps</button></div>
        {selected.if ? <div className="rounded-xl bg-warn/10 p-3 text-xs"><p className="font-medium">Runs only when</p><p className="mt-1 break-words">{recipeCondition(selected, inputs)}</p><details className="mt-2 text-[11px] text-muted-foreground"><summary className="cursor-pointer">Expression</summary><code className="mt-1 block break-all">{String(selected.if)}</code></details></div> : null}
        {selected.type === "agent_run" && field("Instructions", "prompt", undefined, true)}
        {selected.type === "wait" && object(selected.wait).kind === "approval" && <>{field("Approval title", "approval_title", "wait")}{field("What should the reviewer decide?", "approval_prompt", "wait", true)}</>}
        {selected.type === "wait" && object(selected.wait).kind === "event" && field("Event to wait for", "event_type", "wait")}
        {selected.type === "transform" && <>{field("Data to prepare", "input", "transform", true)}{field("Transformation", "expression", "transform")}</>}
        {selected.type === "code" && <><span className="text-xs text-muted-foreground">Runtime · {String(object(selected.code).runtime || "unspecified")}</span>{field("Code", "code", "code", true)}</>}
        {selected.type === "script" && field("Script path", "path", "script")}
        {selected.type === "http" && <>{field("Method", "method", "http")}{field("URL", "url", "http")}</>}
        {selected.timeout_seconds ? <p className="flex items-center gap-2 text-xs text-muted-foreground"><Clock className="h-3.5 w-3.5" />Configured timeout · {String(selected.timeout_seconds)} seconds</p> : null}
        {Array.isArray(selected.needs) && selected.needs.length > 0 && <p className="text-xs text-muted-foreground">After: {selected.needs.map(words).join(", ")}</p>}
        {selected.outcomes ? <p className="flex items-center gap-2 text-xs"><CheckCircle2 className="h-3.5 w-3.5 text-success" />Result checks configured</p> : null}
        <button type="button" onClick={onOpenCode} className="text-xs text-primary">Advanced configuration in Code →</button>
      </motion.aside>}</AnimatePresence>
    </div>
  </section>
}
