"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { toast } from "sonner"

interface FileRef {
  path: string
  name: string
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
  openFileEditor: (node: { path: string; name: string }) => void
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

  const openFileEditor = useCallback((node: { path: string; name: string }) => {
    if (!workspaceId) return
    editorAbortRef.current?.abort()
    const ac = new AbortController()
    editorAbortRef.current = ac
    setEditorFile({ path: node.path, name: node.name })
    setEditorLoading(true)
    setEditorContent(null)
    setEditorDirty(false)
    setEditorExpanded(false)
    fetch(`/api/v1/agents/${agentId}/files/download?workspace_id=${workspaceId}&path=${encodeURIComponent(node.path)}`, { signal: ac.signal })
      .then((r) => r.ok ? r.text() : null)
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
    fetch(`/api/v1/agents/${agentId}/files/save?workspace_id=${workspaceId}&path=${encodeURIComponent(editorFile.path)}`, {
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
