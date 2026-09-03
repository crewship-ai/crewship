"use client"

import { useMemo } from "react"
import Link from "next/link"
import { Activity, Bell, History, Inbox, Users } from "lucide-react"

import { AnimatedNumber } from "@/components/ui/animated-number"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { StatusPill } from "@/components/ui/status-pill"
import { Appear } from "@/components/ui/detail"
import { since } from "@/components/features/inbox/inbox-derive"
import { entityHref } from "@/lib/entity-links"
import { crewColor } from "@/app/(dashboard)/dashboard-helpers"
import { cn } from "@/lib/utils"

import { deadlineBucket, entryAgentRef, entryCrewId, entryTitle, outcomeStatus } from "./inbox-v2-derive"
import type { InboxLookup, InboxV2Entry } from "./inbox-v2-types"

/**
 * The reading pane when nothing is open.
 *
 * It used to say "Select an item to see its context and actions." — with 94
 * items waiting, and with none. README §1 asks every screen to answer "what
 * needs me" first and to say so in one line when it cannot; this card is the
 * inbox's answer, and it is never blank: the zero form is the same card with
 * zeros and the one line about what lands here.
 */
export function InboxTriage({
  action, updates, history, lookup, live, onOpen, onCrew,
}: {
  action: InboxV2Entry[]
  updates: InboxV2Entry[]
  history: InboxV2Entry[]
  lookup: InboxLookup
  live: boolean
  onOpen: (entry: InboxV2Entry) => void
  /** Narrow the list to one crew (by its name — the search box matches it). */
  onCrew: (crewName: string) => void
}) {
  const now = Date.now()
  const stats = useMemo(() => {
    const unread = action.filter((e) => e.unread).length
    const expiring = action.filter((e) => {
      const b = deadlineBucket(e, now)
      return b === "hour" || b === "today"
    }).length
    const approvals = action.filter((e) => e.source === "approval" || e.inboxItem?.kind === "waitpoint").length
    const byCrew = new Map<string, { name: string; color: string | null; count: number; unread: number }>()
    for (const e of action) {
      const id = entryCrewId(e)
      const crew = id ? lookup.crewById.get(id) : null
      const key = crew?.name ?? "No crew"
      const cur = byCrew.get(key) ?? { name: key, color: crew?.color ?? null, count: 0, unread: 0 }
      cur.count += 1
      if (e.unread) cur.unread += 1
      byCrew.set(key, cur)
    }
    const crews = [...byCrew.values()].sort((a, b) => b.count - a.count)
    return { unread, expiring, approvals, crews }
  }, [action, lookup, now])

  const oldest = useMemo(
    () => [...action].sort((a, b) => Date.parse(a.createdAt) - Date.parse(b.createdAt))[0] ?? null,
    [action],
  )
  const recent = useMemo(
    () => [...history].sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt)).slice(0, 5),
    [history],
  )
  const max = Math.max(1, ...stats.crews.map((c) => c.count))

  return (
    <div className="mx-auto flex w-full max-w-4xl flex-col gap-3.5 p-4 lg:p-6" data-testid="inbox-triage">
      <Appear order={0}>
        <DashboardCard
          title="Waiting for you"
          icon={Inbox}
          className={cn(action.length > 0 && "border-warn/35")}
          hint={oldest ? <span>{action.length} items · oldest {since(oldest.createdAt)}</span> : <span>nothing waiting</span>}
          action={oldest ? (
            <button type="button" onClick={() => onOpen(oldest)} className="text-primary-hover hover:underline">
              Open oldest →
            </button>
          ) : undefined}
        >
          <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
            <Stat label="Waiting" value={action.length} tone={action.length > 0 ? "text-warn" : undefined} sub={`${stats.unread} unread`} />
            <Stat label="Expiring today" value={stats.expiring} tone={stats.expiring > 0 ? "text-destructive" : undefined} sub={stats.expiring > 0 ? "decide these first" : "nothing on a clock"} />
            <Stat label="Approvals" value={stats.approvals} tone={stats.approvals > 0 ? "text-primary-hover" : undefined} sub="hires and gated runs" />
            <Stat label="Crews asking" value={stats.crews.length} sub={stats.crews.map((c) => c.name).slice(0, 3).join(" · ") || "—"} />
          </div>
          {action.length === 0 && (
            <InlineEmpty
              icon={Inbox}
              className="mt-4"
              text="Approvals, questions from agents, failed runs and missed schedules land here."
              action={<Link href={entityHref({ kind: "routines" })} className="text-primary-hover hover:underline">Routines →</Link>}
            />
          )}
        </DashboardCard>
      </Appear>

      <div className="grid gap-3.5 lg:grid-cols-2">
        <Appear order={1}>
          <DashboardCard title="By crew" icon={Users} hint={stats.crews.length > 0 ? "click a crew to narrow" : undefined}>
            {stats.crews.length === 0 ? (
              <InlineEmpty icon={Users} text="No crew is asking for anything." />
            ) : (
              <div className="flex flex-col gap-2.5">
                {stats.crews.map((c) => (
                  <button
                    key={c.name}
                    type="button"
                    onClick={() => onCrew(c.name)}
                    className="group flex items-center gap-3 rounded-md text-left hover:bg-foreground/[0.03]"
                  >
                    <span className="flex w-28 shrink-0 items-center gap-1.5 truncate text-label">
                      <span className="h-2 w-2 shrink-0 rounded-full" style={{ background: crewColor(c.color) }} aria-hidden />
                      <span className="truncate group-hover:underline">{c.name}</span>
                    </span>
                    <span className="h-1.5 flex-1 overflow-hidden rounded-full bg-foreground/[0.08]">
                      <span className="block h-full rounded-full" style={{ width: `${Math.round((c.count / max) * 100)}%`, background: crewColor(c.color) }} />
                    </span>
                    <span className="w-6 shrink-0 text-right font-mono text-label tabular-nums text-muted-foreground">{c.count}</span>
                    <span className="w-24 shrink-0 truncate text-micro text-muted-foreground">{c.unread > 0 ? `${c.unread} unread` : "all read"}</span>
                  </button>
                ))}
              </div>
            )}
          </DashboardCard>
        </Appear>

        <Appear order={2}>
          <DashboardCard
            title="Updates"
            icon={Bell}
            hint={<span className="inline-flex items-center gap-1.5"><StatusPill tone={live ? "success" : "muted"} label={live ? "Live" : "Not live"} live={live} /></span>}
          >
            {updates.length === 0 ? (
              <InlineEmpty
                icon={Activity}
                text="No updates yet — agent replies, routine progress and issue reviews land here."
                action={<Link href={entityHref({ kind: "chat", agentSlug: "" }).replace(/\/$/, "")} className="text-primary-hover hover:underline">Chat →</Link>}
              />
            ) : (
              <div className="flex flex-col">
                {updates.slice(0, 5).map((e) => (
                  <button key={e.key} type="button" onClick={() => onOpen(e)} className="flex items-center gap-2.5 border-b border-border/50 py-2 text-left last:border-0 hover:bg-foreground/[0.025]">
                    <span className="min-w-0 flex-1 truncate text-body">{entryTitle(e)}</span>
                    <span className="shrink-0 text-micro text-muted-foreground">{since(e.createdAt)}</span>
                  </button>
                ))}
              </div>
            )}
          </DashboardCard>
        </Appear>
      </div>

      <Appear order={3}>
        <DashboardCard
          title="Recently decided"
          icon={History}
          hint={history.length > 0 ? `${history.length} in history` : undefined}
        >
          {recent.length === 0 ? (
            <InlineEmpty icon={History} text="Nothing has been decided yet. Decisions and archived notices stay here as the record." />
          ) : (
            <div className="flex flex-col">
              {recent.map((e) => {
                const ref = entryAgentRef(e)
                const agent = ref.slug ? lookup.agentBySlug.get(ref.slug) : null
                return (
                  <button key={e.key} type="button" onClick={() => onOpen(e)} className="flex items-center gap-2.5 border-b border-border/50 py-2 text-left last:border-0 hover:bg-foreground/[0.025]">
                    <StatusPill status={outcomeStatus(e.outcome) ?? "RESOLVED"} />
                    <span className="min-w-0 flex-1 truncate text-body">{entryTitle(e)}</span>
                    <span className="flex shrink-0 items-center gap-1.5 text-micro text-muted-foreground">
                      {agent && (
                        <AgentAvatar seed={agent.avatar_seed || agent.slug} style={agent.avatar_style} agentId={agent.id} avatarUrl={agent.avatar_url} alt="" className="h-4 w-4 rounded-[5px]" />
                      )}
                      {agent?.name ?? ref.label} · {since(e.createdAt)}
                    </span>
                  </button>
                )
              })}
            </div>
          )}
        </DashboardCard>
      </Appear>
    </div>
  )
}

function Stat({ label, value, sub, tone }: { label: string; value: number; sub?: string; tone?: string }) {
  return (
    <div className="flex flex-col gap-1">
      <span className="text-label text-muted-foreground">{label}</span>
      <span className={cn("text-[26px] font-bold leading-none tracking-tight tabular-nums", tone)}>
        <AnimatedNumber value={value} />
      </span>
      {sub && <span className="truncate text-micro text-muted-foreground">{sub}</span>}
    </div>
  )
}
