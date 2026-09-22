"use client"

import * as React from "react"
import { Pencil, Save } from "lucide-react"
import { toast } from "sonner"

import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceChoice,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceNotice,
  CreateSurfaceRefusal,
  CREATE_SURFACE_INPUT,
} from "@/components/layout/create-surface"
import { CrewIconPopover } from "@/components/crew-icon-popover"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Switch } from "@/components/ui/switch"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { apiFetch } from "@/lib/api-fetch"
import { cn } from "@/lib/utils"
import { extractProblemDetail } from "@/lib/problem-details"
import { renamePayload } from "@/lib/routine-save-payload"
import { loadRoutineDraft, saveRoutineDraft, RoutineDraftError, type RoutineDraft } from "@/lib/routine-drafts"
import { routineInputSpecs, formatInputDefault, type RoutineInputSpec } from "@/lib/routine-inputs"
import { resolveRoutineColor, resolveRoutineIcon } from "@/lib/routine-identity"
import { isRecord, asString } from "@/lib/routine-step-describe"
import { foreachBody, routineHooks, stepDisplayName, type Step } from "@/lib/routine-steps-layout"
import { routineFilesFromDefinition, routineFileStatus, type RoutineFile } from "@/lib/routine-files"
import { useWorkspaceAgentDirectory } from "@/hooks/use-workspace-agent-directory"
import { approvalSteps } from "@/lib/routine-approval-steps"
import { useUnsavedNavigationGuard } from "@/hooks/use-unsaved-navigation-guard"
import { RoutineDecisionFormBuilder } from "./routine-decision-form-builder"
import { StepFileChip } from "./routine-step-spine"
import type { RoutineDetail } from "./routines-detail-panel"

// routine-edit-dialog — exactly what the DSL lets a person change without
// files, in seven tabs.
//
// Identity (icon, name, purpose) applies at once: it does not change how the
// routine runs. Everything else — inputs, agent prompts, limits, the slash
// command — is a definition change, so saving it creates a draft through the
// same drafts API the CLI and the lead use (POST …/pipelines/drafts with the
// modified document, CAS on the current draft revision), and the published
// version keeps running until the draft is published. Decision forms are editable for existing approval steps. Step structure, checks,
// hooks, scripts and the agents themselves stay read-only here: the web cannot carry
// the files they need.

type Tab = "identity" | "inputs" | "prompts" | "limits" | "slash" | "steps" | "decisions"
const TABS: Tab[] = ["identity", "inputs", "prompts", "decisions", "limits", "slash", "steps"]
const TIERS = ["trivial", "fast", "moderate", "smart"] as const

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  workspaceId: string
  routine: RoutineDetail
  files?: RoutineFile[]
  /** The routine changed on the server — identity saved, or a draft created. */
  onChanged: () => void
  initialTab?: Tab
}

const clone = <T,>(value: T): T => JSON.parse(JSON.stringify(value))
const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)

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

export function RoutineEditDialog({ open, onOpenChange, workspaceId, routine, files, onChanged, initialTab = "identity" }: Props) {
  const { agents } = useWorkspaceAgentDirectory(workspaceId)
  const [tab, setTab] = React.useState<Tab>(initialTab)
  const [name, setName] = React.useState(routine.name ?? "")
  const [description, setDescription] = React.useState(routine.description ?? "")
  const [appearance, setAppearance] = React.useState({ icon: resolveRoutineIcon(routine), color: resolveRoutineColor(routine) })
  const [base, setBase] = React.useState<Record<string, unknown>>(() => clone(routine.definition ?? {}))
  // The draft envelope (id, revision, base) as it was when the dialog opened.
  // Saving sends THIS envelope, never a freshly loaded one: the server's
  // compare-and-set has to compare against the revision the edits started
  // from, otherwise a colleague's r3 saved meanwhile would be overwritten with
  // our r2-based document under r3's number. null = not loaded yet or failed.
  const [envelope, setEnvelope] = React.useState<RoutineDraft | null>(null)
  const [doc, setDoc] = React.useState<Record<string, unknown>>(() => clone(routine.definition ?? {}))
  const [loading, setLoading] = React.useState(false)
  const [busy, setBusy] = React.useState(false)
  const [refusal, setRefusal] = React.useState<string | null>(null)
  const slug = routine.slug

  // Open on the current draft's definition when there is one, so an edit
  // lands as the next revision of that draft rather than a fork of the
  // published version.
  React.useEffect(() => {
    if (!open) return
    setTab(initialTab)
    setName(routine.name ?? "")
    setDescription(routine.description ?? "")
    setAppearance({ icon: resolveRoutineIcon(routine), color: resolveRoutineColor(routine) })
    setRefusal(null)
    const published = clone(routine.definition ?? {})
    setBase(published)
    setDoc(clone(published))
    setEnvelope(null)
    // Always load the envelope, even without a draft: the revision-0 baseline
    // carries the base pipeline id and publication revision the server checks
    // when the first revision is inserted.
    const controller = new AbortController()
    setLoading(true)
    loadRoutineDraft(workspaceId, slug, controller.signal)
      .then((draft) => {
        if (controller.signal.aborted) return
        setEnvelope(draft)
        if (!draft.id || !isRecord(draft.document.definition)) return
        setBase(clone(draft.document.definition))
        setDoc(clone(draft.document.definition))
      })
      .catch(() => {
        if (!controller.signal.aborted) setRefusal("Could not load the routine draft. Close Edit and reopen it before saving.")
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, slug, workspaceId])

  const identityDirty = name.trim() !== (routine.name ?? "").trim() || description.trim() !== (routine.description ?? "").trim()
  const definitionDirty = !same(doc, base)
  const published = (routine.head_version ?? 0) > 0
  const appearanceDirty = !published && !same(appearance, { icon: resolveRoutineIcon(routine), color: resolveRoutineColor(routine) })
  const dirty = identityDirty || definitionDirty || appearanceDirty
  useUnsavedNavigationGuard(open && dirty, "Leave without saving? Your unsaved changes will be lost. Any saved draft stays available.")
  const saveAsDraft = definitionDirty || (!published && dirty)
  const nextRevision = (envelope?.revision ?? routine.draft?.revision ?? 0) + 1
  const update = (patch: (next: Record<string, unknown>) => void) =>
    setDoc((previous) => {
      const next = clone(previous)
      patch(next)
      return next
    })

  const saveAppearance = async (next: Partial<typeof appearance>) => {
    const previous = appearance
    setAppearance({ ...appearance, ...next })
    if (!published) return
    try {
      const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(slug)}/appearance`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(next),
      })
      if (!res.ok) throw new Error("save")
      onChanged()
    } catch {
      setAppearance(previous)
      toast.error("Could not save the icon")
    }
  }

  const save = async () => {
    if (busy || loading || !envelope) return
    if (identityDirty && !name.trim()) {
      setRefusal("The routine needs a name.")
      return
    }
    setBusy(true)
    setRefusal(null)
    try {
      if (identityDirty && !saveAsDraft) {
        const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/save`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(
            renamePayload(
              { slug, name: routine.name, description: routine.description, definition: routine.definition, author_crew_id: routine.author_crew_id },
              { name, description },
            ),
          ),
        })
        if (!res.ok) {
          const problem = await res
            .clone()
            .json()
            .then((body) => extractProblemDetail(body) ?? body?.error)
            .catch(() => null)
          throw new Error(problem || "The name and purpose could not be saved.")
        }
      }
      if (saveAsDraft) {
        if (!envelope) throw new Error("The draft baseline did not load, so this change cannot be saved safely. Close Edit and open it again.")
        const baseline = envelope
        // An input-only edit must retain identity already authored in the
        // draft instead of replacing it with the published page's metadata.
        const draftName = name.trim() !== (routine.name ?? "").trim()
          ? name.trim()
          : asString(baseline.document.name) || asString(doc.display_name) || name.trim() || slug
        const draftDescription = description.trim() !== (routine.description ?? "").trim()
          ? description.trim()
          : typeof baseline.document.description === "string" ? baseline.document.description
            : typeof doc.description === "string" ? doc.description : description.trim()
        const document: Record<string, unknown> = {
          ...(baseline.id ? baseline.document : {}),
          slug,
          name: draftName,
          description: draftDescription,
          definition: { ...doc, display_name: draftName, description: draftDescription },
        }
        if (routine.author_crew_id && !document.author_crew_id) document.author_crew_id = routine.author_crew_id
        if (!document.icon || !published) document.icon = appearance.icon
        if (!document.color || !published) document.color = appearance.color
        const saved = await saveRoutineDraft(workspaceId, { ...baseline, slug }, document)
        setEnvelope(saved)
        toast.success(`Saved as draft r${saved.revision} · publish to make it live`)
      } else if (identityDirty) {
        toast.success("Name and purpose saved")
      }
      onChanged()
      onOpenChange(false)
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e)
      setRefusal(e instanceof RoutineDraftError && e.status === 409 ? "Someone saved a newer draft or published recipe. Your changes are still here. Copy what you want to keep, then reopen Edit to review the latest version." : message)
    } finally {
      setBusy(false)
    }
  }

  const inputs = routineInputSpecs(doc)
  const prompts = agentSteps(doc)
  const decisions = approvalSteps(doc)
  const routineFiles = files ?? routineFilesFromDefinition(routine.definition)
  const steps = Array.isArray(doc.steps) ? doc.steps.filter(isRecord) : []
  const hooks = routineHooks(doc)
  const tier = isRecord(doc.execution_tier) ? doc.execution_tier : {}
  const guardAction = (() => {
    const g = isRecord(doc.guardrails) ? doc.guardrails : {}
    const input = isRecord(g.input) ? g.input : {}
    const pi = isRecord(input.prompt_injection) ? input.prompt_injection : {}
    return asString(pi.action) || "block"
  })()
  const slash = isRecord(doc.slash) ? doc.slash : {}
  const nameOf = (id: string) => {
    const all = [...steps, ...steps.flatMap(foreachBody), ...hooks.map((h) => h.step)]
    const s = all.find((x) => String(x.id) === id)
    return s ? stepDisplayName(s, 0) : id
  }
  const tabLabel: Record<Tab, string> = {
    identity: "Identity",
    inputs: `Inputs · ${inputs.length}`,
    prompts: `Agent prompts · ${prompts.length}`,
    decisions: `Decisions · ${decisions.length}`,
    limits: "Limits",
    slash: "Slash command",
    steps: "Steps & files",
  }

  return (
    <CreateSurface
      open={open}
      onOpenChange={onOpenChange}
      size="lg"
      dirty={dirty}
      discardLabel="your changes"
      discardDescription="Only your unsaved changes will be discarded. Any saved draft stays available."
      onSubmit={() => void save()}
      ariaLabel={`Edit ${routine.name || slug}`}
    >
      <CreateSurfaceHeader concept="routines" context="Routines" title={`Edit ${routine.name || slug}`} onClose={() => onOpenChange(false)} />
      <CreateSurfaceBody className="flex flex-col gap-4">
        <div role="tablist" aria-label="What to edit" className="flex flex-wrap gap-1.5">
          {TABS.map((t) => (
            <button
              key={t}
              type="button"
              role="tab"
              aria-selected={tab === t}
              onClick={() => setTab(t)}
              className={cn(
                "rounded-full border px-2.5 py-1 text-xs transition-colors",
                tab === t ? "border-primary/40 bg-primary/10 text-primary" : "border-hairline text-muted-foreground hover:text-foreground",
              )}
            >
              {tabLabel[t]}
            </button>
          ))}
        </div>
        {loading && <p role="status" className="text-xs text-muted-foreground">Loading the current draft…</p>}

        {tab === "identity" && (
          <div className="flex items-start gap-4">
            <CrewIconPopover {...appearance} size="lg" ariaLabel="Change icon and colour" onIconChange={(icon) => void saveAppearance({ icon })} onColorChange={(color) => void saveAppearance({ color })} />
            <div className="flex min-w-0 flex-1 flex-col gap-3">
              <CreateSurfaceField label="Name" htmlFor="routine-edit-name" required>
                <Input id="routine-edit-name" value={name} onChange={(e) => setName(e.target.value)} className={CREATE_SURFACE_INPUT} />
              </CreateSurfaceField>
              <CreateSurfaceField label="Purpose" htmlFor="routine-edit-purpose" hint="Shown in the list and to the lead.">
                <Textarea id="routine-edit-purpose" rows={3} value={description} onChange={(e) => setDescription(e.target.value)} />
              </CreateSurfaceField>
              <p className="text-[11px] text-muted-foreground">
                {published
                  ? "Icon and colour save at once. Name and purpose save at once when edited alone; combined definition changes stay in the draft."
                  : "Name, purpose, icon and colour stay in this draft until you publish it."}
              </p>
            </div>
          </div>
        )}

        {tab === "inputs" && (
          <div className="flex flex-col gap-3">
            <p className="text-xs text-muted-foreground">
              The questions asked before a run. Label, hint, default, required, choices and number bounds are editable; the technical name and type stay (steps refer to them).
            </p>
            {!inputs.length && <p className="text-xs text-muted-foreground">This routine asks for nothing — Run starts immediately.</p>}
            {inputs.map((input, index) => (
              <InputEditor key={input.name} input={input} onChange={(patch) => update((next) => {
                const list = Array.isArray(next.inputs) ? (next.inputs as Record<string, unknown>[]) : []
                list[index] = { ...list[index], ...patch }
                for (const key of Object.keys(patch)) if (patch[key as keyof RoutineInputSpec] === undefined) delete list[index][key]
                next.inputs = list
              })} />
            ))}
          </div>
        )}

        {tab === "prompts" && (
          <div className="flex flex-col gap-3">
            <p className="text-xs text-muted-foreground">
              What each agent is asked, word for word (the step's <code className="font-mono">prompt</code>). Placeholders like <code className="font-mono">{"{{ inputs.x }}"}</code> are filled in at run time. A change becomes a draft. Tools, skills and the agent itself are set in the CLI.
            </p>
            {!prompts.length && <p className="text-xs text-muted-foreground">No step asks an agent.</p>}
            {prompts.map(({ step, path }) => {
              const agent = agents?.find((a) => a.slug === step.agent_slug)
              const outcomes = isRecord(step.outcomes) ? step.outcomes : null
              const tools = Array.isArray(step.tools) ? step.tools.map(String) : []
              const id = `routine-edit-prompt-${String(step.id)}`
              return (
                <div key={path.join(".")} className="rounded-lg border border-hairline p-3">
                  <div className="mb-2 flex items-center gap-2">
                    {agent ? (
                      <AgentAvatar seed={agent.avatar_seed || agent.name} style={agent.avatar_style || agent.crew?.avatar_style || undefined} agentId={agent.id} avatarUrl={agent.avatar_url} workspaceId={workspaceId} className="h-7 w-7" alt="" />
                    ) : (
                      <span className="flex h-7 w-7 items-center justify-center rounded-lg bg-purple/10 text-purple">
                        <Pencil className="h-3.5 w-3.5" />
                      </span>
                    )}
                    <div className="min-w-0">
                      <label htmlFor={id} className="block text-[13px] font-medium">{stepDisplayName(step, 0)}</label>
                      <p className="text-[11px] text-muted-foreground">
                        {agent?.name || asString(step.agent_slug) || "Agent chosen at run time"}
                        {tools.length ? ` · tools: ${tools.join(", ")}` : ""}
                        {outcomes?.grader_agent_slug ? ` · checked by ${asString(outcomes.grader_agent_slug)}` : ""}
                      </p>
                    </div>
                  </div>
                  <Textarea id={id} rows={4} value={asString(step.prompt)} onChange={(e) => update((next) => setAt(next, path, "prompt", e.target.value))} />
                </div>
              )
            })}
          </div>
        )}

        {tab === "limits" && (
          <div className="flex flex-col gap-3">
            <CreateSurfaceNotice tone="warn">Sketch — which of these belong in the web is not decided yet; every field maps to an existing DSL setting.</CreateSurfaceNotice>
            <CreateSurfaceField label="Cost cap per run" htmlFor="routine-edit-cost" hint="Stops a run that goes over; work already done is paid.">
              <Input id="routine-edit-cost" inputMode="decimal" placeholder="no cap" value={doc.max_cost_usd == null ? "" : String(doc.max_cost_usd)} onChange={(e) => update((next) => { const n = Number(e.target.value); if (!e.target.value.trim() || !Number.isFinite(n)) delete next.max_cost_usd; else next.max_cost_usd = n })} className={CREATE_SURFACE_INPUT} />
            </CreateSurfaceField>
            <CreateSurfaceField label="Runs at the same time" hint="“One at a time” queues a new start until the running one finishes.">
              <CreateSurfaceChoice
                ariaLabel="Runs at the same time"
                value={asString(doc.concurrency_key) ? "one" : "unlimited"}
                options={[{ value: "unlimited", label: "Unlimited" }, { value: "one", label: "One at a time" }]}
                onChange={(v) => update((next) => { if (v === "one") { next.concurrency_key = slug; next.max_concurrent = 1 } else { delete next.concurrency_key; delete next.max_concurrent } })}
              />
            </CreateSurfaceField>
            <CreateSurfaceField label="Model tier for agent steps" hint="A step's own setting wins. Fallback tiers are tried when the preferred one fails its checks.">
              <div className="flex flex-col gap-1.5">
                <span className="text-[11px] text-muted-foreground">Preferred</span>
                <CreateSurfaceChoice
                  ariaLabel="Preferred tier"
                  value={(asString(tier.preferred) || "default") as string}
                  options={[{ value: "default", label: "Workspace default" }, ...TIERS.map((t) => ({ value: t, label: t }))]}
                  onChange={(v) => update((next) => { const t = isRecord(next.execution_tier) ? { ...next.execution_tier } : {}; if (v === "default") delete t.preferred; else t.preferred = v; if (Object.keys(t).length) next.execution_tier = t; else delete next.execution_tier })}
                />
                <span className="text-[11px] text-muted-foreground">Fallback</span>
                <div className="flex flex-wrap gap-1.5">
                  {TIERS.map((t) => {
                    const on = Array.isArray(tier.fallback) && tier.fallback.includes(t)
                    return (
                      <button key={t} type="button" aria-pressed={on} onClick={() => update((next) => { const cur = isRecord(next.execution_tier) ? { ...next.execution_tier } : {}; const list = Array.isArray(cur.fallback) ? cur.fallback.filter((x) => x !== t) : []; if (!on) list.push(t); if (list.length) cur.fallback = list; else delete cur.fallback; if (Object.keys(cur).length) next.execution_tier = cur; else delete next.execution_tier })} className={cn("rounded-full border px-2.5 py-1 text-xs", on ? "border-primary/40 bg-primary/10 text-primary" : "border-hairline text-muted-foreground")}>
                        {t}
                      </button>
                    )
                  })}
                </div>
              </div>
            </CreateSurfaceField>
            <CreateSurfaceField label="Suspicious text in inputs">
              <CreateSurfaceChoice
                ariaLabel="Suspicious text in inputs"
                value={guardAction}
                options={[{ value: "block", label: "Block the run" }, { value: "sanitize", label: "Clean it and continue" }, { value: "log", label: "Only log it" }]}
                onChange={(v) => update((next) => { const g = isRecord(next.guardrails) ? { ...next.guardrails } : {}; const input = isRecord(g.input) ? { ...g.input } : {}; input.prompt_injection = { ...(isRecord(input.prompt_injection) ? input.prompt_injection : {}), action: v }; g.input = input; next.guardrails = g })}
              />
            </CreateSurfaceField>
            <CreateSurfaceField label="Estimates shown before a run" hint="Planning hints only; not enforced.">
              <div className="flex gap-2">
                <Input aria-label="Estimated cost in USD" inputMode="decimal" placeholder="cost, e.g. 0.4" value={doc.estimated_cost_usd == null ? "" : String(doc.estimated_cost_usd)} onChange={(e) => update((next) => { const n = Number(e.target.value); if (!e.target.value.trim() || !Number.isFinite(n)) delete next.estimated_cost_usd; else next.estimated_cost_usd = n })} className={CREATE_SURFACE_INPUT} />
                <Input aria-label="Estimated duration in minutes" inputMode="numeric" placeholder="minutes, e.g. 25" value={typeof doc.estimated_duration_seconds === "number" ? String(Math.round(doc.estimated_duration_seconds / 60)) : ""} onChange={(e) => update((next) => { const n = Number(e.target.value); if (!e.target.value.trim() || !Number.isFinite(n)) delete next.estimated_duration_seconds; else next.estimated_duration_seconds = Math.round(n * 60) })} className={CREATE_SURFACE_INPUT} />
              </div>
            </CreateSurfaceField>
          </div>
        )}

        {tab === "slash" && (
          <div className="flex flex-col gap-3">
            <label className="flex items-center gap-2 text-[13px] font-medium">
              <Switch checked={slash.enabled === true} onCheckedChange={(on) => update((next) => { next.slash = { ...(isRecord(next.slash) ? next.slash : {}), enabled: on } })} aria-label="Offer as a slash command" />
              Offer this routine in chat and the CLI as a slash command
            </label>
            <CreateSurfaceField label="Label" htmlFor="routine-edit-slash" hint={`People type /${slug}; the label is what the palette shows. Typing it opens the same questions as Run.`}>
              <Input id="routine-edit-slash" value={asString(slash.label)} disabled={slash.enabled !== true} onChange={(e) => update((next) => { next.slash = { ...(isRecord(next.slash) ? next.slash : {}), label: e.target.value } })} className={CREATE_SURFACE_INPUT} />
            </CreateSurfaceField>
          </div>
        )}

        {tab === "decisions" && (
          <div className="space-y-4">
            <p className="text-xs text-muted-foreground">Edit the questions and decision buttons for existing approval steps. Save a draft, then publish it to use the form in new runs.</p>
            {decisions.length === 0 && <p className="text-sm text-muted-foreground">This recipe has no approval steps. Add an approval step through the CLI or ask the lead to prepare one, then edit its form here.</p>}
            {decisions.map(({ step, path }) => {
              const wait = isRecord(step.wait) ? step.wait : {}
              const patchWait = (patch: Record<string, unknown>) => update((next) => setAt(next, path, "wait", { ...wait, ...patch }))
              const id = `routine-decision-${path.join("-")}`
              return (
                <section key={path.join(".")} className="space-y-3 rounded-lg border border-hairline p-3" aria-label={`Decision: ${stepDisplayName(step, 0)}`}>
                  <h3 className="text-sm font-medium">{stepDisplayName(step, 0)}</h3>
                  <CreateSurfaceField label="Decision title" htmlFor={`${id}-title`}>
                    <Input id={`${id}-title`} value={asString(wait.approval_title)} onChange={(e) => patchWait({ approval_title: e.target.value })} />
                  </CreateSurfaceField>
                  <CreateSurfaceField label="What should the reviewer decide?" htmlFor={`${id}-prompt`}>
                    <Textarea id={`${id}-prompt`} value={asString(wait.approval_prompt)} onChange={(e) => patchWait({ approval_prompt: e.target.value })} />
                  </CreateSurfaceField>
                  <RoutineDecisionFormBuilder value={wait.decision_form} onChange={(decision_form) => patchWait({ decision_form })} onOpenCode={() => setTab("steps")} codeActionLabel="View editing instructions" />
                </section>
              )
            })}
          </div>
        )}

        {tab === "steps" && (
          <div className="flex flex-col gap-3 text-xs">
            <div className="rounded-lg border border-hairline p-3">
              <p className="mb-1 text-[13px] font-medium">Step structure, checks, hooks and scripts</p>
              <p className="text-muted-foreground">
                Read-only in the web. {steps.length} {steps.length === 1 ? "step" : "steps"}
                {hooks.length ? `, ${hooks.length} ${hooks.length === 1 ? "hook" : "hooks"}` : ""}, {routineFiles.length} {routineFiles.length === 1 ? "file" : "files"}. Change them with the CLI or by asking the lead agent; the result arrives here as a draft to publish.
              </p>
              <pre className="mt-2 overflow-x-auto rounded-md border border-hairline bg-background p-2 font-mono text-[11px]">
{`crewship routine draft get ${slug} -f json > draft.json
# edit draft.json.document; scripts go to the crew share:
crewship crew files save <crew> shared/scripts/x.py --file ./x.py
crewship routine draft save draft.json -f json > saved.json`}
              </pre>
            </div>
            {routineFiles.length > 0 && (
              <ul className="divide-y divide-hairline rounded-lg border border-hairline">
                {routineFiles.map((f) => (
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
        )}
      </CreateSurfaceBody>
      <CreateSurfaceRefusal message={refusal} onDismiss={() => setRefusal(null)} />
      <CreateSurfaceFooter
        hint={
          saveAsDraft
            ? published
              ? `All changes, including name and purpose, are saved as draft r${nextRevision}; published v${routine.head_version ?? 0} keeps running`
              : `Changes are saved as draft r${nextRevision}; publish it before running`
            : "Identity applies at once; definition changes become a draft"
        }
        onCancel={() => onOpenChange(false)}
        primaryLabel={busy ? "Saving…" : saveAsDraft ? "Save draft" : "Save"}
        primaryIcon={Save}
        onPrimary={() => void save()}
        primaryDisabled={!dirty || loading || !envelope}
        busy={busy}
      />
    </CreateSurface>
  )
}

/** One question: label, default, hint, required, choices and bounds; name and type read-only. */
function InputEditor({ input, onChange }: { input: RoutineInputSpec; onChange: (patch: Partial<RoutineInputSpec>) => void }) {
  const type = input.type || "string"
  const numeric = type === "number" || type === "integer"
  const choices = Array.isArray(input.options) ? input.options : null
  const id = `routine-edit-input-${input.name}`
  const [newChoice, setNewChoice] = React.useState("")
  return (
    <div className="rounded-lg border border-hairline p-3" data-testid={`routine-edit-input-${input.name}`}>
      <div className="mb-2 flex flex-wrap items-center gap-1.5 text-[11px]">
        <span className="rounded-md border border-hairline px-2 py-0.5 font-mono">{input.name}</span>
        <span className="rounded-md border border-hairline px-2 py-0.5 text-muted-foreground">{choices ? "choice" : input.format === "absolute_path" ? "path" : type}</span>
        {(input.min != null || input.max != null) && <span className="rounded-md border border-hairline px-2 py-0.5 text-muted-foreground">{input.min ?? "…"} – {input.max ?? "…"}</span>}
        <label className="ml-auto flex items-center gap-1.5 text-xs">
          <input type="checkbox" checked={!!input.required} onChange={(e) => onChange({ required: e.target.checked || undefined })} /> Required
        </label>
      </div>
      <div className="grid gap-2 sm:grid-cols-2">
        <CreateSurfaceField label="Label" htmlFor={`${id}-label`}>
          <Input id={`${id}-label`} value={input.label ?? ""} onChange={(e) => onChange({ label: e.target.value })} className={CREATE_SURFACE_INPUT} />
        </CreateSurfaceField>
        <CreateSurfaceField label="Default" htmlFor={`${id}-default`}>
          {type === "boolean" ? (
            <label className="flex h-8 items-center gap-2 text-xs">
              <input type="checkbox" id={`${id}-default`} checked={input.default === true} onChange={(e) => onChange({ default: e.target.checked })} /> {input.default === true ? "Yes" : "No"}
            </label>
          ) : choices ? (
            <select id={`${id}-default`} value={formatInputDefault(input.default)} onChange={(e) => onChange({ default: e.target.value || undefined })} className={cn("w-full rounded-md border border-hairline bg-background px-2", CREATE_SURFACE_INPUT)}>
              <option value="">Ask each time</option>
              {choices.map((o) => <option key={o} value={o}>{o}</option>)}
            </select>
          ) : (
            <Input id={`${id}-default`} inputMode={numeric ? "decimal" : undefined} value={formatInputDefault(input.default)} placeholder="Ask each time" onChange={(e) => { const raw = e.target.value; if (!raw.trim()) return onChange({ default: undefined }); if (numeric) { const n = Number(raw); onChange({ default: Number.isFinite(n) ? n : raw }) } else onChange({ default: raw }) }} className={CREATE_SURFACE_INPUT} />
          )}
        </CreateSurfaceField>
        <CreateSurfaceField label="Hint" htmlFor={`${id}-hint`} className="sm:col-span-2">
          <Input id={`${id}-hint`} value={input.description ?? ""} placeholder="Shown under the field" onChange={(e) => onChange({ description: e.target.value || undefined })} className={CREATE_SURFACE_INPUT} />
        </CreateSurfaceField>
        {numeric && (
          <>
            <CreateSurfaceField label="Minimum" htmlFor={`${id}-min`}>
              <Input id={`${id}-min`} inputMode="decimal" value={input.min ?? ""} onChange={(e) => onChange({ min: e.target.value.trim() === "" ? undefined : Number(e.target.value) })} className={CREATE_SURFACE_INPUT} />
            </CreateSurfaceField>
            <CreateSurfaceField label="Maximum" htmlFor={`${id}-max`}>
              <Input id={`${id}-max`} inputMode="decimal" value={input.max ?? ""} onChange={(e) => onChange({ max: e.target.value.trim() === "" ? undefined : Number(e.target.value) })} className={CREATE_SURFACE_INPUT} />
            </CreateSurfaceField>
          </>
        )}
        {choices && (
          <CreateSurfaceField label="Choices" className="sm:col-span-2">
            <div className="flex flex-wrap items-center gap-1.5">
              {choices.map((o) => (
                <button key={o} type="button" aria-label={`Remove ${o}`} onClick={() => onChange({ options: choices.filter((x) => x !== o) })} className="rounded-full border border-hairline px-2.5 py-0.5 text-xs text-muted-foreground hover:text-destructive">
                  {o} ✕
                </button>
              ))}
              <Input aria-label="New choice" value={newChoice} placeholder="+ add" onChange={(e) => setNewChoice(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter" && newChoice.trim()) { e.preventDefault(); onChange({ options: [...choices, newChoice.trim()] }); setNewChoice("") } }} className={cn("w-28", CREATE_SURFACE_INPUT)} />
              <label className="flex items-center gap-1.5 text-xs">
                <input type="checkbox" checked={!!input.allow_custom} onChange={(e) => onChange({ allow_custom: e.target.checked || undefined })} /> Allow other answers
              </label>
            </div>
          </CreateSurfaceField>
        )}
      </div>
    </div>
  )
}
