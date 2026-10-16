"use client"

import { Users } from "lucide-react"
import { useContentPresence, type ContentPresenceContext, type PresenceMember } from "@/hooks/use-content-presence"
import { Avatar, AvatarFallback } from "@/components/ui/avatar"
import { Button } from "@/components/ui/button"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"

export function ContentPresence({ context, enabled = true }: { context: ContentPresenceContext; enabled?: boolean }) {
  const presence = useContentPresence(context, enabled)
  if (!enabled) return null
  return <ContentPresenceStatus presence={presence} context={context} />
}
interface Participant {
  id: string
  name: string
  positions: number[]
  joining: boolean
  tabs: number
}
function participants(members: PresenceMember[], self: string | undefined, kind: ContentPresenceContext["kind"]): Participant[] {
  const people = new Map<string, PresenceMember[]>()
  for (const member of members) people.set(member.user_id, [...(people.get(member.user_id) ?? []), member])
  return Array.from(people, ([id, tabs]) => {
    const names = tabs.map(tab => tab.display_name).filter((name): name is string => Boolean(name)).sort()
    const name = names[0] ? `${names[0]}${id === self ? " (you)" : ""}` : id === self ? "You" : "Viewer"
    const values = tabs.map(tab => kind === "artifact" ? tab.revision : tab.board_seq)
    return { id, name, positions: Array.from(new Set(values.filter((position): position is number => position !== null))).sort((a, b) => a - b), joining: values.includes(null), tabs: tabs.length }
  }).sort((a, b) => (a.id === self ? -1 : b.id === self ? 1 : a.id.localeCompare(b.id)))
}
function positionLabel(person: Participant, kind: ContentPresenceContext["kind"]) {
  const noun = kind === "artifact" ? "revision" : "saved sequence"
  const known = person.positions.length ? `${noun}${person.positions.length > 1 ? "s" : ""} ${person.positions.join(", ")}` : "Joining…"
  return `${known}${person.joining && person.positions.length ? " · another tab joining" : ""}${person.tabs > 1 ? ` · ${person.tabs} tabs` : ""}`
}
function initials(name: string) {
  return name.replace(/ \(you\)$/, "").split(/\s+/).slice(0, 2).map(part => Array.from(part)[0] ?? "").join("").toLocaleUpperCase()
}
export function ContentPresenceStatus({ presence, context }: { presence: ReturnType<typeof useContentPresence>; context: ContentPresenceContext }) {
  const location = context.kind === "artifact" ? `revision ${context.revision}` : `saved sequence ${context.boardSeq}`
  if (!presence.available || !presence.confirmed) return <div className="flex items-center gap-1.5 text-xs text-muted-foreground" role="status" aria-live="polite">
    <Users className="h-3.5 w-3.5" aria-hidden />
    {!presence.available ? "Live presence unavailable" : "Checking who is here…"}
  </div>
  const people = participants(presence.allMembers ?? presence.members, presence.currentUserId, context.kind)
  return <div role="status" aria-live="polite">
    <Popover>
      <PopoverTrigger asChild>
        <Button variant="ghost" size="sm" className="gap-2 text-xs text-muted-foreground coarse:h-12" aria-label="Show people viewing this page">
          <span className="flex -space-x-1.5" aria-hidden>
            {people.slice(0, 4).map(person => <Avatar key={person.id} size="sm" className="border border-background"><AvatarFallback>{initials(person.name)}</AvatarFallback></Avatar>)}
            {people.length > 4 && <span className="flex h-6 min-w-6 items-center justify-center rounded-full bg-muted px-1">+{people.length - 4}</span>}
          </span>
          <span>{presence.people} {presence.people === 1 ? "person" : "people"} viewing · {presence.here} at {location}</span>
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80 max-w-[calc(100vw-2rem)]">
        <h3 className="mb-3 text-sm font-medium">People viewing this page</h3>
        {people.length ? <ul className="max-h-72 space-y-3 overflow-auto" aria-label="Page participants">
          {people.map(person => <li key={person.id} className="flex min-w-0 items-center gap-3">
            <Avatar size="sm" aria-hidden><AvatarFallback>{initials(person.name)}</AvatarFallback></Avatar>
            <div className="min-w-0"><p className="break-words text-sm">{person.name}</p><p className="text-xs text-muted-foreground">{positionLabel(person, context.kind)}</p></div>
          </li>)}
        </ul> : <p className="text-sm text-muted-foreground">No viewers currently connected.</p>}
      </PopoverContent>
    </Popover>
  </div>
}
