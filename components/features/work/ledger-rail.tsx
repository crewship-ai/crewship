"use client"

// The Activity rail, switched to a ledger (#3012).
//
// Opening the Work queue or Webhook deliveries used to swap the whole page for
// a table under a "Back to activity" bar, with no rail at all. The rail now
// stays and changes its rows instead — the way it narrows to one routine
// (#2998): the ledger's status rows in the Activity words and colours (the
// same StatusRows), then who holds the work (agents with their own avatars)
// and what arrived (event families). Every row narrows the page beside it; an
// agent or event picked again clears it, and each narrowing shows as a chip
// that removes it — also when its row has left the window.

import * as React from "react"
import {
  Activity,
  AlertCircle,
  CheckCircle2,
  ChevronLeft,
  CircleSlash,
  ClipboardList,
  Clock,
  Layers,
  Webhook,
  XCircle,
} from "lucide-react"

import { CountPill, StatusRows, type StatusRow } from "@/components/features/activity-stream/activity-status-rows"
import {
  SidebarActiveChip,
  SidebarActiveChips,
  SidebarCollapseButton,
  SidebarRow,
  SidebarSection,
} from "@/components/layout/sidebar-kit"
import type { LedgerAgent } from "@/hooks/use-work-items"
import {
  LEDGER_TONE_LABEL,
  LEDGER_TONE_TEXT,
  LEDGER_TONES,
  type DeliveryTone,
  type LedgerCounts,
  type LedgerTone,
} from "@/lib/work-ledger"
import { cn } from "@/lib/utils"
import { LedgerAvatar } from "./ledger-parts"

export type LedgerSection = "work" | "deliveries"

const TONE_ICON: Record<LedgerTone | "all", React.ComponentType<{ className?: string }>> = {
  all: Layers,
  needs: AlertCircle,
  line: Clock,
  running: Activity,
  done: CheckCircle2,
  failed: XCircle,
  cancelled: CircleSlash,
}

export interface RailAgentRow {
  id: string
  name: string
  agent: LedgerAgent | null
  count: number
  /** A short word beside the name: "blocked", "idle", "2 m", "deleted". */
  note: string
  noteTone?: string
}

function AgentRows({
  rows,
  value,
  onPick,
  label,
}: {
  rows: RailAgentRow[]
  value: string | null
  onPick: (id: string | null) => void
  label: string
}) {
  if (rows.length === 0) return null
  return (
    <SidebarSection label={label} count={rows.length}>
      {rows.map((a) => {
        const on = value === a.id
        const live = a.agent != null && !a.agent.deleted
        return (
          <SidebarRow
            key={a.id}
            as="div"
            selected={on}
            onSelect={() => onPick(on ? null : a.id)}
            aria-label={`${a.name}, ${a.note}, ${a.count}`}
          >
            <LedgerAvatar agent={a.agent} className="h-4 w-4" />
            <span className={cn("min-w-0 flex-1 truncate", live ? "text-foreground/80" : "text-muted-foreground line-through")}>{a.name}</span>
            <span className={cn("shrink-0 text-[10.5px]", a.noteTone ?? "text-muted-foreground-soft")}>{a.note}</span>
            <CountPill count={a.count} selected={on} />
          </SidebarRow>
        )
      })}
    </SidebarSection>
  )
}

function FamilyRows({
  rows,
  value,
  onPick,
}: {
  rows: { family: string; count: number }[]
  value: string | null
  onPick: (f: string | null) => void
}) {
  if (rows.length === 0) return null
  return (
    <SidebarSection label="Events" count={rows.length}>
      {rows.map((f) => {
        const on = value === f.family
        return (
          <SidebarRow key={f.family} as="div" selected={on} onSelect={() => onPick(on ? null : f.family)} aria-label={`${f.family}, ${f.count}`}>
            <Webhook className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
            <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-foreground/80">{f.family}</span>
            <CountPill count={f.count} selected={on} />
          </SidebarRow>
        )
      })}
    </SidebarSection>
  )
}

const LEDGERS = [
  ["work", "Work queue", ClipboardList],
  ["deliveries", "Deliveries", Webhook],
] as const

function Header({
  section,
  onSection,
  onLeave,
  onCollapse,
  subtitle,
}: {
  section: LedgerSection
  onSection: (s: LedgerSection) => void
  onLeave: () => void
  onCollapse: () => void
  subtitle: string
}) {
  return (
    <>
      <div className="flex items-center gap-1 border-b border-foreground/[0.06] px-2 py-1.5">
        <button
          type="button"
          onClick={onLeave}
          className="inline-flex items-center gap-1 rounded-md px-1.5 py-1 text-xs text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
        >
          <ChevronLeft className="h-3.5 w-3.5" />
          All activity
        </button>
        <span className="flex-1" />
        <SidebarCollapseButton collapsed={false} onToggle={onCollapse} />
      </div>
      <div className="flex flex-col gap-1.5 px-3 pb-1 pt-2">
        <p className="eyebrow text-muted-foreground">Ledger</p>
        {/* Two pages under one address (?section=): navigation, so it says
            which page is current rather than acting as tabs. */}
        <nav aria-label="Ledger" className="flex rounded-md border border-border p-0.5 text-xs">
          {LEDGERS.map(([key, label, Icon]) => (
            <button
              key={key}
              type="button"
              aria-current={section === key ? "page" : undefined}
              onClick={() => section !== key && onSection(key)}
              className={cn(
                "inline-flex flex-1 items-center justify-center gap-1.5 rounded px-2 py-1 transition-colors",
                section === key ? "bg-foreground/[0.08] font-medium text-foreground" : "text-muted-foreground hover:text-foreground",
              )}
            >
              <Icon className="h-3.5 w-3.5" />
              {label}
            </button>
          ))}
        </nav>
        <p className="text-[10.5px] text-muted-foreground-soft">{subtitle}</p>
      </div>
    </>
  )
}

/** One removable chip per narrowing the page carries. */
export interface RailChip {
  key: string
  label: string
  onRemove: () => void
}

function Chips({ chips }: { chips: RailChip[] }) {
  return (
    <SidebarActiveChips className="px-3 pt-1">
      {chips.map((c) => (
        <SidebarActiveChip key={c.key} onRemove={c.onRemove}>
          {c.label}
        </SidebarActiveChip>
      ))}
    </SidebarActiveChips>
  )
}

function Loading({ what }: { what: string }) {
  return <p className="px-3 py-2 text-[11px] text-muted-foreground-soft">Loading {what}…</p>
}

export interface WorkRailProps {
  counts: LedgerCounts
  tone: LedgerTone | "all"
  onTone: (t: LedgerTone | "all") => void
  agents: RailAgentRow[]
  agentId: string | null
  onAgent: (id: string | null) => void
  families: { family: string; count: number }[]
  family: string | null
  onFamily: (f: string | null) => void
  chips: RailChip[]
  loading: boolean
  windowLabel: string
  onSection: (s: LedgerSection) => void
  onLeave: () => void
  onCollapse: () => void
}

export function WorkRail(p: WorkRailProps) {
  const rows: StatusRow<LedgerTone | "all">[] = [
    { key: "all", label: "All", count: p.counts.total, tone: "text-muted-foreground", icon: TONE_ICON.all },
    ...LEDGER_TONES.map((t) => ({ key: t, label: LEDGER_TONE_LABEL[t], count: p.counts[t], tone: LEDGER_TONE_TEXT[t], icon: TONE_ICON[t] })),
  ]
  return (
    <div className="flex flex-col">
      <Header
        section="work"
        onSection={p.onSection}
        onLeave={p.onLeave}
        onCollapse={p.onCollapse}
        subtitle={`${p.counts.total} ${p.counts.total === 1 ? "piece" : "pieces"} of work · ${p.windowLabel}`}
      />
      <Chips chips={p.chips} />
      <SidebarSection label="Status" count={rows.length} className="border-b border-foreground/[0.06] pb-1">
        <StatusRows rows={rows} scope={p.tone} onPick={p.onTone} />
      </SidebarSection>
      {p.loading && p.counts.total === 0 ? (
        <Loading what="work" />
      ) : (
        <>
          <AgentRows label="Agents" rows={p.agents} value={p.agentId} onPick={p.onAgent} />
          <FamilyRows rows={p.families} value={p.family} onPick={p.onFamily} />
        </>
      )}
    </div>
  )
}

export interface DeliveriesRailProps {
  counts: { all: number; accepted: number; ignored: number }
  decision: DeliveryTone | "all"
  onDecision: (d: DeliveryTone | "all") => void
  endpoints: RailAgentRow[]
  endpointId: string | null
  onEndpoint: (id: string | null) => void
  families: { family: string; count: number }[]
  family: string | null
  onFamily: (f: string | null) => void
  chips: RailChip[]
  loading: boolean
  windowLabel: string
  onSection: (s: LedgerSection) => void
  onLeave: () => void
  onCollapse: () => void
}

export function DeliveriesRail(p: DeliveriesRailProps) {
  const rows: StatusRow<DeliveryTone | "all">[] = [
    { key: "all", label: "All", count: p.counts.all, tone: "text-muted-foreground", icon: Layers },
    { key: "accepted", label: "Accepted", count: p.counts.accepted, tone: "text-success", icon: CheckCircle2 },
    { key: "ignored", label: "Ignored", count: p.counts.ignored, tone: "text-muted-foreground", icon: CircleSlash },
  ]
  return (
    <div className="flex flex-col">
      <Header
        section="deliveries"
        onSection={p.onSection}
        onLeave={p.onLeave}
        onCollapse={p.onCollapse}
        subtitle={`${p.counts.all} received · ${p.windowLabel}`}
      />
      <Chips chips={p.chips} />
      <SidebarSection label="Decision" count={rows.length} className="border-b border-foreground/[0.06] pb-1">
        <StatusRows rows={rows} scope={p.decision} onPick={p.onDecision} label="Decision" />
      </SidebarSection>
      {p.loading && p.counts.all === 0 ? (
        <Loading what="deliveries" />
      ) : (
        <>
          <AgentRows label="Endpoints" rows={p.endpoints} value={p.endpointId} onPick={p.onEndpoint} />
          <FamilyRows rows={p.families} value={p.family} onPick={p.onFamily} />
        </>
      )}
    </div>
  )
}
