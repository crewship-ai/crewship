"use client"
import { useState } from "react"
import { Button } from "@/components/ui/button"
import type { OpenLinked } from "./artifact-context-cards"
import { ConversationLibrary } from "./library/conversation-library"
import type { LibraryFilter } from "./library/library-model"
import { useArtifactContextEnabled } from "./use-artifact-context-enabled"
import { useConversationLibrary } from "./use-artifact-context-data"

interface Props { workspaceId: string; chatId: string; currentUserId: string | null | undefined; onOpen: OpenLinked }

/** A "Library" disclosure for the conversation: linked items only, loaded when opened. */
export function ArtifactLibraryHost({ workspaceId, chatId, currentUserId, onOpen }: Props) {
  const enabled = useArtifactContextEnabled()
  const [open, setOpen] = useState(false)
  const [filter, setFilter] = useState<LibraryFilter>("all")
  const { rows, result, loading, refetch } = useConversationLibrary({ workspaceId, chatId, currentUserId, open })
  if (!enabled || !workspaceId || !chatId || !currentUserId) return null
  const status = loading ? "loading" : result && (result.status === "unsupported" || result.status === "error") ? "unavailable" : "ready"
  // A denied read drops everything: nothing from a lost grant stays on screen.
  const items = result?.status === "ok" ? rows.map(row => row.item) : []
  return <div className="shrink-0 border-b px-4 py-1 md:px-6">
    <Button type="button" size="sm" variant="ghost" className="coarse:min-h-12" aria-expanded={open} aria-controls="chat-artifact-library" onClick={() => setOpen(value => !value)}>Library</Button>
    {open && <div id="chat-artifact-library" className="max-h-60 overflow-auto py-2">
      <ConversationLibrary items={items} filter={filter} status={status} onRetry={() => void refetch()} onFilterChange={setFilter}
        onOpen={linkId => {
          const row = rows.find(candidate => candidate.item.linkId === linkId)
          if (row?.target && row.item.status === "readable") onOpen(row.target, row.item.name, document.activeElement instanceof HTMLElement ? document.activeElement : null)
        }} />
    </div>}
  </div>
}
