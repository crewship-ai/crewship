"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import dynamic from "next/dynamic"
import { ChevronDown, File, Folder, Loader2, Pencil, Save } from "lucide-react"
import { toast } from "sonner"
import { cn } from "@/lib/utils"

import { getEditorLanguage } from "@/components/features/chat/chat-tree-row"
import { useUserPreference } from "@/hooks/use-user-preference"

import type { BottomPanelContext, FileEntry } from "./types"
import { EmptyState, formatBytes } from "./shared"

// Files only operate on a container-backed entity — an agent's home dir or
// a crew's shared tree. The dock router guarantees this narrower context;
// the type keeps the field accesses below sound.
type FilesContext = Extract<BottomPanelContext, { kind: "agent" } | { kind: "crew" }> | null

const FileEditor = dynamic(
  () => import("@/components/features/files/file-editor").then((m) => m.FileEditor),
  {
    ssr: false,
    loading: () => (
      <div className="h-full grid place-items-center text-xs text-muted-foreground">
        Loading editor…
      </div>
    ),
  },
)

/** Per-context tree state we persist for the user. The pref is keyed
 *  by `<kind>:<id>` so each agent + each crew has its own remembered
 *  tree (agent on Filip ≠ agent on Lucie ≠ crew DevOps view). */
interface TreeState {
  expandedPaths: string[]
  lastOpenedPath: string | null
  editing: boolean
}

/**
 * Files — uses /api/v1/agents/{agentId}/files for an agent and
 * /api/v1/crews/{crewId}/files for a crew. The crew variant lists the
 * shared crew tree (/crew/shared) via the sidecar proxy.
 *
 * Now supports lazy directory expansion (click a folder to fetch its
 * children inline) + inline file preview pane on the right (click a
 * file to read its contents via /agents/{id}/files/download).
 */
export function FilesTab({ workspaceId, context }: { workspaceId: string; context: FilesContext }) {
  const [tree, setTree] = useState<FileEntry[] | null>(null)
  const [expanded, setExpanded] = useState<Record<string, FileEntry[] | "loading" | "error">>({})
  const [error, setError] = useState<string | null>(null)
  const [previewPath, setPreviewPath] = useState<string | null>(null)
  const [previewContent, setPreviewContent] = useState<string | null>(null)
  const [previewError, setPreviewError] = useState<string | null>(null)
  // Edit mode state — false means read-only (default). Toggle via the
  // top-right button. dirty is tracked by the FileEditor's onDirtyChange
  // callback so Save only enables when there are real changes.
  const [editing, setEditing] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const editorSaveRef = useRef<(() => void) | null>(null)

  // Per-user persistence: which folders are open + which file the
  // user last had open + whether they were editing it. Keyed by
  // context so each agent and each crew remembers its own state.
  const ctxKey = context
    ? context.kind === "agent"
      ? `agent.${context.agentId}`
      : `crew.${context.crewId}`
    : "none"
  const [savedTreeState, setSavedTreeState] = useUserPreference<TreeState>(
    `crews.fileTree.${ctxKey}`,
    { expandedPaths: [], lastOpenedPath: null, editing: false },
  )
  // Snapshot the saved state at context-change time. Subsequent updates
  // (when the user clicks around) flow OUT to the pref; we don't want
  // those to retrigger the replay path.
  const savedRef = useRef(savedTreeState)
  savedRef.current = savedTreeState

  useEffect(() => {
    if (!context) return
    let cancelled = false
    const saved = savedRef.current
    setTree(null)
    setError(null)
    setExpanded({})
    setPreviewPath(saved.lastOpenedPath)
    setPreviewContent(null)
    setEditing(saved.editing && saved.lastOpenedPath !== null)
    setDirty(false)
    const url = context.kind === "agent"
      ? `/api/v1/agents/${context.agentId}/files?workspace_id=${workspaceId}&path=/`
      : `/api/v1/crews/${context.crewId}/files?workspace_id=${workspaceId}`
    fetch(url)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((data) => {
        if (cancelled) return
        // Crew endpoint wraps in {crew_id, files}; agent endpoint
        // already unwraps to a bare array via the proxy. Accept both.
        const entries = Array.isArray(data?.files)
          ? data.files
          : Array.isArray(data?.entries)
            ? data.entries
            : Array.isArray(data)
              ? data
              : []
        setTree(entries)
      })
      .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : String(err)) })
    return () => { cancelled = true }
  }, [context, workspaceId])

  const fetchDir = useCallback(async (subdir: string) => {
    if (!context) return
    setExpanded((p) => ({ ...p, [subdir]: "loading" }))
    try {
      const url = context.kind === "agent"
        ? `/api/v1/agents/${context.agentId}/files?workspace_id=${workspaceId}&subdir=${encodeURIComponent(subdir)}`
        : `/api/v1/crews/${context.crewId}/files?workspace_id=${workspaceId}&subdir=${encodeURIComponent(subdir)}`
      const r = await fetch(url)
      if (!r.ok) throw new Error(`HTTP ${r.status}`)
      const data = await r.json()
      // Crew endpoint wraps in {crew_id, files}; agent endpoint already
      // unwraps to a bare array. Handle both.
      const entries = Array.isArray(data?.files)
        ? data.files
        : Array.isArray(data?.entries)
          ? data.entries
          : Array.isArray(data)
            ? data
            : []
      setExpanded((p) => ({ ...p, [subdir]: entries as FileEntry[] }))
    } catch {
      setExpanded((p) => ({ ...p, [subdir]: "error" }))
    }
  }, [context, workspaceId])

  const toggleFolder = useCallback((path: string) => {
    setExpanded((p) => {
      if (p[path] && p[path] !== "loading" && p[path] !== "error") {
        const next = { ...p }
        delete next[path]
        return next
      }
      return p
    })
    if (!expanded[path] || expanded[path] === "error") {
      void fetchDir(path)
    }
  }, [expanded, fetchDir])

  const openFile = useCallback(async (filePath: string, _fileName: string) => {
    if (!context) return
    setPreviewPath(filePath)
    setPreviewContent(null)
    setPreviewError(null)
    setEditing(false)
    setDirty(false)
    try {
      const url = context.kind === "agent"
        ? `/api/v1/agents/${context.agentId}/files/download?workspace_id=${workspaceId}&path=${encodeURIComponent(filePath)}`
        : `/api/v1/crews/${context.crewId}/files/download?workspace_id=${workspaceId}&path=${encodeURIComponent(filePath)}`
      const r = await fetch(url)
      if (!r.ok) throw new Error(`HTTP ${r.status}`)
      const text = await r.text()
      // Cap at 256 KB to avoid hammering the panel with pathological files.
      const MAX = 256 * 1024
      setPreviewContent(text.length > MAX ? text.slice(0, MAX) + `\n\n... [truncated · file is ${text.length.toLocaleString()} bytes total]` : text)
    } catch (err) {
      setPreviewError(err instanceof Error ? err.message : String(err))
    }
  }, [context, workspaceId])

  // Replay saved tree state once the top-level listing loads. Folders
  // open in the saved order; the last-opened file is fetched + shown
  // (along with the editing flag if the user had Edit mode active
  // when they last left). Runs once per context-change cycle.
  const replayedForCtxRef = useRef<string>("")
  useEffect(() => {
    if (!context || tree === null) return
    if (replayedForCtxRef.current === ctxKey) return
    replayedForCtxRef.current = ctxKey
    const saved = savedRef.current
    for (const p of saved.expandedPaths) {
      void fetchDir(p)
    }
    if (saved.lastOpenedPath) {
      const name = saved.lastOpenedPath.split("/").pop() ?? ""
      void openFile(saved.lastOpenedPath, name).then(() => {
        if (saved.editing) setEditing(true)
      })
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [context, tree, ctxKey])

  // Persist current tree state. Debounced inside useUserPreference
  // so a folder-expand storm doesn't hammer the API.
  useEffect(() => {
    if (!context || tree === null) return
    const expandedPaths = Object.keys(expanded).filter(
      (k) => Array.isArray(expanded[k]),
    )
    setSavedTreeState({
      expandedPaths,
      lastOpenedPath: previewPath,
      editing,
    })
    // setSavedTreeState is stable from the hook; including it would
    // be a no-op but keeps the dep linter happy.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [context, tree, expanded, previewPath, editing])

  const handleSave = useCallback(async (next: string) => {
    if (!context || !previewPath) return
    setSaving(true)
    try {
      const url = context.kind === "agent"
        ? `/api/v1/agents/${context.agentId}/files/save?workspace_id=${workspaceId}&path=${encodeURIComponent(previewPath)}`
        : `/api/v1/crews/${context.crewId}/files/save?workspace_id=${workspaceId}&path=${encodeURIComponent(previewPath)}`
      const r = await fetch(url, {
        method: "PUT",
        headers: { "Content-Type": "text/plain" },
        body: next,
      })
      if (!r.ok) {
        const data = await r.json().catch(() => ({ error: `HTTP ${r.status}` }))
        toast.error(typeof data.error === "string" ? data.error : "Save failed")
        return
      }
      // Persist the new content as the new baseline so re-opening the
      // file or hitting Cancel doesn't revert to the pre-save text.
      setPreviewContent(next)
      setDirty(false)
      setEditing(false)
      toast.success(`Saved · ${previewPath.split("/").pop()}`)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Save failed")
    } finally {
      setSaving(false)
    }
  }, [context, previewPath, workspaceId])

  if (!context) return <EmptyState>Select an agent or crew to browse files.</EmptyState>
  if (error) return <EmptyState><span className="text-red-300">Failed to load: {error}</span></EmptyState>
  if (tree === null) return <EmptyState>Loading…</EmptyState>
  if (tree.length === 0) {
    return (
      <EmptyState>
        {context.kind === "agent"
          ? `No files in ${context.agentName}'s home dir.`
          : "No shared files in this crew yet."}
      </EmptyState>
    )
  }

  // Path label shown atop the tree. For an agent this is the agent's
  // home dir inside the runtime container. For a crew it's the bind-
  // mount root that holds every member's workspace — so the label
  // names the crew rather than calling it "shared" (which incorrectly
  // suggests there's only one merged folder).
  const rootPath = context.kind === "agent"
    ? `/crew/agents/${context.agentSlug}/`
    : `${context.crewSlug} · all agents`

  return (
    <div className="h-full grid grid-cols-1 md:grid-cols-[minmax(220px,40%)_1fr] gap-0">
      {/* Tree */}
      <div className="overflow-y-auto p-3 text-xs border-r border-white/8">
        <div className="text-muted-foreground mb-2 font-mono">{rootPath}</div>
        <ul className="font-mono space-y-0.5">
          {tree.map((f) => (
            <FileRow
              key={f.name}
              entry={f}
              parentPath=""
              depth={0}
              expanded={expanded}
              onToggleFolder={toggleFolder}
              onOpenFile={openFile}
              activePath={previewPath}
            />
          ))}
        </ul>
      </div>
      {/* Preview pane */}
      <div className="overflow-hidden flex flex-col min-h-0">
        {previewPath ? (
          <>
            <div className="flex items-center gap-2 px-3 py-1.5 border-b border-white/8 text-xs text-muted-foreground">
              <File className="h-3 w-3 shrink-0" />
              <span className="font-mono truncate flex-1">{previewPath}</span>
              {dirty && (
                <span className="text-[10px] text-amber-300 inline-flex items-center gap-1">
                  <span className="h-1.5 w-1.5 rounded-full bg-amber-400" />
                  Unsaved
                </span>
              )}
              {previewContent !== null && !dirty && (
                <span className="text-[10px]">
                  {previewContent.length.toLocaleString()} chars
                </span>
              )}
              {/* Edit / Save / Cancel — primary actions for the pane */}
              {previewContent !== null && previewError === null && (
                <>
                  {!editing ? (
                    <button
                      type="button"
                      onClick={() => setEditing(true)}
                      className="flex items-center gap-1 text-xs px-2 py-0.5 rounded bg-blue-500/15 hover:bg-blue-500/25 text-blue-300 border border-blue-500/30 ml-1"
                    >
                      <Pencil className="h-3 w-3" />
                      Edit
                    </button>
                  ) : (
                    <div className="flex items-center gap-1 ml-1">
                      <button
                        type="button"
                        onClick={() => editorSaveRef.current?.()}
                        disabled={!dirty || saving}
                        className={cn(
                          "flex items-center gap-1 text-xs px-2 py-0.5 rounded border transition-colors",
                          dirty && !saving
                            ? "bg-blue-500 hover:bg-blue-400 text-white border-blue-400"
                            : "bg-zinc-800 text-muted-foreground border-white/10 cursor-default",
                        )}
                      >
                        {saving
                          ? <Loader2 className="h-3 w-3 animate-spin" />
                          : <Save className="h-3 w-3" />}
                        Save
                      </button>
                      <button
                        type="button"
                        onClick={() => {
                          setEditing(false)
                          setDirty(false)
                          // Re-fetch to drop in-flight CodeMirror edits
                          if (previewPath) void openFile(previewPath, previewPath.split("/").pop() ?? "")
                        }}
                        disabled={saving}
                        className="flex items-center gap-1 text-xs px-2 py-0.5 rounded border border-white/10 hover:bg-white/5 text-muted-foreground"
                      >
                        Cancel
                      </button>
                    </div>
                  )}
                </>
              )}
            </div>
            {previewError ? (
              <div className="p-4 text-xs text-red-300">Failed: {previewError}</div>
            ) : previewContent === null ? (
              <div className="p-4 text-xs text-muted-foreground">Loading…</div>
            ) : (
              <div className={cn("flex-1 min-h-0 overflow-hidden", !editing && "pointer-events-none")}>
                <FileEditor
                  // CodeMirror remounts when the doc string ref changes,
                  // so the key includes the editing flag — switching
                  // between read-only and editable modes builds a fresh
                  // editor with the current baseline content (otherwise
                  // dirty state can leak across mode toggles).
                  key={`${previewPath}::${editing ? "edit" : "read"}`}
                  code={previewContent}
                  language={getEditorLanguage(previewPath.split("/").pop() ?? "")}
                  onSave={handleSave}
                  onDirtyChange={setDirty}
                  saveRef={editorSaveRef}
                />
              </div>
            )}
          </>
        ) : (
          <div className="flex items-center justify-center h-full text-xs text-muted-foreground px-6 text-center">
            Click a file in the tree to preview its contents.
          </div>
        )}
      </div>
    </div>
  )
}

interface FileRowProps {
  entry: FileEntry
  parentPath: string
  depth: number
  expanded: Record<string, FileEntry[] | "loading" | "error">
  onToggleFolder: (path: string) => void
  onOpenFile: (path: string, name: string) => void
  activePath: string | null
}

function FileRow({ entry, parentPath, depth, expanded, onToggleFolder, onOpenFile, activePath }: FileRowProps) {
  // Prefer the storage-rooted path returned by the API. The IPC layer
  // prefix-checks against the crewID, so reconstructing from name +
  // parentPath would drop the leading `<crewID>/<slug>/` prefix and
  // hit the wrong target (or fail the prefix check entirely).
  const path = entry.path ?? (parentPath ? `${parentPath}/${entry.name}` : entry.name)
  const state = expanded[path]
  const isOpen = state && state !== "loading" && state !== "error"
  const children = isOpen ? (state as FileEntry[]) : []
  const isActive = activePath === path

  return (
    <>
      <li>
        <button
          type="button"
          onClick={() => {
            if (entry.is_dir) {
              onToggleFolder(path)
            } else {
              onOpenFile(path, entry.name)
            }
          }}
          className={cn(
            "w-full flex items-center gap-2 px-2 -mx-2 py-0.5 rounded text-left transition-colors",
            isActive ? "bg-blue-500/15 text-blue-200" : "text-foreground/85 hover:bg-white/[0.03]",
          )}
          style={{ paddingLeft: `${depth * 12 + 8}px` }}
        >
          {entry.is_dir ? (
            <span className="inline-flex items-center w-3 shrink-0">
              <ChevronDown className={cn("h-3 w-3 text-muted-foreground transition-transform", !isOpen && "-rotate-90")} />
            </span>
          ) : (
            <span className="inline-block w-3 shrink-0" />
          )}
          {entry.is_dir
            ? <Folder className="h-3 w-3 shrink-0 text-blue-300" />
            : <File className="h-3 w-3 shrink-0 text-muted-foreground" />}
          <span className="flex-1 truncate">{entry.name}</span>
          {entry.size !== undefined && !entry.is_dir && (
            <span className="text-[10px] text-muted-foreground">{formatBytes(entry.size)}</span>
          )}
          {state === "loading" && <span className="text-[10px] text-muted-foreground italic">…</span>}
          {state === "error" && <span className="text-[10px] text-red-400">!</span>}
        </button>
      </li>
      {isOpen && children.map((child) => (
        <FileRow
          key={child.name}
          entry={child}
          parentPath={path}
          depth={depth + 1}
          expanded={expanded}
          onToggleFolder={onToggleFolder}
          onOpenFile={onOpenFile}
          activePath={activePath}
        />
      ))}
    </>
  )
}
