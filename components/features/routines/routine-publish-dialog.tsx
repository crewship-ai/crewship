"use client"

import * as React from "react"
import { Upload } from "lucide-react"
import { toast } from "sonner"

import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceNotice,
  CreateSurfaceRefusal,
  CreateSurfaceSecondaryAction,
} from "@/components/layout/create-surface"
import { DetailCard } from "@/components/ui/detail"
import { apiFetch } from "@/lib/api-fetch"
import { relTime } from "@/lib/time"
import { discardRoutineDraft, loadRoutineDraft, type RoutineDraft } from "@/lib/routine-drafts"
import { routinePublicationChanges } from "@/lib/routine-publication-changes"
import { routineEffects } from "@/lib/routine-effects"
import { isRecord } from "@/lib/routine-step-describe"
import { usePipelineSchedules, type PipelineSchedule } from "@/hooks/use-pipeline-schedules"
import { useSessionSafe } from "@/hooks/use-auth"
import { draftAuthorLabel } from "./routine-identity-header"
import type { RoutineDetail } from "./routines-detail-panel"

// routine-publish-dialog — "Publish draft rM as vN+1".
//
// Publishing is a control action, not authoring: who saved the draft, what
// changes against the published version, what was checked, and who is
// affected once it is live. It uses the same two server calls the old
// editor used for a stored draft: POST …/pipelines/test_run for the static
// check (it mints the save_token the publication needs) and POST
// …/pipelines/{slug}/publish with the draft's exact id and revision. The
// "Approve capability changes" question appears only when the server says
// the draft needs it (409 with risk_reasons) — a technical term stays off
// the main path.

type ScheduleRow = PipelineSchedule & { version_pinned?: boolean; effective_version?: number }

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  workspaceId: string
  routine: Pick<RoutineDetail, "slug" | "name" | "head_version" | "definition" | "draft" | "author_crew_id" | "id">
  /** Active (running/waiting) runs, which keep their version. */
  activeRuns?: number
  onPublished: (slug: string) => void
  onDiscarded: () => void
}

/** Affected plans: those following the latest version move; pinned ones stay. */
export function scheduleImpact(schedules: ScheduleRow[], routine: { id: string; slug: string }) {
  const mine = schedules.filter(
    (s) => s.target_pipeline_id === routine.id || s.target_pipeline_slug === routine.slug,
  )
  const pinned = mine.filter((s) => s.version_pinned ?? s.target_pipeline_version != null)
  return { latest: mine.length - pinned.length, pinned: pinned.length }
}

/** The five groups the review reads: Inputs / Steps / People / Effects / Access. */
export function publicationSummary(published: Record<string, unknown>, draft: Record<string, unknown>) {
  const changes = routinePublicationChanges(published, draft)
  const group = (label: string) => changes.groups.find((g) => g.label === label)
  const line = (g: ReturnType<typeof group>) => {
    if (!g) return "Unchanged"
    if (!g.readable) return "Needs review in the CLI"
    const parts: string[] = []
    if (g.added.length) parts.push(`Added · ${g.added.join(", ")}`)
    if (g.changed.length) parts.push(`Changed · ${g.changed.join(", ")}`)
    if (g.removed.length) parts.push(`Removed · ${g.removed.join(", ")}`)
    if (g.reordered) parts.push("Order changed")
    return parts.length ? parts.join(" · ") : "Unchanged"
  }
  const people = (definition: Record<string, unknown>) => {
    const steps = Array.isArray(definition.steps) ? definition.steps.filter(isRecord) : []
    return steps
      .filter((s) => s.type === "wait")
      .map((s) => String(s.name || s.id))
      .sort()
  }
  const before = people(published)
  const after = people(draft)
  const peopleLine =
    JSON.stringify(before) === JSON.stringify(after)
      ? after.length
        ? `Unchanged · ${after.join(", ")}`
        : "Unchanged · no decisions by a person"
      : `Changed · ${after.length ? after.join(", ") : "no decisions by a person"}`
  const effectsOf = (definition: Record<string, unknown>) => {
    const e = routineEffects(definition)
    return { hosts: [...e.hosts].sort(), credentials: [...e.credentials].sort(), agents: [...e.agents].sort(), indirect: e.indirect, http: e.http }
  }
  const eb = effectsOf(published)
  const ea = effectsOf(draft)
  const describe = (e: ReturnType<typeof effectsOf>) => {
    const parts: string[] = []
    if (e.agents.length) parts.push(`agents ${e.agents.join(", ")}`)
    if (e.http) parts.push(`HTTP${e.hosts.length ? ` to ${e.hosts.join(", ")}` : ""}`)
    if (e.indirect) parts.push("scripts, notifications or called routines")
    return parts.length ? parts.join(", ") : "no external writes declared"
  }
  const effectsLine =
    JSON.stringify([eb.agents, eb.hosts, eb.indirect, eb.http]) === JSON.stringify([ea.agents, ea.hosts, ea.indirect, ea.http])
      ? `Unchanged · ${describe(ea)}`
      : `Changed · ${describe(ea)}`
  const newCreds = ea.credentials.filter((c) => !eb.credentials.includes(c))
  const newHosts = ea.hosts.filter((h) => !eb.hosts.includes(h))
  const accessLine =
    newCreds.length || newHosts.length
      ? `Changed · ${[newCreds.length ? `needs ${newCreds.join(", ")}` : "", newHosts.length ? `reaches ${newHosts.join(", ")}` : ""].filter(Boolean).join(" · ")}`
      : "Unchanged · no new credentials or hosts"
  return {
    rows: [
      ["Inputs", line(group("Input questions"))],
      ["Steps", line(group("Workflow steps"))],
      ["People", peopleLine],
      ["Effects", effectsLine],
      ["Access", accessLine],
    ] as [string, string][],
    settings: changes.settings,
  }
}

export function RoutinePublishDialog({ open, onOpenChange, workspaceId, routine, activeRuns = 0, onPublished, onDiscarded }: Props) {
  const { data: session } = useSessionSafe()
  const { schedules } = usePipelineSchedules(open ? workspaceId : null)
  const [draft, setDraft] = React.useState<RoutineDraft | null>(null)
  const [loadError, setLoadError] = React.useState<string | null>(null)
  const [check, setCheck] = React.useState<{ passed: boolean; detail: string; token: string | null } | null>(null)
  const [busy, setBusy] = React.useState<"publish" | "discard" | null>(null)
  const [refusal, setRefusal] = React.useState<string | null>(null)
  const [riskReasons, setRiskReasons] = React.useState<string[] | null>(null)
  const [approveRisk, setApproveRisk] = React.useState(false)
  const base = `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines`

  React.useEffect(() => {
    if (!open) return
    const controller = new AbortController()
    setDraft(null)
    setLoadError(null)
    setCheck(null)
    setRefusal(null)
    setRiskReasons(null)
    setApproveRisk(false)
    void (async () => {
      try {
        const loaded = await loadRoutineDraft(workspaceId, routine.slug, controller.signal)
        if (controller.signal.aborted) return
        if (!loaded.id) throw new Error("There is no saved draft to publish.")
        setDraft(loaded)
        const body: Record<string, unknown> = { definition: loaded.document.definition, sample_inputs: {} }
        const crew = loaded.document.author_crew_id ?? routine.author_crew_id
        if (crew) body.author_crew_id = crew
        const res = await apiFetch(`${base}/test_run`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
          signal: controller.signal,
        })
        const data = (await res.json().catch(() => ({}))) as { status?: string; passed?: boolean; error?: string; save_token?: string }
        if (controller.signal.aborted) return
        const passed = res.ok && (data.status === "DRY_RUN_OK" || data.status === "COMPLETED" || (data.status === undefined && data.passed !== false))
        setCheck({
          passed,
          detail: passed ? "" : data.error || `The check did not pass (${res.status}).`,
          token: passed && data.save_token ? data.save_token : null,
        })
      } catch (e) {
        if (controller.signal.aborted) return
        setLoadError(e instanceof Error ? e.message : String(e))
      }
    })()
    return () => controller.abort()
  }, [open, workspaceId, routine.slug, routine.author_crew_id, base])

  const published = isRecord(routine.definition) ? routine.definition : {}
  const draftDefinition = draft && isRecord(draft.document.definition) ? draft.document.definition : null
  const summary = React.useMemo(
    () => (draftDefinition ? publicationSummary(routine.head_version ? published : {}, draftDefinition) : null),
    [draftDefinition, published, routine.head_version],
  )
  const impact = scheduleImpact(schedules as ScheduleRow[], routine)
  const next = (routine.head_version ?? 0) + 1
  const revision = draft?.revision ?? routine.draft?.revision
  const author = draftAuthorLabel(draft?.updated_by ?? routine.draft?.updated_by, session?.user?.id)
  const savedAt = draft?.updated_at ?? routine.draft?.updated_at

  const publish = async () => {
    if (!draft || !check?.passed || busy) return
    setBusy("publish")
    setRefusal(null)
    try {
      const res = await apiFetch(`${base}/${encodeURIComponent(draft.slug)}/publish`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ id: draft.id, revision: draft.revision, save_token: check.token, approve_risk: approveRisk }),
      })
      const data = (await res.json().catch(() => ({}))) as { error?: string; slug?: string; risk_reasons?: string[]; schedule_conflict?: { name?: string; reason?: string } }
      if (res.status === 409 && Array.isArray(data.risk_reasons) && data.risk_reasons.length) {
        setRiskReasons(data.risk_reasons)
        setRefusal(null)
        return
      }
      if (!res.ok) {
        const conflict = data.schedule_conflict
        throw new Error(
          conflict
            ? `A plan blocks this publish: ${conflict.name ?? "a schedule"}${conflict.reason ? ` — ${conflict.reason}` : ""}. Fix its saved answers in Plan, then publish again. The draft is still saved.`
            : data.error || "Publication failed. The draft is still saved.",
        )
      }
      if (!data.slug) throw new Error("Publication could not be confirmed. Reload the routine before retrying.")
      toast.success(`Published v${next}`, {
        description: impact.latest
          ? `Run and ${impact.latest} ${impact.latest === 1 ? "schedule" : "schedules"} now use it.`
          : "Run now uses it.",
      })
      onPublished(data.slug)
      onOpenChange(false)
    } catch (e) {
      setRefusal(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(null)
    }
  }

  const discard = async () => {
    if (!draft || busy) return
    if (!window.confirm(`Discard draft r${draft.revision}? v${routine.head_version ?? 0} stays as it is.`)) return
    setBusy("discard")
    setRefusal(null)
    try {
      await discardRoutineDraft(workspaceId, draft)
      toast.success(`Draft discarded · v${routine.head_version ?? 0} unchanged`)
      onDiscarded()
      onOpenChange(false)
    } catch (e) {
      setRefusal(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(null)
    }
  }

  const canPublish = !!draft && !!check?.passed && !busy && (!riskReasons || approveRisk)

  return (
    <CreateSurface open={open} onOpenChange={onOpenChange} size="md" onSubmit={() => void publish()} ariaLabel="Publish the draft">
      <CreateSurfaceHeader
        concept="routines"
        context={`Routines › ${routine.name || routine.slug}`}
        title={`Publish draft r${revision ?? "?"} as v${next}`}
        description={savedAt ? `Saved ${relTime(savedAt)} by ${author}` : undefined}
        onClose={() => onOpenChange(false)}
      />
      <CreateSurfaceBody className="flex flex-col gap-3">
        {loadError && <CreateSurfaceNotice tone="error">{loadError}</CreateSurfaceNotice>}
        {!draft && !loadError && <p role="status" className="text-xs text-muted-foreground">Loading the draft…</p>}
        {summary && (
          <DetailCard title={routine.head_version ? `What changes vs v${routine.head_version}` : "What this first version contains"} data-testid="publish-changes">
            <dl className="grid grid-cols-[max-content_1fr] gap-x-3 gap-y-1.5 text-xs">
              {summary.rows.map(([label, value]) => (
                <React.Fragment key={label}>
                  <dt className="font-medium">{label}</dt>
                  <dd className={value.startsWith("Unchanged") ? "text-muted-foreground" : "text-foreground"}>{value}</dd>
                </React.Fragment>
              ))}
            </dl>
            {summary.settings.length > 0 && (
              <p className="mt-2 text-[11px] text-muted-foreground">
                Other settings changed: {summary.settings.map((k) => k.replaceAll("_", " ")).join(", ")}.
              </p>
            )}
          </DetailCard>
        )}
        {draft && (
          <DetailCard title="Checked" data-testid="publish-checked">
            <p className="text-xs">
              {check == null ? (
                <span role="status" className="text-muted-foreground">Checking the recipe…</span>
              ) : check.passed ? (
                <>
                  Recipe parses; every step references a known agent, tool and input; saved plan answers still fit the inputs.{" "}
                  <span className="text-muted-foreground">This check runs no agents, scripts or services — it is not a test run.</span>
                </>
              ) : (
                <span role="alert" className="text-destructive">
                  {check.detail} Publish stays off until the draft passes; fix it with the CLI or ask the lead.
                </span>
              )}
            </p>
          </DetailCard>
        )}
        {draft && (
          <DetailCard title="After publishing" data-testid="publish-after">
            <ul className="space-y-1 text-xs">
              <li>
                • <b className="font-medium">Run</b>
                {impact.latest > 0 && (
                  <>
                    {" "}and <b className="font-medium">{impact.latest} {impact.latest === 1 ? "schedule" : "schedules"}</b> following the latest version
                  </>
                )}{" "}
                use v{next} from the next start.
              </li>
              {impact.pinned > 0 && (
                <li>
                  • {impact.pinned} pinned {impact.pinned === 1 ? "schedule stays" : "schedules stay"} on {impact.pinned === 1 ? "its" : "their"} version.
                </li>
              )}
              <li>• Pinned one-time starts keep the version they were scheduled with.</li>
              <li>
                • {activeRuns > 0 ? `${activeRuns} running or waiting ${activeRuns === 1 ? "run keeps" : "runs keep"}` : "Running or waiting runs keep"} v{routine.head_version ?? 0}; nothing in progress changes.
              </li>
              {routine.head_version ? <li>• v{routine.head_version} stays in Versions and can be restored as a draft.</li> : null}
            </ul>
          </DetailCard>
        )}
        {riskReasons && (
          <CreateSurfaceNotice tone="warn">
            <div className="space-y-1.5">
              <p>
                This draft changes what the routine can reach: {riskReasons.join(", ")}. A manager has to approve that before it goes live.
              </p>
              <label className="flex items-center gap-2 text-xs font-medium">
                <input type="checkbox" checked={approveRisk} onChange={(e) => setApproveRisk(e.target.checked)} />
                Approve capability changes
              </label>
            </div>
          </CreateSurfaceNotice>
        )}
        {draftDefinition && (
          <details className="rounded-lg border border-hairline px-3 py-2 text-xs">
            <summary className="cursor-pointer text-muted-foreground">Technical details · full draft definition</summary>
            <pre className="mt-2 max-h-64 overflow-auto whitespace-pre-wrap break-all text-[11px] text-muted-foreground">
              {JSON.stringify(draftDefinition, null, 2)}
            </pre>
          </details>
        )}
      </CreateSurfaceBody>
      <CreateSurfaceRefusal message={refusal} onDismiss={() => setRefusal(null)} />
      <CreateSurfaceFooter
        hint="Nothing runs on publish"
        onCancel={() => onOpenChange(false)}
        cancelLabel="Not now"
        guardCancel={false}
        secondary={
          <CreateSurfaceSecondaryAction onClick={() => void discard()} disabled={!draft || !!busy}>
            {busy === "discard" ? "Discarding…" : "Discard draft"}
          </CreateSurfaceSecondaryAction>
        }
        primaryLabel={busy === "publish" ? "Publishing…" : `Publish v${next}`}
        primaryIcon={Upload}
        onPrimary={() => void publish()}
        primaryDisabled={!canPublish}
        busy={busy === "publish"}
      />
    </CreateSurface>
  )
}
