"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import * as Y from "yjs"
import type { ArtifactOrigin } from "@/hooks/use-artifact-agent-dispatch"
import type { ArtifactComment } from "@/hooks/use-artifact-project"
import { useSessionSafe } from "@/hooks/use-auth"
import { apiFetch, tryRefresh } from "@/lib/api-fetch"
import { getAuthMode, resolveWsBase } from "@/lib/server-base"
import { DesignProvider, type DesignConnection, type DesignPeer } from "./design-provider"
import { designNodes, type DesignNode } from "./design-document"

interface DesignState {
 page_id: string
 authority_revision: string
 origin?: ArtifactOrigin
 can_comment?: boolean
 comments?: ArtifactComment[]
 nodes: DesignNode[]
 order: string[]
 can_edit: boolean
 version: number
 latest_version: number
 shared_version: number
 actor?: { id: string; name: string }
}
export interface DesignVersion { version: number; message?: string; actor?: string; created_at?: string }
interface DesignVersions { versions: DesignVersion[]; latest_version: number; shared_version: number }
const COLORS = ["#2563eb", "#c026d3", "#059669", "#ea580c", "#9333ea"]

export function useDesign(workspaceId: string, slug: string, selectedVersion?: number) {
 const session = useSessionSafe()
 const userId = session?.data?.user?.id ?? "anonymous"
 const client = useQueryClient()
 const endpoint = `/api/v1/pages/${encodeURIComponent(slug)}/design`
 const key = ["artifact-design", workspaceId, slug, userId]
 const request = useCallback(async <T,>(path: string, body?: unknown, targetPageId?: string): Promise<T> => {
  const target = targetPageId ? `/api/v1/pages/@${encodeURIComponent(targetPageId)}/design` : endpoint
  const url = `${target}${path}${path.includes("?") ? "&" : "?"}workspace_id=${encodeURIComponent(workspaceId)}`
  const response = await apiFetch(url, body === undefined ? { cache: "no-store" } : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) })
  const data = await response.json().catch(() => null)
  if (!response.ok) throw Object.assign(new Error(data?.error ?? "Design could not be loaded."), { status: response.status })
  return data
 }, [endpoint, workspaceId])
 const query = useQuery({ queryKey: [...key, selectedVersion ?? "live"], enabled: userId !== "anonymous", queryFn: async () => {
  const current = await request<DesignState>(selectedVersion ? `?version=${selectedVersion}` : "")
  if (current.actor?.id !== userId) throw new Error("Design session changed. Reopen the canvas.")
  return current
 }, retry: false, gcTime: 0, refetchInterval: 10_000 })
 const pageId = query.data?.page_id
 // Opaque server policy generation: only successful metadata can admit a
 // replacement socket after the previous connection was retired.
 const authorityRevision = !query.isError && typeof query.data?.authority_revision === "string" && query.data.authority_revision !== "" ? query.data.authority_revision : null
 const pinnedRequest = useCallback(async <T,>(path: string, body?: unknown): Promise<T> => {
  if (!pageId) throw new Error("Design identity is unavailable.")
  return request<T>(path, body, pageId)
 }, [pageId, request])
 const versions = useQuery({ queryKey: [...key, pageId, authorityRevision, "versions"], queryFn: () => pinnedRequest<DesignVersions>("/versions"), enabled: Boolean(pageId && authorityRevision !== null && !query.isError), retry: false, gcTime: 0, refetchInterval: 15_000 })
 const comments = useQuery({ queryKey: [...key, pageId, authorityRevision, "comments"], queryFn: () => pinnedRequest<{ comments: ArtifactComment[] }>("/comments"), enabled: Boolean(pageId && authorityRevision !== null && !query.isError), retry: false, gcTime: 0, refetchInterval: 5000 })
 const [doc, setDoc] = useState<Y.Doc | null>(null)
 const [nodes, setNodes] = useState<DesignNode[]>([])
 const [peers, setPeers] = useState<DesignPeer[]>([])
 const [connection, setConnection] = useState<DesignConnection>("connecting")
 const [dirty, setDirty] = useState(false)
 const [draftError, setDraftError] = useState(false)
 const provider = useRef<DesignProvider | null>(null)
 const editable = Boolean(authorityRevision !== null && query.data?.can_edit && !query.isError && !selectedVersion && connection !== "denied")
 const editableRef = useRef(editable)
 useEffect(() => { editableRef.current = editable; provider.current?.setEditable(editable) }, [editable])
 const canConnect = Boolean(pageId && authorityRevision !== null && !query.isError && !selectedVersion)
 const actorName = query.data?.actor?.name ?? session?.data?.user?.name ?? "Collaborator"
 const initialCanEdit = query.data?.can_edit === true
 const admittedActor = query.data?.actor?.id
 useEffect(() => {
  if (!canConnect || !pageId) return
  let active = true
  const document = new Y.Doc()
  // A reused slug must never replay another Page’s private draft. Legacy
  // slug-scoped records stay in browser storage and are never loaded here.
  const storageKey = `crewship-design:v2:${userId}:${workspaceId}:${pageId}`
  if (initialCanEdit && userId !== "anonymous") {
   try {
    const draft = localStorage.getItem(storageKey)
    if (draft) Y.applyUpdate(document, Uint8Array.from(atob(draft), char => char.charCodeAt(0)))
   } catch { setDraftError(true) }
  }
  setDoc(document)
  setNodes(designNodes(document))
  const update = (_update: Uint8Array, origin: unknown) => {
   setNodes(designNodes(document))
   if (origin === "editor" && initialCanEdit && userId !== "anonymous") {
    try {
     const bytes = Y.encodeStateAsUpdate(document)
     // This bounded canvas draft survives browser crashes without coupling saving to close.
     if (bytes.length <= 2 * 1024 * 1024) {
      let value = ""
      for (let index = 0; index < bytes.length; index += 8192) value += String.fromCharCode(...bytes.subarray(index, index + 8192))
      localStorage.setItem(storageKey, btoa(value))
     } else setDraftError(true)
    } catch { setDraftError(true) }
   }
  }
  document.on("update", update)
  const color = COLORS[Array.from(userId).reduce((sum, char) => sum + char.charCodeAt(0), 0) % COLORS.length]
  const live = new DesignProvider(document, `${resolveWsBase()}/api/v1/pages/@${encodeURIComponent(pageId)}/design/ws?workspace_id=${encodeURIComponent(workspaceId)}&authority_revision=${encodeURIComponent(authorityRevision!)}`, initialCanEdit, { name: actorName, color }, status => {
   setConnection(status)
   if (status === "denied") void client.invalidateQueries({ queryKey: ["artifact-design", workspaceId, slug, userId] })
  }, () => setDirty(true), async () => {
   if (getAuthMode() !== "bearer") {
    const refreshed = await tryRefresh()
    if (!active) return "denied"
    if (refreshed !== "ok") return refreshed === "auth_failed" ? "denied" : "retry"
   }
   try {
    const current = await request<DesignState>("", undefined, pageId)
    if (!active) return "denied"
    // A new cookie may belong to another person, or permissions may have
    // changed while refreshing. Never carry this document across either gap.
    if (!admittedActor || current.actor?.id !== admittedActor || current.page_id !== pageId || current.authority_revision !== authorityRevision || current.can_edit !== initialCanEdit) {
     void client.invalidateQueries({ queryKey: ["artifact-design", workspaceId, slug, userId] })
     return "denied"
    }
    return "ready"
   } catch (error) {
    const status = (error as { status?: number }).status
    return status === 401 || status === 403 || status === 404 ? "denied" : "retry"
   }
  })
  provider.current = live
  const awareness = () => setPeers([...live.awareness.getStates()].flatMap(([clientId, state]) => {
   if (clientId === document.clientID || !state.user) return []
   return [{ clientId, name: String(state.user.name ?? "Collaborator"), color: String(state.user.color ?? "#2563eb"), cursor: state.cursor && Number.isFinite(state.cursor.x) && Number.isFinite(state.cursor.y) ? state.cursor : undefined }]
  }))
  live.awareness.on("change", awareness)
  return () => {
   active = false
   provider.current = null
   live.awareness.off("change", awareness)
   live.destroy()
   document.off("update", update)
   document.destroy()
   setDoc(null)
   setNodes([])
   setPeers([])
  }
 }, [canConnect, endpoint, workspaceId, slug, userId, pageId, authorityRevision, actorName, admittedActor, initialCanEdit, client, request])
 const invalidate = () => { void client.invalidateQueries({ queryKey: key }) }
 const save = useMutation({ mutationFn: async (message: string) => {
  if (!editableRef.current || !provider.current?.ready) throw new Error("Reconnect before saving a version. Your changes remain in this browser.")
  await provider.current.synchronize()
  return pinnedRequest<{ version: number }>("/snapshots", { message })
 }, onSuccess: () => { setDirty(false); invalidate() } })
 const saveRef = useRef(save.mutate)
 useEffect(() => { saveRef.current = save.mutate }, [save.mutate])
 useEffect(() => {
  if (!dirty || !editable || connection !== "connected") return
  const timer = setTimeout(() => saveRef.current("Automatic checkpoint"), 10_000)
  return () => clearTimeout(timer)
 }, [dirty, nodes, editable, connection])
 const restore = useMutation({ mutationFn: (version: number) => pinnedRequest("/restore", { version }), onSuccess: invalidate })
 const connectAgent = useMutation({ mutationFn: (chat_id: string) => pinnedRequest("/agent", { chat_id }), onSuccess: invalidate })
 const comment = useMutation({ mutationFn: (body: { version: number; path: string; selector: string; quote: string; text: string; send_to_agent: boolean }) => pinnedRequest<ArtifactComment>("/comments", body), onSuccess: invalidate })
 const share = useMutation({ mutationFn: (version: number) => pinnedRequest("/share", { version }), onSuccess: invalidate })
 return { query, versions, comments, doc, nodes: selectedVersion ? query.data?.nodes ?? [] : nodes, peers, connection, editable, dirty, draftError, save, restore, share, comment, connectAgent, setCursor: (cursor: { x: number; y: number } | null) => provider.current?.setCursor(cursor) }
}
