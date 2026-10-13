"use client"
import { Users } from "lucide-react"
import { useContentPresence, type ContentPresenceContext } from "@/hooks/use-content-presence"
export function ContentPresence({ context, enabled = true }: { context: ContentPresenceContext; enabled?: boolean }) {
  const presence = useContentPresence(context, enabled)
  if (!enabled) return null
  return <ContentPresenceStatus presence={presence} context={context} />
}
export function ContentPresenceStatus({ presence, context }: { presence: ReturnType<typeof useContentPresence>; context: ContentPresenceContext }) {
  const location = context.kind === "artifact" ? `revision ${context.revision}` : `saved sequence ${context.boardSeq}`
  return <div className="flex items-center gap-1.5 text-xs text-muted-foreground" role="status" aria-live="polite">
    <Users className="h-3.5 w-3.5" aria-hidden />
    {!presence.available ? "Live presence unavailable" : !presence.confirmed ? "Checking who is here…" : `${presence.people} ${presence.people === 1 ? "person" : "people"} viewing · ${presence.here} at ${location}`}
  </div>
}
