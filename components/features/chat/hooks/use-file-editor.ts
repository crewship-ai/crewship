"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { toast } from "sonner"
import { apiFetch } from "@/lib/api-fetch"

/**
 * Which tree a file was read from — and therefore the only tree it can be
 * written back to.
 *
 * The right rail shows two scopes at once. The agent scope lists
 * `/agents/{id}/files`, whose keys are `<crewId>/<slug>/…`; the crew scope
 * lists `/crews/{crewId}/files`, whose keys are `<crewId>/…`. They are
 * different trees behind different routes, and the API enforces it:
 * `AgentFileDownload` / `AgentFileSave` reject a `<crewId>/` path that is not
 * under `<crewId>/<slug>/` with 403 "path not scoped to this agent". So a crew
 * file opened through the agent editor could not be read at all — and if a
 * path ever did slip past that prefix test, the save would have PUT it into
 * the agent's private tree instead of the crew's shared one.
 *
 * The scope is therefore not a per-call decision. It is captured with the file
 * when it is opened, stored alongside it, and both URLs are built from that one
 * stored value by `fileRoute` below. There is no code path that can pick one
 * tree to read and another to write: `handleEditorSave` has no scope of its
 * own to get wrong.
 */
export type EditorScope = { kind: "agent" } | { kind: "crew"; crewId: string }

interface FileRef {
  path: string
  name: string
  /** The tree this file came from. Reads AND writes go here, or nowhere. */
  scope: EditorScope
}

/** The one place a file URL is built, for both actions and both trees. */
function fileRoute(
  scope: EditorScope,
  agentId: string,
  action: "download" | "save",
  workspaceId: string,
  path: string,
): string {
  const base =
    scope.kind === "crew"
      ? `/api/v1/crews/${encodeURIComponent(scope.crewId)}`
      : `/api/v1/agents/${agentId}`
  return `${base}/files/${action}?workspace_id=${workspaceId}&path=${encodeURIComponent(path)}`
}

interface UseFileEditorOptions {
  agentId: string
  workspaceId: string | null
}

interface UseFileEditorReturn {
  editorFile: FileRef | null
  editorContent: string | null
  editorLoading: boolean
  editorDirty: boolean
  editorExpanded: boolean
  editorSaving: boolean
  saveRef: React.MutableRefObject<(() => void) | null>
  setEditorDirty: (dirty: boolean) => void
  setEditorExpanded: (expanded: boolean) => void
  /** The scope is required: every caller has to say which tree it is handing over. */
  openFileEditor: (node: { path: string; name: string }, scope: EditorScope) => void
  closeEditor: () => void
  handleEditorSave: (content: string) => void
}

export function useFileEditor({ agentId, workspaceId }: UseFileEditorOptions): UseFileEditorReturn {
  const [editorFile, setEditorFile] = useState<FileRef | null>(null)
  const [editorContent, setEditorContent] = useState<string | null>(null)
  const [editorLoading, setEditorLoading] = useState(false)
  const [editorDirty, setEditorDirty] = useState(false)
  const [editorExpanded, setEditorExpanded] = useState(false)
  const [editorSaving, setEditorSaving] = useState(false)
  const editorAbortRef = useRef<AbortController | null>(null)
  const saveRef = useRef<(() => void) | null>(null)

  // Clear editor and abort in-flight downloads when agent/workspace context changes
  useEffect(() => {
    editorAbortRef.current?.abort()
    setEditorFile(null)
    setEditorContent(null)
    setEditorDirty(false)
    setEditorExpanded(false)
  }, [agentId, workspaceId])

  const openFileEditor = useCallback((node: { path: string; name: string }, scope: EditorScope) => {
    if (!workspaceId) return
    editorAbortRef.current?.abort()
    const ac = new AbortController()
    editorAbortRef.current = ac
    // The scope is stored with the file, in the same statement that reads it.
    setEditorFile({ path: node.path, name: node.name, scope })
    setEditorLoading(true)
    setEditorContent(null)
    setEditorDirty(false)
    setEditorExpanded(false)
    apiFetch(fileRoute(scope, agentId, "download", workspaceId, node.path), { signal: ac.signal })
      .then((r) => { if (!r.ok) throw new Error(`HTTP ${r.status}`); return r.text() })
      .then((text) => { if (!ac.signal.aborted) setEditorContent(text) })
      .catch((err) => { if (err.name !== "AbortError") { setEditorContent(null); toast.error("Failed to load file") } })
      .finally(() => { if (!ac.signal.aborted) setEditorLoading(false) })
  }, [agentId, workspaceId])

  const closeEditor = useCallback(() => {
    editorAbortRef.current?.abort()
    setEditorFile(null)
    setEditorContent(null)
    setEditorLoading(false)
    setEditorDirty(false)
    setEditorExpanded(false)
  }, [])

  const handleEditorSave = useCallback((content: string) => {
    if (!workspaceId || !editorFile) return
    setEditorSaving(true)
    // editorFile.scope, not a scope of this callback's own: the write goes to
    // the tree the bytes came from or it does not go.
    apiFetch(fileRoute(editorFile.scope, agentId, "save", workspaceId, editorFile.path), {
      method: "PUT",
      headers: { "Content-Type": "text/plain" },
      body: content,
    })
      .then((r) => {
        if (r.ok) { setEditorDirty(false); toast.success("File saved") }
        else toast.error("Save failed")
      })
      .catch(() => toast.error("Save failed"))
      .finally(() => setEditorSaving(false))
  }, [agentId, workspaceId, editorFile])

  return {
    editorFile,
    editorContent,
    editorLoading,
    editorDirty,
    editorExpanded,
    editorSaving,
    saveRef,
    setEditorDirty,
    setEditorExpanded,
    openFileEditor,
    closeEditor,
    handleEditorSave,
  }
}
