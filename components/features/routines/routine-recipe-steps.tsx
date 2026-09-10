"use client"

import { describeStep } from "@/lib/routine-step-describe"
import { useState } from "react"
import { AnimatePresence, motion, useReducedMotion } from "motion/react"
import {
  Bot,
  Braces,
  CheckCircle2,
  ChevronRight,
  Clock,
  GitBranch,
  Globe,
  ListOrdered,
  Network,
  Play,
  Repeat2,
  SlidersHorizontal,
  Terminal,
} from "lucide-react"
import { routineDataSources, routineSourcePatch } from "@/lib/routine-data-sources"
import { cn } from "@/lib/utils"
import { Input } from "@/components/ui/input"
import { RoutineTestWorkspace, type RoutineTestWorkspaceProps } from "./routine-test-workspace"
import { RoutineDecisionFormBuilder } from "./routine-decision-form-builder"
import { RoutineDefinitionCanvas } from "./routine-definition-canvas"

type Step = Record<string, unknown>
const object = (value: unknown): Step =>
  value && typeof value === "object" && !Array.isArray(value) ? (value as Step) : {}
const words = (value: unknown) =>
  String(value ?? "")
    .replaceAll("_", " ")
    .replaceAll("-", " ")
    .replace(/^./, (c) => c.toUpperCase())
const kinds = {
  agent_run: { label: "Agent task", icon: Bot, tone: "text-purple bg-purple/10" },
  transform: {
    label: "Prepare data",
    icon: SlidersHorizontal,
    tone: "text-success bg-success/10",
  },
  code: { label: "Run code", icon: Braces, tone: "text-info bg-info/10" },
  script: { label: "Run a script", icon: Terminal, tone: "text-info bg-info/10" },
  http: { label: "Call a service", icon: Globe, tone: "text-info bg-info/10" },
  wait: { label: "Wait", icon: Clock, tone: "text-warn bg-warn/10" },
  call_pipeline: { label: "Run another routine", icon: Play, tone: "text-info bg-info/10" },
  foreach: { label: "For each item", icon: Repeat2, tone: "text-purple bg-purple/10" },
} as const
export function recipeStepSummary(step: Step, inputs: Step[] = []) {
  const config = object(step[String(step.type)])
  if (step.type === "wait")
    return config.kind === "approval"
      ? String(config.approval_title || "Ask for approval")
      : config.kind === "event"
        ? `Wait for ${config.event_type || "an event"}`
        : "Wait until a date"
  if (step.type === "agent_run")
    return String(step.prompt || "Choose an agent and give it instructions")
  if (step.type === "script") return String(config.path || "Configure a script")
  if (step.type === "http")
    return `${config.method || "GET"} ${config.url || "Choose a service"}`
  if (step.type === "transform") {
    const source = String(config.input || "")
    const answers = [...new Set([...source.matchAll(/\{\{\s*inputs\.(\w+)/g)].map((m) => m[1]))]
    const prior = [
      ...new Set([...source.matchAll(/\{\{\s*steps\.(\w+)\.output/g)].map((m) => words(m[1]))),
    ]
    if (answers.length > 2) return `Prepare data from ${answers.length} inputs`
    if (answers.length)
      return `Uses ${answers.map((key) => inputs.find((i) => i.name === key)?.label || words(key)).join(" and ")}`
    if (prior.length) return `Uses the result of ${prior.join(", ")}`
    return String(config.input || config.expression || "Prepare the next step’s input").slice(
      0,
      160,
    )
  }
  if (step.type === "code")
    return `${config.runtime || "Code"} · ${String(config.code || "").split("\n")[0]}`
  if (step.type === "call_pipeline") return `Run ${step.pipeline_slug || "another routine"}`
  return String(step.description || step.action || words(step.type))
}
export function recipeCondition(step: Step, inputs: Step[]) {
  if (!step.if) return null
  const match = /^inputs\.([\w]+)\s*==\s*(['"])(.*?)\2$/.exec(String(step.if))
  return match
    ? `${inputs.find((i) => i.name === match[1])?.label || words(match[1])}: ${match[3]}`
    : "Conditional step"
}
export function RoutineRecipeSteps({
  definition,
  slug,
  name,
  onChange,
  onOpenCode,
  testPanel,
}: {
  testPanel?: Omit<RoutineTestWorkspaceProps, "stepId">
  definition: Step
  slug: string
  name: string
  onChange: (definition: Step) => void
  onOpenCode: () => void
}) {
  const steps = Array.isArray(definition.steps)
    ? (definition.steps.filter(
        (s) => s && typeof s === "object" && !Array.isArray(s),
      ) as Step[])
    : []
  const inputs = Array.isArray(definition.inputs)
    ? (definition.inputs.filter(
        (i) => i && typeof i === "object" && !Array.isArray(i),
      ) as Step[])
    : []
  const [selectedId, setSelectedId] = useState<string | null>(() =>
    steps[0] ? String(steps[0].id) : null,
  )
  const [panelMode, setPanelMode] = useState("edit")
  const [view, setView] = useState("steps")
  const reduced = useReducedMotion()
  const selected = steps.find((s) => s.id === selectedId)
  const dataSources = selected ? routineDataSources(definition, String(selected.id)) : []
  const update = (patch: Step) =>
    onChange({
      ...definition,
      steps: (definition.steps as unknown[]).map((s) =>
        s === selected ? { ...selected, ...patch } : s,
      ),
    })
  const field = (label: string, key: string, nested?: string, multiline = false) => {
    const value = nested ? object(selected?.[nested])[key] : selected?.[key]
    const change = (text: string) =>
      update(
        nested ? { [nested]: { ...object(selected?.[nested]), [key]: text } } : { [key]: text },
      )
    return (
      <label className="block space-y-2 text-xs font-medium">
        {label}
        {multiline ? (
          <textarea
            value={String(value ?? "")}
            onChange={(e) => change(e.target.value)}
            className="min-h-36 w-full resize-y rounded-xl border border-border/60 bg-background p-3 text-sm font-normal leading-relaxed"
          />
        ) : (
          <Input
            value={String(value ?? "")}
            onChange={(e) => change(e.target.value)}
            className="font-normal"
          />
        )}
      </label>
    )
  }
  const sourcePicker = (
    label: string,
    key: string,
    nested?: string,
    append = false,
    acceptedTypes?: string[],
  ) => {
    if (!selected) return null
    const choices = acceptedTypes
      ? routineDataSources(definition, String(selected.id), acceptedTypes)
      : dataSources
    return (
      <div className="space-y-1">
        <label className="block space-y-2 text-xs font-medium">
          {label}
          <select
            aria-label={label}
            className="h-10 w-full rounded-md border bg-background px-3 text-sm"
            value=""
            onChange={(event) => {
              const source = choices.find((s) => s.value === event.target.value)
              if (source)
                update(routineSourcePatch(definition, selected, source, key, nested, append))
            }}
          >
            <option value="">Choose inputs or a step result…</option>
            {choices.map((source) => (
              <option key={source.value} value={source.value}>
                {source.label}
              </option>
            ))}
          </select>
        </label>
        <p className="text-xs text-muted-foreground">
          {append
            ? "Inserts a reference after the existing text."
            : "Uses the selected value for this field."}{" "}
          {acceptedTypes
            ? `Only declared ${acceptedTypes.join(" / ")} sources are shown; other expressions remain in Code.`
            : selected.type === "call_pipeline" && nested === "inputs"
              ? "The called routine’s input schema determines the value type; incompatible values stop before its work starts."
              : "Values render as text; object and list values render as JSON."}
        </p>
      </div>
    )
  }
  return (
    <section className="space-y-4" aria-label="Recipe workflow">
      {(definition.parallelism === "auto" ||
        steps.some((step) => Array.isArray(step.needs) && step.needs.length > 0)) && (
        <p className="text-xs text-muted-foreground">
          Steps are listed in recipe order, not execution order. Independent steps may run in
          parallel unless parallelism is turned off.
        </p>
      )}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <span className="inline-flex items-center gap-2 text-xs text-muted-foreground">
          <Play className="h-3.5 w-3.5" />
          Recipe · {steps.length} steps
        </span>
        <div className="flex rounded-full bg-muted/60 p-1">
          {[
            { id: "steps", label: "List", icon: ListOrdered },
            { id: "graph", label: "Map", icon: Network },
          ].map((v) => (
            <button
              key={v.id}
              type="button"
              aria-pressed={view === v.id}
              onClick={() => {
                setView(v.id)
                setSelectedId(null)
              }}
              className={cn(
                "flex items-center gap-2 rounded-full px-3 py-1.5 text-xs",
                view === v.id
                  ? "bg-background text-foreground shadow-sm"
                  : "text-muted-foreground",
              )}
            >
              <v.icon className="h-3.5 w-3.5" />
              {v.label}
            </button>
          ))}
        </div>
      </div>
      <div
        className={cn(
          "grid items-start gap-4",
          selected && "lg:grid-cols-[minmax(240px,1fr)_minmax(280px,1fr)]",
        )}
      >
        {view === "graph" ? (
          <div
            className={cn(
              "relative h-[480px] overflow-hidden rounded-2xl border border-hairline",
              selected && "hidden lg:block",
            )}
          >
            <RoutineDefinitionCanvas
              definition={definition}
              slug={slug}
              name={name}
              selectedStepId={selectedId}
              onStepSelect={setSelectedId}
            />
          </div>
        ) : (
          <div
            className={cn(
              "overflow-hidden rounded-2xl border border-hairline bg-muted/10",
              selected && "hidden lg:block",
            )}
          >
            {steps.map((step, index) => {
              const kind = kinds[step.type as keyof typeof kinds] ?? {
                label: words(step.type),
                icon: Play,
                tone: "text-muted-foreground bg-muted",
              }
              const condition = recipeCondition(step, inputs)
              const active = selectedId === step.id
              return (
                <button
                  type="button"
                  key={String(step.id)}
                  aria-expanded={active}
                  onClick={() => setSelectedId(String(step.id))}
                  className={cn(
                    "group flex w-full items-start gap-3 border-b border-hairline px-4 py-3 text-left transition-colors last:border-b-0 hover:bg-muted/40",
                    active && "bg-primary/5 ring-1 ring-inset ring-primary/30",
                  )}
                >
                  <span
                    className={cn(
                      "relative mt-0.5 flex h-9 w-9 shrink-0 items-center justify-center rounded-xl",
                      kind.tone,
                    )}
                  >
                    <kind.icon className="h-4 w-4" />
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
                      <span className="text-sm font-medium break-all">
                        {describeStep(step, index + 1).title}
                      </span>
                      <span className="text-[11px] text-muted-foreground">
                        {kind.label}
                        {step.agent_slug ? ` · ${step.agent_slug}` : ""}
                      </span>
                    </span>
                    <span className="mt-2 line-clamp-2 break-words text-xs leading-relaxed text-foreground/75">
                      {recipeStepSummary(step, inputs)}
                    </span>
                    {Array.isArray(step.needs) && step.needs.length > 0 && (
                      <span className="mt-1 block break-all text-xs text-muted-foreground">
                        Depends on:{" "}
                        {step.needs
                          .filter((id): id is string => typeof id === "string")
                          .join(", ")}
                      </span>
                    )}
                    {condition && (
                      <span className="mt-2 inline-flex max-w-full items-center gap-1 rounded-md bg-warn/10 px-2 py-1 text-[11px] text-warn">
                        <GitBranch className="h-3 w-3 shrink-0" />
                        <span className="truncate">{condition}</span>
                      </span>
                    )}
                  </span>
                  <ChevronRight
                    className={cn(
                      "mt-2 h-4 w-4 shrink-0 text-muted-foreground transition-transform",
                      active && "rotate-90 text-primary",
                    )}
                  />
                </button>
              )
            })}
            {!steps.length && (
              <div className="p-6 text-sm text-muted-foreground">
                No steps yet. Add your recipe in Code.
              </div>
            )}
          </div>
        )}
        <AnimatePresence initial={false}>
          {selected && (
            <motion.aside
              key={String(selected.id)}
              initial={reduced ? false : { opacity: 0, y: 8 }}
              animate={{ opacity: 1, y: 0 }}
              exit={{ opacity: 0 }}
              transition={{ duration: reduced ? 0 : 0.16 }}
              className="min-w-0 space-y-5 rounded-2xl border border-hairline bg-card p-5"
              aria-label="Selected step"
            >
              <div className="flex items-start justify-between gap-2">
                <div>
                  <h3 className="text-sm font-medium">{describeStep(selected, 1).title}</h3>
                  <p className="mt-1 text-xs text-muted-foreground">Edit this step</p>
                </div>
                <button
                  type="button"
                  onClick={() => setSelectedId(null)}
                  className="text-xs text-primary"
                >
                  Back to steps
                </button>
              </div>
              {testPanel && (
                <nav
                  aria-label="Selected step tools"
                  className="flex gap-3 border-b border-border pb-2"
                >
                  {["edit", "test"].map((mode) => (
                    <button
                      type="button"
                      key={mode}
                      aria-pressed={panelMode === mode}
                      onClick={() => setPanelMode(mode)}
                      className={cn(
                        "text-xs",
                        panelMode === mode
                          ? "font-medium text-primary"
                          : "text-muted-foreground",
                      )}
                    >
                      {mode === "edit" ? "Edit" : "Test"}
                    </button>
                  ))}
                </nav>
              )}
              <div hidden={panelMode !== "edit"} className="space-y-4">
                <label className="block space-y-2 text-xs font-medium">
                  Step name
                  <Input
                    value={typeof selected.name === "string" ? selected.name : ""}
                    aria-label="Step name"
                    placeholder={describeStep({ ...selected, name: undefined }, 1).title}
                    onChange={(event) => update({ name: event.target.value })}
                  />
                </label>
                {selected.if ? (
                  <div className="rounded-xl bg-warn/10 p-3 text-xs">
                    <p className="font-medium">Runs only when</p>
                    <p className="mt-1 break-words">{recipeCondition(selected, inputs)}</p>
                    <details className="mt-2 text-[11px] text-muted-foreground">
                      <summary className="cursor-pointer">Expression</summary>
                      <code className="mt-1 block break-all">{String(selected.if)}</code>
                    </details>
                  </div>
                ) : null}
                {selected.type === "agent_run" && (
                  <>
                    {field("Instructions", "prompt", undefined, true)}
                    {sourcePicker("Insert data into instructions", "prompt", undefined, true)}
                  </>
                )}
                {selected.type === "wait" && object(selected.wait).kind === "approval" && (
                  <>
                    {field("Approval title", "approval_title", "wait")}
                    {field("What should the reviewer decide?", "approval_prompt", "wait", true)}
                    {sourcePicker(
                      "Insert data into the decision request",
                      "approval_prompt",
                      "wait",
                      true,
                    )}
                    <RoutineDecisionFormBuilder
                      value={object(selected.wait).decision_form}
                      onChange={(decision_form) =>
                        update({ wait: { ...object(selected.wait), decision_form } })
                      }
                      onOpenCode={onOpenCode}
                    />
                  </>
                )}
                {selected.type === "wait" &&
                  object(selected.wait).kind === "event" &&
                  field("Event to wait for", "event_type", "wait")}
                {selected.type === "transform" && (
                  <>
                    <label className="block space-y-2 text-xs font-medium">
                      Data source
                      <select
                        aria-label="Data source"
                        className="h-11 w-full rounded-md border bg-background px-3 text-sm"
                        value={
                          dataSources.some((s) => s.value === object(selected.transform).input)
                            ? String(object(selected.transform).input)
                            : ""
                        }
                        onChange={(event) => {
                          const source = dataSources.find((s) => s.value === event.target.value)
                          if (!source) return
                          update(
                            routineSourcePatch(
                              definition,
                              selected,
                              source,
                              "input",
                              "transform",
                            ),
                          )
                        }}
                      >
                        <option value="">Custom value or expression</option>
                        {dataSources.map((source) => (
                          <option key={source.value} value={source.value}>
                            {source.label}
                          </option>
                        ))}
                      </select>
                    </label>
                    <p className="text-xs text-muted-foreground">
                      The selected value is rendered as text for this transformation. Advanced
                      projections remain editable below.
                    </p>
                    {field("Data to prepare", "input", "transform", true)}
                    {field("Transformation", "expression", "transform")}
                  </>
                )}
                {selected.type === "code" && (
                  <>
                    <span className="text-xs text-muted-foreground">
                      Runtime · {String(object(selected.code).runtime || "unspecified")}
                    </span>
                    {field("Code", "code", "code", true)}
                  </>
                )}
                {selected.type === "script" && field("Script path", "path", "script")}
                {selected.type === "http" && (
                  <>
                    {field("Method", "method", "http")}
                    {field("URL", "url", "http")}
                    {sourcePicker("URL source", "url", "http", false, ["string"])}
                    {field("Request body", "body", "http", true)}
                    {sourcePicker("Request body source", "body", "http")}
                  </>
                )}
                {selected.type === "notify" && (
                  <>
                    {field("Notification title", "title", "notify")}
                    {field("Notification body", "body", "notify", true)}
                    {sourcePicker("Insert data into notification", "body", "notify", true)}
                  </>
                )}
                {selected.type === "call_pipeline" && (
                  <>
                    <p className="text-xs text-muted-foreground">
                      Called routine · {String(selected.pipeline_slug || "Not configured")}
                    </p>
                    {Object.entries(object(selected.inputs)).map(([key, value]) => (
                      <div
                        key={key}
                        className="space-y-2 rounded-xl border border-hairline p-3"
                      >
                        <p className="text-xs font-medium">{words(key)}</p>
                        <pre className="max-h-32 overflow-auto whitespace-pre-wrap break-all text-xs text-muted-foreground">
                          {typeof value === "string" ? value : JSON.stringify(value, null, 2)}
                        </pre>
                        {sourcePicker(`Source for ${key}`, key, "inputs")}
                      </div>
                    ))}
                    <p className="text-xs text-muted-foreground">
                      Add input bindings or change the called routine in Code. Existing values
                      stay unchanged until you choose a source.
                    </p>
                  </>
                )}
                {selected.type === "foreach" && (
                  <>
                    {field("Items", "items", "foreach")}
                    {sourcePicker("List source", "items", "foreach", false, ["array"])}
                  </>
                )}
                {selected.timeout_seconds ? (
                  <p className="flex items-center gap-2 text-xs text-muted-foreground">
                    <Clock className="h-3.5 w-3.5" />
                    Configured timeout · {String(selected.timeout_seconds)} seconds
                  </p>
                ) : null}
                {Array.isArray(selected.needs) && selected.needs.length > 0 && (
                  <p className="text-xs text-muted-foreground">
                    After: {selected.needs.map(words).join(", ")}
                  </p>
                )}
                {selected.outcomes ? (
                  <p className="flex items-center gap-2 text-xs">
                    <CheckCircle2 className="h-3.5 w-3.5 text-success" />
                    Result checks configured
                  </p>
                ) : null}
                <button type="button" onClick={onOpenCode} className="text-xs text-primary">
                  Advanced configuration in Code →
                </button>
              </div>
              {testPanel && (
                <div hidden={panelMode !== "test"}>
                  <RoutineTestWorkspace {...testPanel} stepId={String(selected.id)} />
                </div>
              )}
            </motion.aside>
          )}
        </AnimatePresence>
      </div>
    </section>
  )
}
