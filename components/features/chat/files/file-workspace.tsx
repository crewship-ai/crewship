"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import dynamic from "next/dynamic"
import { EditorState } from "@codemirror/state"
import { EditorView } from "@codemirror/view"
import { ArrowLeft, Download, MessageSquare, Pencil, Save, X } from "lucide-react"
import { apiFetch } from "@/lib/api-fetch"
import { isPreviewable } from "@/lib/file-format"
import { isManagerTier } from "@/lib/permissions/tiers"
import { useWorkspace } from "@/hooks/use-workspace"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { getChatFileIcon, getEditorLanguage } from "../chat-tree-row"
import { fileRoute, type EditorScope } from "../hooks/use-file-editor"
import { FilePreview } from "./file-preview"
import { readPreviewBytes } from "./file-preview-data"

const FileEditor = dynamic(
  () => import("@/components/features/files/file-editor").then((module) => module.FileEditor),
  { ssr: false, loading: () => <div className="flex h-full items-center justify-center"><Spinner className="size-5" /></div> },
)

export interface WorkspaceFile {
  path: string
  name: string
  scope: EditorScope
}

export interface FileWorkspaceProps {
  file: WorkspaceFile
  agentId: string
  workspaceId: string
  /** Parent checks unsaved edits before changing selection or closing. */
  onDirtyChange?: (dirty: boolean) => void
  onClose: () => void
  /** The parent owns whether conversation remains alongside this workspace. */
  onChatAlongside?: () => void
}

const TEXT_LIMIT = 2 * 1024 * 1024

/** Authenticated, agent-scoped file reader. The Files tree remains in its own panel. */
export function FileWorkspace(props: FileWorkspaceProps) {
  const { file, agentId, workspaceId } = props
  return <FileWorkspaceContent key={`${workspaceId}:${agentId}:${file.scope.kind}:${file.scope.kind === "crew" ? file.scope.crewId : ""}:${file.path}`} {...props} />
}

function FileWorkspaceContent(props: FileWorkspaceProps) {
  const { file, agentId, workspaceId, onDirtyChange, onClose, onChatAlongside } = props
  const { role } = useWorkspace()
  const canEdit = isManagerTier(role) && isPreviewable(file.name)
  const textFile = isPreviewable(file.name)
  const route = fileRoute(file.scope, agentId, "download", workspaceId, file.path)
  const [content, setContent] = useState<string | null>(null)
  const [loading, setLoading] = useState(textFile)
  const [error, setError] = useState("")
  const [attempt, setAttempt] = useState(0)
  const [editing, setEditing] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState("")
  const [editorVersion, setEditorVersion] = useState(0)
  const saveRef = useRef<(() => void) | null>(null)
  const latestBytes = useRef<Uint8Array<ArrayBuffer> | null>(null)

  useEffect(() => {
    setContent(null)
    setLoading(textFile)
    setError("")
    setEditing(false)
    setDirty(false)
    setSaveError("")
    latestBytes.current = null
    onDirtyChange?.(false)
    if (!textFile) return
    const controller = new AbortController()
    void readPreviewBytes(route, controller.signal).then((bytes) => {
      if (bytes.byteLength > TEXT_LIMIT) throw new Error("This text file is too large to preview (2 MiB limit). Download it to inspect locally.")
      if (bytes.includes(0)) throw new Error("This file contains binary data and cannot be opened as text.")
      const decoded = new TextDecoder("utf-8", { fatal: true }).decode(bytes)
      if (!controller.signal.aborted) {
        latestBytes.current = bytes
        setContent(decoded)
      }
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : "Could not load this file.")
    }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
    // The selected file identity and retry attempt are the only read dependencies.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [route, textFile, attempt])

  const editorExtensions = useMemo(() => [], [])
  // CodeMirror renders the same syntax-highlighted document in both states.
  // Read-only is the default; the user explicitly enters edit mode.
  const readOnlyExtensions = useMemo(() => [EditorState.readOnly.of(true), EditorView.editable.of(false)], [])

  const onEditorDirty = useCallback((value: boolean) => {
    setDirty(value)
    onDirtyChange?.(value)
  }, [onDirtyChange])

  const save = useCallback(async (next: string) => {
    if (!canEdit || saving) return
    setSaving(true)
    setSaveError("")
    try {
      const response = await apiFetch(fileRoute(file.scope, agentId, "save", workspaceId, file.path), {
        method: "PUT",
        headers: { "Content-Type": "text/plain" },
        body: next,
      })
      if (!response.ok) throw new Error(`Could not save file (${response.status}).`)
      setContent(next)
      latestBytes.current = new TextEncoder().encode(next)
      setEditing(false)
      setDirty(false)
      onDirtyChange?.(false)
      setEditorVersion((value) => value + 1)
    } catch (cause) {
      setSaveError(cause instanceof Error ? cause.message : "Could not save file.")
    } finally {
      setSaving(false)
    }
  }, [agentId, canEdit, file.path, file.scope, onDirtyChange, saving, workspaceId])

  const download = () => {
    const bytes = latestBytes.current
    if (!bytes) return
    const url = URL.createObjectURL(new Blob([bytes], { type: "application/octet-stream" }))
    const link = document.createElement("a")
    link.href = url
    link.download = file.name
    link.click()
    window.setTimeout(() => URL.revokeObjectURL(url), 1000)
  }

  return <section aria-label={`File workspace ${file.name}`} className="flex h-full min-h-0 min-w-0 flex-col bg-background">
    <header className="flex min-h-12 shrink-0 flex-wrap items-center gap-2 border-b px-3 py-1.5">
      <Button variant="ghost" size="icon" aria-label="Back to chat" onClick={onClose}><ArrowLeft className="size-4" /></Button>
      {getChatFileIcon(file.name, false)}
      <div className="min-w-0 flex-1">
        <div className="truncate text-sm font-medium" title={file.path}>{file.name}</div>
        <div className="truncate text-xs text-muted-foreground" title={file.path}>{file.path}</div>
      </div>
      {dirty && <span className="text-xs text-warn">Unsaved changes</span>}
      {onChatAlongside && <Button variant="outline" size="sm" onClick={onChatAlongside}><MessageSquare className="size-3.5" /> Chat alongside</Button>}
      {textFile && <Button variant="ghost" size="icon" aria-label="Download file" disabled={content === null} onClick={download}><Download className="size-4" /></Button>}
      {canEdit && content !== null && !editing && <Button variant="outline" size="sm" onClick={() => setEditing(true)}><Pencil className="size-3.5" /> Edit</Button>}
      {editing && <>
        <Button variant="outline" size="sm" onClick={() => {
          if (dirty && !window.confirm("Discard unsaved file changes?")) return
          setEditing(false); setDirty(false); onDirtyChange?.(false); setEditorVersion((value) => value + 1)
        }}><X className="size-3.5" /> Cancel</Button>
        <Button size="sm" disabled={!dirty || saving} onClick={() => saveRef.current?.()}>
          {saving ? <Spinner className="size-3.5" /> : <Save className="size-3.5" />} Save
        </Button>
      </>}
    </header>
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
      {textFile ? <>
        {loading && <div role="status" className="flex items-center gap-2 p-4 text-sm text-muted-foreground"><Spinner className="size-4" /> Loading file…</div>}
        {error && <div className="space-y-3 p-4 text-sm"><p role="alert">{error}</p><Button variant="outline" onClick={() => setAttempt((value) => value + 1)}>Retry</Button></div>}
        {content !== null && <FileEditor
          key={`${route}:${editorVersion}:${editing}`}
          code={content}
          language={getEditorLanguage(file.name)}
          onSave={(next) => { if (editing) void save(next) }}
          onDirtyChange={onEditorDirty}
          saveRef={saveRef}
          extraExtensions={editing ? editorExtensions : readOnlyExtensions}
        />}
      </> : <FilePreview url={route} name={file.name} onClose={onClose} showHeader={false} />}
    </div>
    <footer className="flex shrink-0 items-center justify-between border-t px-3 py-1.5 text-xs text-muted-foreground">
      <span>{editing ? "Edit mode · Ctrl+S to save" : "Read-only preview · Select Edit to make changes"}</span>
      <span>{getEditorLanguage(file.name)}</span>
    </footer>
    {saveError && <p role="alert" className="border-t px-3 py-1.5 text-xs text-destructive">{saveError}</p>}
  </section>
}
