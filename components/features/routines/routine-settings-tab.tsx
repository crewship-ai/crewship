"use client"

// The routine page's Settings (Harbor 2 RT-SET): About, Inputs, Limits, the
// chat command, Access, Budget and Turn off or delete. What changes how the
// routine runs is saved as a draft with the page's save bar; name and purpose
// alone apply at once (useRoutineEditor). Full detail adds the model tiers,
// input guardrails, estimates and the technical metadata.

import * as React from "react"
import Link from "next/link"
import { Gauge, Info, MessageSquare, PowerOff, Power, Trash2, Type } from "lucide-react"
import {
  CREATE_SURFACE_INPUT,
  CreateSurfaceChoice,
  CreateSurfaceField,
  CreateSurfaceRefusal,
} from "@/components/layout/create-surface"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Switch } from "@/components/ui/switch"
import { Button } from "@/components/ui/button"
import { DetailCard } from "@/components/ui/detail"
import { cn } from "@/lib/utils"
import { routineInputSpecs, formatInputDefault, type RoutineInputSpec } from "@/lib/routine-inputs"
import { isRecord, asString } from "@/lib/routine-step-describe"
import { crewshipActionsInDefinition } from "@/lib/automations"
import type { DetailDepth } from "@/lib/routine-page"
import { ResourceShareDialog } from "@/components/features/settings/resource-share-dialog"
import { RoutineBudgetCard } from "./routine-budget-card"
import { AccessCard, Metadata } from "./routine-card-detail"
import type { RoutineEditor } from "./use-routine-editor"
import type { RoutineDetail } from "./routines-detail-panel"

const TIERS = ["trivial", "fast", "moderate", "smart"] as const

export function RoutineSettingsTab({
  routine,
  workspaceId,
  editor,
  canEdit,
  canManage,
  depth,
  lifecycle,
  onGovernance,
  onDelete,
}: {
  routine: RoutineDetail
  workspaceId: string
  editor: RoutineEditor
  /** Manager and above: the draft-saving fields are editable. */
  canEdit: boolean
  /** Owner and Admin: Turn off and Delete. */
  canManage: boolean
  depth: DetailDepth
  lifecycle: "active" | "proposed" | "disabled"
  onGovernance: (action: "disable" | "enable") => void
  onDelete: () => void
}) {
  const doc = editor.doc
  const update = editor.update
  const slug = routine.slug
  const inputs = routineInputSpecs(doc)
  const tier = isRecord(doc.execution_tier) ? doc.execution_tier : {}
  const guardAction = (() => {
    const g = isRecord(doc.guardrails) ? doc.guardrails : {}
    const input = isRecord(g.input) ? g.input : {}
    const pi = isRecord(input.prompt_injection) ? input.prompt_injection : {}
    return asString(pi.action) || "block"
  })()
  const slash = isRecord(doc.slash) ? doc.slash : {}
  const published = editor.published
  const crewshipActions = React.useMemo(() => crewshipActionsInDefinition(routine.definition), [routine.definition])
  const disabled = !canEdit || editor.loading || editor.unavailable
  const steps = Array.isArray(routine.definition.steps) ? routine.definition.steps.length : 0

  return (
    <div className="flex flex-col gap-4" data-testid="routine-settings">
      {editor.loading && <p role="status" className="text-xs text-muted-foreground">Loading the current draft…</p>}
      <CreateSurfaceRefusal message={editor.refusal} onDismiss={() => editor.setRefusal(null)} />
      {editor.dirty && (
        <p className="text-xs text-muted-foreground" data-testid="routine-save-hint">
          {editor.saveAsDraft
            ? published
              ? `All changes, including name and purpose, are saved as draft r${editor.nextRevision}; the live version keeps running until you publish.`
              : `Changes are saved as draft r${editor.nextRevision}; publish it before running.`
            : "Name and purpose apply at once; definition changes become a draft."}
        </p>
      )}

      <DetailCard title="About" icon={Info}>
        <div className="flex flex-col gap-3">
          <CreateSurfaceField label="Name" htmlFor="routine-edit-name" required>
            <Input id="routine-edit-name" value={editor.name} disabled={disabled} onChange={(e) => editor.setName(e.target.value)} className={CREATE_SURFACE_INPUT} />
          </CreateSurfaceField>
          <CreateSurfaceField label="Purpose" htmlFor="routine-edit-purpose" hint="Shown in the list and to the lead.">
            <Textarea id="routine-edit-purpose" rows={3} value={editor.description} disabled={disabled} onChange={(e) => editor.setDescription(e.target.value)} />
          </CreateSurfaceField>
          <p className="text-xs text-muted-foreground">
            {published
              ? "The icon and colour (in the header) save at once. Name and purpose save at once when edited alone; with other changes they go into the draft."
              : "Name, purpose, icon and colour stay in this draft until you publish it."}
          </p>
        </div>
      </DetailCard>

      <DetailCard title="Inputs" icon={Type} subtitle={String(inputs.length)}>
        <div className="flex flex-col gap-3">
          <p className="text-xs text-muted-foreground">
            The questions asked before a run. Label, hint, default, required, choices and number bounds are editable; the technical name and type stay, because steps refer to them.
          </p>
          {!inputs.length && <p className="text-xs text-muted-foreground">This routine asks for nothing — Run starts immediately.</p>}
          {inputs.map((input, index) => (
            <InputEditor
              key={input.name}
              input={input}
              disabled={disabled}
              onChange={(patch) =>
                update((next) => {
                  const list = Array.isArray(next.inputs) ? (next.inputs as Record<string, unknown>[]) : []
                  list[index] = { ...list[index], ...patch }
                  for (const key of Object.keys(patch)) if (patch[key as keyof RoutineInputSpec] === undefined) delete list[index][key]
                  next.inputs = list
                })
              }
            />
          ))}
        </div>
      </DetailCard>

      <DetailCard title="Limits" icon={Gauge}>
        <div className="flex flex-col gap-3">
          <CreateSurfaceField label="Cost cap per run" htmlFor="routine-edit-cost" hint="Checked before and after each step; a run that goes over stops. Work already done is paid.">
            <Input
              id="routine-edit-cost"
              inputMode="decimal"
              placeholder="no cap"
              disabled={disabled}
              value={doc.max_cost_usd == null ? "" : String(doc.max_cost_usd)}
              onChange={(e) =>
                update((next) => {
                  const n = Number(e.target.value)
                  if (!e.target.value.trim() || !Number.isFinite(n)) delete next.max_cost_usd
                  else next.max_cost_usd = n
                })
              }
              className={cn(CREATE_SURFACE_INPUT, "sm:w-40")}
            />
          </CreateSurfaceField>
          <CreateSurfaceField label="Runs at the same time" hint="“One at a time” queues a new start until the running one finishes.">
            <CreateSurfaceChoice
              ariaLabel="Runs at the same time"
              value={asString(doc.concurrency_key) ? "one" : "unlimited"}
              options={[
                { value: "unlimited", label: "Unlimited" },
                { value: "one", label: "One at a time" },
              ]}
              onChange={(v) =>
                !disabled &&
                update((next) => {
                  if (v === "one") {
                    next.concurrency_key = slug
                    next.max_concurrent = 1
                  } else {
                    delete next.concurrency_key
                    delete next.max_concurrent
                  }
                })
              }
            />
          </CreateSurfaceField>
          {depth === "full" ? (
            <>
              <CreateSurfaceField label="Model tier for agent steps" hint="A step's own setting wins. Fallback tiers are tried when the preferred one fails its checks.">
                <div className="flex flex-col gap-1.5">
                  <span className="text-xs text-muted-foreground">Preferred</span>
                  <CreateSurfaceChoice
                    ariaLabel="Preferred tier"
                    value={(asString(tier.preferred) || "default") as string}
                    options={[{ value: "default", label: "Workspace default" }, ...TIERS.map((t) => ({ value: t, label: t }))]}
                    onChange={(v) =>
                      !disabled &&
                      update((next) => {
                        const t = isRecord(next.execution_tier) ? { ...next.execution_tier } : {}
                        if (v === "default") delete t.preferred
                        else t.preferred = v
                        if (Object.keys(t).length) next.execution_tier = t
                        else delete next.execution_tier
                      })
                    }
                  />
                  <span className="text-xs text-muted-foreground">Fallback</span>
                  <div className="flex flex-wrap gap-1.5">
                    {TIERS.map((t) => {
                      const on = Array.isArray(tier.fallback) && tier.fallback.includes(t)
                      return (
                        <button
                          key={t}
                          type="button"
                          aria-pressed={on}
                          disabled={disabled}
                          onClick={() =>
                            update((next) => {
                              const cur = isRecord(next.execution_tier) ? { ...next.execution_tier } : {}
                              const list = Array.isArray(cur.fallback) ? cur.fallback.filter((x) => x !== t) : []
                              if (!on) list.push(t)
                              if (list.length) cur.fallback = list
                              else delete cur.fallback
                              if (Object.keys(cur).length) next.execution_tier = cur
                              else delete next.execution_tier
                            })
                          }
                          className={cn("rounded-full border px-2.5 py-1 text-xs coarse:min-h-[44px]", on ? "border-primary/40 bg-primary/10 text-primary" : "border-hairline text-muted-foreground")}
                        >
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
                  options={[
                    { value: "block", label: "Block the run" },
                    { value: "sanitize", label: "Clean it and continue" },
                    { value: "log", label: "Only log it" },
                  ]}
                  onChange={(v) =>
                    !disabled &&
                    update((next) => {
                      const g = isRecord(next.guardrails) ? { ...next.guardrails } : {}
                      const input = isRecord(g.input) ? { ...g.input } : {}
                      input.prompt_injection = { ...(isRecord(input.prompt_injection) ? input.prompt_injection : {}), action: v }
                      g.input = input
                      next.guardrails = g
                    })
                  }
                />
              </CreateSurfaceField>
              <CreateSurfaceField label="Estimates shown before a run" hint="Planning hints only; not enforced.">
                <div className="flex gap-2">
                  <Input
                    aria-label="Estimated cost in USD"
                    inputMode="decimal"
                    placeholder="cost, e.g. 0.4"
                    disabled={disabled}
                    value={doc.estimated_cost_usd == null ? "" : String(doc.estimated_cost_usd)}
                    onChange={(e) =>
                      update((next) => {
                        const n = Number(e.target.value)
                        if (!e.target.value.trim() || !Number.isFinite(n)) delete next.estimated_cost_usd
                        else next.estimated_cost_usd = n
                      })
                    }
                    className={CREATE_SURFACE_INPUT}
                  />
                  <Input
                    aria-label="Estimated duration in minutes"
                    inputMode="numeric"
                    placeholder="minutes, e.g. 25"
                    disabled={disabled}
                    value={typeof doc.estimated_duration_seconds === "number" ? String(Math.round(doc.estimated_duration_seconds / 60)) : ""}
                    onChange={(e) =>
                      update((next) => {
                        const n = Number(e.target.value)
                        if (!e.target.value.trim() || !Number.isFinite(n)) delete next.estimated_duration_seconds
                        else next.estimated_duration_seconds = Math.round(n * 60)
                      })
                    }
                    className={CREATE_SURFACE_INPUT}
                  />
                </div>
              </CreateSurfaceField>
            </>
          ) : (
            <p className="text-xs text-muted-foreground">Model tiers, input guardrails and estimates are in Full detail.</p>
          )}
          <p className="text-xs text-muted-foreground-soft">What a missed schedule does is a setting of the schedule, under Triggers.</p>
        </div>
      </DetailCard>

      <DetailCard title="Chat command" icon={MessageSquare}>
        <div className="flex flex-col gap-3">
          <label className="flex items-center gap-2 text-sm font-medium">
            <Switch
              checked={slash.enabled === true}
              disabled={disabled}
              onCheckedChange={(on) => update((next) => { next.slash = { ...(isRecord(next.slash) ? next.slash : {}), enabled: on } })}
              aria-label="Offer as a slash command"
            />
            Start it from chat and the CLI as /{slug}
          </label>
          <CreateSurfaceField label="Label" htmlFor="routine-edit-slash" hint={`People type /${slug}; the label is what the palette shows. Typing it asks the same questions as Run.`}>
            <Input
              id="routine-edit-slash"
              value={asString(slash.label)}
              disabled={disabled || slash.enabled !== true}
              onChange={(e) => update((next) => { next.slash = { ...(isRecord(next.slash) ? next.slash : {}), label: e.target.value } })}
              className={cn(CREATE_SURFACE_INPUT, "sm:w-64")}
            />
          </CreateSurfaceField>
        </div>
      </DetailCard>

      <AccessCard
        workspaceId={workspaceId}
        routine={routine}
        crewshipActions={crewshipActions}
        share={published && routine.id ? <ResourceShareDialog workspaceId={workspaceId} resource={{ kind: "routine", id: routine.id, name: routine.name || routine.slug }} /> : undefined}
      />
      <p className="-mt-2 text-xs text-muted-foreground">
        People who can run it use the crew&apos;s credentials. Only Owner and Admin can change or delete schedules.{" "}
        <Link className="text-primary hover:underline" href="/credentials">Credentials</Link> ·{" "}
        <Link className="text-primary hover:underline" href="/integrations">Integrations</Link>
      </p>
      {published && <RoutineBudgetCard workspaceId={workspaceId} slug={slug} />}

      {depth === "full" && (
        <DetailCard title="Technical details">
          <Metadata routine={routine} steps={steps} />
        </DetailCard>
      )}

      {canManage && published && (
        <section className="flex flex-col gap-2 rounded-card border border-destructive/30 px-4 py-3" aria-label="Turn off or delete">
          <h3 className="text-sm font-medium">Turn off or delete</h3>
          <p className="text-xs text-muted-foreground">
            Turning off stops new runs, including schedules and webhooks; runs in progress finish. Deleting removes it from the list; its run history stays readable.
          </p>
          <div className="flex flex-wrap gap-2">
            {lifecycle !== "proposed" &&
              (lifecycle === "disabled" ? (
                <Button type="button" variant="outline" size="sm" onClick={() => onGovernance("enable")} className="gap-1.5 coarse:h-[44px]">
                  <Power className="h-3.5 w-3.5" aria-hidden />
                  Turn on
                </Button>
              ) : (
                <Button type="button" variant="outline" size="sm" onClick={() => onGovernance("disable")} className="gap-1.5 coarse:h-[44px]">
                  <PowerOff className="h-3.5 w-3.5" aria-hidden />
                  Turn off
                </Button>
              ))}
            <Button type="button" variant="destructive" size="sm" onClick={onDelete} className="gap-1.5 coarse:h-[44px]">
              <Trash2 className="h-3.5 w-3.5" aria-hidden />
              Delete routine…
            </Button>
          </div>
        </section>
      )}
    </div>
  )
}

/** One question: label, default, hint, required, choices and bounds; name and type read-only. */
function InputEditor({ input, disabled, onChange }: { input: RoutineInputSpec; disabled?: boolean; onChange: (patch: Partial<RoutineInputSpec>) => void }) {
  const type = input.type || "string"
  const numeric = type === "number" || type === "integer"
  const choices = Array.isArray(input.options) ? input.options : null
  const id = `routine-edit-input-${input.name}`
  const [newChoice, setNewChoice] = React.useState("")
  return (
    <fieldset disabled={disabled} className="rounded-lg border border-hairline p-3" data-testid={`routine-edit-input-${input.name}`}>
      <div className="mb-2 flex flex-wrap items-center gap-1.5 text-xs">
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
              {choices.map((o) => (
                <option key={o} value={o}>{o}</option>
              ))}
            </select>
          ) : (
            <Input
              id={`${id}-default`}
              inputMode={numeric ? "decimal" : undefined}
              value={formatInputDefault(input.default)}
              placeholder="Ask each time"
              onChange={(e) => {
                const raw = e.target.value
                if (!raw.trim()) return onChange({ default: undefined })
                if (numeric) {
                  const n = Number(raw)
                  onChange({ default: Number.isFinite(n) ? n : raw })
                } else onChange({ default: raw })
              }}
              className={CREATE_SURFACE_INPUT}
            />
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
              <Input
                aria-label="New choice"
                value={newChoice}
                placeholder="+ add"
                onChange={(e) => setNewChoice(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && newChoice.trim()) {
                    e.preventDefault()
                    onChange({ options: [...choices, newChoice.trim()] })
                    setNewChoice("")
                  }
                }}
                className={cn("w-28", CREATE_SURFACE_INPUT)}
              />
              <label className="flex items-center gap-1.5 text-xs">
                <input type="checkbox" checked={!!input.allow_custom} onChange={(e) => onChange({ allow_custom: e.target.checked || undefined })} /> Allow other answers
              </label>
            </div>
          </CreateSurfaceField>
        )}
      </div>
    </fieldset>
  )
}
