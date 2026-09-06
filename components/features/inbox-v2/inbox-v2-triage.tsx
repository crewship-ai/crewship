"use client"

import { useMemo } from "react"
import { Bell, CheckCheck, History, Inbox, Users } from "lucide-react"
import { CrewIcon } from "@/components/ui/crew-icon"
import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { Skeleton } from "@/components/ui/skeleton"
import { StatusPill } from "@/components/ui/status-pill"
import { Appear } from "@/components/ui/detail"
import { since } from "@/components/features/inbox/inbox-derive"
import { cn } from "@/lib/utils"
import { entryTitle, entryVerb, outcomeStatus, sortEntries } from "./inbox-v2-derive"
import { EntryAvatar, entryIdentity } from "./inbox-entry-identity"
import type { InboxLookup, InboxV2Entry } from "./inbox-v2-types"

/** Overview without an open item: actual requests first, followed by readable updates. */
export function InboxTriage({ action, updates, history, lookup, live, onOpen, onCrew, incomplete = false, loading = false }: {
  incomplete?: boolean
  loading?: boolean
  action: InboxV2Entry[]
  updates: InboxV2Entry[]
  history: InboxV2Entry[]
  lookup: InboxLookup
  live: boolean
  onOpen: (entry: InboxV2Entry) => void
  onCrew: (crewId: string) => void
}) {
  const crews = useMemo(() => {
    const buckets = new Map<string, { id: string | null; name: string; color: string | null; icon: string; requests: number; updates: number }>()
    for (const entry of [...action, ...updates]) {
      const { crew } = entryIdentity(entry, lookup)
      const key = crew?.id || "workspace"
      const bucket = buckets.get(key) || { id: crew?.id || null, name: crew?.name || "Workspace", icon: crew?.icon || "users", color: crew?.color || null, requests: 0, updates: 0 }
      if (entry.actionable) bucket.requests++
      else bucket.updates++
      buckets.set(key, bucket)
    }
    return [...buckets.values()].sort((a, b) => b.requests - a.requests || a.name.localeCompare(b.name))
  }, [action, updates, lookup])
  if (loading) return <div className="space-y-4 p-4 lg:p-6" role="status" aria-label="Loading inbox"><Skeleton className="h-12 rounded-lg" /><Skeleton className="h-24 rounded-xl" /><Skeleton className="h-80 rounded-xl" /></div>
  const oldest = [...action].sort((a, b) => Date.parse(a.createdAt) - Date.parse(b.createdAt))[0]
  const recent = [...history].sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt)).slice(0, 3)
  return (
    <div className="mx-auto flex w-full max-w-6xl flex-col gap-4 p-4 lg:p-6" data-testid="inbox-triage">
      <div><h1 className="text-xl font-semibold tracking-tight">Your inbox, at a glance</h1><p className="mt-1 text-body text-muted-foreground">Decisions first. Results and updates in one place.</p></div>
      <Appear order={0}>
        <DashboardCard title="Waiting for you" icon={action.length ? Inbox : CheckCheck} className={action.length ? "border-warn/30 bg-warn/5" : "border-success/20 bg-success/5"} hint={action.length ? `${action.length} need action` : incomplete ? "Partial information" : "All caught up"} action={oldest ? <button type="button" onClick={() => onOpen(oldest)} className="text-primary-hover hover:underline">Open oldest →</button> : undefined}>
          {action.length ? <div className="flex flex-col divide-y divide-border/50">{sortEntries(action).slice(0, 4).map((entry) => <OverviewRow key={entry.key} entry={entry} lookup={lookup} onOpen={onOpen} />)}</div> : <p className="text-body text-muted-foreground">{incomplete ? "Some sources could not be read. Check the notice above before assuming nothing needs you." : "Nothing needs a decision right now. Approvals, questions from agents, failed runs and missed schedules land here."}</p>}
        </DashboardCard>
      </Appear>
      <div className="grid min-w-0 gap-4 xl:grid-cols-[minmax(0,1fr)_260px]">
        <Appear order={1} className="min-w-0">
          <DashboardCard title="Updates" icon={Bell} hint={`${updates.length} available`} className="border-primary/20" action={<StatusPill tone={live ? "success" : "muted"} label={live ? "Live" : "Not live"} />}>
            <p className="mb-3 text-label text-muted-foreground">Agent replies, work ready for review and routine results.</p>
            {updates.length ? <div className="flex flex-col divide-y divide-border/50">{sortEntries(updates).slice(0, 6).map((entry) => <OverviewRow key={entry.key} entry={entry} lookup={lookup} onOpen={onOpen} />)}</div> : <InlineEmpty icon={Bell} text="No updates yet. New results will appear here." />}
          </DashboardCard>
        </Appear>
        <Appear order={2} className="min-w-0">
          <DashboardCard title="By crew" icon={Users} hint="Filter inbox">
            {crews.length ? <div className="flex flex-col gap-2">{crews.map((crew) => <CrewBucket key={crew.id || crew.name} crew={crew} onSelect={onCrew} />)}</div> : <InlineEmpty icon={Users} text="No crew is asking for anything." />}
          </DashboardCard>
        </Appear>
      </div>
      <Appear order={2}>
        <DashboardCard title="Recent history" icon={History} hint="Saved in History">
          {recent.length ? <div className="flex flex-col divide-y divide-border/50">{recent.map((entry) => <OverviewRow key={entry.key} entry={entry} lookup={lookup} onOpen={onOpen} />)}</div> : <p className="text-label text-muted-foreground">Nothing has been decided yet. Decisions and archived notices stay here as the record.</p>}
        </DashboardCard>
      </Appear>
    </div>
  )
}

function OverviewRow({ entry, lookup, onOpen }: { entry: InboxV2Entry; lookup: InboxLookup; onOpen: (entry: InboxV2Entry) => void }) {
  const { crew, name } = entryIdentity(entry, lookup)
  return <button type="button" onClick={() => onOpen(entry)} className="flex w-full min-w-0 items-center gap-3 rounded-lg py-3 text-left transition-colors duration-150 hover:bg-primary/5 focus-visible:outline-2 focus-visible:outline-primary">
    <EntryAvatar entry={entry} lookup={lookup} />
    <span className="min-w-0 flex-1"><span className="mb-1 flex flex-wrap items-center gap-2">{entry.historical && <StatusPill status={outcomeStatus(entry.outcome) || "RESOLVED"} />}<span className="line-clamp-2 text-body font-medium">{entryTitle(entry)}</span></span><span className="block truncate text-label text-muted-foreground">{crew ? `${crew.name} · ` : ""}{name} · {since(entry.createdAt)}</span></span>
    <span className="shrink-0 text-label text-primary-hover">{entry.actionable ? "Review" : entryVerb(entry)} →</span>
  </button>
}

function CrewBucket({ crew, onSelect }: { crew: { id: string | null; name: string; icon: string; color: string | null; requests: number; updates: number }; onSelect: (id: string) => void }) {
  const content = <><CrewIcon icon={crew.icon} color={crew.color} size="md" /><span className="min-w-0"><span className="block truncate text-body font-medium">{crew.name}</span><span className={cn("mt-1 block text-label", crew.requests ? "text-warn" : "text-muted-foreground")}>{crew.requests ? `${crew.requests} need action` : `${crew.updates} ${crew.updates === 1 ? "update" : "updates"}`}</span></span></>
  if (!crew.id) return <div className="flex items-center gap-2.5 rounded-xl border border-border/60 p-3">{content}</div>
  return <button type="button" aria-label={`Filter inbox by ${crew.name}`} onClick={() => onSelect(crew.id!)} className="flex items-center gap-2.5 rounded-xl border border-border/60 p-3 text-left transition-colors duration-150 hover:border-primary/30 hover:bg-primary/5">{content}</button>
}
