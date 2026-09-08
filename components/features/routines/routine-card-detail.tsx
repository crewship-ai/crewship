"use client"

import { Button } from "@/components/ui/button"

// Read the saved recipe here; author and edit it in the shared routine builder.

import * as React from "react"
import Link from "next/link"
import { motion, useReducedMotion } from "motion/react"
import {
  ArrowUpRight,
  Bot,
  CalendarClock,
  CheckCircle2,
  ChevronRight,
  Clock,
  Globe,
  KeyRound,
  PenSquare,
  Puzzle,
  ShieldAlert,
  Webhook,
  XCircle,
  Zap,
} from "lucide-react"

import { routineRunLabel } from "./routines-workspace"
import { cn } from "@/lib/utils"
import { relTime, formatDurationDecimal } from "@/lib/time"
import { Appear, DetailCard, EntityChip, Pill } from "@/components/ui/detail"
import { usePipelineRunRecords, type PipelineRunRecord } from "@/hooks/use-pipeline-run-records"
import { usePipelineSchedules } from "@/hooks/use-pipeline-schedules"
import { useAutomations } from "@/hooks/use-automations"
import { automationsForRoutine, crewshipActionsInDefinition } from "@/lib/automations"
import { runProvenance } from "@/lib/run-provenance"
import { AutomationList } from "@/components/features/automations/automation-list"
import { integrationLabel } from "@/lib/integration-labels"
import { credentialTypeLabel } from "@/lib/credential-labels"
import { RoutineIdentityHeader } from "./routine-identity-header"
import { RoutineNavigation, ROUTINE_VIEWS } from "./routine-navigation"
import { useUrlSelection } from "@/hooks/use-issue-detail"
import { brandIconForType, BrandGlyph } from "./brand-icons"
import { RoutineStepDefinition } from "./routine-step-definition"
import { RoutineDefinitionCanvas } from "./routine-definition-canvas"
import { RoutineBudgetCard } from "./routine-budget-card"
import { RoutineCreateDialog } from "./routine-create-dialog"
import { useAbilities } from "@/hooks/use-abilities"
import { roleAtLeast } from "@/lib/routine-governance"
import { RoutineSchedulesTab } from "./routine-schedules-tab"
import { RoutineWebhooksTab } from "./routine-webhooks-tab"
import { RoutineVersionsTab } from "./routine-versions-tab"
import { RoutineRunsTab } from "./routine-runs-tab"
import { RoutineReachCard } from "./routine-reach-card"
import type { RoutineDetail } from "./routines-detail-panel"

interface Props {
  routine: RoutineDetail
  workspaceId: string
  onChanged: () => void
  /**
   * Run / Dry run / Enable / Disable / Cancel, rendered top-right of the
   * identity card.
   *
   * Passed in rather than rebuilt here: the panel owns the handlers, the
   * RBAC guards and the busy states, and a second copy of that wiring is
   * a second thing to keep correct.
   */
  actions?: React.ReactNode
  /**
   * Lifecycle + run-status pills, rendered first in the identity row.
   *
   * Computed by the panel because the logic is not "read
   * last_invocation_status": a live approval gate wins over the
   * persisted value — the run reads as running in the DB while parked,
   * but the human is the bottleneck — and the colours route through the
   * shared palette so the pill matches Inbox, Issues and Activity.
   */
  statusPills?: React.ReactNode
  /** Bumped when something outside asks for the code editor. */
  editRequest?: number
}


/**
 * Which kind of trigger the Triggers card is showing.
 *
 * Automations join cron and webhooks here rather than taking a card of their
 * own, because they are the same question — what starts this routine — and the
 * card's own design note argues that case: one card behind a switch beats two
 * half-empty ones. The switch only offers `automations` when at least one rule
 * targets this routine; a permanent third option that is empty on nearly every
 * routine is the scaffolding this page was built to remove.
 */
type TriggerKind = "schedules" | "webhooks" | "automations"

const TRIGGER_KINDS: readonly TriggerKind[] = ["schedules", "webhooks", "automations"]

const TRIGGER_TITLE: Record<TriggerKind, string> = {
  schedules: "Triggers",
  webhooks: "Webhooks",
  automations: "Automations",
}

const TRIGGER_ICON: Record<TriggerKind, React.ComponentType<{ className?: string }>> = {
  schedules: CalendarClock,
  webhooks: Webhook,
  automations: Zap,
}

// The same curve the detail kit's Appear uses, so a pane opening and a
// card arriving are visibly the same product rather than two people's
// idea of a transition.
const PANE_EASE = { duration: 0.34, ease: [0.22, 1, 0.36, 1] as const }

export function RoutineCardDetail({
  routine,
  workspaceId,
  onChanged,
  actions,
  statusPills,
  editRequest = 0,
}: Props) {
  const [selectedView, setView] = useUrlSelection("view")
  const view = ROUTINE_VIEWS.find(v => v === selectedView) ?? "definition"
  const reduceMotion = useReducedMotion()
  const [editing, setEditing] = React.useState(false)
  const { role } = useAbilities()
  const canEdit = roleAtLeast(role, "MANAGER")
  const [draft, setDraft] = React.useState<{ definition: Record<string, unknown>; version: number } | null>(null)
  React.useEffect(() => {
    if (editRequest > 0 && canEdit) setEditing(true)
  }, [editRequest, canEdit])
  React.useEffect(() => {
    if ((selectedView === "edit" || selectedView === "settings") && canEdit) { setEditing(true); setView("definition") }
  }, [selectedView, canEdit, setView])
  const [selected, setSelected] = React.useState<string | null>(null)
  // Separate from `selected`: selection is a persistent choice, focus a
  // one-shot "bring this into view". Merged, a re-render could yank the
  // viewport back after the reader had panned away from it.
  const [focus, setFocus] = React.useState<string | null>(null)
  const handleSelect = React.useCallback((id: string | null) => {
    setSelected(id)
    setFocus(null)
  }, [])
  const [triggerKind, setTriggerKind] = React.useState<TriggerKind>("schedules")
  const [manageTriggers, setManageTriggers] = React.useState(false)
  // Cancelling a specific run lives in RoutineRunsTab, which has the
  // per-row buttons and the RBAC handling. Dropping the tab must not
  // drop the capability, so Manage mounts the real thing rather than a
  // reimplementation of it.
  const [manageRuns, setManageRuns] = React.useState(false)

  const { records } = usePipelineRunRecords(workspaceId, routine.slug)
  const { schedules } = usePipelineSchedules(workspaceId)
  const { automations } = useAutomations(workspaceId)

  const mine = React.useMemo(
    () => schedules.filter((s) => s.target_pipeline_id === routine.id),
    [schedules, routine.id],
  )
  // The rules that can start THIS routine. A routine a rule can fire, on a
  // page listing only cron schedules, reads as manual-or-cron — and the reader
  // is right to conclude that from what the page shows them.
  const myAutomations = React.useMemo(
    () => automationsForRoutine(automations, routine.slug),
    [automations, routine.slug],
  )
  // A `crewship` step is the line between a routine that reads the board and
  // one that writes to it (internal/pipeline/crewship_step.go).
  const crewshipActions = React.useMemo(
    () => crewshipActionsInDefinition(routine.definition),
    [routine.definition],
  )
  // §13.2 "If it overlaps" (F18/B9) — concurrency_key/max_concurrent live on
  // the DSL, not on any one schedule, so the reliability editor reads them
  // straight from the parsed definition rather than a schedule field.
  const concurrencyKey = React.useMemo(() => {
    const raw = (routine.definition as { concurrency_key?: unknown })?.concurrency_key
    return typeof raw === "string" && raw ? raw : undefined
  }, [routine.definition])
  const maxConcurrent = React.useMemo(() => {
    const raw = (routine.definition as { max_concurrent?: unknown })?.max_concurrent
    return typeof raw === "number" && raw > 0 ? raw : undefined
  }, [routine.definition])
  // Losing the automations view when the last rule is deleted must not strand
  // the card on an empty pane.
  React.useEffect(() => {
    if (triggerKind === "automations" && myAutomations.length === 0) setTriggerKind("schedules")
  }, [triggerKind, myAutomations.length])
  const steps = React.useMemo(() => {
    const raw = (routine.definition as { steps?: unknown })?.steps
    return Array.isArray(raw) ? (raw as { type?: string }[]) : []
  }, [routine.definition])

  const lastRun = records[0] ?? null

  return (
    <div className="flex flex-col gap-4 p-4">
      {/* Identity, as a card that scrolls with the page rather than a
          fixed header band. The name is the first thing on the page —
          it used to sit under a row of status chrome. */}
      <RoutineIdentityHeader routine={routine} workspaceId={workspaceId} onChanged={onChanged} onEdit={() => setEditing(true)} actions={actions}>
        {statusPills}
        <Pill tone="default">{mine.some(s => s.enabled) ? "scheduled" : "manual / event"}</Pill>
        {myAutomations.length > 0 && <span data-testid="routine-automations-pill"><Pill tone="default"><Zap className="h-3 w-3" />{myAutomations.length} automation{myAutomations.length === 1 ? "" : "s"}</Pill></span>}
        <Pill tone="default">{steps.length} {steps.length === 1 ? "step" : "steps"}</Pill>
        {routine.ephemeral && <Pill tone="warn">ephemeral</Pill>}
      </RoutineIdentityHeader>
      <RoutineNavigation slug={routine.slug} view={view} onChange={setView} />
      {view === "versions" && <RoutineVersionsTab workspaceId={workspaceId} slug={routine.slug} onRolledBack={onChanged} onPrepareDraft={(definition, version) => { setDraft({ definition, version }); setEditing(true); setView("definition") }} />}
      {view === "plan" && <div className="space-y-4"><RoutineSchedulesTab workspaceId={workspaceId} pipelineId={routine.id} slug={routine.slug} concurrencyKey={concurrencyKey} maxConcurrent={maxConcurrent} /><RoutineWebhooksTab workspaceId={workspaceId} pipelineId={routine.id} slug={routine.slug} /></div>}
      {editing && <RoutineCreateDialog workspaceId={workspaceId} routine={routine} initialDraft={draft?.definition} open={editing} onClose={() => { setEditing(false); setDraft(null) }} onCreated={() => { setDraft(null); onChanged() }} advancedDetails={<><DetailCard title="Connected workspace"><div className="flex flex-wrap gap-4 text-xs"><Link className="text-primary" href="/credentials">Credentials ↗</Link><Link className="text-primary" href="/integrations">Integrations ↗</Link><Link className="text-primary" href={`/activity?pipeline=${encodeURIComponent(routine.slug)}`}>Activity ↗</Link></div><p className="mt-3 text-xs text-muted-foreground">The access checks are applied when a run starts. Editing these connections does not rewrite historical runs.</p></DetailCard><AccessCard routine={routine} crewshipActions={crewshipActions} /><RoutineReachCard workspaceId={workspaceId} agentSlugs={routine.manifest?.agents ?? []} /><RoutineBudgetCard workspaceId={workspaceId} slug={routine.slug} /><DetailCard title="Technical metadata"><Metadata routine={routine} steps={steps.length} /></DetailCard></>} />}

      {view === "definition" && draft && <DetailCard><p className="text-sm">Unsaved draft from version {draft.version}. Review the editor and save to create a new version. The graph still shows the currently saved recipe.</p><button className="mt-2 text-xs text-primary" onClick={() => { setDraft(null); setEditing(false) }}>Discard draft</button></DetailCard>}
      {view === "definition" &&
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3 2xl:grid-cols-4">
        <Appear order={2} className="xl:col-span-2 2xl:col-span-3">
          <DetailCard
            title="Definition"
            subtitle={`${steps.length} ${steps.length === 1 ? "step" : "steps"}`}
            bare
          >
            <div className="flex h-[56vh] min-h-[380px] flex-col md:flex-row">
              <motion.div
                layout={reduceMotion ? false : "position"}
                transition={PANE_EASE}
                className="relative min-h-[240px] w-full min-w-0 flex-1 md:min-w-[380px]"
              >
                <RoutineDefinitionCanvas
                  definition={routine.definition}
                  slug={routine.slug}
                  name={routine.name}
                  selectedStepId={selected}
                  onStepSelect={handleSelect}
                  focusStepId={focus}
                />
                {/* On the canvas, not in the card header: the button that
                    opens an editor for this graph belongs next to the
                    graph, not a title-bar away from it. */}
                {canEdit && <button type="button" onClick={() => setEditing(true)} className="absolute right-3 top-3 z-10 inline-flex items-center gap-1.5 rounded-lg border border-border/60 bg-card/85 px-2.5 py-1.5 text-[11px] text-muted-foreground hover:text-foreground"><PenSquare className="h-3.5 w-3.5" />Edit recipe</button>}
              </motion.div>
              {selected && !editing && <aside className="h-[45%] max-h-[45%] w-full shrink-0 overflow-auto border-t p-4 md:h-auto md:max-h-none md:w-[320px] md:border-l md:border-t-0"><button onClick={() => setSelected(null)} className="mb-3 text-xs text-muted-foreground">Close step detail</button><RoutineStepDefinition step={(routine.definition.steps as Record<string, unknown>[] | undefined)?.find(s => s.id === selected)} /></aside>}

            </div>
          </DetailCard>
        </Appear>

        <div className="flex flex-col gap-4">
          <Appear order={3}>
            <LastRunCard
              status={lastRun?.outcome === "FAILED" ? "failed" : lastRun?.outcome === "NEEDS_HUMAN" ? "needs attention" : lastRun?.status ?? routine.last_invocation_status}
              at={lastRun?.started_at ?? routine.last_invoked_at}
              runId={lastRun?.id}
              durationMs={lastRun?.duration_ms}
              slug={routine.slug}
            />
          </Appear>

          <Appear order={4}>
            <DetailCard
              title={TRIGGER_TITLE[triggerKind]}
              subtitle={triggerKind === "schedules" ? String(mine.length) : triggerKind === "automations" ? String(myAutomations.length) : undefined}
              icon={TRIGGER_ICON[triggerKind]}
              tone="purple"
              action={triggerKind !== "automations" && <button type="button" onClick={() => setManageTriggers(v => !v)} className="rounded-md border border-border/60 px-1.5 py-1 text-[10px] font-medium text-muted-foreground hover:text-foreground">{manageTriggers ? "Done" : "Manage"}</button>}
              footer={(
                  // Was a link that toggled between two kinds. A third kind
                  // makes a toggle unreadable — you cannot see the option you
                  // are not on — so the same switch the card already uses in
                  // its header does the job here. `automations` appears only
                  // when a rule actually targets this routine.
                  <div className="flex items-center gap-0.5 rounded-md border border-border/60 p-0.5">
                    {TRIGGER_KINDS.filter(
                      (k) => k !== "automations" || myAutomations.length > 0,
                    ).map((k) => (
                      <button
                        key={k}
                        type="button"
                        onClick={() => {
                          setTriggerKind(k)
                          setManageTriggers(false)
                        }}
                        aria-pressed={triggerKind === k}
                        className={cn(
                          "rounded px-1.5 py-0.5 text-[10px] font-medium capitalize transition-colors",
                          triggerKind === k
                            ? "bg-primary/15 text-primary"
                            : "text-muted-foreground hover:text-foreground",
                        )}
                      >
                        {k}
                      </button>
                    ))}
                  </div>
                )
              }
            >
              {triggerKind === "automations" ? (
                <div data-testid="routine-automations" className="space-y-2.5">
                  <p className="text-[12px] text-muted-foreground">
                    <span data-testid="routine-automations-count" className="text-foreground/85">
                      {myAutomations.length}
                    </span>{" "}
                    {myAutomations.length === 1 ? "automation" : "automations"} can start this
                    routine.
                  </p>
                  <AutomationList automations={myAutomations} />
                </div>
              ) : manageTriggers ? (
                triggerKind === "webhooks" ? (
                  <RoutineWebhooksTab
                    workspaceId={workspaceId}
                    pipelineId={routine.id}
                    slug={routine.slug}
                  />
                ) : (
                  <RoutineSchedulesTab
                    workspaceId={workspaceId}
                    pipelineId={routine.id}
                    slug={routine.slug}
                    concurrencyKey={concurrencyKey}
                    maxConcurrent={maxConcurrent}
                  />
                )
              ) : triggerKind === "webhooks" ? (
                <p className="text-[12px] text-muted-foreground">
                  Inbound HTTP triggers. Press Manage to add or rotate one.
                </p>
              ) : (
                <ScheduleList schedules={mine} />
              )}
            </DetailCard>
          </Appear>

          <Appear order={5}>
            <AccessCard routine={routine} crewshipActions={crewshipActions} />
          </Appear>

          {(routine.manifest?.agents?.length ?? 0) > 0 && (
            <Appear order={6}>
              <RoutineReachCard
                workspaceId={workspaceId}
                agentSlugs={routine.manifest?.agents ?? []}
              />
            </Appear>
          )}

          {/* A monthly cap belongs to the routine that carries it.
              It used to live only in a workspace-wide roll-up on the
              overview, which put a third card about money on one row —
              and before that on a tab nobody opened. Here it sits next
              to what the routine costs. */}
        </div>
      </div>

      }
      {view === "history" && <Appear order={9}>
        <RunsCard
          slug={routine.slug}
          workspaceId={workspaceId}
          records={records}
          manage={manageRuns}
          onManageChange={setManageRuns}
        />
      </Appear>}
    </div>
  )
}

/* ------------------------------------------------------------------ *
 *  Pieces                                                             *
 * ------------------------------------------------------------------ */

function toneOf(status?: string): "success" | "destructive" | "default" {
  const s = status?.toLowerCase()
  if (s === "completed" || s === "succeeded" || s === "success") return "success"
  if (s === "failed" || s === "error") return "destructive"
  return "default"
}

/**
 * Last run, with a tinted header.
 *
 * The one place on the page where colour carries meaning rather than
 * decoration: it says how this ended before a word is read, and does it
 * with a 6%-opacity gradient rather than shouting.
 */
function LastRunCard({
  status,
  at,
  runId,
  durationMs,
  slug,
}: {
  status?: string
  at?: string
  runId?: string
  durationMs?: number
  slug: string
}) {
  const tone = toneOf(status)
  const ok = tone === "success"
  const bad = tone === "destructive"
  const Icon = ok ? CheckCircle2 : bad ? XCircle : Clock

  if (!at) {
    return (
      <DetailCard title="Last run">
        <p className="text-[12px] text-muted-foreground">
          This routine hasn&apos;t been invoked yet.
        </p>
      </DetailCard>
    )
  }

  return (
    <div className="overflow-hidden rounded-xl border border-border/60 bg-card">
      <div
        className={cn(
          "flex items-center gap-3 border-b border-border/40 px-4 py-3",
          ok && "bg-gradient-to-r from-success/[0.06] to-transparent",
          bad && "bg-gradient-to-r from-destructive/[0.06] to-transparent",
        )}
      >
        <div
          className={cn(
            "flex h-8 w-8 shrink-0 items-center justify-center rounded-full",
            ok && "bg-success/20 text-success",
            bad && "bg-destructive/20 text-destructive",
            !ok && !bad && "bg-primary/20 text-primary",
          )}
        >
          <Icon className="h-4 w-4" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="text-[13px] font-medium capitalize">
            Last run · {status ?? "unknown"}
          </div>
          {runId && (
            <div className="truncate font-mono text-[10px] text-muted-foreground">{runId}</div>
          )}
        </div>
      </div>
      <div className="space-y-2 px-4 py-3">
        <dl className="grid grid-cols-3 gap-2 text-[11px]">
          <Fact label="started" value={relTime(at)} />
          <Fact
            label="duration"
            value={durationMs && durationMs > 0 ? formatDurationDecimal(durationMs) : "—"}
          />
        </dl>
        <Link
          href={activityHref(slug, runId)}
          className="inline-flex items-center gap-1 text-[11px] text-primary hover:underline"
        >
          Open run
          <ArrowUpRight className="h-3 w-3" />
        </Link>
      </div>
    </div>
  )
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-[10px] uppercase tracking-wider text-muted-foreground-soft">{label}</dt>
      <dd className="tabular-nums text-foreground/85">{value}</dd>
    </div>
  )
}

/**
 * Where a run row lands.
 *
 * Filtered to this routine AND selecting the run — arriving at an
 * unfiltered rail of every run in the workspace makes the reader rebuild
 * the context they came with.
 */
function activityHref(slug: string, runId?: string): string {
  const params = new URLSearchParams({ slug })
  if (runId) params.set("run", runId)
  return `/routines?${params.toString()}`
}

function ScheduleList({
  schedules,
}: {
  schedules: { id: string; name: string; cron_expr: string; timezone: string; enabled: boolean; next_run_at?: string }[]
}) {
  if (schedules.length === 0) {
    return (
      <p className="text-[12px] text-muted-foreground">
        Run manually or choose a date and repetition under Manage.
      </p>
    )
  }
  return (
    <ul className="space-y-2.5 text-[12px]">
      {schedules.map((s) => (
        <li key={s.id} className="flex items-start gap-2">
          <CalendarClock
            className={cn(
              "mt-0.5 h-3.5 w-3.5 shrink-0",
              s.enabled ? "text-muted-foreground" : "text-muted-foreground-soft",
            )}
          />
          <div className="min-w-0 flex-1">
            <div className={cn("truncate", s.enabled ? "text-foreground/90" : "text-muted-foreground")}>
              {s.name}
            </div>
            <div className="flex flex-wrap items-baseline gap-x-2 text-[10px] text-muted-foreground">
              <span className="font-mono">{s.cron_expr}</span>
              <span aria-hidden>·</span>
              <span>{s.timezone}</span>
              {s.enabled && s.next_run_at && (
                <>
                  <span aria-hidden>·</span>
                  <span className="text-info">next {relTime(s.next_run_at)}</span>
                </>
              )}
              {!s.enabled && (
                <>
                  <span aria-hidden>·</span>
                  <span>paused</span>
                </>
              )}
            </div>
          </div>
        </li>
      ))}
    </ul>
  )
}

/**
 * Everything the routine can reach, as one row of chips.
 *
 * Four cards answering one question — "what could this do if it
 * misbehaved?" — meant nobody read the third. Real brand marks where
 * one exists: a chip reading "Gmail" beside a generic puzzle piece has
 * stopped carrying its own meaning.
 */
function AccessCard({
  routine,
  crewshipActions,
}: {
  routine: RoutineDetail
  /** `crewship` verbs the definition acts with, e.g. ["issue.create"]. */
  crewshipActions: string[]
}) {
  const m = routine.manifest
  const items: { key: string; label: string; icon: React.ComponentType<{ className?: string }>; risk?: boolean }[] = []

  for (const i of m?.integrations ?? routine.integrations_required ?? []) {
    items.push({ key: `i:${i}`, label: integrationLabel(i), icon: Puzzle, risk: true })
  }
  for (const c of m?.credentials ?? []) {
    items.push({ key: `c:${c.type}`, label: credentialTypeLabel(c.type), icon: KeyRound, risk: true })
  }
  for (const a of m?.agents ?? []) {
    items.push({ key: `a:${a}`, label: a, icon: Bot })
  }
  for (const e of m?.egress ?? []) {
    items.push({ key: `e:${e}`, label: e, icon: Globe, risk: true })
  }

  return (
    <DetailCard
      title="Access"
      subtitle="what this can reach"
      icon={ShieldAlert}
      tone="warn"
      footer={items.length > 0 ? "Amber marks reach a reviewer should look at twice." : undefined}
    >
      <div className="space-y-3">
        {items.length === 0 ? (
          <p className="text-[12px] text-muted-foreground">
            Nothing outside Crewship — no integrations, credentials or egress.
          </p>
        ) : (
          <div className="flex flex-wrap gap-1.5">
            {items.map((item) => {
              const brand = brandIconForType(item.label)
              return (
                <EntityChip
                  key={item.key}
                  icon={
                    brand
                      ? () => <BrandGlyph brand={brand} fallback={item.icon} className="h-3 w-3" />
                      : item.icon
                  }
                  label={item.label}
                  tone={item.risk ? "warn" : "default"}
                />
              )
            })}
          </div>
        )}

        {/* Reach that points back at us.

            The card above is about what a routine can touch OUTSIDE Crewship,
            and it answered "nothing" for a routine that files issues and
            reassigns work on the board — which is the reach a reviewer most
            needs to see, not the least. Kept as its own labelled group rather
            than mixed into the chips above, because "writes to your board" and
            "can call Stripe" are different risks and a reviewer sorts them
            differently. Absent entirely when the routine only reads. */}
        {crewshipActions.length > 0 && (
          <div data-testid="routine-crewship-actions" className="space-y-1.5">
            <p className="text-[10px] uppercase tracking-wider text-muted-foreground-soft">
              Writes to Crewship
            </p>
            <div className="flex flex-wrap gap-1.5">
              {crewshipActions.map((action) => (
                <EntityChip key={action} icon={PenSquare} label={action} tone="warn" />
              ))}
            </div>
          </div>
        )}
      </div>
    </DetailCard>
  )
}

/**
 * The flat facts.
 *
 * Low weight on their own, which is why they sit inside the Versions
 * card rather than taking one of their own — but paired with the version
 * history they answer "what changed since the run that worked", which is
 * the question you have precisely when something has broken.
 */
function Metadata({ routine, steps }: { routine: RoutineDetail; steps: number }) {
  const rows: [string, string][] = [
    ["DSL version", routine.dsl_version],
    ["Visibility", routine.workspace_visible ? "workspace" : "private"],
    ["Hash", routine.definition_hash ? `${routine.definition_hash.slice(0, 10)}…` : "—"],
    ["Steps", String(steps)],
    ["Created", routine.created_at ? relTime(routine.created_at) : "—"],
    ["Updated", routine.updated_at ? relTime(routine.updated_at) : "—"],
  ]
  return (
    <dl className="grid grid-cols-2 gap-x-4 gap-y-2.5 text-[11px]">
      {rows.map(([k, v]) => (
        <div key={k}>
          <dt className="text-[10px] uppercase tracking-wider text-muted-foreground-soft">{k}</dt>
          <dd className="mt-0.5 truncate font-mono text-foreground/85">{v}</dd>
        </div>
      ))}
    </dl>
  )
}

function RunsCard({
  slug,
  workspaceId,
  records,
  manage,
  onManageChange,
}: {
  slug: string
  workspaceId: string
  records: PipelineRunRecord[]
  manage: boolean
  onManageChange: (next: boolean) => void
}) {
  return (
    <DetailCard
      title="Runs"
      subtitle={records.length > 0 ? String(records.length) : undefined}
      icon={Clock}
      bare
      action={
        <button
          type="button"
          onClick={() => onManageChange(!manage)}
          className="rounded-md border border-border/60 px-1.5 py-1 text-[10px] font-medium text-muted-foreground transition-colors hover:text-foreground"
        >
          {manage ? "Done" : "Manage"}
        </button>
      }
    >
      {manage ? (
        <div className="px-4 py-3">
          <RoutineRunsTab workspaceId={workspaceId} slug={slug} />
        </div>
      ) : (
        <RunsList key={slug} slug={slug} records={records} workspaceId={workspaceId} />
      )}
    </DetailCard>
  )
}

function RunsList({
  slug,
  workspaceId,
}: {
  slug: string
  records: PipelineRunRecord[]
  workspaceId: string
}) {
  const [pages, setPages] = React.useState<string[]>([])
  const before = pages.at(-1)
  const { records, error, loading, refresh } = usePipelineRunRecords(workspaceId, slug, undefined, before)
  return (
    <>
      {error && <p role="alert" className="p-4 text-sm text-destructive">Run history could not be loaded. <button onClick={refresh}>Retry</button></p>}
      {loading && <p className="p-4 text-sm text-muted-foreground">Loading history…</p>}
      {records.length === 0 ? (
        <p className="px-4 py-3 text-[12px] text-muted-foreground">No runs recorded yet.</p>
      ) : (
        <ul className="divide-y divide-border/40">
          {records.map((r) => {
            const tone = toneOf(r.outcome === "FAILED" ? "failed" : r.outcome === "NEEDS_HUMAN" ? "waiting" : r.status)
            const Icon = tone === "success" ? CheckCircle2 : tone === "destructive" ? XCircle : Clock
            // Not `r.triggered_via`. Every deferred run is stored as
            // "schedule", automations included, so the raw enum reports a cron
            // for a rule-fired run — on the one line whose job is "why did
            // this happen". See lib/run-provenance.ts.
            const prov = runProvenance(r)
            return (
              <li key={r.id}>
                <Link
                  href={activityHref(slug, r.id)}
                  data-testid={`run-row-${r.id}`}
                  className="grid grid-cols-[auto_1fr_auto_auto] items-center gap-3 px-4 py-2.5 transition-colors hover:bg-white/[0.025]"
                >
                  <Icon
                    className={cn(
                      "h-4 w-4 shrink-0",
                      tone === "success" && "text-success",
                      tone === "destructive" && "text-destructive",
                      tone === "default" && "text-muted-foreground",
                    )}
                  />
                  <div className="min-w-0">
                    <div className="truncate font-mono text-[11px] text-foreground/85">{new Date(r.started_at).toLocaleString("en-GB")} · {routineRunLabel(r)}</div>
                    <div className="flex flex-wrap items-baseline gap-x-1.5 text-[10px] uppercase tracking-wide text-muted-foreground">
                      <span>{prov.label}</span>
                      {r.pipeline_version != null && <span>· v{r.pipeline_version}</span>}
                      {prov.source && (
                        <span className="truncate normal-case text-muted-foreground-soft">
                          {prov.source}
                        </span>
                      )}
                      {/* Only on a composed run. A depth-0 badge on every row
                          would be chrome for a fact that is the default. */}
                      {prov.chainDepth !== undefined && (
                        <span
                          data-testid={`run-chain-depth-${r.id}`}
                          title="Composed run: hops from whatever a human did (max 8)"
                          className="rounded border border-border/60 px-1 normal-case tabular-nums text-muted-foreground"
                        >
                          chain {prov.chainDepth}
                        </span>
                      )}
                    </div>
                  </div>
                  <div className="text-right text-[11px] tabular-nums text-muted-foreground">
                    {r.started_at ? relTime(r.started_at) : "—"}
                  </div>
                  <ChevronRight className="h-3.5 w-3.5 text-muted-foreground-soft" />
                </Link>
              </li>
            )
          })}
        </ul>
      )}
      <div className="flex gap-2 border-t border-border/60 px-4 py-2">
        {pages.length > 0 && <Button variant="outline" size="sm" disabled={loading} onClick={() => setPages(pages.slice(0,-1))}>Newer runs</Button>}
        {records.length === 50 && <Button variant="outline" size="sm" disabled={loading} onClick={() => setPages([...pages, records[records.length-1].id])}>Older runs</Button>}
      </div>
    </>
  )
}

// Referenced for the Pill import so the identity chrome in the panel
// above can keep using the same tone vocabulary.
void Pill
