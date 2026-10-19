"use client"

import * as React from "react"
import { useSessionSafe } from "@/hooks/use-auth"
import { usePages } from "@/hooks/use-pages"
import type { PageView } from "@/hooks/use-pages"
import { DocumentCatalog } from "./document-catalog"
import { artifactCopyMove } from "@/lib/feature-flags"
import { DuplicateError, duplicatePage, newMutationId } from "@/lib/pages/catalog-duplicate"
import { ShareToFolderDialog } from "./share-to-folder-dialog"

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
  const { pages, loading, error } = usePages(workspaceId, "personal")
  const [copyError, setCopyError] = React.useState<string | null>(null)
  const copying = React.useRef(false)
  // One key per user action, kept until it succeeds so a retry after a lost
  // answer replays the same mutation instead of making a second copy.
  const mutationIds = React.useRef(new Map<string, string>())
  const duplicate = (page: PageView) => {
    if (!workspaceId || !page.immutablePageId || copying.current) return
    copying.current = true
    setCopyError(null)
    const id = page.immutablePageId
    const mutationId = mutationIds.current.get(id) ?? newMutationId()
    mutationIds.current.set(id, mutationId)
    duplicatePage(workspaceId, { id, mutationId, pagesVersion: page.pagesVersion }).then(copy => { mutationIds.current.delete(id); onSelect(`@${copy.id}`) }, error => { if (error instanceof DuplicateError && error.status < 500) mutationIds.current.delete(id); setCopyError(error instanceof Error ? error.message : "The copy could not be made.") }).finally(() => { copying.current = false })
  }
  const [sharing, setSharing] = React.useState<PageView[] | null>(null)
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
        {own.length > 0 && <DocumentCatalog pages={own} onSelect={onSelect} immutableTargets filterable onShare={workspaceId ? setSharing : undefined} onDuplicate={duplicate} />}
        {copyError && <p role="alert" className="px-6">{copyError}</p>}
        {!own.length && !unknown && <p role="status" className="px-6">No private Documents in your space.</p>}
      </>}
    {workspaceId && sharing && artifactCopyMove() && <ShareToFolderDialog workspaceId={workspaceId} open onOpenChange={open => { if (!open) setSharing(null) }}
      subjects={sharing.flatMap(page => page.immutablePageId ? [{ id: page.immutablePageId, slug: page.slug, name: page.name, pagesVersion: page.pagesVersion }] : [])} />}
  </section>
}
