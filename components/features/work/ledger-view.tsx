"use client"

// The Work queue and Webhook deliveries inside Activity (#3012): the Activity
// rail switched to the ledger's rows, beside a page drawn like Activity's home.
//
// The rail and the page read one narrowing — status, agent, event family — so
// a pick on the left changes every card on the right, and the window toggle
// changes both. A piece of work opens in a panel beside the page; a delivery
// opens its work by switching to the Work queue with it selected.

import * as React from "react"
import { X } from "lucide-react"

import { TIME_WINDOW_MS, type TimeWindow } from "@/components/features/activity-stream/time-window"
import { SidebarCollapseButton } from "@/components/layout/sidebar-kit"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { useAbilities } from "@/hooks/use-abilities"
import { useIsMobile } from "@/hooks/use-mobile"
import { useLedgerDeliveries } from "@/hooks/use-webhook-deliveries"
import { isTerminalWorkState, useLedgerWork } from "@/hooks/use-work-items"
import { roleAtLeast } from "@/lib/routine-governance"
import { relTime } from "@/lib/time"
import { cn } from "@/lib/utils"
import {
  LEDGER_TONE_LABEL,
  agentName,
  deliveryFamily,
  deliveryTone,
  endpointHealth,
  endpointName,
  familyCounts,
  inWindow,
  ledgerAgents,
  ledgerCounts,
  narrowDeliveries,
  narrowWork,
  workFamily,
  type DeliveryTone,
  type LedgerTone,
} from "@/lib/work-ledger"
import { DeliveriesPage, DeliveryDetail } from "./deliveries-page"
import { DeliveriesRail, WorkRail, type LedgerSection, type RailAgentRow, type RailChip } from "./ledger-rail"
import { WorkItemDetail } from "./work-item-detail"
import { WorkQueuePage } from "./work-queue-page"

const AGENT_NOTE: Record<string, { note: string; noteTone?: string }> = {
  blocked: { note: "blocked", noteTone: "text-warn" },
  running: { note: "running", noteTone: "text-primary" },
  idle: { note: "idle" },
  deleted: { note: "deleted" },
}

const ENDPOINT_NOTE = { gone: "deleted", blocked: "blocked", quiet: "quiet" } as const

/**
 * The picked agent or family keeps a row while it is picked, even when the
 * window or the other ledger holds none of it: a filter with no row to unpick
 * left the page empty with no way back.
 */
function keepPicked<T>(rows: T[], picked: string | null, key: (r: T) => string, make: (id: string) => T): T[] {
  if (!picked || rows.some((r) => key(r) === picked)) return rows
  return [...rows, make(picked)]
}

export function LedgerView({
  workspaceId,
  section,
  onSection,
  onLeave,
}: {
  workspaceId: string
  section: LedgerSection
  onSection: (s: LedgerSection) => void
  onLeave: () => void
}) {
  const [win, setWin] = React.useState<TimeWindow>("24h")
  const [now, setNow] = React.useState(() => Date.now())
  React.useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 60_000)
    return () => clearInterval(id)
  }, [])
  const [tone, setTone] = React.useState<LedgerTone | "all">("all")
  const [decision, setDecision] = React.useState<DeliveryTone | "all">("all")
  const [agentId, setAgentId] = React.useState<string | null>(null)
  const [family, setFamily] = React.useState<string | null>(null)
  const [openWorkId, setOpenWorkId] = React.useState<string | null>(null)
  const [openDeliveryId, setOpenDeliveryId] = React.useState<string | null>(null)
  const isMobile = useIsMobile()
  const [railCollapsed, setRailCollapsed] = React.useState(false)
  // On a phone the rail is a drawer over the page; start it closed, as
  // Activity and the Routines explorer do.
  React.useEffect(() => {
    if (isMobile) setRailCollapsed(true)
  }, [isMobile])
  const { role } = useAbilities()

  const from = now - TIME_WINDOW_MS[win]
  // The server reads from the start of the window's first hour, so the query
  // stays put while `now` ticks; the window itself is cut here.
  const since = new Date(Math.floor(from / 3_600_000) * 3_600_000).toISOString()
  const work = useLedgerWork(workspaceId, since)
  const deliveries = useLedgerDeliveries(workspaceId, since)
  const windowLabel = win === "24h" ? "last 24 h" : "last 7 days"

  // A pick on a phone closes the drawer, so the page it narrowed shows.
  function picked<T>(set: (v: T) => void) {
    return (v: T) => {
      set(v)
      if (isMobile) setRailCollapsed(true)
    }
  }

  // A ledger switch keeps the agent (an agent's endpoint IS the agent) and
  // drops the status, whose words differ between the two, the family and the
  // open work panel — also when Back or Forward switches it.
  const keepOpenWork = React.useRef(false)
  const lastSection = React.useRef(section)
  React.useEffect(() => {
    if (lastSection.current === section) return
    lastSection.current = section
    setTone("all")
    setDecision("all")
    setFamily(null)
    setOpenDeliveryId(null)
    if (keepOpenWork.current) keepOpenWork.current = false
    else setOpenWorkId(null)
  }, [section])
  function switchSection(s: LedgerSection) {
    // An endpoint that is not an agent's has no work queue of its own.
    if (s === "work" && agentId && !work.items.some((i) => i.agent_id === agentId)) {
      const d = deliveries.deliveries.find((x) => x.endpoint_id === agentId)
      if (d && d.endpoint_kind && d.endpoint_kind !== "agent") setAgentId(null)
    }
    onSection(s)
  }

  // The window, plus whatever is still unfinished however old it is: stuck
  // work holds its agent's queue and must not fall out of a 24 h view.
  const workInWindow = work.items.filter((i) => {
    const t = Date.parse(i.created_at)
    return (Number.isFinite(t) && t >= from) || !isTerminalWorkState(i.state)
  })
  const narrowedWork = narrowWork(workInWindow, { tone, agentId, family })
  // The rail counts each facet over the OTHER facets, so a row survives its
  // own selection — the rule the Activity rail keeps.
  const workForCounts = narrowWork(workInWindow, { agentId, family })

  const dlvInWindow = inWindow(deliveries.deliveries, (d) => d.received_at, from)
  const narrowedDlv = narrowDeliveries(dlvInWindow, { decision, endpointId: agentId, family })
  const dlvForCounts = narrowDeliveries(dlvInWindow, { endpointId: agentId, family })

  const pickedWork = work.items.find((i) => i.agent_id === agentId)
  const pickedDelivery = deliveries.deliveries.find((d) => d.endpoint_id === agentId)
  const pickedAgent = pickedWork?.agent ?? pickedDelivery?.agent ?? null
  const agentLabel = !agentId ? null : pickedWork || !pickedDelivery ? agentName(pickedAgent) : endpointName(pickedDelivery)
  const emptyAgentRow = (id: string): RailAgentRow => ({
    id,
    name: agentLabel ?? agentName(pickedAgent),
    agent: pickedAgent,
    deleted: pickedDelivery ? pickedDelivery.endpoint_kind === "agent" && !pickedAgent : !pickedAgent || Boolean(pickedAgent.deleted),
    count: 0,
    note: "none here",
  })
  const emptyFamilyRow = (f: string) => ({ family: f, count: 0 })

  const workAgents = keepPicked(
    ledgerAgents(narrowWork(workInWindow, { tone, family })).map(
      (a): RailAgentRow => ({ id: a.id, name: a.name, agent: a.agent, deleted: a.state === "deleted", count: a.count, ...AGENT_NOTE[a.state] }),
    ),
    agentId,
    (r) => r.id,
    emptyAgentRow,
  )
  const workFamilies = keepPicked(
    familyCounts(narrowWork(workInWindow, { tone, agentId }).map(workFamily)),
    family,
    (f) => f.family,
    emptyFamilyRow,
  )
  const endpoints = keepPicked(
    endpointHealth(narrowDeliveries(dlvInWindow, { decision, family }), now).map(
      (h): RailAgentRow => ({
        id: h.endpointId,
        name: h.name,
        agent: h.agent,
        deleted: h.verdict === "gone",
        count: h.count,
        note: h.verdict === "ok" ? relTime(h.last) : ENDPOINT_NOTE[h.verdict],
        noteTone: h.verdict === "blocked" ? "text-warn" : undefined,
      }),
    ),
    agentId,
    (r) => r.id,
    emptyAgentRow,
  )
  const dlvFamilies = keepPicked(
    familyCounts(narrowDeliveries(dlvInWindow, { decision, endpointId: agentId }).map(deliveryFamily)),
    family,
    (f) => f.family,
    emptyFamilyRow,
  )

  const statusLabel =
    section === "work"
      ? tone !== "all"
        ? LEDGER_TONE_LABEL[tone]
        : null
      : decision !== "all"
        ? decision === "accepted"
          ? "Accepted"
          : "Ignored"
        : null
  const chips: RailChip[] = [
    statusLabel && {
      key: "status",
      label: statusLabel,
      onRemove: () => (section === "work" ? setTone("all") : setDecision("all")),
    },
    agentLabel && { key: "agent", label: agentLabel, onRemove: () => setAgentId(null) },
    family && { key: "family", label: family, onRemove: () => setFamily(null) },
  ].filter((c): c is RailChip => Boolean(c))
  const narrowedTo = chips.map((c) => c.label).join(" · ") || null

  const openDelivery = deliveries.deliveries.find((d) => d.id === openDeliveryId) ?? null
  // A delivery's "Became" opens its work in the Work queue, panel open.
  function openDeliveryWork(id: string) {
    keepOpenWork.current = true
    setOpenDeliveryId(null)
    switchSection("work")
    setOpenWorkId(id)
  }

  const failure = section === "work" ? work.error : deliveries.error
  const retry = section === "work" ? work.refetch : deliveries.refetch

  const detail = openWorkId ? (
    <WorkItemDetail workspaceId={workspaceId} workItemId={openWorkId} onNavigate={(id) => setOpenWorkId(id)} />
  ) : null

  return (
    <div className="relative flex min-h-0 flex-1 overflow-hidden">
      {isMobile && !railCollapsed && (
        <button
          type="button"
          aria-label="Close filters"
          onClick={() => setRailCollapsed(true)}
          className="absolute inset-0 z-20 bg-black/50"
        />
      )}
      <aside
        className={cn(
          "shrink-0 overflow-hidden border-r border-foreground/[0.06] bg-card transition-all",
          railCollapsed ? "w-9" : "w-[280px]",
          isMobile && !railCollapsed && "absolute inset-y-0 left-0 z-30 shadow-2xl",
        )}
      >
        {railCollapsed ? (
          <div className="flex h-full flex-col items-center pt-1.5">
            <SidebarCollapseButton collapsed onToggle={() => setRailCollapsed(false)} />
          </div>
        ) : (
          // The rail changes its rows the way it enters a routine focus:
          // from the right, 12px, 200ms — and again when the ledger switches.
          <div key={section} className="h-full overflow-y-auto pb-4 animate-in fade-in-0 slide-in-from-right-3 duration-200 ease-out">
            {section === "work" ? (
              <WorkRail
                counts={ledgerCounts(workForCounts)}
                tone={tone}
                onTone={picked(setTone)}
                agents={workAgents}
                agentId={agentId}
                onAgent={picked(setAgentId)}
                families={workFamilies}
                family={family}
                onFamily={picked(setFamily)}
                chips={chips}
                loading={work.loading}
                windowLabel={windowLabel}
                onSection={switchSection}
                onLeave={onLeave}
                onCollapse={() => setRailCollapsed(true)}
              />
            ) : (
              <DeliveriesRail
                counts={{
                  all: dlvForCounts.length,
                  accepted: dlvForCounts.filter((d) => deliveryTone(d) === "accepted").length,
                  ignored: dlvForCounts.filter((d) => deliveryTone(d) === "ignored").length,
                }}
                decision={decision}
                onDecision={picked(setDecision)}
                endpoints={endpoints}
                endpointId={agentId}
                onEndpoint={picked(setAgentId)}
                families={dlvFamilies}
                family={family}
                onFamily={picked(setFamily)}
                chips={chips}
                loading={deliveries.loading}
                windowLabel={windowLabel}
                onSection={switchSection}
                onLeave={onLeave}
                onCollapse={() => setRailCollapsed(true)}
              />
            )}
          </div>
        )}
      </aside>

      <div key={section} className="flex min-w-0 flex-1 animate-in fade-in-0 slide-in-from-right-3 duration-200 ease-out">
        <div className="min-w-0 flex-1 overflow-y-auto">
          {failure && (
            // Without this the cards below would say "nothing failed" about
            // a ledger that never loaded.
            <div
              role="alert"
              className="mx-4 mt-4 flex flex-wrap items-center gap-2 rounded-md border border-destructive/40 bg-destructive/10 px-2.5 py-2 text-[11px] text-destructive md:mx-6"
            >
              <span>
                Could not load the {section === "work" ? "work queue" : "webhook deliveries"}: {failure.message}. What shows below may be out of date.
              </span>
              <button type="button" onClick={() => void retry()} className="w-fit rounded border border-destructive/40 px-2 py-0.5 hover:bg-destructive/10">
                Try again
              </button>
            </div>
          )}
          {section === "work" ? (
            <WorkQueuePage
              workspaceId={workspaceId}
              items={narrowedWork}
              allItems={workInWindow}
              win={win}
              onWin={setWin}
              from={from}
              now={now}
              narrowedTo={narrowedTo}
              capped={work.capped}
              loading={work.loading}
              canResolve={roleAtLeast(role, "MANAGER")}
              onTone={(t) => setTone((cur) => (cur === t ? "all" : t))}
              onOpen={setOpenWorkId}
            />
          ) : (
            <DeliveriesPage
              deliveries={narrowedDlv}
              win={win}
              onWin={setWin}
              from={from}
              now={now}
              narrowedTo={narrowedTo}
              capped={deliveries.capped}
              loading={deliveries.loading}
              openId={openDeliveryId}
              onOpen={setOpenDeliveryId}
              onOpenAgentWork={(id) => {
                switchSection("work")
                setAgentId(id)
              }}
            />
          )}
        </div>
        {!isMobile && section === "deliveries" && openDelivery && (
          <aside
            aria-label="Delivery"
            className="w-[360px] shrink-0 overflow-y-auto border-l border-hairline bg-card/40 p-4 animate-in fade-in-0 slide-in-from-right-3 duration-200 ease-out"
          >
            <DeliveryDetail delivery={openDelivery} onClose={() => setOpenDeliveryId(null)} onOpenWork={openDeliveryWork} />
          </aside>
        )}
        {!isMobile && section === "work" && openWorkId && (
          <aside
            aria-label="Work item detail"
            className="w-[440px] shrink-0 overflow-y-auto border-l border-hairline animate-in fade-in-0 slide-in-from-right-3 duration-200 ease-out"
          >
            <div className="flex justify-end px-2 pt-2">
              <button
                type="button"
                aria-label="Close work item"
                onClick={() => setOpenWorkId(null)}
                className="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
              >
                <X className="h-4 w-4" />
              </button>
            </div>
            {detail}
          </aside>
        )}
      </div>

      {isMobile && (
        <Sheet open={section === "deliveries" && openDelivery != null} onOpenChange={(open) => !open && setOpenDeliveryId(null)}>
          <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-md">
            <SheetHeader>
              <SheetTitle>Delivery</SheetTitle>
            </SheetHeader>
            <div className="px-4 pb-4">
              {openDelivery && <DeliveryDetail delivery={openDelivery} onOpenWork={openDeliveryWork} />}
            </div>
          </SheetContent>
        </Sheet>
      )}
      {isMobile && (
        <Sheet open={section === "work" && Boolean(openWorkId)} onOpenChange={(open) => !open && setOpenWorkId(null)}>
          <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-md">
            <SheetHeader>
              <SheetTitle>Work item</SheetTitle>
            </SheetHeader>
            {detail}
          </SheetContent>
        </Sheet>
      )}
    </div>
  )
}
