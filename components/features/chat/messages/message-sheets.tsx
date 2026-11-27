"use client"

import { useContext } from "react"
import { QueryClientContext } from "@tanstack/react-query"
import { FileSpreadsheet } from "lucide-react"
import { usePage } from "@/hooks/use-pages"
import { useMessageSheets } from "../artifact-context/sheet/use-conversation-sheets"

/** One Sheet the message went with: its name once the server confirms this reader sees a Sheet there. */
function MessageSheet({ workspaceId, pageId }: { workspaceId: string; pageId: string }) {
  const { page } = usePage(workspaceId, `@${pageId}`)
  if (!page || (page.artifactKind !== "sheet" && page.nativeCraftApplication !== "gridcraft")) return null
  const title = page.name || "Sheet"
  return <li title={`Sent with the Sheet ${title}`}
    className="inline-flex min-w-0 max-w-full items-center gap-1 rounded-md border bg-background px-2 py-0.5 text-xs text-muted-foreground">
    <FileSpreadsheet className="size-3.5 shrink-0 text-sheet" aria-hidden /><span className="truncate">{title}</span>
  </li>
}

function MessageSheetList({ workspaceId, chatId, messageId, sentRefs }: { workspaceId: string; chatId: string; messageId?: string; sentRefs: unknown }) {
  const ids = useMessageSheets(workspaceId, chatId, messageId, sentRefs)
  if (ids.length === 0) return null
  return <ul aria-label="Sheets sent with this message" data-testid="message-sheets" className="flex flex-wrap justify-end gap-1.5">
    {ids.map(id => <MessageSheet key={id} workspaceId={workspaceId} pageId={id} />)}
  </ul>
}

/**
 * The Sheets a person's message went with, one chip each, under the bubble: the composer
 * shows the Sheet before sending, the sent message keeps showing it. Nothing without a query client
 * (a transcript rendered on its own) or without the chat it belongs to.
 */
export function MessageSheets(props: { workspaceId?: string; chatId?: string; messageId?: string; sentRefs: unknown }) {
  const client = useContext(QueryClientContext)
  if (!client || !props.workspaceId || !props.chatId) return null
  if (!props.messageId && !Array.isArray(props.sentRefs)) return null
  return <MessageSheetList workspaceId={props.workspaceId} chatId={props.chatId} messageId={props.messageId} sentRefs={props.sentRefs} />
}
