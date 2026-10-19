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
  const ownedDocuments = human && workspaceId && !error ? pages.filter(page =>
    page.ownerRef === `user/${human}` && page.hasDocument === true) : []
  const own = ownedDocuments.filter(page => page.privateAudience === true)
  const unknown = ownedDocuments.some(page => page.privateAudience === undefined)
  return <section aria-label="My space" className="space-y-3">
    <div className="px-6 pt-6"><h1 className="text-xl font-semibold">My space</h1>
      <p className="text-sm text-muted-foreground">Your private Documents in this workspace. Workspace OWNER access is audited.</p></div>
    {!human || !workspaceId ? <p role="status" className="px-6">Choose a workspace with a current account.</p>
      : loading ? <p role="status" className="px-6">Loading your Documents…</p>
      : error ? <p role="alert" className="px-6">Your Documents are unavailable.</p>
      : <>
        {unknown && <p role="status" className="px-6">Document audience information is unavailable for some of your Documents.</p>}
        {own.length > 0 && <DocumentCatalog pages={own} onSelect={onSelect} />}
        {!own.length && !unknown && <p role="status" className="px-6">No private Documents in your space.</p>}
      </>}
  </section>
}
