"use client"

import { resolveRoutineIcon, resolveRoutineColor } from "@/lib/routine-identity"
import { Bell, CircleDot } from "lucide-react"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CrewIcon } from "@/components/ui/crew-icon"
import { ActorAvatar } from "@/components/features/inbox/inbox-actor"
import { payloadString, subjectOf } from "@/components/features/inbox/inbox-derive"
import { entryAgentRef, entryCrewId, filterAndSortEntries, matchesSearch, type InboxV2Filters } from "./inbox-v2-derive"
import { resolveAgent, type InboxLookup, type InboxV2Entry } from "./inbox-v2-types"

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

export function EntryAvatar({ entry, lookup, compact = false }: { entry: InboxV2Entry; lookup: InboxLookup; compact?: boolean }) {
  const box = compact ? "h-5 w-5 shrink-0 rounded [&_svg]:h-3 [&_svg]:w-3" : "h-8 w-8 shrink-0 rounded-lg"
  const { agent, crew, actor, routine } = entryIdentity(entry, lookup)
  if (agent) return <AgentAvatar seed={agent.avatar_seed || agent.slug} style={agent.avatar_style} agentId={agent.id} avatarUrl={agent.avatar_url} alt="" className={`${box} bg-muted`} />
  if (routine) return <CrewIcon icon={resolveRoutineIcon(routine)} color={resolveRoutineColor(routine)} size="sm" className={box} />
  if (crew && entry.inboxItem?.payload?.mission_id) return <CrewIcon icon={crew.icon || "users"} color={crew.color} size="sm" className={box} />
  if (actor && actor.kind !== "crew") return <ActorAvatar actor={actor} size={compact ? 20 : 32} />
  if (crew) return <CrewIcon icon={crew.icon || "users"} color={crew.color} size="sm" className={box} />
  const Icon = entry.source === "mission" ? CircleDot : Bell
  return <span className={`flex items-center justify-center bg-primary/10 text-primary-hover ${box}`}><Icon className="h-4 w-4" aria-hidden /></span>
}

/** Search the names shown on screen, and use the same crew resolution for filtering. */
export function filterInboxEntries(entries: InboxV2Entry[], filters: InboxV2Filters, lookup: InboxLookup) {
  const search = filters.search.trim().toLowerCase()
  return filterAndSortEntries(entries, { ...filters, crew: null, search: "" }).filter((entry) => {
    const { crew, agent, name } = entryIdentity(entry, lookup)
    if (filters.crew && crew?.id !== filters.crew) return false
    return !search || matchesSearch(entry, search) || `${crew?.name || ""} ${crew?.slug || ""} ${agent?.name || ""} ${agent?.slug || ""} ${name}`.toLowerCase().includes(search)
  })
}
