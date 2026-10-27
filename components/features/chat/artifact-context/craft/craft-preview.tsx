"use client"
import type { ReactNode } from "react"
import { useRouter } from "next/navigation"
import { Bot } from "lucide-react"
import { CraftLockBar, craftEditorHref } from "./craft-lock-bar"
import { useCraftAvailable, useCraftLease } from "./use-craft-lease"

export { craftEditorHref }

interface CraftPreviewProps {
  workspaceId: string
  slug: string
  kind: "word" | "sheet"
  title: string
  /** Read-only rendering, banners above it (draft, proposals) included. */
  children: ReactNode
  /** Called after navigation starts, e.g. to close the chat side panel. */
  onEdit?: () => void
}

/**
 * Someone asked for the lease I hold. Only copy for now: there is no
 * hand-over or ask-for-proposal endpoint, and the lease payload carries the
 * requester's name but not whether it is an agent.
 */
export function CraftHandoffBanner({ workspaceId, slug }: { workspaceId: string; slug: string }) {
  const available = useCraftAvailable(workspaceId, slug)
  const status = useCraftLease(workspaceId, available ? slug : null).data
  if (!available || !status?.mine || !status.handoff_requested_by) return null
  return <div className="flex items-center gap-2 rounded-lg border bg-info/10 px-3 py-2 text-sm">
    <Bot aria-hidden className="size-4 shrink-0 text-info" />
    <div className="min-w-0">
      <b className="font-semibold">{status.handoff_requested_by} is waiting for handoff</b>
      <span className="ml-1.5 text-muted-foreground">Nothing changes in the file while you hold it.</span>
    </div>
  </div>
}

/**
 * Read-only frame around a Craft document in chat. Editing never happens in
 * the chat; Edit takes the lease and opens the full editor on its own page.
 * The toolbar shows only version-independent facts: History, Share and
 * Download have no chat-side action yet.
 */
export function CraftPreview({ workspaceId, slug, kind, title, children, onEdit }: CraftPreviewProps) {
  const router = useRouter()
  const openEditor = () => { router.push(craftEditorHref(workspaceId, slug)); onEdit?.() }
  return <section aria-label={title} className="flex min-w-0 flex-col overflow-hidden rounded-xl border bg-card">
    <header className="flex min-w-0 items-center gap-2 border-b px-3 py-2">
      <h3 className="min-w-0 truncate text-sm font-medium">{title}</h3>
      <span className="text-xs text-muted-foreground">{kind === "word" ? "WordCraft" : "GridCraft"}</span>
    </header>
    <CraftLockBar workspaceId={workspaceId} slug={slug} onEdit={openEditor} />
    <div className="grid min-w-0 gap-3 p-3">
      <CraftHandoffBanner workspaceId={workspaceId} slug={slug} />
      {children}
    </div>
  </section>
}
