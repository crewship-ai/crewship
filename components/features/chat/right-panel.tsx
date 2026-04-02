"use client"

import React, { useCallback, useEffect, useState } from "react"
import dynamic from "next/dynamic"
import {
  FileText,
  Zap,
  Users,
  Bookmark,
  Loader2,
  X,
  Save,
  Maximize2,
  Minimize2,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { toast } from "sonner"

import {
  ChatTreeRow,
  type TreeNode,
  type FileEntry,
  buildTopLevelTree,
  insertTreeChildren,
  findTreeNode,
  getChatFileIcon,
  getEditorLanguage,
} from "./chat-tree-row"
import { useFileEditor } from "./hooks/use-file-editor"

const FileEditor = dynamic(
  () => import("@/components/features/files/file-editor").then((m) => m.FileEditor),
  { ssr: false, loading: () => <div className="flex items-center justify-center h-full"><Loader2 className="h-5 w-5 animate-spin text-muted-foreground" /></div> },
)

const RIGHT_PANEL_TABS = [
  { id: "files", label: "Files", icon: FileText },
  { id: "triggers", label: "Triggers", icon: Zap },
  { id: "team", label: "Team", icon: Users },
  { id: "context", label: "Context", icon: Bookmark },
] as const

interface RightPanelProps {
  agentId: string
  workspaceId: string | null
  files: FileEntry[]
  initialTab?: string
  style?: React.CSSProperties
}

export const RightPanel = React.memo(function RightPanel({ agentId, workspaceId, files, initialTab, style }: RightPanelProps) {
  const [activeTab, setActiveTab] = useState<string>(initialTab ?? "files")
  const [tree, setTree] = useState<TreeNode[]>([])
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [loadingDirs, setLoadingDirs] = useState<Set<string>>(new Set())
  const [basePrefix, setBasePrefix] = useState("")

  const {
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
  } = useFileEditor({ agentId, workspaceId })

  useEffect(() => {
    setTree(buildTopLevelTree(files))
    if (files.length > 0) {
      const first = files[0]
      const idx = first.path.lastIndexOf(first.name)
      setBasePrefix(idx > 0 ? first.path.slice(0, idx) : "")
    }
  }, [files])

  const toggleFolder = useCallback((path: string) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(path)) { next.delete(path); return next }
      next.add(path)
      const node = tree.reduce<TreeNode | undefined>((found, n) => found ?? findTreeNode(n, path), undefined)
      if (node && !node.childrenLoaded && workspaceId) {
        const relPath = path.startsWith(basePrefix) ? path.slice(basePrefix.length) : path
        setLoadingDirs((p) => new Set(p).add(path))
        fetch(`/api/v1/agents/${agentId}/files?workspace_id=${workspaceId}&subdir=${encodeURIComponent(relPath)}`)
          .then((r) => { if (!r.ok) throw new Error("Failed"); return r.json() })
          .then((data: FileEntry[] | null) => setTree((prev) => insertTreeChildren(prev, path, data ?? [])))
          .catch(() => { toast.error("Failed to load folder") })
          .finally(() => setLoadingDirs((p) => { const n = new Set(p); n.delete(path); return n }))
      }
      return next
    })
  }, [tree, agentId, workspaceId, basePrefix])

  const fileCount = files.filter((f) => !f.is_dir).length
  const editorOpen = editorFile !== null && activeTab === "files"

  return (
    <div className="flex flex-col border-l overflow-hidden" style={style}>
      <div className="flex items-end shrink-0 overflow-x-auto scrollbar-none border-b h-[41px]">
        {RIGHT_PANEL_TABS.map((tab) => (
          <button
            key={tab.id}
            onClick={() => setActiveTab(tab.id)}
            className={cn(
              "flex-1 flex items-center justify-center gap-1.5 pb-2.5 text-micro font-medium transition-colors shrink-0 border-b-2 mb-[-1px]",
              tab.id === activeTab
                ? "text-foreground border-primary"
                : "text-muted-foreground hover:text-foreground border-transparent"
            )}
          >
            <tab.icon className="h-3.5 w-3.5" />
            {tab.label}
          </button>
        ))}
      </div>

      {/* Tree area -- scrolls independently */}
      <div className={cn("overflow-y-auto", editorOpen ? "flex-1 min-h-0" : "flex-1")}>
        {activeTab === "files" && (
          tree.length > 0 ? (
            <div className="py-1">
              {tree.map((node) => (
                <ChatTreeRow
                  key={node.path}
                  node={node}
                  depth={0}
                  expanded={expanded}
                  loadingDirs={loadingDirs}
                  selectedFile={editorFile?.path ?? null}
                  onToggle={toggleFolder}
                  onFileClick={openFileEditor}
                />
              ))}
            </div>
          ) : (
            <div className="flex flex-col items-center justify-center h-full text-muted-foreground">
              <FileText className="h-8 w-8 mb-2 opacity-20" />
              <p className="text-label">No files yet</p>
            </div>
          )
        )}

        {activeTab === "triggers" && (
          <div className="flex flex-col items-center justify-center h-full text-center p-6">
            <div className="h-10 w-10 rounded-full bg-accent flex items-center justify-center mb-3">
              <Zap className="h-5 w-5 text-muted-foreground" />
            </div>
            <p className="text-body font-medium">Triggers</p>
            <p className="text-label text-muted-foreground mt-1">Schedule cron jobs, webhooks, and automated triggers for this agent.</p>
            <span className="text-micro text-muted-foreground mt-3 px-2 py-1 bg-accent rounded-full">Coming soon</span>
          </div>
        )}

        {activeTab === "team" && (
          <div className="flex flex-col items-center justify-center h-full text-center p-6">
            <div className="h-10 w-10 rounded-full bg-accent flex items-center justify-center mb-3">
              <Users className="h-5 w-5 text-muted-foreground" />
            </div>
            <p className="text-body font-medium">Team Chat</p>
            <p className="text-label text-muted-foreground mt-1">Real-time communication between agents, leads, and crew members.</p>
            <span className="text-micro text-muted-foreground mt-3 px-2 py-1 bg-accent rounded-full">Coming soon</span>
          </div>
        )}

        {activeTab === "context" && (
          <div className="flex flex-col items-center justify-center h-full text-center p-6">
            <div className="h-10 w-10 rounded-full bg-accent flex items-center justify-center mb-3">
              <Bookmark className="h-5 w-5 text-muted-foreground" />
            </div>
            <p className="text-body font-medium">Shared Context</p>
            <p className="text-label text-muted-foreground mt-1">Shared instructions, knowledge base, and mission context for the agent.</p>
            <span className="text-micro text-muted-foreground mt-3 px-2 py-1 bg-accent rounded-full">Coming soon</span>
          </div>
        )}
      </div>

      {/* Slide-up editor */}
      {editorOpen && (
        <div className={cn(
          "flex flex-col border-t bg-[#1e1e1e] shrink-0 transition-all duration-300 ease-in-out",
          editorExpanded ? "h-[70%]" : "h-[40%]",
        )}>
          {/* Editor header */}
          <div className="flex items-center justify-between px-3 py-1.5 bg-[#252526] border-b border-[#3c3c3c] shrink-0">
            <div className="flex items-center gap-2 min-w-0">
              {getChatFileIcon(editorFile.name, false)}
              <span className="text-label text-[#cccccc] font-medium truncate">{editorFile.name}</span>
              {editorDirty && <span className="w-1.5 h-1.5 rounded-full bg-amber-400 shrink-0" />}
            </div>
            <div className="flex items-center gap-1 shrink-0">
              <button
                onClick={() => { if (saveRef.current) saveRef.current() }}
                disabled={!editorDirty || editorSaving}
                className={cn(
                  "flex items-center gap-1 px-2 py-0.5 rounded text-micro font-medium transition-colors",
                  editorDirty && !editorSaving
                    ? "bg-blue-600 text-white hover:bg-blue-700"
                    : "bg-[#3c3c3c] text-[#666] cursor-default",
                )}
              >
                {editorSaving ? <Loader2 className="h-3 w-3 animate-spin" /> : <Save className="h-3 w-3" />}
                Save
              </button>
              <button onClick={() => setEditorExpanded(!editorExpanded)} className="p-1 rounded hover:bg-[#3c3c3c] text-[#888]">
                {editorExpanded ? <Minimize2 className="h-3 w-3" /> : <Maximize2 className="h-3 w-3" />}
              </button>
              <button onClick={closeEditor} className="p-1 rounded hover:bg-[#3c3c3c] text-[#888]">
                <X className="h-3 w-3" />
              </button>
            </div>
          </div>

          {/* Editor body */}
          <div className="flex-1 min-h-0 overflow-hidden">
            {editorLoading ? (
              <div className="flex items-center justify-center h-full">
                <Loader2 className="h-5 w-5 animate-spin text-[#888]" />
              </div>
            ) : editorContent !== null ? (
              <FileEditor
                code={editorContent}
                language={getEditorLanguage(editorFile.name)}
                onSave={handleEditorSave}
                onDirtyChange={setEditorDirty}
                saveRef={saveRef}
              />
            ) : (
              <div className="flex items-center justify-center h-full text-[#888] text-label">
                Unable to load file
              </div>
            )}
          </div>

          {/* Status bar */}
          <div className="flex items-center justify-between px-3 py-0.5 bg-[#007acc] text-micro text-white shrink-0">
            <span>Ctrl+S to save</span>
            {editorDirty && <span className="font-medium">Modified</span>}
          </div>
        </div>
      )}

      {/* Footer (file count) -- only when no editor */}
      {!editorOpen && activeTab === "files" && tree.length > 0 && (
        <div className="px-3 py-1.5 border-t text-micro text-muted-foreground shrink-0">
          {fileCount} file{fileCount !== 1 ? "s" : ""}
        </div>
      )}
    </div>
  )
})
