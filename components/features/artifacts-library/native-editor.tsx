"use client"

import { useEffect, useState, type ReactNode } from "react"
import { History } from "lucide-react"
import { Button } from "@/components/ui/button"
import { useSessionSafe } from "@/hooks/use-auth"
import { apiFetch } from "@/lib/api-fetch"
import { verifyNativeReadLoss } from "@/lib/mooring/native-read-loss"
import { NativeCraftEditorHost, type NativeCraftHostStatus } from "@/components/features/pages/native-craft-editor-host"
import { NativeCraftPreview } from "@/components/features/pages/native-craft-preview"
import { NativeCraftReaderHistory } from "@/components/features/pages/native-craft-reader-history"
import { ArtifactAgentMode, ArtifactDrawerSection, useArtifactFrame } from "./frame"

/** History lives in the frame's drawer, never as a row above the editor (#5132). */
const inHistory = (node: ReactNode, presence?: ReactNode) => <ArtifactDrawerSection tab="history" label="History" icon={History}>{presence}{node}</ArtifactDrawerSection>
/**
 * The Craft agent task is the Edit mode of the Agent drawer, mounted only once
 * the server admits agents. Its form (and the agent directory it reads) mounts
 * the first time the Agent tab opens, never just because the artifact opened.
 */
const asEditMode = (form: ReactNode) => <ArtifactAgentMode mode="edit"><OnceAgentOpened>{form}</OnceAgentOpened></ArtifactAgentMode>

function OnceAgentOpened({ children }: { children: ReactNode }) {
  const frame = useArtifactFrame()
  const [seen, setSeen] = useState(false)
  const opened = frame === null || frame.drawerTab === "agent"
  useEffect(() => { if (opened) setSeen(true) }, [opened])
  return seen || opened ? <>{children}</> : null
}

type State = "loading" | "enabled" | "preview" | "readonly" | "disabled" | "error"

/**
 * A native Word or Sheet opens inside its Artifacts detail: the same
 * single-editor host /pages/craft/editor mounts; its lock controls (who edits
 * now, ask for the handoff) sit in the frame's status popover. The server's availability answer decides, exactly
 * as on that route; without a confirmed editor or preview the detail keeps
 * what it showed before (`fallback`) under a notice that says why, and
 * nothing is opened or written. An unconfirmed answer can be asked again.
 */
export type NativeAvailability = State

export function ArtifactNativeEditor({ workspaceId, pageId, fallback, onStatus, onAvailability, placeLease, editorHidden, historyPresence }: { workspaceId: string; pageId: string; fallback: ReactNode
  /** Where the lock controls render: the frame's status popover. */
  placeLease?: (controls: ReactNode) => ReactNode
  /** The body shows a reflowed read-only view instead of the editor surface (Word on a phone). */
  editorHidden?: boolean
  /** Rendered inside the History tab while it exists, so the body knows it can open it. */
  historyPresence?: ReactNode
  /** The host's save and lock state, for the frame's status chip. */
  onStatus?: (status: NativeCraftHostStatus) => void
  /** The server's availability answer (editor, preview, read-only…). */
  onAvailability?: (state: NativeAvailability) => void }) {
  const session = useSessionSafe()
  const humanId = session.status === "authenticated" ? session.data?.user?.id ?? null : null
  const scope = humanId ? JSON.stringify([humanId, workspaceId, pageId]) : null
  const [availability, setAvailability] = useState<{ scope: string; state: State }>()
  const [attempt, setAttempt] = useState(0)
  useEffect(() => {
    if (!scope) return
    const abort = new AbortController()
    // Anything short of the editor may mean this person lost native read, but
    // says neither way: a draft left here is erased only on proof.
    const proveReadLoss = () => { if (humanId) void verifyNativeReadLoss({ human: humanId, workspace: workspaceId, page: pageId }).catch(() => {}) }
    setAvailability({ scope, state: "loading" })
    const request = `/api/v1/pages/${encodeURIComponent("@" + pageId)}/craft/availability?${new URLSearchParams({ workspace_id: workspaceId })}`
    void apiFetch(request, { signal: abort.signal, cache: "no-store", redirect: "error" }).then(async response => {
      if (!response.ok) throw Error("native_availability_unconfirmed")
      const value: unknown = await response.json()
      const fields = value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {}
      const state: State = fields.enabled === true && fields.can_edit === true ? "enabled" : fields.preview_enabled === true ? "preview" : fields.enabled === true ? "readonly" : "disabled"
      if (abort.signal.aborted) return
      setAvailability({ scope, state })
      if (state !== "enabled") proveReadLoss()
    }).catch(() => { if (!abort.signal.aborted) { setAvailability({ scope, state: "error" }); proveReadLoss() } })
    return () => abort.abort()
  }, [scope, humanId, workspaceId, pageId, attempt])

  // A previous person's or document's answer never authorizes this render.
  const state = availability?.scope === scope ? availability.state : "loading"
  useEffect(() => { onAvailability?.(state) }, [state]) // eslint-disable-line react-hooks/exhaustive-deps -- report each answer once
  if (!scope || !humanId) return <>{fallback}</>
  if (state === "loading") return <p role="status" className="p-4 text-sm text-muted-foreground">Connecting…</p>
  // The host's UI and draft journal are remounted for every person and document.
  if (state === "enabled") return <div data-testid="native-editor" className={editorHidden ? "flex flex-col" : "flex min-h-[520px] flex-1 flex-col overflow-hidden"}>
    <NativeCraftEditorHost key={scope} workspaceId={workspaceId} pageId={pageId} humanId={humanId} enabled
      placeHistory={node => inHistory(node, historyPresence)} placeAgentStart={asEditMode} onStatus={onStatus} placeLease={placeLease} editorHidden={editorHidden} />
  </div>
  if (state === "preview") return <div className="flex min-h-[520px] flex-1 flex-col overflow-hidden">
    {inHistory(<NativeCraftReaderHistory key={scope} humanId={humanId} workspaceId={workspaceId} pageId={pageId} embedded />, historyPresence)}
    <NativeCraftPreview key={scope} humanId={humanId} workspaceId={workspaceId} pageId={pageId} />
  </div>
  return <>
    {state === "error" ? <p role="status" className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
      Editing could not be confirmed right now.
      <Button type="button" size="sm" variant="outline" onClick={() => setAttempt(n => n + 1)}>Try again</Button>
    </p> : <p role="status" className="text-sm text-muted-foreground">
      {state === "readonly" ? "You can read this document but not edit it." : "Editing is not available here right now."}
    </p>}
    {state === "readonly" && inHistory(<NativeCraftReaderHistory key={scope} humanId={humanId} workspaceId={workspaceId} pageId={pageId} embedded />, historyPresence)}
    {fallback}
  </>
}
