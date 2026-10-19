"use client"

import { resolveRoutineIcon, resolveRoutineColor } from "@/lib/routine-identity"
import { Bell, CalendarClock, CircleDot } from "lucide-react"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CrewIcon } from "@/components/ui/crew-icon"
import { ActorAvatar } from "@/components/features/inbox/inbox-actor"
import { payloadString, subjectOf } from "@/components/features/inbox/inbox-derive"
import { entryAgentRef, entryCrewId, filterEntries, matchesSearch, sortInboxEntries, type InboxSort, type InboxV2Filters } from "./inbox-v2-derive"
import { resolveAgent, type InboxAgentRef, type InboxLookup, type InboxV2Entry } from "./inbox-v2-types"

export function entryIdentity(entry: InboxV2Entry, lookup: InboxLookup) {
  const item = entry.inboxItem
  const mission = entry.mission || (item ? lookup.missionById?.get(payloadString(item, "mission_id")) : undefined)
  const issue = mission ? lookup.issueById?.get(mission.id) : undefined
  const assignedId = issue?.assignee_type === "agent" ? issue.assignee_id : entry.source === "mission" ? entry.task?.assigned_agent_id : null
  const agent = resolveAgent(lookup, entryAgentRef(entry)) || (assignedId ? lookup.agentById.get(assignedId) : null) || null
  const routineSlug = item ? payloadString(item, "pipeline_slug") || (item.sender_type === "pipeline" ? item.sender_name || "" : "") : ""
  const routine = lookup.routineBySlug?.get(routineSlug)
  const crewId = entryCrewId(entry) || mission?.crew_id || routine?.author_crew_id
  const crew = crewId ? lookup.crewById.get(crewId) : [...lookup.crewById.values()].find((c) => c.slug === agent?.crew?.slug)
  const actor = item ? subjectOf(item) : null
  if (actor?.kind === "routine" && routine) actor.label = routine.name
  return { agent, crew, actor, routine, name: agent?.name || (mission ? "Issue" : actor?.label) || (entry.source === "group" ? "Grouped updates" : entry.source === "mission" ? "Issue" : "Workspace") }
}

/** Sizes from the Harbor 2 inbox: 20 compact, 26 in a list row, 32 in the detail head. */
const AVATAR_BOX: Record<20 | 26 | 32, string> = {
  20: "h-5 w-5 shrink-0 rounded [&_svg]:h-3 [&_svg]:w-3",
  26: "h-[26px] w-[26px] shrink-0 rounded-md [&_svg]:h-3.5 [&_svg]:w-3.5",
  32: "h-8 w-8 shrink-0 rounded-lg",
}

export function EntryAvatar({ entry, lookup, compact = false, size }: { entry: InboxV2Entry; lookup: InboxLookup; compact?: boolean; size?: 20 | 26 | 32 }) {
  const px = size ?? (compact ? 20 : 32)
  const box = AVATAR_BOX[px]
  compact = px === 20
  const { agent, crew, actor, routine } = entryIdentity(entry, lookup)
  if (entry.inboxItem?.kind === "schedule_missed" || entry.inboxItem?.kind === "schedule_circuit_breaker_tripped") {
    // One colour per row: severity lives in the kind pill, not here.
    return <span className={`${box} icon-tile flex items-center justify-center`}><CalendarClock className={px === 32 ? "h-4 w-4" : "h-3 w-3"} aria-hidden /></span>
  }
  if (agent) return <AgentAvatar seed={agent.avatar_seed || agent.slug} style={agent.avatar_style} avatarUrl={agent.avatar_url} alt="" className={`${box} bg-muted`} />
  if (routine) return <CrewIcon icon={resolveRoutineIcon(routine)} color={resolveRoutineColor(routine)} size="sm" className={box} />
  if (crew && entry.inboxItem?.payload?.mission_id) return <CrewIcon icon={crew.icon || "users"} color={crew.color} size="sm" className={box} />
  if (actor && actor.kind !== "crew") return <ActorAvatar actor={actor} size={px === 26 ? 24 : px} />
  if (crew) return <CrewIcon icon={crew.icon || "users"} color={crew.color} size="sm" className={box} />
  const Icon = entry.source === "mission" ? CircleDot : Bell
  return <span className={`flex items-center justify-center bg-primary/10 text-primary-hover ${box}`}><Icon className="h-4 w-4" aria-hidden /></span>
}

/**
 * Is this row from that agent? `ref` is an id or a slug — old /approvals
 * links send the id, agent pages the slug. The roster resolves either; a
 * sender the roster does not know still matches by the key the row carries.
 */
export function entryFromAgent(entry: InboxV2Entry, ref: string, lookup: InboxLookup): boolean {
  const agent = entryIdentity(entry, lookup).agent
  if (agent && (agent.id === ref || agent.slug === ref)) return true
  const raw = entryAgentRef(entry)
  return raw.id === ref || raw.slug === ref
}

/** The roster agent a facet value or a link names, by id first, then slug. */
export function agentByRef(lookup: InboxLookup, ref: string): InboxAgentRef | null {
  return lookup.agentById.get(ref) ?? lookup.agentBySlug.get(ref) ?? null
}

/**
 * Search the names shown on screen, and use the same crew resolution for filtering.
 *
 * `serverMatches` is the set of inbox item ids the server's search returned
 * for this text (GET /api/v1/inbox?q=): it searches every row, not just the
 * loaded ones, so an inbox row (or a group of them) answers to it instead of
 * to the text. Approval-queue rows and issue tasks are not inbox rows, are
 * fully loaded, and are still matched here. Null: no server search, match
 * everything here.
 */
export function filterInboxEntries(
  entries: InboxV2Entry[],
  filters: InboxV2Filters,
  lookup: InboxLookup,
  sort: InboxSort = "urgency",
  serverMatches: ReadonlySet<string> | null = null,
) {
  const search = filters.search.trim().toLowerCase()
  const kept = filterEntries(entries, { ...filters, crew: null, search: "" }).filter((entry) => {
    const { crew, agent, name } = entryIdentity(entry, lookup)
    if (filters.crew && crew?.id !== filters.crew) return false
    if (filters.agent && !entryFromAgent(entry, filters.agent, lookup)) return false
    if (!search) return true
    if (serverMatches) {
      const ids = entry.groupedItems?.map((item) => item.id) ?? (entry.inboxItem ? [entry.inboxItem.id] : null)
      if (ids) return ids.some((id) => serverMatches.has(id))
    }
    return matchesSearch(entry, search) || `${crew?.name || ""} ${crew?.slug || ""} ${agent?.name || ""} ${agent?.slug || ""} ${name}`.toLowerCase().includes(search)
  })
  return sortInboxEntries(kept, sort, (entry) => entryIdentity(entry, lookup).crew?.name ?? "")
}
