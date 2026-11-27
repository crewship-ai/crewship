"use client"

import { FileSpreadsheet } from "lucide-react"
import { Button } from "@/components/ui/button"
import { usePage } from "@/hooks/use-pages"

/** A Sheet opened earlier in this conversation, to open again. Shows nothing the server does not confirm for this reader. */
function ConversationSheetLink({ workspaceId, pageId, onOpen, choose }: { workspaceId: string; pageId: string; onOpen: (pageId: string) => void; choose: boolean }) {
  const { page } = usePage(workspaceId, `@${pageId}`)
  if (!page || (page.artifactKind !== "sheet" && page.nativeCraftApplication !== "gridcraft")) return null
  const title = page.name || "Sheet"
  return <li className="flex min-w-0 items-center gap-2">
    <FileSpreadsheet className="size-4 shrink-0 text-sheet" aria-hidden />
    <span className="min-w-0 flex-1 truncate">{title}</span>
    <Button type="button" size="xs" variant="secondary" className="coarse:min-h-[44px]" aria-label={choose ? `Use ${title} with the next message` : `Open ${title}`} onClick={() => onOpen(page.id)}>{choose ? "Use" : "Open"}</Button>
  </li>
}

/** With choose, a link picks the Sheet for the next message instead of opening it (a phone, where no Sheet opens beside the chat). */
export function ConversationSheetLinks({ workspaceId, pageIds, onOpen, choose = false }: { workspaceId: string; pageIds: string[]; onOpen: (pageId: string) => void; choose?: boolean }) {
  if (pageIds.length === 0) return null
  return <section aria-label="Sheets in this conversation" data-testid="conversation-sheets" className="mx-auto w-full max-w-3xl px-4 py-1 text-xs">
    <ul className="flex flex-col gap-1">{pageIds.map(id => <ConversationSheetLink key={id} workspaceId={workspaceId} pageId={id} onOpen={onOpen} choose={choose} />)}</ul>
  </section>
}
