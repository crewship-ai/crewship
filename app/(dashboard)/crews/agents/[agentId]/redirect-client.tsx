"use client"

import { useEffect } from "react"
import { useParams, useRouter } from "next/navigation"
import { useWorkspace } from "@/hooks/use-workspace"

/**
 * Client-side redirect for legacy /crews/agents/[agentId] links. The
 * dedicated agent full page retired in Phase 8; the live view is at
 * /crews?agent=<slug>. Static export can't do a server redirect by
 * slug (no DB access at build time), so we fetch the agent at runtime
 * and `router.replace` into the canvas.
 */
export function AgentRedirect() {
  const params = useParams<{ agentId: string }>()
  const router = useRouter()
  const { workspaceId } = useWorkspace()

  useEffect(() => {
    if (!params?.agentId) {
      router.replace("/crews")
      return
    }
    if (!workspaceId) return
    const controller = new AbortController()
    fetch(`/api/v1/agents/${params.agentId}?workspace_id=${workspaceId}`, {
      signal: controller.signal,
    })
      .then((r) => (r.ok ? r.json() : null))
      .then((agent: { slug?: string; crew?: { slug?: string } } | null) => {
        if (controller.signal.aborted) return
        if (agent?.slug) {
          const crewParam = agent.crew?.slug ? `&crew=${agent.crew.slug}` : ""
          router.replace(`/crews?agent=${agent.slug}${crewParam}`)
        } else {
          router.replace("/crews")
        }
      })
      .catch(() => {
        router.replace("/crews")
      })
    return () => controller.abort()
  }, [params?.agentId, workspaceId, router])

  return (
    <div className="flex items-center justify-center h-full text-body text-muted-foreground">
      Redirecting…
    </div>
  )
}
