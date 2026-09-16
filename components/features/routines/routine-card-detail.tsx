"use client"

import { formatRoutineTime } from "@/lib/routine-time"

import { Button } from "@/components/ui/button"

// The routine page: identity, a live-run banner, then Overview · History ·
// Versions · Plan. Authoring is not here — Edit changes what the DSL lets a
// person change without files, Publish reviews a draft, and the recipe itself
// is written with the CLI or by the lead (docs/ux/routines-operator-console).

import * as React from "react"
import Link from "next/link"
import {
  ArrowUpRight,
  CheckCircle2,
  ChevronRight,
  Clock,
  Eye,
  Globe,
  KeyRound,
  PenSquare,
  Puzzle,
  ShieldAlert,
  XCircle,
  Zap,
} from "lucide-react"

import { describeCron } from "@/lib/cron-describe"
import { routineRunPresentation } from "@/lib/routine-run-presentation"
import { routineRunLabel } from "./routines-workspace"
import { cn } from "@/lib/utils"
import { relTime, formatDurationDecimal } from "@/lib/time"
import { Appear, DetailCard, EntityChip, Pill } from "@/components/ui/detail"
import {
  usePipelineRunRecords,
  type PipelineRunRecord,
} from "@/hooks/use-pipeline-run-records"
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
import { RoutineBehaviorSummary } from "./routine-behavior"
import { RoutineStepSpine } from "./routine-step-spine"
import { routineInputSpecs } from "@/lib/routine-inputs"
import { routineEffects } from "@/lib/routine-effects"
import { isRecord, asString } from "@/lib/routine-step-describe"
import { describeTimeout, foreachBody, nameLookup, stepChips } from "@/lib/routine-steps-layout"
import { routineFilesFromDefinition } from "@/lib/routine-files"
import { RoutineDefinitionCanvas } from "./routine-definition-canvas"
import { RoutineBudgetCard } from "./routine-budget-card"
import { RoutineFilesCard } from "./routine-files-card"
import { RoutineEditDialog } from "./routine-edit-dialog"
import { RoutinePublishDialog } from "./routine-publish-dialog"
import { isActiveRunStatus } from "@/hooks/use-pipeline-run-records"
import { useAbilities } from "@/hooks/use-abilities"
import { roleAtLeast } from "@/lib/routine-governance"
import { RoutineSchedulesTab } from "./routine-schedules-tab"
import { RoutineWebhooksTab } from "./routine-webhooks-tab"
import { RoutineComparison } from "./routine-comparison"
import { RoutineVersionsTab } from "./routine-versions-tab"
import { RoutineRunsTab } from "./routine-runs-tab"
import { RoutineReachCard } from "./routine-reach-card"
import type { RoutineDetail } from "./routines-detail-panel"

interface Props {
  routine: RoutineDetail
  workspaceId: string
  onChanged: () => void
  /**
   * Run, first in the identity card's action row, and the ⋯ menu, last.
   *
   * Passed in rather than rebuilt here: the panel owns the handlers, the
   * RBAC guards and the busy states, and a second copy of that wiring is
   * a second thing to keep correct.
   */
  primary?: React.ReactNode
  menu?: React.ReactNode
  /** The live-run banner under the header; the panel owns the run records. */
  liveRuns?: React.ReactNode
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
export function RoutineCardDetail({
  routine,
  workspaceId,
  onChanged,
  primary,
  menu,
  liveRuns,
  statusPills,
}: Props) {
  const [selectedView, setView] = useUrlSelection("view")
  const view = ROUTINE_VIEWS.find((v) => v === selectedView) ?? "definition"
  const { role } = useAbilities()
  const canEdit = roleAtLeast(role, "MANAGER")
  // `?view=edit` IS the editing state (#2519): reload, Back and Forward all
  // land where the address says, and leaving the routine clears it. The Edit
  // dialog opens over the page; `?view=publish` opens the publish review.
  const editing = (selectedView === "edit" || selectedView === "settings") && canEdit
  const publishing = selectedView === "publish" && canEdit && !!routine.draft
  const openEditor = React.useCallback(() => setView("edit"), [setView])
  const openPublish = React.useCallback(() => setView("publish"), [setView])
  const editButtonRef = React.useRef<HTMLButtonElement>(null)
  const wasEditing = React.useRef(editing)
  React.useEffect(() => {
    if (!editing && wasEditing.current) {
      editButtonRef.current?.focus()
    }
    wasEditing.current = editing
  }, [editing])
  const closeDialog = React.useCallback(() => {
    setView(null, { replace: true })
  }, [setView])
  const [selected, setSelected] = React.useState<string | null>(null)
  // Separate from `selected`: selection is a persistent choice, focus a
  // one-shot "bring this into view". Merged, a re-render could yank the
  // viewport back after the reader had panned away from it.
  const [focus, setFocus] = React.useState<string | null>(null)
  const handleSelect = React.useCallback((id: string | null) => {
    setSelected(id)
    setFocus(null)
  }, [])
  // Cancelling a specific run lives in RoutineRunsTab, which has the
  // per-row buttons and the RBAC handling. Dropping the tab must not
  // drop the capability, so Manage mounts the real thing rather than a
  // reimplementation of it.
  const [manageRuns, setManageRuns] = React.useState(false)

  const { records } = usePipelineRunRecords(workspaceId, routine.slug)
  const { schedules } = usePipelineSchedules(workspaceId)
  const { automations } = useAutomations(workspaceId)

  const mine = React.useMemo(
    () =>
      schedules.filter(
        (s) =>
          s.target_pipeline_id === routine.id || s.target_pipeline_slug === routine.slug,
      ),
    [schedules, routine.id, routine.slug],
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
  const steps = React.useMemo(() => {
    const raw = (routine.definition as { steps?: unknown })?.steps
    return Array.isArray(raw) ? (raw as { type?: string }[]) : []
  }, [routine.definition])

  const lastRun = records[0] ?? null
  const activeRuns = records.filter((r) => isActiveRunStatus(r.status)).length
  // The graph pane is as tall as its graph needs. Two nodes in a 56vh box
  // read as a broken page, not as a small recipe.
  const mapHeight = Math.min(560, Math.max(300, steps.length * 78))
  // Files come from the detail's projection; an older server sends none, and
  // then the recipe's own script paths stand in with presence unknown.
  const files = React.useMemo(
    () => routine.files ?? routineFilesFromDefinition(routine.definition),
    [routine.files, routine.definition],
  )
  const nameOf = React.useMemo(
    () => nameLookup(isRecord(routine.definition) ? routine.definition : {}),
    [routine.definition],
  )

  return (
    <div className="flex flex-col gap-4 p-4">
      {/* Identity, as a card that scrolls with the page rather than a
          fixed header band. The name is the first thing on the page —
          it used to sit under a row of status chrome. */}
      <RoutineIdentityHeader
        routine={routine}
        workspaceId={workspaceId}
        onChanged={onChanged}
        onEdit={canEdit ? openEditor : undefined}
        onPublish={canEdit ? openPublish : undefined}
        editButtonRef={editButtonRef}
        primary={primary}
        actions={menu}
        runUses
      >
        {routine.ephemeral && <Pill tone="warn">ephemeral</Pill>}
      </RoutineIdentityHeader>
      {liveRuns}
      <RoutineNavigation slug={routine.slug} view={view} onChange={setView} />
      {view === "versions" && (
        <div className="space-y-5">
          <RoutineVersionsTab
            workspaceId={workspaceId}
            slug={routine.slug}
            draft={routine.draft}
            routine={routine}
            onPublish={canEdit ? openPublish : undefined}
            onChanged={onChanged}
          />
          <details className="rounded-xl border border-border/60 bg-card px-4 py-3 text-xs">
            <summary className="cursor-pointer text-muted-foreground">
              Compare two versions on the same data · live runs, real costs
            </summary>
            <div className="mt-3">
              <RoutineComparison
                key={`${workspaceId}:${routine.slug}`}
                workspaceId={workspaceId}
                slug={routine.slug}
              />
            </div>
          </details>
        </div>
      )}
      {view === "plan" && (
        <div className="space-y-4">
          <RoutineSchedulesTab
            workspaceId={workspaceId}
            pipelineId={routine.id}
            slug={routine.slug}
            headVersion={routine.head_version}
            draft={routine.draft}
            concurrencyKey={concurrencyKey}
            maxConcurrent={maxConcurrent}
            otherWays={
              <>
                <RoutineWebhooksTab
                  workspaceId={workspaceId}
                  pipelineId={routine.id}
                  slug={routine.slug}
                />
                {myAutomations.length > 0 && (
                  <DetailCard title="Automations" icon={Zap}>
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
                  </DetailCard>
                )}
              </>
            }
          />
          <details className="rounded-xl border border-hairline p-4">
            <summary className="cursor-pointer text-sm font-medium">
              Access and budget
            </summary>
            <div className="mt-4 space-y-4">
              <DetailCard title="Connected workspace">
                <div className="flex flex-wrap gap-4 text-xs">
                  <Link className="text-primary" href="/credentials">
                    Credentials ↗
                  </Link>
                  <Link className="text-primary" href="/integrations">
                    Integrations ↗
                  </Link>
                  <Link
                    className="text-primary"
                    href={`/activity?pipeline=${encodeURIComponent(routine.slug)}`}
                  >
                    Activity ↗
                  </Link>
                </div>
                <p className="mt-3 text-xs text-muted-foreground">
                  The access checks are applied when a run starts. Editing these
                  connections does not rewrite historical runs.
                </p>
              </DetailCard>
              <AccessCard
                workspaceId={workspaceId}
                routine={routine}
                crewshipActions={crewshipActions}
              />
              <RoutineBudgetCard workspaceId={workspaceId} slug={routine.slug} />
              <DetailCard title="Technical metadata">
                <Metadata routine={routine} steps={steps.length} />
              </DetailCard>
            </div>
          </details>
        </div>
      )}

      {view === "definition" && (
        <>
          {/* Six answers a reader brings, before a single step is read. */}
          <Appear order={2}>
            <InOneLookCard routine={routine} files={files} />
          </Appear>
          <Appear order={3}>
            <RoutineStepSpine
              workspaceId={workspaceId}
              definition={routine.definition}
              behavior={routine.behavior}
              slug={routine.slug}
              map={() => (
                <div className="flex flex-col md:flex-row" style={{ height: mapHeight }}>
                  <div className="relative min-h-[240px] w-full min-w-0 flex-1 md:min-w-[380px]">
                    <RoutineDefinitionCanvas
                      definition={routine.definition}
                      slug={routine.slug}
                      name={routine.name}
                      selectedStepId={selected}
                      onStepSelect={handleSelect}
                      focusStepId={focus}
                    />
                  </div>
                  {selected && (
                    <aside className="h-[45%] max-h-[45%] w-full shrink-0 overflow-auto border-t p-4 md:h-auto md:max-h-none md:w-[320px] md:border-l md:border-t-0">
                      <button
                        onClick={() => setSelected(null)}
                        className="mb-3 text-xs text-muted-foreground"
                      >
                        Close step detail
                      </button>
                      <RoutineStepDefinition
                        step={(
                          routine.definition.steps as
                            | Record<string, unknown>[]
                            | undefined
                        )?.find((s) => s.id === selected)}
                      />
                    </aside>
                  )}
                </div>
              )}
            />
          </Appear>
          <Appear order={4}>
            <RoutineFilesCard
              files={files}
              workspaceId={workspaceId}
              crewId={routine.author_crew_id}
              nameOf={nameOf}
              derived={routine.files == null}
            />
          </Appear>
          <div className="grid grid-cols-1 gap-4 xl:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)]">
            <Appear order={5} className="min-w-0">
              <LastRunCard
                status={lastRun?.status ?? routine.last_invocation_status}
                outcome={lastRun?.outcome}
                at={lastRun?.started_at ?? routine.last_invoked_at}
                runId={lastRun?.id}
                durationMs={lastRun?.duration_ms}
                version={lastRun?.pipeline_version}
                reason={lastRunReason(lastRun, nameOf)}
                slug={routine.slug}
              />
            </Appear>
            <Appear order={6} className="min-w-0">
              <PlanCard
                schedules={mine}
                automations={myAutomations.length}
                headVersion={routine.head_version}
                onPlan={() => setView("plan")}
              />
            </Appear>
          </div>
          <Appear order={7}>
            <details
              data-testid="routine-technical"
              className="rounded-xl border border-border/60 bg-card px-4 py-3 text-xs"
            >
              <summary className="cursor-pointer text-muted-foreground">
                Technical details · identifier, version hash, access, budget, webhooks, automations
              </summary>
              <div className="mt-3 space-y-4">
                <div className="flex flex-wrap items-center gap-1.5">
                  {statusPills}
                  <Pill tone="default">
                    {mine.some((s) => s.enabled) ? "scheduled" : "manual / event"}
                  </Pill>
                  {myAutomations.length > 0 && (
                    <span data-testid="routine-automations-pill">
                      <Pill tone="default">
                        <Zap className="h-3 w-3" />
                        {myAutomations.length} automation
                        {myAutomations.length === 1 ? "" : "s"}
                      </Pill>
                    </span>
                  )}
                </div>
                <Metadata routine={routine} steps={steps.length} />
                <RoutineBehaviorSummary behavior={routine.behavior} />
                <AccessCard
                  workspaceId={workspaceId}
                  routine={routine}
                  crewshipActions={crewshipActions}
                />
                <RoutineBudgetCard workspaceId={workspaceId} slug={routine.slug} />
              </div>
            </details>
          </Appear>
        </>
      )}
      {view === "history" && (
        <Appear order={9}>
          <RunsCard
            slug={routine.slug}
            workspaceId={workspaceId}
            records={records}
            manage={manageRuns}
            onManageChange={setManageRuns}
          />
        </Appear>
      )}
      {canEdit && (
        <RoutineEditDialog
          open={editing}
          onOpenChange={(open) => {
            if (!open) closeDialog()
          }}
          workspaceId={workspaceId}
          routine={routine}
          files={files}
          onChanged={onChanged}
        />
      )}
      {canEdit && routine.draft && (
        <RoutinePublishDialog
          open={publishing}
          onOpenChange={(open) => {
            if (!open) closeDialog()
          }}
          workspaceId={workspaceId}
          routine={routine}
          activeRuns={activeRuns}
          onPublished={onChanged}
          onDiscarded={onChanged}
        />
      )}
    </div>
  )
}

/* ------------------------------------------------------------------ *
 *  In one look                                                        *
 * ------------------------------------------------------------------ */

const listOf = (items: string[]) =>
  items.length <= 1 ? items.join("") : `${items.slice(0, -1).join(", ")} and ${items[items.length - 1]}`

/** Six answers, every one derived from the definition — nothing invented. */
export function inOneLook(
  routine: Pick<RoutineDetail, "definition" | "description" | "manifest">,
  files: { path: string; language: string }[],
): { label: string; text: string }[] {
  const dsl = isRecord(routine.definition) ? routine.definition : {}
  const steps = Array.isArray(dsl.steps) ? dsl.steps.filter(isRecord) : []
  const all = [...steps, ...steps.flatMap(foreachBody)]
  const nameOf = nameLookup(dsl)
  const inputs = routineInputSpecs(dsl)
  const outputs = Array.isArray(dsl.outputs) ? dsl.outputs.filter(isRecord) : []

  const get = outputs.length
    ? outputs
        .map((o) => {
          const label = typeof o.label === "string" ? o.label : readableName(o.name || "Result")
          return typeof o.description === "string" && o.description ? `${label} — ${o.description}` : label
        })
        .join("; ") + "."
    : routine.description || "The results of the steps below — no result labels are declared."

  const provide = inputs.length
    ? inputs.map((i) => `${i.label || readableName(i.name)}${i.required ? "" : " (optional)"}`).join(", ")
    : "Nothing — Run starts immediately."

  const agentSlugs = [
    ...new Set([
      ...(routine.manifest?.agents ?? []),
      ...all.map((s) => asString(s.agent_slug)).filter(Boolean),
      ...all.map((s) => (isRecord(s.outcomes) ? asString(s.outcomes.grader_agent_slug) : "")).filter(Boolean),
    ]),
  ]
  const people = all.filter((s) => s.type === "wait" && (!isRecord(s.wait) || !asString(s.wait.kind) || asString(s.wait.kind) === "approval"))
  const scripts = Math.max(
    files.filter((f) => !["yaml", "json", "md"].includes(f.language)).length,
    all.filter((s) => s.type === "script").length,
  )
  const who: string[] = []
  if (agentSlugs.length) who.push(`${listOf(agentSlugs)} (${agentSlugs.length === 1 ? "agent" : "agents"})`)
  if (people.length) who.push(people.length === 1 ? "a person who decides" : `${people.length} decisions by people`)
  if (scripts) who.push(`${scripts} ${scripts === 1 ? "script" : "scripts"} on the crew share`)
  if (all.some((s) => s.type === "notify")) who.push("notifications")
  if (all.some((s) => s.type === "http")) who.push("external services")
  if (all.some((s) => s.type === "crewship")) who.push("Crewship pages and issues")
  if (all.some((s) => s.type === "call_pipeline")) who.push("another routine")
  const whoText = who.length ? who.join(", ") : "Local data preparation only."

  const checked = all
    .map((s) => ({ s, chips: stepChips(s, nameOf) }))
    .filter(({ chips }) => chips.checks)
    .map(({ s, chips }) =>
      chips.checks!.grader
        ? `${chips.checks!.count} ${chips.checks!.count === 1 ? "rule" : "rules"} by ${chips.checks!.grader} on “${nameOf(String(s.id))}”`
        : `${chips.checks!.count} ${chips.checks!.count === 1 ? "check" : "checks"} on “${nameOf(String(s.id))}”`,
    )
  const checkedText = checked.length ? checked.join("; ") + "." : "No checks declared — completion alone does not prove quality."

  const needs = people.map((s) => {
    const when = typeof s.if === "string" && s.if.trim() ? `Only when ${s.if.trim()}` : `At “${nameOf(String(s.id))}”`
    const timeout = typeof s.timeout_seconds === "number" && s.timeout_seconds > 0 ? ` · answer within ${describeTimeout(s.timeout_seconds)}` : ""
    return when + timeout
  })
  const needsText = needs.length ? needs.join("; ") : "Never — it runs without a decision."

  const effects = routineEffects(dsl)
  const crewship = crewshipActionsInDefinition(dsl)
  const touch: string[] = []
  if (effects.http) touch.push(`Calls ${effects.hosts.length ? listOf(effects.hosts) : "external services"}.`)
  if (crewship.length) touch.push(`Writes to Crewship (${crewship.join(", ")}).`)
  if (all.some((s) => s.type === "script" || s.type === "code")) touch.push("Runs scripts on the crew.")
  if (all.some((s) => s.type === "notify")) touch.push("Sends notifications.")
  if (effects.credentials.length) touch.push(`Uses ${listOf(effects.credentials)} from the crew vault.`)
  if (!touch.length) touch.push(effects.agents.length ? "Agents act with their own tools; no external writes are declared." : "No external writes declared.")
  touch.push("Stopping does not undo what already happened.")
  if (typeof dsl.max_cost_usd === "number" && dsl.max_cost_usd > 0) touch.push(`Cost cap $${dsl.max_cost_usd} per run.`)

  return [
    { label: "What you get", text: get },
    { label: "What you provide", text: provide },
    { label: "Who does the work", text: whoText },
    { label: "What is checked", text: checkedText },
    { label: "When it needs you", text: needsText },
    { label: "What it can touch", text: touch.join(" ") },
  ]
}

function InOneLookCard({ routine, files }: { routine: RoutineDetail; files: { path: string; language: string }[] }) {
  const cells = React.useMemo(() => inOneLook(routine, files), [routine, files])
  return (
    <DetailCard title="In one look" icon={Eye} bare data-testid="routine-in-one-look">
      <dl className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3">
        {cells.map((cell, i) => (
          <div
            key={cell.label}
            data-testid={`routine-look-${cell.label.toLowerCase().replace(/[^a-z]+/g, "-")}`}
            className={cn(
              "border-hairline px-4 py-3",
              i % 3 !== 2 && "lg:border-r",
              i < 3 && "lg:border-b",
              i % 2 === 0 && "sm:border-r lg:border-r-0",
              i < 4 && "sm:border-b",
              i < 5 && "border-b sm:border-b-0",
              i === 2 && "lg:border-r-0",
              i === 3 && "lg:border-r",
            )}
          >
            <dt className="mb-1 text-[10.5px] font-semibold uppercase tracking-wider text-muted-foreground-soft">{cell.label}</dt>
            <dd className="text-[13px] leading-relaxed text-foreground/90">{cell.text}</dd>
          </div>
        ))}
      </dl>
    </DetailCard>
  )
}

/** One line under the Last run facts: what stopped it, or what it produced. */
function lastRunReason(run: PipelineRunRecord | null, nameOf: (id: string) => string): string | undefined {
  if (!run) return undefined
  const failure = (run as PipelineRunRecord & { failure?: { step_name?: string; summary?: string } }).failure
  if (failure?.summary) return failure.step_name ? `Stopped at “${failure.step_name}”: ${failure.summary}` : failure.summary
  if (run.status === "failed" || run.outcome === "FAILED") {
    const step = run.failed_at_step ? `Stopped at “${nameOf(run.failed_at_step)}”` : "Could not finish"
    return run.error_message ? `${step}: ${run.error_message.split("\n")[0].slice(0, 160)}` : step
  }
  if (run.status === "waiting") return run.current_step_id ? `Waiting for a person at “${nameOf(run.current_step_id)}”.` : "Waiting for a person."
  if (run.status === "cancelled") return "Stopped. What already happened was not undone."
  if (run.output) return run.output.split("\n")[0].slice(0, 160)
  return undefined
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
  outcome,
  at,
  runId,
  durationMs,
  version,
  reason,
  slug,
}: {
  status?: string
  outcome?: string
  at?: string
  runId?: string
  durationMs?: number
  version?: number
  reason?: string
  slug: string
}) {
  const presentation = routineRunPresentation({ status, outcome })
  const tone = presentation.tone
  const ok = tone === "success"
  const bad = tone === "destructive"
  const Icon = ok ? CheckCircle2 : bad ? XCircle : Clock

  if (!at) {
    return (
      <DetailCard title="Last run">
        <p className="text-[12px] text-muted-foreground">Not run yet.</p>
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
            Last run · {presentation.label}
          </div>
          {runId && (
            <div className="truncate font-mono text-[10px] text-muted-foreground">
              {runId}
            </div>
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
          <Fact label="version" value={version != null ? `v${version}` : "—"} />
        </dl>
        {reason && (
          <p data-testid="routine-last-run-reason" className="text-[12px] text-foreground/85">
            {reason}
          </p>
        )}
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
      <dt className="text-[10px] uppercase tracking-wider text-muted-foreground-soft">
        {label}
      </dt>
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

const readableName = (value: unknown) =>
  String(value ?? "")
    .replace(/[_-]+/g, " ")
    .replace(/^\w/, (c) => c.toUpperCase())

/** The plan in one card: each schedule, which version it uses, and the door to Plan. */
function PlanCard({
  schedules,
  automations,
  headVersion,
  onPlan,
}: {
  schedules: { id: string; enabled: boolean; cron_expr: string; timezone?: string; target_pipeline_version?: number | null }[]
  automations: number
  headVersion?: number
  onPlan: () => void
}) {
  return (
    <DetailCard title="Plan" icon={Clock} data-testid="routine-plan-card">
      <div className="space-y-2 text-[12px]">
        {schedules.length === 0 ? (
          <p className="text-muted-foreground">Manual only.</p>
        ) : (
          schedules.map((s) => (
            <p key={s.id}>
              <span className="font-medium">{describeCron(s.cron_expr)}</span>
              <span className="text-muted-foreground">
                {" "}· {s.timezone || "UTC"} · {s.enabled ? "on" : "off"} ·{" "}
                {s.target_pipeline_version != null
                  ? `pinned to v${s.target_pipeline_version}`
                  : `uses latest published${headVersion ? ` (v${headVersion})` : ""}`}
              </span>
            </p>
          ))
        )}
        {automations > 0 && (
          <p className="text-muted-foreground">
            {automations} {automations === 1 ? "automation" : "automations"} can start it.
          </p>
        )}
        <Button variant="outline" size="sm" className="mt-1" onClick={onPlan}>
          Change plan
        </Button>
      </div>
    </DetailCard>
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
  workspaceId,
}: {
  routine: RoutineDetail
  crewshipActions: string[]
  workspaceId: string
}) {
  const m = routine.manifest
  const integrations = [
    ...new Set(m?.integrations ?? routine.integrations_required ?? []),
  ]
  const credentials = m?.credentials ?? []
  const agentSlugs = [...new Set(m?.agents ?? [])]
  const hosts = [...new Set(m?.egress ?? [])]
  return (
    <DetailCard
      title="Access"
      subtitle="what this can reach"
      icon={ShieldAlert}
      tone="warn"
    >
      <div className="space-y-4">
        {agentSlugs.length > 0 && (
          <div>
            <h3 className="mb-2 text-xs font-medium text-muted-foreground">
              Agents and what they reach
            </h3>
            <RoutineReachCard bare workspaceId={workspaceId} agentSlugs={agentSlugs} />
          </div>
        )}
        {credentials.length > 0 && (
          <div>
            <h3 className="mb-2 text-xs font-medium text-muted-foreground">
              Required credentials{" "}
              <span className="font-normal">
                · accounts are resolved when the run starts
              </span>
            </h3>
            <div className="flex flex-wrap gap-1.5">
              {credentials.map((credential, index) => (
                <Link
                  key={`${credential.type}:${credential.scope}:${index}`}
                  href="/credentials"
                  title="Open Credentials to find a matching account"
                  className="inline-flex items-center gap-1.5 rounded-full border border-warn/20 bg-warn/10 px-2.5 py-1 text-xs text-warn hover:bg-warn/20"
                >
                  <KeyRound className="h-3.5 w-3.5" />
                  {credential.type === "AI_CLI_TOKEN"
                    ? "AI CLI token"
                    : credentialTypeLabel(credential.type)}
                  {credential.scope && (
                    <span className="text-muted-foreground">· {credential.scope}</span>
                  )}
                  <ArrowUpRight className="h-3 w-3" />
                </Link>
              ))}
            </div>
          </div>
        )}
        {integrations.length > 0 && (
          <div>
            <h3 className="mb-2 text-xs font-medium text-muted-foreground">
              Integrations
            </h3>
            <div className="flex flex-wrap gap-1.5">
              {integrations.map((integration) => {
                const brand = brandIconForType(integration)
                return (
                  <EntityChip
                    key={integration}
                    href="/integrations"
                    icon={
                      brand
                        ? () => (
                            <BrandGlyph
                              brand={brand}
                              fallback={Puzzle}
                              className="h-3 w-3"
                            />
                          )
                        : Puzzle
                    }
                    label={integrationLabel(integration)}
                    tone="warn"
                  />
                )
              })}
            </div>
          </div>
        )}
        {hosts.length > 0 && (
          <details className="border-t border-border/60 pt-3">
            <summary className="flex cursor-pointer items-center gap-2 text-xs">
              <Globe className="h-3.5 w-3.5 text-warn" />
              Allowed network hosts{" "}
              <span className="text-muted-foreground">{hosts.length}</span>
            </summary>
            <ul className="mt-2 space-y-1 text-xs text-muted-foreground">
              {hosts.map((host) => (
                <li key={host} className="break-all">
                  {host}
                </li>
              ))}
            </ul>
            <p className="mt-2 text-[11px] text-muted-foreground">
              Declared network access; this is not a connection health check.
            </p>
          </details>
        )}
        {crewshipActions.length > 0 && (
          <div data-testid="routine-crewship-actions">
            <h3 className="mb-2 text-xs font-medium text-muted-foreground">
              Writes to Crewship
            </h3>
            <div className="flex flex-wrap gap-1.5">
              {crewshipActions.map((action) => (
                <EntityChip key={action} icon={PenSquare} label={action} tone="warn" />
              ))}
            </div>
          </div>
        )}
        {!integrations.length && !credentials.length && !hosts.length && (
          <p className="text-xs text-muted-foreground">
            No external integrations, credentials or network hosts declared.
          </p>
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
          <dt className="text-[10px] uppercase tracking-wider text-muted-foreground-soft">
            {k}
          </dt>
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
        <RunsList key={slug} slug={slug} workspaceId={workspaceId} />
      )}
    </DetailCard>
  )
}

function RunsList({ slug, workspaceId }: { slug: string; workspaceId: string }) {
  const [pages, setPages] = React.useState<string[]>([])
  const before = pages.at(-1)
  const { records, error, loading, refresh } = usePipelineRunRecords(
    workspaceId,
    slug,
    undefined,
    before,
  )
  return (
    <>
      {error && (
        <p role="alert" className="p-4 text-sm text-destructive">
          Run history could not be loaded. <button onClick={refresh}>Retry</button>
        </p>
      )}
      {loading && <p className="p-4 text-sm text-muted-foreground">Loading history…</p>}
      {records.length === 0 ? (
        <p className="px-4 py-3 text-[12px] text-muted-foreground">
          No runs recorded yet.
        </p>
      ) : (
        <ul className="divide-y divide-border/40">
          {records.map((r) => {
            const tone = toneOf(
              r.outcome === "FAILED"
                ? "failed"
                : r.outcome === "NEEDS_HUMAN"
                  ? "waiting"
                  : r.status,
            )
            const Icon =
              tone === "success" ? CheckCircle2 : tone === "destructive" ? XCircle : Clock
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
                    <div className="truncate font-mono text-[11px] text-foreground/85">
                      {formatRoutineTime(r.started_at)} · {routineRunLabel(r)}
                    </div>
                    <div className="flex flex-wrap items-baseline gap-x-1.5 text-[10px] uppercase tracking-wide text-muted-foreground">
                      <span>{prov.label}</span>
                      {r.pipeline_version != null && <span>· v{r.pipeline_version}</span>}
                      {prov.source && (
                        <span className="truncate normal-case text-muted-foreground-soft">
                          {prov.source}
                        </span>
                      )}
                    </div>
                  </div>
                  <div className="text-right text-[11px] tabular-nums text-muted-foreground">
                    {r.started_at ? relTime(r.started_at) : "—"}
                  </div>
                  <ChevronRight className="h-3.5 w-3.5 text-muted-foreground-soft" />
                </Link>
                {prov.chainDepth !== undefined && (
                  <details className="px-4 pb-2 text-xs text-muted-foreground">
                    <summary className="cursor-pointer">Technical details</summary>
                    <p data-testid={`run-chain-depth-${r.id}`}>
                      Composition depth: {prov.chainDepth}
                    </p>
                  </details>
                )}
              </li>
            )
          })}
        </ul>
      )}
      <div className="flex gap-2 border-t border-border/60 px-4 py-2">
        {pages.length > 0 && (
          <Button
            variant="outline"
            size="sm"
            disabled={loading}
            onClick={() => setPages(pages.slice(0, -1))}
          >
            Newer runs
          </Button>
        )}
        {records.length === 50 && (
          <Button
            variant="outline"
            size="sm"
            disabled={loading}
            onClick={() => setPages([...pages, records[records.length - 1].id])}
          >
            Older runs
          </Button>
        )}
      </div>
    </>
  )
}

