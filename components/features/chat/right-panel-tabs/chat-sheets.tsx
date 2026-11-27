"use client"

import { ExternalLink, Table2 } from "lucide-react"
import { cn } from "@/lib/utils"
import { usePage } from "@/hooks/use-pages"
import { artifactsItemHref } from "../artifact-context/artifacts-href"
import { useSheetPanel } from "../artifact-context/sheet/sheet-panel-context"
import { useConversationSheets } from "../artifact-context/sheet/use-conversation-sheets"
import { conversationSheets } from "../artifact-context/sheet/conversation-sheets"

/** One Sheet of this chat, as the server shows it to this reader; nothing at all for one they cannot see. */
function ChatSheetRow({ workspaceId, pageId }: { workspaceId: string; pageId: string }) {
  const { page } = usePage(workspaceId, `@${pageId}`)
  const panel = useSheetPanel()
  if (!page || (page.artifactKind !== "sheet" && page.nativeCraftApplication !== "gridcraft")) return null
  const title = page.name || "Sheet"
  const active = panel?.activePageId === page.id
  const href = artifactsItemHref(workspaceId, page.id)
  const body = <><Table2 className="mt-0.5 size-4 shrink-0 text-primary" aria-hidden />
    <span className="min-w-0 flex-1"><span className="block truncate font-medium">{title}</span><span className="block truncate text-xs text-muted-foreground">Sheet{active ? " · open beside the chat" : ""}</span></span></>
  return <li className={cn("flex items-stretch gap-1 rounded-xl border border-border bg-card", active && "border-primary/50 bg-primary/10")}>
    {panel
      ? <button type="button" aria-current={active ? "true" : undefined} aria-label={`Open ${title} beside the chat`} className="flex min-w-0 flex-1 items-start gap-2 p-2.5 text-left" onClick={() => panel.open(page.id)}>{body}</button>
      : <span className="flex min-w-0 flex-1 items-start gap-2 p-2.5">{body}</span>}
    <a href={href} target={panel ? "_blank" : undefined} rel={panel ? "noopener noreferrer" : undefined} aria-label={`${title} in Artifacts`} className="flex shrink-0 items-center rounded-r-xl px-2 text-muted-foreground hover:bg-accent"><ExternalLink className="size-3.5" aria-hidden /></a>
  </li>
}

/**
 * The Sheets of this chat in its Artifacts panel: those its messages carried
 * (and, once the server records it, those uploaded here), as the server lists
 * them for this reader, then those opened in this browser. A Sheet the reader
 * cannot see is left out without a trace; the section hides when none is left.
 */
export function ChatSheets({ workspaceId, chatId, viewerId }: { workspaceId: string; chatId: string; viewerId: string }) {
  const ids = useConversationSheets(workspaceId, chatId, conversationSheets({ userId: viewerId, workspaceId, chatId }))
  if (ids.length === 0) return null
  return <section className="mb-3 [&:not(:has(li))]:hidden">
    <h3 className="eyebrow mb-2">Sheets in this chat</h3>
    <ul aria-label="Sheets in this chat" className="space-y-1.5">{ids.map(id => <ChatSheetRow key={id} workspaceId={workspaceId} pageId={id} />)}</ul>
  </section>
}
