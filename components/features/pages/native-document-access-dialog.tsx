"use client"

import * as React from "react"
import { MutationCache, QueryClient, QueryClientProvider, useQueryClient } from "@tanstack/react-query"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { useSessionSafe } from "@/hooks/use-auth"
import { pagesKeys, usePage } from "@/hooks/use-pages"
import { pageAccessKeys, toPageOwner, usePageGrants } from "@/hooks/use-page-grants"
import { pageFoldersKeys } from "@/hooks/use-page-folders"
import { AccessCard } from "./page-settings"
import { effectiveAccessKey, EffectiveAccessCard } from "./editor/section-access-effective"
import { FolderAccessCard } from "./editor/section-access-folder"

interface Props {
  workspaceId: string
  slug: string
  onClose: () => void
  returnFocus?: HTMLElement | null
}

/** An access overlay, never a route into the legacy panel/content editor. */
export function NativeDocumentAccessDialog(props: Props) {
  const humanId = useSessionSafe().data?.user?.id
  return <AccessView key={`${props.workspaceId}:${props.slug}:${humanId ?? ""}`} {...props} humanId={humanId} />
}

function AccessView({ humanId, ...props }: Props & { humanId?: string }) {
  const parentQueries = useQueryClient()
  // A fresh, short-lived client also isolates nested folder dialogs and their
  // viewer-specific projections; no previous viewer's ACL can be reused.
  const [queries] = React.useState(() => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false, gcTime: 0, refetchInterval: 1000 } },
      mutationCache: new MutationCache({ onSuccess: async () => {
        await Promise.all([
          client.invalidateQueries(),
          parentQueries.invalidateQueries({ queryKey: pagesKeys.all(props.workspaceId) }),
          parentQueries.invalidateQueries({ queryKey: pageAccessKeys.grants(props.workspaceId, props.slug) }),
          parentQueries.invalidateQueries({ queryKey: effectiveAccessKey(props.workspaceId, props.slug) }),
          parentQueries.invalidateQueries({ queryKey: pageFoldersKeys.all(props.workspaceId) }),
        ])
      } }),
    })
    return client
  })
  React.useEffect(() => () => queries.clear(), [queries])
  return <QueryClientProvider client={queries}><AccessBody {...props} humanId={humanId} /></QueryClientProvider>
}

function AccessBody({ workspaceId, slug, onClose, returnFocus, humanId }: Props & { humanId?: string }) {
  const detail = usePage(workspaceId, slug)
  const available = Boolean(humanId) && detail.page?.hasCollaboration === true && !detail.loading && !detail.error
  const grants = usePageGrants(workspaceId, slug, available)
  const canManage = available && !grants.loading && !grants.refusal && !grants.error
  const owner = available ? toPageOwner(detail.raw) : null

  return <Dialog open onOpenChange={open => { if (!open) onClose() }}>
    <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-2xl" onCloseAutoFocus={event => {
      if (returnFocus?.isConnected) { event.preventDefault(); returnFocus.focus() }
    }}>
      <DialogHeader>
        <DialogTitle>Document access</DialogTitle>
        <DialogDescription>Document rights and an agent’s task permission are separate. Sharing does not widen an existing task.</DialogDescription>
      </DialogHeader>
      {!available ? <p role={humanId && detail.loading ? "status" : "alert"}>
        {humanId && detail.loading ? "Checking document access…" : "Document access is unavailable."}
      </p> : <div className="flex flex-col gap-4">
        <p className="text-sm">Owner: {owner?.label ?? "Not recorded"}</p>
        {!canManage ? <p role={grants.loading ? "status" : "alert"} className="text-sm">
          {grants.loading ? "Checking sharing permissions…" : grants.refusal ?? grants.error ?? "Sharing permissions are unavailable."}
        </p> : <>
          <AccessCard workspaceId={workspaceId} slug={slug} panelIDs={[]} documentContent
            canManage={Boolean(canManage)}
            manageRefusal="Only the document owner or a workspace administrator may change sharing. Permission to edit is not permission to share."
            footer="Changes take effect immediately. The server checks current rights on every read and write." />
          <FolderAccessCard workspaceId={workspaceId} slug={slug} page={detail.raw} />
          <EffectiveAccessCard workspaceId={workspaceId} slug={slug} />
        </>}
      </div>}
    </DialogContent>
  </Dialog>
}
