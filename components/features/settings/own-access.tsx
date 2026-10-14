"use client"

import { useQuery } from "@tanstack/react-query"

import { useWorkspace } from "@/hooks/use-workspace"
import { useAuth } from "@/hooks/use-auth"
import { apiFetch } from "@/lib/api-fetch"
import { requireApiPathSegment } from "@/lib/api-path-segment"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"

interface OwnAccess {
  explanation: string
  decisions: { kind: string; id: string; name?: string; operation: string; state: string; reason: string }[]
}

export function OwnAccessCard() {
  const { workspaceId, workspaceUnavailable, loading } = useWorkspace()
  const { session } = useAuth()
  const query = useQuery<OwnAccess>({
    queryKey: ["own-access", workspaceId, session?.user?.id],
    enabled: Boolean(workspaceId && session?.user?.id) && !workspaceUnavailable && !loading,
    queryFn: async ({ signal }) => {
      const response = await apiFetch(`/api/v1/workspaces/${requireApiPathSegment(workspaceId!)}/access/me`, { signal })
      if (!response.ok) throw new Error("Access details unavailable. Retry or ask your administrator.")
      return response.json()
    },
    // Guest sessions have no WebSocket; recheck while the screen is visible.
    refetchInterval: 60_000,
    refetchIntervalInBackground: false,
  })

  return <Card>
    <CardHeader><CardTitle>Your access</CardTitle></CardHeader>
    <CardContent className="space-y-3">
      {workspaceUnavailable ? <p>Select an available workspace to see your access.</p>
        : query.isError ? <div role="alert"><p>{query.error.message}</p><Button variant="outline" onClick={() => void query.refetch()}>Retry</Button></div>
          : query.isPending ? <p role="status">Loading your access…</p>
            : <>
              <p className="text-sm text-muted-foreground">{query.data.explanation}</p>
              {query.data.decisions.length === 0 ? <p>No individual resources are shared with you in this workspace. Ask your administrator for access.</p>
                : <ul className="space-y-3">{query.data.decisions.map(decision => <li key={`${decision.kind}:${decision.id}:${decision.operation}`}>
                  <p className="text-sm font-medium break-all">{decision.name || decision.id} · {decision.operation} · {decision.state}</p>
                  <p className="text-sm text-muted-foreground">{decision.reason}</p>
                </li>)}</ul>}
            </>}
    </CardContent>
  </Card>
}
