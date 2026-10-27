"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import type { ArtifactOrigin } from "@/hooks/use-artifact-agent-dispatch"
import { useSessionSafe } from "@/hooks/use-auth"
import { apiFetch } from "@/lib/api-fetch"

export interface ArtifactFile { path: string; content: string; media_type: string }
export interface ArtifactComment { id: string; version: number; path: string; selector: string; quote: string; text: string; task_id?: string; replies?: { text: string; version: number }[] }
export interface ArtifactProjectSnapshot {
 page_id: string
 origin?: ArtifactOrigin
 can_write: boolean
 latest_version: number
 shared_version: number
 manifest: { version: number; entrypoint: string; files: ArtifactFile[] }
 versions: { version: number; created_at?: string; message?: string }[]
 quota: { used_bytes: number; limit_bytes: number; warning: boolean }
 comments: ArtifactComment[]
}
export interface ArtifactAnchor { version: number; path: string; selector: string; quote: string }
export function useArtifactProject(workspace: string, slug: string, version?: number) {
 const client = useQueryClient()
 const session = useSessionSafe()
 const key = ["artifact-project", workspace, slug, session?.data?.user?.id ?? "anonymous"]
 const endpoint = `/api/v1/pages/${encodeURIComponent(slug)}/artifact`
 const params = new URLSearchParams({ workspace_id: workspace })
 const request = async <T,>(path: string, body?: unknown): Promise<T> => {
  const response = await apiFetch(`${endpoint}${path}${path.includes("?") ? "&" : "?"}${params}`, body === undefined ? { cache: "no-store" } : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) })
  const data = await response.json().catch(() => null)
  if (!response.ok) throw Object.assign(new Error(response.status === 409 ? "This project or selected element changed. Your comment draft is preserved. Load the latest version and select the element again. Ask the agent to identify an element without a stable ID before editing." : data?.error ?? "Could not load the artifact project."), { status: response.status })
  return data
 }
 const query = useQuery({ queryKey: [...key, version ?? "latest"], enabled: Boolean(workspace && slug), retry: false, gcTime: 0, refetchInterval: 15_000, queryFn: () => request<ArtifactProjectSnapshot>(version ? `?version=${version}` : "") })
 const invalidate = () => { void client.invalidateQueries({ queryKey: key }) }
 const restore = useMutation({ mutationFn: (body: { version: number; base_version: number }) => request("/restore", body), onSuccess: invalidate })
 const share = useMutation({ mutationFn: (body: { version: number; base_version: number }) => request("/share", body), onSuccess: invalidate })
 const comment = useMutation({ mutationFn: (body: ArtifactAnchor & { text: string; send_to_agent: boolean }) => request<ArtifactComment>("/comments", body), onSuccess: invalidate })
 return { query, restore, share, comment }
}
