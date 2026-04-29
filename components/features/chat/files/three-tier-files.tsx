"use client"

import { useEffect, useState } from "react"
import { Bot, Users, Globe, FileText } from "lucide-react"
import { Loader2 } from "lucide-react"

import {
  ChatTreeRow,
  type FileEntry,
  type TreeNode,
  buildTopLevelTree,
} from "../chat-tree-row"
import { useFileEditor } from "../hooks/use-file-editor"
import { ScopeSection } from "./scope-section"

interface ThreeTierFilesProps {
  agentId: string
  crewId?: string | null
  workspaceId: string | null
  /** Pre-fetched agent-level files (for the active session). */
  agentFiles: FileEntry[]
}

export function ThreeTierFiles({
  agentId,
  crewId,
  workspaceId,
  agentFiles,
}: ThreeTierFilesProps) {
  const [agentTree, setAgentTree] = useState<TreeNode[]>([])
  const [crewFiles, setCrewFiles] = useState<FileEntry[]>([])
  const [crewLoading, setCrewLoading] = useState(false)

  const editor = useFileEditor({ agentId, workspaceId })
  const expanded = new Set<string>()

  useEffect(() => {
    setAgentTree(buildTopLevelTree(agentFiles))
  }, [agentFiles])

  useEffect(() => {
    if (!crewId || !workspaceId) return
    const ac = new AbortController()
    setCrewLoading(true)
    fetch(`/api/v1/crews/${crewId}/files?workspace_id=${workspaceId}`, {
      signal: ac.signal,
      credentials: "include",
    })
      .then((r) => (r.ok ? r.json() : []))
      .then((data: FileEntry[] | null) => setCrewFiles(data ?? []))
      .catch(() => {
        if (!ac.signal.aborted) setCrewFiles([])
      })
      .finally(() => {
        if (!ac.signal.aborted) setCrewLoading(false)
      })
    return () => ac.abort()
  }, [crewId, workspaceId])

  const crewTree = buildTopLevelTree(crewFiles)
  const agentFileCount = agentFiles.filter((f) => !f.is_dir).length
  const crewFileCount = crewFiles.filter((f) => !f.is_dir).length

  return (
    <div className="flex flex-col">
      <ScopeSection
        icon={Bot}
        title="Agent"
        count={agentFileCount}
        defaultOpen={true}
      >
        {agentTree.length === 0 ? (
          <EmptyHint>No files in this session yet</EmptyHint>
        ) : (
          <div className="py-0.5">
            {agentTree.map((node) => (
              <ChatTreeRow
                key={node.path}
                node={node}
                depth={0}
                expanded={expanded}
                loadingDirs={new Set()}
                selectedFile={editor.editorFile?.path ?? null}
                onToggle={() => {}}
                onFileClick={editor.openFileEditor}
              />
            ))}
          </div>
        )}
      </ScopeSection>

      <ScopeSection
        icon={Users}
        title="Crew"
        count={crewId ? crewFileCount : undefined}
        defaultOpen={false}
        badge={
          crewLoading ? <Loader2 className="h-3 w-3 animate-spin" /> : null
        }
      >
        {!crewId ? (
          <EmptyHint>Agent is not assigned to a crew</EmptyHint>
        ) : crewTree.length === 0 ? (
          <EmptyHint>No shared crew files</EmptyHint>
        ) : (
          <div className="py-0.5">
            {crewTree.map((node) => (
              <ChatTreeRow
                key={node.path}
                node={node}
                depth={0}
                expanded={expanded}
                loadingDirs={new Set()}
                selectedFile={null}
                onToggle={() => {}}
                onFileClick={() => {}}
              />
            ))}
          </div>
        )}
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
        <EmptyHint>
          <FileText className="h-3 w-3" />
          Workspace-level shared files — backend pending
        </EmptyHint>
      </ScopeSection>
    </div>
  )
}

function EmptyHint({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex items-center gap-1.5 px-3 py-1.5 text-xs text-muted-foreground/70">
      {children}
    </div>
  )
}
