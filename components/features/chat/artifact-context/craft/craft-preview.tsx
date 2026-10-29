"use client"
import type { ReactNode } from "react"
import { useRouter } from "next/navigation"
import { CraftLockBar, craftEditorHref } from "./craft-lock-bar"

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
 * Read-only frame around a Craft document in chat. Editing never happens in
 * the chat; Edit opens the full editor on its own page, which takes the lease.
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
      {children}
    </div>
  </section>
}
