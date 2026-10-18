"use client"

import { useMemo } from "react"
import { Archive, ChevronRight, Clock3, Inbox, Users, type LucideIcon } from "lucide-react"

import { remainingLabel } from "@/components/features/inbox/inbox-derive"
import { CrewIcon } from "@/components/ui/crew-icon"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { StatusPill } from "@/components/ui/status-pill"
import { cn } from "@/lib/utils"

import { EntryAvatar, entryIdentity } from "./inbox-entry-identity"
import {
  ageLabel, deadlineBucket, entryAttention, entryKindPill, entryTitle, INBOX_ATTENTION, outcomeStatus, sortEntries,
  type InboxAttentionKey,
} from "./inbox-v2-derive"
import type { InboxLookup, InboxV2Entry } from "./inbox-v2-types"

/**
 * Overview — "Your inbox, at a glance" (Harbor 2 v67). What the reading pane
 * shows while nothing is open: on desktop it replaces the old automatic
 * preview of the first row, which marked nothing read but put a live Approve
 * button for an item nobody chose in front of the reader.
 *
 * Four attention tiles (each one narrows the list to that class), the load by
 * crew, what expires soon and the last decisions. A Dashboard link
 * (`?attention=`) adds a card with exactly the rows it promised.
 */

const TILE_TONE: Record<InboxAttentionKey, string> = {
  decision: "bg-chip-warn-bg text-chip-warn-fg",
  input: "bg-chip-info-bg text-chip-info-fg",
  review: "bg-chip-violet-bg text-chip-violet-fg",
  repair: "bg-chip-danger-bg text-chip-danger-fg",
}

export interface InboxOverviewProps {
  action: InboxV2Entry[]
  updates: InboxV2Entry[]
  history: InboxV2Entry[]
  lookup: InboxLookup
  onOpen: (entry: InboxV2Entry) => void
  /** A tile: narrow To handle to one attention class. */
  onAttention: (attention: InboxAttentionKey) => void
  /** A crew row: narrow the list to that crew. */
  onCrew: (crewId: string) => void
  /** Some source could not be read: never claim there is nothing to do. */
  incomplete?: boolean
  /** A Dashboard attention link: the rows it promised, and a way back. */
  focus?: { label: string; entries: InboxV2Entry[]; onClear: () => void }
}

export function InboxOverview({ action, updates, history, lookup, onOpen, onAttention, onCrew, incomplete = false, focus }: InboxOverviewProps) {
  const unread = useMemo(() => [...action, ...updates].filter((entry) => entry.unread).length, [action, updates])
  const expiringHour = action.filter((entry) => entry.deadlineAt && deadlineBucket(entry) === "hour").length
  const byAttention = useMemo(() => {
    const out: Record<InboxAttentionKey, number> = { decision: 0, input: 0, review: 0, repair: 0 }
    for (const entry of action) {
      const a = entryAttention(entry)
      if (a) out[a] += 1
    }
    return out
  }, [action])
  const crews = useMemo(() => {
    const buckets = new Map<string, { id: string; name: string; icon: string; color: string | null; count: number }>()
    for (const entry of [...action, ...updates]) {
      const { crew } = entryIdentity(entry, lookup)
      if (!crew) continue
      const bucket = buckets.get(crew.id) ?? { id: crew.id, name: crew.name, icon: crew.icon || "users", color: crew.color, count: 0 }
      bucket.count += 1
      buckets.set(crew.id, bucket)
    }
    return [...buckets.values()].sort((a, b) => b.count - a.count || a.name.localeCompare(b.name))
  }, [action, updates, lookup])
  const maxCrew = crews[0]?.count ?? 0
  const expiring = sortEntries(action.filter((entry) => entry.deadlineAt)).slice(0, 5)
  const recent = [...history].sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt)).slice(0, 3)
  const meta = [
    `${action.length} to handle`,
    `${unread} unread`,
    expiringHour > 0 ? `${expiringHour} ${expiringHour === 1 ? "expires" : "expire"} within an hour` : null,
  ].filter(Boolean).join(" · ")

  return (
    <div className="mx-auto flex w-full max-w-[860px] flex-col gap-4 px-4 py-5 sm:px-6 lg:py-6" data-testid="inbox-overview">
      <div>
        <h1 className="text-title font-semibold tracking-tight">Your inbox, at a glance</h1>
        <p className="mt-1 text-body text-muted-foreground">{meta}</p>
        {incomplete && (
          <p className="mt-1 text-label text-warn">Partial information — some sources could not be read, so this may not be everything.</p>
        )}
      </div>

      <div className="grid grid-cols-2 gap-2.5 xl:grid-cols-4">
        {INBOX_ATTENTION.map((a) => (
          <button
            key={a.key}
            type="button"
            onClick={() => onAttention(a.key)}
            aria-label={`${a.label}: ${byAttention[a.key]}`}
            className="kit-tap group flex min-w-0 items-center gap-3 rounded-card border border-border bg-card px-3.5 py-3 text-left text-muted-foreground transition-colors hover:border-line-strong"
          >
            <span className={cn("grid h-[34px] w-[34px] shrink-0 place-items-center rounded-lg text-heading font-semibold tabular-nums", TILE_TONE[a.key])}>
              {byAttention[a.key]}
            </span>
            <span className="min-w-0 flex-1">
              <b className="block truncate text-control font-semibold text-foreground">{a.label}</b>
              <small className="block truncate text-label">{a.hint}</small>
            </span>
            <ChevronRight className="hidden h-3.5 w-3.5 shrink-0 sm:block" aria-hidden />
          </button>
        ))}
      </div>

      {focus && (
        <OverviewCard icon={Inbox} title={focus.label} right={<button type="button" onClick={focus.onClear} className="text-label text-primary-hover hover:underline">All inbox →</button>} testId="inbox-focus-overview">
          {focus.entries.length ? focus.entries.map((entry) => (
            <OverviewLine key={entry.key} entry={entry} lookup={lookup} onOpen={onOpen} />
          )) : <InlineEmpty icon={Inbox} text="Nothing matches this filter right now." />}
        </OverviewCard>
      )}

      <OverviewCard icon={Users} title="By crew">
        {crews.length ? crews.map((crew) => (
          <button
            key={crew.id}
            type="button"
            onClick={() => onCrew(crew.id)}
            aria-label={`Filter inbox by ${crew.name}`}
            className="row-hover flex w-full min-w-0 items-center gap-3 px-4 py-2.5 text-left"
          >
            <CrewIcon icon={crew.icon} color={crew.color} size="sm" className="h-[26px] w-[26px] shrink-0 rounded-md" />
            <span className="min-w-0 flex-1">
              <b className="block truncate text-body font-medium">{crew.name}</b>
              <span className="block text-label text-muted-foreground">{crew.count} {crew.count === 1 ? "item" : "items"}</span>
            </span>
            <span className="hidden h-1.5 w-40 shrink-0 overflow-hidden rounded-full bg-muted sm:block" aria-hidden>
              <span className="block h-full rounded-full bg-primary" style={{ width: `${maxCrew ? (crew.count / maxCrew) * 100 : 0}%` }} />
            </span>
          </button>
        )) : <InlineEmpty icon={Users} text="No crew is asking for anything." />}
      </OverviewCard>

      <OverviewCard icon={Clock3} title="Expiring soon">
        {expiring.length ? expiring.map((entry) => {
          const mins = Math.round((Date.parse(entry.deadlineAt!) - Date.now()) / 60_000)
          return (
            <OverviewLine
              key={entry.key}
              entry={entry}
              lookup={lookup}
              onOpen={onOpen}
              sub={entryKindPill(entry).label}
              right={<span className="inline-flex items-center gap-1 whitespace-nowrap text-label font-semibold text-warn"><Clock3 className="h-3.5 w-3.5" aria-hidden />{mins > 0 ? `in ${remainingLabel(mins)}` : "expired"}</span>}
            />
          )
        }) : <p className="px-4 py-3 text-control text-muted-foreground">{incomplete ? "Some sources could not be read — check the notice above." : "Nothing expires soon."}</p>}
      </OverviewCard>

      <OverviewCard icon={Archive} title="Recent history">
        {recent.length ? recent.map((entry) => (
          <OverviewLine
            key={entry.key}
            entry={entry}
            lookup={lookup}
            onOpen={onOpen}
            sub={ageLabel(entry.createdAt)}
            right={<StatusPill status={outcomeStatus(entry.outcome) || "RESOLVED"} />}
          />
        )) : <p className="px-4 py-3 text-control text-muted-foreground">Nothing has been decided yet. Decisions and archived notices stay here as the record.</p>}
      </OverviewCard>
    </div>
  )
}

function OverviewCard({ icon: Icon, title, right, testId, children }: { icon: LucideIcon; title: string; right?: React.ReactNode; testId?: string; children: React.ReactNode }) {
  return (
    <section className="overflow-hidden rounded-card border border-border bg-card" data-testid={testId} aria-label={title}>
      <header className="flex items-center gap-2.5 px-4.5 pb-2 pt-3.5">
        <span className="icon-tile grid size-[28px] place-items-center rounded-md"><Icon className="h-4 w-4" aria-hidden /></span>
        <h2 className="text-control font-semibold tracking-normal">{title}</h2>
        <span className="flex-1" />
        {right}
      </header>
      <div className="divide-y divide-border border-t border-border">{children}</div>
    </section>
  )
}

function OverviewLine({ entry, lookup, onOpen, sub, right }: { entry: InboxV2Entry; lookup: InboxLookup; onOpen: (entry: InboxV2Entry) => void; sub?: string; right?: React.ReactNode }) {
  const { crew, name } = entryIdentity(entry, lookup)
  return (
    <button type="button" onClick={() => onOpen(entry)} className="row-hover flex w-full min-w-0 items-center gap-3 px-4 py-2.5 text-left">
      <EntryAvatar entry={entry} lookup={lookup} size={26} />
      <span className="min-w-0 flex-1">
        <b className="block truncate text-body font-medium">{entryTitle(entry)}</b>
        <span className="block truncate text-label text-muted-foreground">{sub ?? `${crew ? `${crew.name} · ` : ""}${name} · ${ageLabel(entry.createdAt)}`}</span>
      </span>
      {right}
    </button>
  )
}
