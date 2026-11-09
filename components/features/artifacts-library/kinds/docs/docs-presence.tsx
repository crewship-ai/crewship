"use client"

import { readablePeerFill } from "@/components/features/artifacts-design/peer-colors"
import { presentPeople, type PresencePeer } from "@/lib/documents/presence"

const initials = (name: string) => name.split(/\s+/).filter(Boolean).slice(0, 2).map(part => part[0]).join("").toUpperCase() || "?"

/**
 * Who else has this Doc open (#5166), for the frame header: one mark per
 * person in the colour their caret has in the text, named for assistive
 * technology and on hover. Nothing renders while the person is alone.
 */
export function DocsPresence({ peers }: { peers: PresencePeer[] }) {
  const people = presentPeople(peers)
  if (!people.length) return null
  const shown = people.slice(0, 4)
  return <ul aria-label="People on this document" className="flex -space-x-1">
    {shown.map(peer => <li key={peer.userId} aria-label={peer.name} title={peer.name}
      className="flex size-6 items-center justify-center rounded-full border-2 border-background text-[11px] font-medium text-white"
      style={{ backgroundColor: readablePeerFill(peer.color) }}><span aria-hidden>{initials(peer.name)}</span></li>)}
    {people.length > shown.length && <li aria-label={`${people.length - shown.length} more`} title={people.slice(shown.length).map(peer => peer.name).join(", ")}
      className="flex size-6 items-center justify-center rounded-full border-2 border-background bg-muted text-[11px]"><span aria-hidden>+{people.length - shown.length}</span></li>}
  </ul>
}
