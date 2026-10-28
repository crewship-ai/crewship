"use client"

import { useEffect, useState } from "react"
import { MutationCache, QueryClient, QueryClientProvider, useQueryClient } from "@tanstack/react-query"
import { useSessionSafe } from "@/hooks/use-auth"
import { usePageGrants } from "@/hooks/use-page-grants"
import { AccessCard } from "@/components/features/pages/page-settings"
import { CreateSurface, CreateSurfaceBody, CreateSurfaceHeader } from "@/components/layout/create-surface"

interface Props { workspaceId: string; slug: string; onClose: () => void }

export function DesignAccessDialog(props: Props) {
 const humanId = useSessionSafe()?.data?.user?.id
 return <AccessScope key={`${props.workspaceId}:${props.slug}:${humanId ?? ""}`} {...props} humanId={humanId} />
}

function AccessScope({ humanId, ...props }: Props & { humanId?: string }) {
 const parent = useQueryClient()
 const [queries] = useState(() => {
  const client = new QueryClient({
   defaultOptions: { queries: { retry: false, gcTime: 0, refetchInterval: 1000 } },
   mutationCache: new MutationCache({ onSuccess: async () => { await Promise.all([client.invalidateQueries(), parent.invalidateQueries({ queryKey: ["artifact-design", props.workspaceId, props.slug] })]) } }),
  })
  return client
 })
 useEffect(() => () => queries.clear(), [queries])
 return <QueryClientProvider client={queries}><AccessBody {...props} humanId={humanId} /></QueryClientProvider>
}

function AccessBody({ workspaceId, slug, onClose, humanId }: Props & { humanId?: string }) {
 const grants = usePageGrants(workspaceId, slug, Boolean(humanId))
 const canManage = Boolean(humanId) && !grants.loading && !grants.refusal && !grants.error
 return <CreateSurface open size="md" restoreOpenerFocus onOpenChange={open => { if (!open) onClose() }}>
  <CreateSurfaceHeader title="Design access" description="Invite people to view or edit the live canvas. Agent task permissions stay separate." onClose={onClose} />
  <CreateSurfaceBody>
  {!canManage ? <p role={grants.loading ? "status" : "alert"} className="text-sm">{grants.loading ? "Checking sharing permissions…" : grants.refusal ?? grants.error ?? "Sharing permissions are unavailable."}</p> : <AccessCard workspaceId={workspaceId} slug={slug} panelIDs={[]} documentContent canManage manageRefusal="Only the owner or a workspace administrator may change sharing." footer="Read follows changes live. Edit can change the canvas. Changes take effect immediately." />}
  </CreateSurfaceBody>
 </CreateSurface>
}
