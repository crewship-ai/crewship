"use client"

import { useSessionSafe } from "@/hooks/use-auth"
import { usePages } from "@/hooks/use-pages"
import { DocumentCatalog } from "./document-catalog"

/** Documents-only view of this workspace, not another workspace or grant.
 * usePages keys and masks data by current human/workspace. Missing server
 * classification remains unknown and is excluded, including legacy own Pages.
 * Foreign OWNER custody belongs to its separate audited access path.
 */
export function PersonalDocumentSpace({ workspaceId, onSelect }: {
  workspaceId: string | null | undefined
  onSelect: (slug: string) => void
}) {
  const { data: session } = useSessionSafe()
  const human = session?.user?.id
  const { pages, loading, error } = usePages(workspaceId)
  const own = human && workspaceId && !error ? pages.filter(page =>
    page.privateAudience === true && page.ownerRef === `user/${human}` && page.hasDocument === true) : []
  return <section aria-label="My space" className="space-y-3">
    <div className="px-6 pt-6"><h1 className="text-xl font-semibold">My space</h1>
      <p className="text-sm text-muted-foreground">Your private Documents in this workspace. Workspace OWNER access is audited.</p></div>
    {!human || !workspaceId ? <p role="status" className="px-6">Choose a workspace with a current account.</p>
      : loading ? <p role="status" className="px-6">Loading your Documents…</p>
      : error ? <p role="alert" className="px-6">Your Documents are unavailable.</p>
      : own.length ? <DocumentCatalog pages={own} onSelect={onSelect} />
      : <p role="status" className="px-6">No private Documents in your space.</p>}
  </section>
}
