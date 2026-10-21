"use client"

import { useQuery } from "@tanstack/react-query"
import { Button } from "@/components/ui/button"
import { IssueCodeLinksCard } from "@/components/features/issues/issue-code-links-card"
import { useWorkspace } from "@/hooks/use-workspace"
import { useSession } from "@/hooks/use-auth"
import { apiFetch } from "@/lib/api-fetch"
import type { IssueCodeLink } from "@/lib/types/mission"

/** Mount only after an explicit user request, using the crew returned by issue detail.
 * This reads stored link metadata, never private repository contents or a forge API. */
export function ChatIssuePullRequests({ crewId, identifier, revision }: { crewId: string; identifier: string; revision: number }) {
  const { workspaceId } = useWorkspace()
  const userId = useSession().data?.user?.id
  const query = useQuery<IssueCodeLink[]>({
    queryKey: ["chat-issue-pull-requests", workspaceId, userId, crewId, identifier, revision],
    enabled: !!workspaceId && !!userId,
    staleTime: 0,
    gcTime: 0,
    retry: false,
    refetchInterval: 30_000,
    queryFn: async ({ signal }) => {
      const path = `/api/v1/crews/${encodeURIComponent(crewId)}/issues/${encodeURIComponent(identifier)}/code-links?workspace_id=${encodeURIComponent(workspaceId!)}`
      const response = await apiFetch(path, { signal, cache: "no-store" })
      if (!response.ok) throw new Error(response.status === 401 || response.status === 403 || response.status === 404 ? "Pull requests unavailable" : "Could not refresh pull requests")
      const data: unknown = await response.json()
      if (!Array.isArray(data)) throw new Error("Could not read pull requests")
      return data as IssueCodeLink[]
    },
  })
  // Never show stale metadata after a failed authorization/refresh or identity switch.
  if (!workspaceId || !userId) return <p role="status" className="text-xs text-muted-foreground">Pull requests unavailable</p>
  if (query.isError) return <div role="alert" className="space-y-2 text-xs"><p>{query.error.message}</p><Button variant="outline" size="sm" onClick={() => void query.refetch()}>Retry pull requests</Button></div>
  if (!query.data) return <p role="status" className="text-xs text-muted-foreground">Loading pull requests…</p>
  return <section aria-label="Linked pull requests" className="space-y-2">
    <p className="text-xs text-muted-foreground">Stored data · live CI unavailable</p>
    <IssueCodeLinksCard links={query.data} />
    {!!query.data.length && <div className="space-y-1 px-2 text-[11px] text-muted-foreground">
      {query.data.map(link => <p key={link.id}>
        {link.owner}/{link.repo} #{link.number} · CI unknown · {link.last_sync_error ? "Last synchronization failed; stored state may be outdated" : link.last_synced_at && Number.isFinite(Date.parse(link.last_synced_at)) ? `State last synchronized ${new Date(link.last_synced_at).toLocaleString()}` : "State not synchronized"}
      </p>)}
      <p>Stored provider metadata. Source links use your repository access; no diff or test evidence is loaded.</p>
    </div>}
  </section>
}
