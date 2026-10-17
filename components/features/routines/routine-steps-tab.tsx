"use client"

// The routine page's Steps (Harbor 2 RT-STEPS): the steps in order with what
// each does (and Test step for the ones that can be tried on sample data), a
// read-only map, and in Full detail the stored definition. "Edit steps" turns
// the list into the editor the New routine wizard uses — add, remove, reorder
// and edit agent tasks, questions, notifications, waits, issue actions, web
// calls and sub-routines — plus the agent prompts and decision forms of steps
// written in code. Everything saves as a draft through the page's save bar;
// nothing goes live until it is published.

import * as React from "react"
import { ArrowDown, ArrowUp, Eye, Pencil } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { DetailCard } from "@/components/ui/detail"
import { CreateSurfaceField } from "@/components/layout/create-surface"
import { Segmented } from "@/components/ui/segmented"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { useWorkspaceAgentDirectory } from "@/hooks/use-workspace-agent-directory"
import type { Pipeline } from "@/hooks/use-pipelines"
import { isRecord, asString } from "@/lib/routine-step-describe"
import { foreachBody, routineHooks, stepDisplayName, type Step } from "@/lib/routine-steps-layout"
import { routineFileStatus, type RoutineFile } from "@/lib/routine-files"
import { approvalSteps } from "@/lib/routine-approval-steps"
import { routineInputSpecs } from "@/lib/routine-inputs"
import { definitionWithSteps, editorSteps, moveStep, stepsReferencing, type DetailDepth } from "@/lib/routine-page"
import type { WizardAgent, WizardState, WizardStep } from "@/lib/routine-wizard"
import { StepsPane } from "./routine-wizard-panes"
import { RoutineStepSpine, StepFileChip } from "./routine-step-spine"
import { RoutineDefinitionCanvas } from "./routine-definition-canvas"
import { RoutineDecisionFormBuilder } from "./routine-decision-form-builder"
import { RoutinePublishedStepTest } from "./routine-published-step-test"
import type { RoutineEditor } from "./use-routine-editor"
import type { RoutineDetail } from "./routines-detail-panel"

type Mode = "list" | "map" | "code"

/** Every agent_run step, including those inside a foreach, with its path. */
export function agentSteps(definition: Record<string, unknown>): { step: Step; path: (number | string)[] }[] {
  const out: { step: Step; path: (number | string)[] }[] = []
  const steps = Array.isArray(definition.steps) ? definition.steps.filter(isRecord) : []
  steps.forEach((s, i) => {
    if (s.type === "agent_run") out.push({ step: s, path: ["steps", i] })
    foreachBody(s).forEach((child, j) => {
      if (child.type === "agent_run") out.push({ step: child, path: ["steps", i, "foreach", "steps", j] })
    })
  })
  return out
}

function setAt(root: Record<string, unknown>, path: (number | string)[], key: string, value: unknown) {
  let node: unknown = root
  for (const segment of path) node = (node as Record<string, unknown>)[segment]
  ;(node as Record<string, unknown>)[key] = value
}

export function RoutineStepsTab({
  routine,
  workspaceId,
  editor,
  canEdit,
  depth,
  files,
  routines,
  onTest,
}: {
  routine: RoutineDetail
  workspaceId: string
  editor: RoutineEditor
  canEdit: boolean
  depth: DetailDepth
  files: RoutineFile[]
  routines: Pipeline[]
  /** Test the whole routine without side effects. */
  onTest?: () => void
}) {
  const [mode, setMode] = React.useState<Mode>("list")
  const [editing, setEditing] = React.useState(false)
  const [testedStep, setTestedStep] = React.useState<string | null>(null)
  const effectiveMode: Mode = mode === "code" && depth !== "full" ? "list" : mode
  const steps = React.useMemo(() => {
    const raw = (routine.definition as { steps?: unknown })?.steps
    return Array.isArray(raw) ? raw.filter(isRecord) : []
  }, [routine.definition])
  const mapHeight = Math.min(560, Math.max(300, steps.length * 78))

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <Segmented<Mode>
          ariaLabel="Show steps as"
          value={effectiveMode}
          onChange={setMode}
          options={[
            { value: "list", label: "Steps" },
            { value: "map", label: "Map" },
            ...(depth === "full" ? [{ value: "code" as const, label: "Definition" }] : []),
          ]}
        />
        <span className="flex-1" />
        <span className="hidden text-xs text-muted-foreground md:inline">Edits are saved as changes; nothing goes live until you publish</span>
        {canEdit && effectiveMode === "list" && (
          <Button type="button" variant={editing ? "default" : "outline"} size="sm" className="gap-1.5 coarse:h-[44px]" aria-pressed={editing} onClick={() => setEditing((v) => !v)}>
            <Pencil className="h-3.5 w-3.5" aria-hidden />
            {editing ? "Done editing" : "Edit steps"}
          </Button>
        )}
        {onTest && (
          <Button type="button" variant="outline" size="sm" className="gap-1.5 coarse:h-[44px]" onClick={onTest}>
            <Eye className="h-3.5 w-3.5" aria-hidden />
            Test the whole routine
          </Button>
        )}
      </div>

      {effectiveMode === "list" && editing && canEdit && (
        <StepsEditor routine={routine} workspaceId={workspaceId} editor={editor} files={files} routines={routines} />
      )}
      {effectiveMode === "list" && !(editing && canEdit) && (
        <RoutineStepSpine
          workspaceId={workspaceId}
          definition={routine.definition}
          behavior={routine.behavior}
          slug={routine.slug}
          renderStepTools={
            canEdit
              ? (step) => {
                  // The fixture endpoint addresses top-level steps by ID. Do not
                  // accidentally test a same-named nested step against that ID.
                  if (!steps.includes(step) || !["transform", "agent_run", "http", "script"].includes(String(step.type))) return null
                  const id = String(step.id)
                  return (
                    <div className="min-w-0 space-y-3">
                      <Button type="button" variant="outline" size="sm" aria-expanded={testedStep === id} onClick={() => setTestedStep(testedStep === id ? null : id)}>
                        {testedStep === id ? "Close step test" : "Test step"}
                      </Button>
                      {testedStep === id && (
                        <RoutinePublishedStepTest
                          key={`${workspaceId}:${routine.slug}:${routine.definition_hash}:${id}`}
                          workspaceId={workspaceId}
                          definition={routine.definition}
                          stepId={id}
                          authorCrewId={routine.author_crew_id}
                        />
                      )}
                    </div>
                  )
                }
              : undefined
          }
        />
      )}
      {effectiveMode === "map" && (
        <div className="flex flex-col gap-2">
          <div className="relative overflow-hidden rounded-card border border-hairline" style={{ height: mapHeight }}>
            <RoutineDefinitionCanvas definition={routine.definition} slug={routine.slug} name={routine.name} />
          </div>
          <p className="text-xs text-muted-foreground">A read-only map of the same steps. Edit them in the list.</p>
        </div>
      )}
      {effectiveMode === "code" && (
        <div className="flex flex-col gap-2">
          <pre className="max-h-[60dvh] overflow-auto rounded-card border border-hairline bg-surface-subtle p-3 font-mono text-xs" data-testid="routine-definition-json">
            {JSON.stringify(routine.definition, null, 2)}
          </pre>
          <p className="text-xs text-muted-foreground">
            The definition is stored as JSON. The CLI edits the same file: <span className="font-mono">crewship routine draft get {routine.slug}</span>.
          </p>
        </div>
      )}
    </div>
  )
}

function StepsEditor({
  routine,
  workspaceId,
  editor,
  files,
  routines,
}: {
  routine: RoutineDetail
  workspaceId: string
  editor: RoutineEditor
  files: RoutineFile[]
  routines: Pipeline[]
}) {
  const { agents } = useWorkspaceAgentDirectory(workspaceId)
  const doc = editor.doc
  // The wizard's step model, kept locally so typing is not re-derived from the
  // definition on every keystroke; pushed to the shared document on change and
  // re-read when the document changes underneath (a reload after a save).
  const emitted = React.useRef<unknown>(null)
  const [wsteps, setWsteps] = React.useState<WizardStep[]>(() => editorSteps(doc))
  React.useEffect(() => {
    if (doc.steps !== emitted.current) setWsteps(editorSteps(doc))
  }, [doc])
  const [open, setOpen] = React.useState<string | null>(null)
  const [warning, setWarning] = React.useState<string | null>(null)

  const apply = (next: WizardStep[]) => {
    const removed = wsteps.filter((s) => !next.some((n) => n.id === s.id))
    for (const r of removed) {
      const readers = stepsReferencing(next, r.id)
      if (readers.length) {
        setWarning(`“${r.name || r.id}” cannot be removed: ${readers.map((s) => `“${s.name || s.id}”`).join(", ")} ${readers.length === 1 ? "reads" : "read"} its result.`)
        return
      }
    }
    setWarning(null)
    setWsteps(next)
    const nextDoc = definitionWithSteps(doc, next)
    emitted.current = nextDoc.steps
    editor.setDoc(nextDoc)
  }

  const state: WizardState = {
    name: editor.name,
    slug: routine.slug,
    description: editor.description,
    crewId: routine.author_crew_id ?? null,
    trigger: { kind: "manual", preset: "week", minute: 0, time: "08:00", weekday: 1, cron: "", timezone: "UTC", catchup: "once", maxFailures: 5, fireAt: "" },
    slash: false,
    inputs: routineInputSpecs(doc),
    steps: wsteps,
    maxCost: "",
    concurrency: "queue",
    sideBySide: 2,
    extra: {},
  }
  const prompts = agentSteps(doc).filter(({ path }) => path.length > 2 || wsteps[Number(path[1])]?.kind === "other")
  const decisions = approvalSteps(doc)
  const hooks = routineHooks(doc)
  const raw = Array.isArray(doc.steps) ? doc.steps.filter(isRecord) : []
  const nameOf = (id: string) => {
    const all = [...raw, ...raw.flatMap(foreachBody), ...hooks.map((h) => h.step)]
    const s = all.find((x) => String(x.id) === id)
    return s ? stepDisplayName(s, 0) : id
  }

  return (
    <div className="flex flex-col gap-4" data-testid="routine-steps-editor">
      {editor.loading && <p role="status" className="text-xs text-muted-foreground">Loading the current draft…</p>}
      {warning && (
        <p role="alert" className="rounded-lg border border-warn/30 bg-warn/10 px-3 py-2 text-xs text-warn">
          {warning}
        </p>
      )}
      <StepsPane
        state={state}
        set={(patch) => patch.steps && apply(patch.steps)}
        agents={(agents ?? []) as unknown as WizardAgent[]}
        routines={routines}
        openStep={open}
        onOpenStep={setOpen}
        whenLabel="Starts as its triggers say"
        fromNote={null}
        onDismissFrom={() => {}}
      />
      {wsteps.length > 1 && (
        <DetailCard title="Order" subtitle="top to bottom">
          <ol className="flex flex-col gap-1">
            {wsteps.map((s, i) => (
              <li key={s.id} className="flex items-center gap-2 text-xs">
                <span className="w-5 text-right font-mono text-muted-foreground">{i + 1}</span>
                <span className="min-w-0 flex-1 truncate">{s.name || s.id}</span>
                <Button type="button" variant="ghost" size="icon-sm" aria-label={`Move ${s.name || s.id} up`} disabled={i === 0} onClick={() => apply(moveStep(wsteps, i, i - 1))}>
                  <ArrowUp className="h-3.5 w-3.5" />
                </Button>
                <Button type="button" variant="ghost" size="icon-sm" aria-label={`Move ${s.name || s.id} down`} disabled={i === wsteps.length - 1} onClick={() => apply(moveStep(wsteps, i, i + 1))}>
                  <ArrowDown className="h-3.5 w-3.5" />
                </Button>
              </li>
            ))}
          </ol>
          <p className="mt-2 text-xs text-muted-foreground-soft">A step can only read the results of steps above it.</p>
        </DetailCard>
      )}

      {prompts.length > 0 && (
        <DetailCard title="Agent prompts" subtitle="steps written in code">
          <div className="flex flex-col gap-3">
            <p className="text-xs text-muted-foreground">
              What each agent is asked, word for word. Placeholders like <code className="font-mono">{"{{ inputs.x }}"}</code> are filled in at run time. Tools, checks and the agent itself are set in the CLI.
            </p>
            {prompts.map(({ step, path }) => {
              const agent = agents?.find((a) => a.slug === step.agent_slug)
              const outcomes = isRecord(step.outcomes) ? step.outcomes : null
              const tools = Array.isArray(step.tools) ? step.tools.map(String) : []
              const id = `routine-edit-prompt-${String(step.id)}`
              return (
                <div key={path.join(".")} className="rounded-lg border border-hairline p-3">
                  <div className="mb-2 flex items-center gap-2">
                    {agent ? (
                      <AgentAvatar seed={agent.avatar_seed || agent.name} style={agent.avatar_style || agent.crew?.avatar_style || undefined} avatarUrl={agent.avatar_url} className="h-7 w-7" alt="" />
                    ) : (
                      <span className="flex h-7 w-7 items-center justify-center rounded-lg bg-purple/10 text-purple">
                        <Pencil className="h-3.5 w-3.5" />
                      </span>
                    )}
                    <div className="min-w-0">
                      <label htmlFor={id} className="block text-sm font-medium">{stepDisplayName(step, 0)}</label>
                      <p className="text-xs text-muted-foreground">
                        {agent?.name || asString(step.agent_slug) || "Agent chosen at run time"}
                        {tools.length ? ` · tools: ${tools.join(", ")}` : ""}
                        {outcomes?.grader_agent_slug ? ` · checked by ${asString(outcomes.grader_agent_slug)}` : ""}
                      </p>
                    </div>
                  </div>
                  <Textarea id={id} rows={4} value={asString(step.prompt)} onChange={(e) => editor.update((next) => setAt(next, path, "prompt", e.target.value))} />
                </div>
              )
            })}
          </div>
        </DetailCard>
      )}

      {decisions.length > 0 && (
        <DetailCard title="Decisions" subtitle={String(decisions.length)}>
          <div className="flex flex-col gap-4">
            <p className="text-xs text-muted-foreground">The question and the buttons a person sees at each approval step.</p>
            {decisions.map(({ step, path }) => {
              const wait = isRecord(step.wait) ? step.wait : {}
              const patchWait = (patch: Record<string, unknown>) => editor.update((next) => setAt(next, path, "wait", { ...wait, ...patch }))
              const id = `routine-decision-${path.join("-")}`
              return (
                <section key={path.join(".")} className="flex flex-col gap-3 rounded-lg border border-hairline p-3" aria-label={`Decision: ${stepDisplayName(step, 0)}`}>
                  <h3 className="text-sm font-medium">{stepDisplayName(step, 0)}</h3>
                  <CreateSurfaceField label="Decision title" htmlFor={`${id}-title`}>
                    <Input id={`${id}-title`} value={asString(wait.approval_title)} onChange={(e) => patchWait({ approval_title: e.target.value })} />
                  </CreateSurfaceField>
                  <CreateSurfaceField label="What should the reviewer decide?" htmlFor={`${id}-prompt`}>
                    <Textarea id={`${id}-prompt`} value={asString(wait.approval_prompt)} onChange={(e) => patchWait({ approval_prompt: e.target.value })} />
                  </CreateSurfaceField>
                  <RoutineDecisionFormBuilder
                    value={wait.decision_form}
                    onChange={(decision_form) => patchWait({ decision_form })}
                    onOpenCode={() => document.getElementById("routine-steps-cli")?.scrollIntoView?.({ block: "center" })}
                    codeActionLabel="View editing instructions"
                  />
                </section>
              )
            })}
          </div>
        </DetailCard>
      )}

      <DetailCard title="Code, scripts and files">
        <div id="routine-steps-cli" className="flex flex-col gap-2 text-xs">
          <p className="text-muted-foreground">
            Code, transform, query and loop steps, checks, hooks and scripts are read-only here. {raw.length} {raw.length === 1 ? "step" : "steps"}
            {hooks.length ? `, ${hooks.length} ${hooks.length === 1 ? "hook" : "hooks"}` : ""}, {files.length} {files.length === 1 ? "file" : "files"}. Change them with the CLI or by asking the lead agent; the result arrives here as a draft to publish.
          </p>
          <pre className="overflow-x-auto rounded-md border border-hairline bg-background p-2 font-mono text-xs">
{`crewship routine draft get ${routine.slug} -f json > draft.json
# edit draft.json.document; scripts go to the crew share:
crewship crew files save <crew> shared/scripts/x.py --file ./x.py
crewship routine draft save draft.json -f json > saved.json`}
          </pre>
          {files.length > 0 && (
            <ul className="divide-y divide-hairline rounded-lg border border-hairline">
              {files.map((f) => (
                <li key={f.path} className="flex flex-wrap items-center gap-2 px-3 py-2">
                  <StepFileChip path={f.path} />
                  {f.description && <span className="min-w-0 flex-1 truncate text-muted-foreground">{f.description}</span>}
                  <span className="ml-auto text-muted-foreground">used by {f.step_ids.map(nameOf).join(", ") || "—"}</span>
                  {routineFileStatus(f) === "missing" && <span className="text-destructive">missing on the share</span>}
                  {routineFileStatus(f) === "unverified" && <span className="text-muted-foreground">not verified</span>}
                </li>
              ))}
            </ul>
          )}
        </div>
      </DetailCard>
    </div>
  )
}
