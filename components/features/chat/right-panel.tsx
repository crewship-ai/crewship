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
  Globe,
  Bot as BotIcon,
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
import { useUserPreference } from "@/hooks/use-user-preference"
import { ScopeSection } from "./files/scope-section"
import { TriggersTab } from "./right-panel-tabs/triggers-tab"
import { SharedContextTab } from "./right-panel-tabs/shared-context-tab"
import { TeamTab } from "./right-panel-tabs/team-tab"

interface ChatFileTreeState {
  expandedPaths: string[]
  lastOpenedPath: string | null
}

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
  hideTabs?: boolean
  style?: React.CSSProperties
}

export const RightPanel = React.memo(function RightPanel({ agentId, workspaceId, files, initialTab, hideTabs, style }: RightPanelProps) {
  const [activeTab, setActiveTab] = useState<string>(initialTab ?? "files")
  const [tree, setTree] = useState<TreeNode[]>([])
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [loadingDirs, setLoadingDirs] = useState<Set<string>>(new Set())
  const [basePrefix, setBasePrefix] = useState("")
  // Per-agent persistence for the chat-side Files tab — same shape as
  // the bottom-panel FilesTab pref. Keyed by agentId so each agent
  // remembers its own tree state.
  const [savedTreeState, setSavedTreeState] = useUserPreference<ChatFileTreeState>(
    `chat.fileTree.${agentId}`,
    { expandedPaths: [], lastOpenedPath: null },
  )
  // Keyed by agentId so per-agent replay state doesn't leak to the
  // next agent. Cleared in the agent-change effect below.
  const replayedForAgentRef = React.useRef<string | null>(null)
  const fetchedDirsRef = React.useRef<Set<string>>(new Set())

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

  // Reset all per-agent state on agent change so the next agent
  // doesn't inherit the previous one's expanded set or open editor.
  useEffect(() => {
    replayedForAgentRef.current = null
    fetchedDirsRef.current = new Set()
    setExpanded(new Set())
    closeEditor()
  }, [agentId, closeEditor])

  // Replay saved expanded paths + last-opened file. Bulk-adds to
  // `expanded`; the fetch effect below handles loading children
  // sequentially as each parent's response arrives, so deeply-nested
  // saved paths restore correctly.
  useEffect(() => {
    if (replayedForAgentRef.current === agentId) return
    if (files.length === 0 || !workspaceId) return
    replayedForAgentRef.current = agentId
    const saved = savedTreeState
    if (saved.expandedPaths.length > 0) {
      setExpanded(new Set(saved.expandedPaths))
    }
    if (saved.lastOpenedPath) {
      const name = saved.lastOpenedPath.split("/").pop() ?? ""
      openFileEditor({ path: saved.lastOpenedPath, name })
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agentId, files, workspaceId])

  // Persist current state. Debounced inside the useUserPreference hook.
  useEffect(() => {
    if (replayedForAgentRef.current !== agentId) return
    setSavedTreeState({
      expandedPaths: Array.from(expanded),
      lastOpenedPath: editorFile?.path ?? null,
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [expanded, editorFile?.path, agentId])

  // Fetch children for any expanded path whose tree node is reachable
  // but not yet loaded. Both user toggles and replay write into
  // `expanded`; this watcher centralizes the fetch logic so deep
  // saved paths (`src/components/foo`) replay correctly — once `src`
  // resolves, the watcher re-fires and fetches `src/components`,
  // and so on.
  useEffect(() => {
    if (!workspaceId) return
    for (const path of expanded) {
      if (loadingDirs.has(path) || fetchedDirsRef.current.has(path)) continue
      const node = tree.reduce<TreeNode | undefined>(
        (found, n) => found ?? findTreeNode(n, path),
        undefined,
      )
      if (!node || node.childrenLoaded) continue
      fetchedDirsRef.current.add(path)
      const relPath = path.startsWith(basePrefix) ? path.slice(basePrefix.length) : path
      setLoadingDirs((p) => new Set(p).add(path))
      fetch(`/api/v1/agents/${agentId}/files?workspace_id=${workspaceId}&subdir=${encodeURIComponent(relPath)}`)
        .then((r) => { if (!r.ok) throw new Error("Failed"); return r.json() })
        .then((data: FileEntry[] | null) => setTree((prev) => insertTreeChildren(prev, path, data ?? [])))
        .catch(() => { toast.error("Failed to load folder") })
        .finally(() => setLoadingDirs((p) => { const n = new Set(p); n.delete(path); return n }))
    }
  }, [tree, expanded, workspaceId, basePrefix, agentId, loadingDirs])

  const toggleFolder = useCallback((path: string) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }, [])

  const fileCount = files.filter((f) => !f.is_dir).length
  const editorOpen = editorFile !== null && activeTab === "files"

  return (
    <div className="flex flex-col border-l overflow-hidden" style={style}>
      {!hideTabs && <div className="flex items-end shrink-0 overflow-x-auto scrollbar-none border-b h-[41px]">
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
      </div>}

      {/* Tree area -- scrolls independently */}
      <div className={cn("overflow-y-auto", editorOpen ? "flex-1 min-h-0" : "flex-1")}>
        {activeTab === "files" && (
          <div>
            <ScopeSection icon={BotIcon} title="Agent" count={fileCount} defaultOpen>
              {tree.length > 0 ? (
                <div className="py-0.5">
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
                <div className="flex items-center gap-1.5 px-3 py-1.5 text-xs text-muted-foreground/70">
                  <FileText className="h-3 w-3" />
                  No files in this session yet
                </div>
              )}
            </ScopeSection>
            <ScopeSection icon={Users} title="Crew" defaultOpen={false}>
              <div className="flex items-center gap-1.5 px-3 py-1.5 text-xs text-muted-foreground/70">
                <FileText className="h-3 w-3" />
                Shared crew files (loaded on demand)
              </div>
            </ScopeSection>
            <ScopeSection
              icon={Globe}
              title="Workspace"
              defaultOpen={false}
              badge={
                <span className="rounded bg-amber-50 dark:bg-amber-950/30 px-1.5 text-[10px] text-amber-700 dark:text-amber-400">
                  soon
                </span>
              }
            >
              <div className="flex items-center gap-1.5 px-3 py-1.5 text-xs text-muted-foreground/70">
                <FileText className="h-3 w-3" />
                Workspace-level files — backend pending
              </div>
            </ScopeSection>
          </div>
        )}

        {activeTab === "triggers" && (
          <TriggersTab agentId={agentId} workspaceId={workspaceId} />
        )}

        {activeTab === "team" && (
          <TeamTab agentId={agentId} workspaceId={workspaceId} />
        )}

        {activeTab === "context" && (
          <SharedContextTab agentId={agentId} workspaceId={workspaceId} />
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
