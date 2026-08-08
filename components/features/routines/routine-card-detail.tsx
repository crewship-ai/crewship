"use client"

// The routine detail, as one scrolling surface of cards.
//
// Replaces the five-tab shell (Overview / Preview / Runs / Schedules /
// Advanced). The design was argued on a throwaway /routines-new preview
// route against the real renderer before landing here; that route is gone
// now that this is the shipping layout. What follows is it wired to real
// data.
//
// What went, and why:
//
//   Preview      the graph is on the page now. A tab holding a picture
//                of the thing you are already looking at exists to hide
//                the picture.
//   Advanced     three levels of nesting over four unrelated things.
//                The machinery is not gone — Editor opens beside the
//                graph, Schedules/Webhooks live inside Triggers, and
//                Versions has its own card. Nothing that worked was
//                deleted; it stopped being filed under a word that told
//                the reader nothing.
//   Wait points  belong to a RUN, not to a definition. Activity is
//                where the run is.
//
// Ordered by what an operator asks, in order: what is it → when does it
// run → is it healthy → what does it do → what can it reach → what did
// it do last.

import * as React from "react"
import Link from "next/link"
import { AnimatePresence, motion, useReducedMotion } from "motion/react"
import {
  ArrowUpRight,
  Bot,
  CalendarClock,
  CheckCircle2,
  ChevronRight,
  Clock,
  Code2,
  GitBranch,
  Globe,
  KeyRound,
  PenSquare,
  Puzzle,
  ShieldAlert,
  Webhook,
  XCircle,
  Zap,
} from "lucide-react"

import { cn } from "@/lib/utils"
import { relTime, formatDurationDecimal } from "@/lib/time"
import { Appear, DetailCard, EntityChip, Pill, StatStrip } from "@/components/ui/detail"
import { usePipelineRunRecords, type PipelineRunRecord } from "@/hooks/use-pipeline-run-records"
import { usePipelineSchedules } from "@/hooks/use-pipeline-schedules"
import { useAutomations } from "@/hooks/use-automations"
import { automationsForRoutine, crewshipActionsInDefinition } from "@/lib/automations"
import { runProvenance } from "@/lib/run-provenance"
import { AutomationList } from "@/components/features/automations/automation-list"
import { integrationLabel } from "@/lib/integration-labels"
import { credentialTypeLabel } from "@/lib/credential-labels"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CrewIconPopover } from "@/components/crew-icon-popover"
import { resolveRoutineIcon, resolveRoutineColor } from "@/lib/routine-identity"
import { apiFetch } from "@/lib/api-fetch"
import { toast } from "sonner"
import { brandIconForType, BrandGlyph } from "./brand-icons"
import { RoutineDefinitionCanvas } from "./routine-definition-canvas"
import { RoutineBudgetCard } from "./routine-budget-card"
import { RoutineEditorTab } from "./routine-editor-tab"
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

type SideTab = "triggers" | "versions"

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
  const reduceMotion = useReducedMotion()
  const [editing, setEditing] = React.useState(false)
  React.useEffect(() => {
    if (editRequest > 0) setEditing(true)
  }, [editRequest])
  const [selected, setSelected] = React.useState<string | null>(null)
  // Separate from `selected`: selection is a persistent choice, focus a
  // one-shot "bring this into view". Merged, a re-render could yank the
  // viewport back after the reader had panned away from it.
  const [focus, setFocus] = React.useState<string | null>(null)
  const handleCaret = React.useCallback((stepId: string | null) => {
    if (!stepId) return
    setSelected(stepId)
    setFocus(stepId)
  }, [])
  const handleSelect = React.useCallback((id: string | null) => {
    setSelected(id)
    setFocus(null)
  }, [])
  const [sideTab, setSideTab] = React.useState<SideTab>("triggers")
  const [triggerKind, setTriggerKind] = React.useState<TriggerKind>("schedules")
  const [manageTriggers, setManageTriggers] = React.useState(false)
  const [manageVersions, setManageVersions] = React.useState(false)
  // Cancelling a specific run lives in RoutineRunsTab, which has the
  // per-row buttons and the RBAC handling. Dropping the tab must not
  // drop the capability, so Manage mounts the real thing rather than a
  // reimplementation of it.
  const [manageRuns, setManageRuns] = React.useState(false)

  // Stored if chosen, derived from the slug if not — resolved in one
  // place so the header and the explorer row can never disagree.
  const [icon, setIcon] = React.useState(() => resolveRoutineIcon(routine))
  const [color, setColor] = React.useState(() => resolveRoutineColor(routine))
  React.useEffect(() => {
    setIcon(resolveRoutineIcon(routine))
    setColor(resolveRoutineColor(routine))
  }, [routine])

  const saveAppearance = React.useCallback(
    async (next: { icon?: string; color?: string }) => {
      const prev = { icon, color }
      if (next.icon !== undefined) setIcon(next.icon)
      if (next.color !== undefined) setColor(next.color)
      try {
        const res = await apiFetch(
          `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(routine.slug)}/appearance`,
          {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(next),
          },
        )
        if (!res.ok) throw new Error(String(res.status))
        onChanged()
      } catch {
        // Put it back. A picker that silently keeps a colour the server
        // rejected is the same lie as a save button that saves nothing.
        setIcon(prev.icon)
        setColor(prev.color)
        toast.error("Could not save the icon")
      }
    },
    [icon, color, routine.slug, workspaceId, onChanged],
  )

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
  // Losing the automations view when the last rule is deleted must not strand
  // the card on an empty pane.
  React.useEffect(() => {
    if (triggerKind === "automations" && myAutomations.length === 0) setTriggerKind("schedules")
  }, [triggerKind, myAutomations.length])
  const nextRun = React.useMemo(() => {
    const upcoming = mine
      .filter((s) => s.enabled && s.next_run_at)
      .map((s) => new Date(s.next_run_at!).getTime())
      .filter((t) => Number.isFinite(t))
      .sort((a, b) => a - b)
    return upcoming[0] ?? null
  }, [mine])

  const steps = React.useMemo(() => {
    const raw = (routine.definition as { steps?: unknown })?.steps
    return Array.isArray(raw) ? (raw as { type?: string }[]) : []
  }, [routine.definition])

  const stats = React.useMemo(() => summarise(records), [records])
  const estimatedCost = React.useMemo(() => {
    const raw = (routine.definition as { estimated_cost_usd?: unknown })?.estimated_cost_usd
    return typeof raw === "number" && raw > 0 ? raw : null
  }, [routine.definition])

  const agentPct = React.useMemo(() => {
    if (steps.length === 0) return 0
    const opaque = steps.filter((st) => st.type === "agent_run").length
    return Math.round((opaque / steps.length) * 100)
  }, [steps])
  // The agent the routine runs through, for the avatar. Manifest first —
  // it is derived from the step graph — falling back to nothing rather
  // than guessing from the author, who may not be an agent at all.
  const ownerAgent = routine.manifest?.agents?.[0] ?? null
  const lastRun = records[0] ?? null

  return (
    <div className="flex flex-col gap-4 p-4">
      {/* Identity, as a card that scrolls with the page rather than a
          fixed header band. The name is the first thing on the page —
          it used to sit under a row of status chrome. */}
      <Appear order={0}>
        <DetailCard>
          <div className="flex flex-col gap-3">
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div className="flex min-w-0 items-start gap-3">
                {/* A real picker now: PATCH /appearance writes two
                    columns and leaves the definition alone, so choosing
                    an icon does not mint a routine version. Optimistic,
                    because a colour that lags a round-trip feels
                    broken — and it reverts loudly if the write fails. */}
                <CrewIconPopover
                  icon={icon}
                  color={color}
                  size="lg"
                  onIconChange={(next) => saveAppearance({ icon: next })}
                  onColorChange={(next) => saveAppearance({ color: next })}
                />
                <div className="min-w-0">
                  <h1 className="truncate text-lg font-semibold tracking-tight">
                    {routine.name || routine.slug}
                  </h1>
                  <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
                    <span className="font-mono">{routine.slug}</span>
                    {routine.head_version != null && (
                      <>
                        <span aria-hidden>·</span>
                        <span className="font-mono">v{routine.head_version}</span>
                      </>
                    )}
                    {ownerAgent && (
                      <>
                        <span aria-hidden>·</span>
                        <Link
                          href="/crews"
                          className="inline-flex items-center gap-1.5 rounded-full border border-border/60 py-0.5 pl-0.5 pr-2 transition-colors hover:border-border hover:text-foreground"
                        >
                          <AgentAvatar seed={ownerAgent} className="h-4 w-4" alt="" />
                          <span className="font-medium">{ownerAgent}</span>
                        </Link>
                      </>
                    )}
                  </div>
                </div>
              </div>
              {actions && <div className="flex shrink-0 items-center gap-1.5">{actions}</div>}
            </div>

            {routine.description && (
              <p className="max-w-[80ch] text-[13px] leading-relaxed text-foreground/85">
                {routine.description}
              </p>
            )}

            <div className="flex flex-wrap items-center gap-1.5">
              {statusPills}
              <Pill tone="default">{mine.length > 0 ? "scheduled" : "manual"}</Pill>
              {/* The pill above answers "does a clock start this". It has no
                  word for "an event starts this", and without one a routine a
                  rule fires reads as manual at a glance. */}
              {myAutomations.length > 0 && (
                <span data-testid="routine-automations-pill">
                  <Pill tone="default">
                    <Zap className="h-3 w-3" />
                    {myAutomations.length} automation{myAutomations.length === 1 ? "" : "s"}
                  </Pill>
                </span>
              )}
              {steps.length > 0 && (
                <Pill tone={agentPct >= 60 ? "warn" : "default"}>{agentPct}% agent steps</Pill>
              )}
              <Pill tone="default">
                {steps.length} {steps.length === 1 ? "step" : "steps"}
              </Pill>
              {routine.ephemeral && <Pill tone="warn">ephemeral</Pill>}
            </div>
          </div>
        </DetailCard>
      </Appear>

      <Appear order={1}>
        <StatStrip
          items={[
            {
              label: "Next run",
              value: nextRun ? relTime(new Date(nextRun).toISOString()) : "—",
            },
            {
              label: "Last run",
              value: routine.last_invoked_at ? relTime(routine.last_invoked_at) : "never",
              tone: toneOf(routine.last_invocation_status),
            },
            { label: "Runs", value: String(routine.invocation_count ?? 0) },
            {
              label: "Pass rate",
              value: stats.passRate === null ? "—" : `${stats.passRate}%`,
            },
            {
              label: "Avg duration",
              value: stats.avgMs > 0 ? formatDurationDecimal(stats.avgMs) : "—",
              mono: true,
            },
            // The author's own estimate, straight from the definition.
            // The dry-run panel computed the same number and charged a
            // round-trip and a full-width report for it.
            {
              label: "Est. cost",
              value: estimatedCost !== null ? `$${estimatedCost.toFixed(4)}` : "—",
              mono: true,
            },
          ]}
        />
      </Appear>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3 2xl:grid-cols-4">
        <Appear order={2} className="xl:col-span-2 2xl:col-span-3">
          <DetailCard
            title="Definition"
            subtitle={`${steps.length} ${steps.length === 1 ? "step" : "steps"}`}
            bare
          >
            {/* The editor is a sibling, not an overlay: as a sibling it
                takes real width and the graph slides left into what is
                still visible, instead of hiding underneath it.
                
                Both panes are FLOORED so the graph can never be squeezed
                away — a 380px canvas is the smallest one a step node is
                still readable in, and the editor is capped at 560px
                because past that it takes width the graph needs without
                showing more code. Stacked, the same floors apply to
                height. Whatever the window does, both halves survive. */}
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
                <button
                  type="button"
                  onClick={() => setEditing((v) => !v)}
                  className={cn(
                    "absolute right-3 top-3 z-10 inline-flex items-center gap-1.5 rounded-lg border px-2.5 py-1.5 text-[11px] font-medium backdrop-blur transition-colors",
                    editing
                      ? "border-primary/40 bg-primary/15 text-primary"
                      : "border-border/60 bg-card/85 text-muted-foreground hover:text-foreground",
                  )}
                >
                  <Code2 className="h-3.5 w-3.5" />
                  {editing ? "Close code" : "Edit code"}
                </button>
              </motion.div>
              {/* The editor arrives from the side it will occupy, and
                  the graph pane's `layout` animates it out of the way in
                  the same beat — so the two read as one movement rather
                  than a pane popping into existence. Opacity and offset
                  only: animating the width itself fights the responsive
                  floors above, which are the thing keeping the graph
                  readable. */}
              <AnimatePresence initial={false}>
                {editing && (
                  <motion.aside
                    key="editor"
                    initial={reduceMotion ? false : { opacity: 0, x: 28 }}
                    animate={{ opacity: 1, x: 0 }}
                    exit={reduceMotion ? { opacity: 0 } : { opacity: 0, x: 28 }}
                    transition={PANE_EASE}
                    className="h-[45%] max-h-[45%] w-full shrink-0 overflow-auto border-t border-border/60 md:h-auto md:max-h-none md:w-[48%] md:max-w-[560px] md:border-l md:border-t-0"
                  >
                    <RoutineEditorTab
                      routine={routine}
                      workspaceId={workspaceId}
                      onSaved={onChanged}
                      onStepAtCaret={handleCaret}
                    />
                  </motion.aside>
                )}
              </AnimatePresence>
            </div>
          </DetailCard>
        </Appear>

        <div className="flex flex-col gap-4">
          <Appear order={3}>
            <LastRunCard
              status={routine.last_invocation_status}
              at={routine.last_invoked_at}
              runId={lastRun?.id}
              durationMs={lastRun?.duration_ms}
              slug={routine.slug}
            />
          </Appear>

          {/* Triggers and Versions share one card behind a switch —
              the mockup's arrangement, and better than two half-empty
              cards or a tab that hides one of them. Manage reveals the
              working editors rather than replacing them. */}
          <Appear order={4}>
            <DetailCard
              title={sideTab === "triggers" ? TRIGGER_TITLE[triggerKind] : "Versions"}
              subtitle={
                sideTab === "triggers"
                  ? triggerKind === "schedules"
                    ? String(mine.length)
                    : triggerKind === "automations"
                      ? String(myAutomations.length)
                      : undefined
                  : routine.head_version != null
                    ? `v${routine.head_version}`
                    : undefined
              }
              icon={sideTab === "triggers" ? TRIGGER_ICON[triggerKind] : GitBranch}
              tone="purple"
              action={
                <div className="flex items-center gap-1">
                  <div className="flex items-center gap-0.5 rounded-md border border-border/60 p-0.5">
                    {(["triggers", "versions"] as const).map((t) => (
                      <button
                        key={t}
                        type="button"
                        onClick={() => {
                          setSideTab(t)
                          setManageTriggers(false)
                          setManageVersions(false)
                        }}
                        aria-pressed={sideTab === t}
                        className={cn(
                          "rounded px-1.5 py-0.5 text-[10px] font-medium capitalize transition-colors",
                          sideTab === t
                            ? "bg-primary/15 text-primary"
                            : "text-muted-foreground hover:text-foreground",
                        )}
                      >
                        {t}
                      </button>
                    ))}
                  </div>
                  {/* Automations have no in-app editor to reveal, so the card
                      does not offer one. A Manage button that opens the same
                      read-only list is a button that lies. */}
                  {!(sideTab === "triggers" && triggerKind === "automations") && (
                    <button
                      type="button"
                      onClick={() =>
                        sideTab === "triggers"
                          ? setManageTriggers((v) => !v)
                          : setManageVersions((v) => !v)
                      }
                      className="rounded-md border border-border/60 px-1.5 py-1 text-[10px] font-medium text-muted-foreground transition-colors hover:text-foreground"
                    >
                      {(sideTab === "triggers" ? manageTriggers : manageVersions)
                        ? "Done"
                        : "Manage"}
                    </button>
                  )}
                </div>
              }
              footer={
                sideTab === "triggers" ? (
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
                ) : undefined
              }
            >
              {sideTab === "versions" ? (
                manageVersions ? (
                  <RoutineVersionsTab
                    workspaceId={workspaceId}
                    slug={routine.slug}
                    onRolledBack={onChanged}
                  />
                ) : (
                  <p className="text-[12px] text-muted-foreground">
                    Head is v{routine.head_version ?? 1}. Press Manage for the full history and
                    rollback.
                  </p>
                )
              ) : triggerKind === "automations" ? (
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
          <Appear order={7}>
            <RoutineBudgetCard workspaceId={workspaceId} slug={routine.slug} />
          </Appear>

          <Appear order={8}>
            <DetailCard title="Metadata">
              <Metadata routine={routine} steps={steps.length} />
            </DetailCard>
          </Appear>
        </div>
      </div>

      <Appear order={9}>
        <RunsCard
          slug={routine.slug}
          workspaceId={workspaceId}
          records={records}
          manage={manageRuns}
          onManageChange={setManageRuns}
        />
      </Appear>
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

function summarise(records: { status: string; duration_ms?: number }[]) {
  if (records.length === 0) return { passRate: null as number | null, avgMs: 0 }
  const terminal = records.filter((r) => {
    const s = r.status?.toLowerCase()
    return s === "completed" || s === "succeeded" || s === "failed" || s === "error"
  })
  const ok = terminal.filter((r) => {
    const s = r.status?.toLowerCase()
    return s === "completed" || s === "succeeded"
  }).length
  const durations = records.map((r) => r.duration_ms ?? 0).filter((d) => d > 0)
  return {
    passRate: terminal.length > 0 ? Math.round((ok / terminal.length) * 100) : null,
    avgMs: durations.length > 0 ? durations.reduce((a, b) => a + b, 0) / durations.length : 0,
  }
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
          Open full trace
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
  const params = new URLSearchParams({ pipeline: slug })
  if (runId) params.set("run", runId)
  return `/activity?${params.toString()}`
}

function ScheduleList({
  schedules,
}: {
  schedules: { id: string; name: string; cron_expr: string; timezone: string; enabled: boolean; next_run_at?: string }[]
}) {
  if (schedules.length === 0) {
    return (
      <p className="text-[12px] text-muted-foreground">
        No cron triggers. Press Manage to add one.
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
        <RunsList slug={slug} records={records} />
      )}
    </DetailCard>
  )
}

function RunsList({
  slug,
  records,
}: {
  slug: string
  records: PipelineRunRecord[]
}) {
  return (
    <>
      {records.length === 0 ? (
        <p className="px-4 py-3 text-[12px] text-muted-foreground">No runs recorded yet.</p>
      ) : (
        <ul className="divide-y divide-border/40">
          {records.slice(0, 8).map((r) => {
            const tone = toneOf(r.status)
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
                    <div className="truncate font-mono text-[11px] text-foreground/85">{r.id}</div>
                    <div className="flex flex-wrap items-baseline gap-x-1.5 text-[10px] uppercase tracking-wide text-muted-foreground">
                      <span>{prov.label}</span>
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
      <div className="border-t border-border/60 px-4 py-2">
        <Link
          href={activityHref(slug)}
          className="inline-flex items-center gap-1 text-[11px] text-primary hover:underline"
        >
          All runs in Activity
          <ArrowUpRight className="h-3 w-3" />
        </Link>
      </div>
    </>
  )
}

// Referenced for the Pill import so the identity chrome in the panel
// above can keep using the same tone vocabulary.
void Pill
