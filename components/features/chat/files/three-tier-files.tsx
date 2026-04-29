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
import { ScopeSection } from "./scope-section"

interface ThreeTierFilesProps {
  crewId?: string | null
  workspaceId: string | null
  /** Pre-fetched agent-level files (for the active session). */
  agentFiles: FileEntry[]
  /** Currently open file (path), used for selection highlight. */
  selectedFile?: string | null
  /** Click handler — fires for any file row across all 3 scopes. */
  onFileClick: (node: TreeNode) => void
}

export function ThreeTierFiles({
  crewId,
  workspaceId,
  agentFiles,
  selectedFile,
  onFileClick,
}: ThreeTierFilesProps) {
  const [agentTree, setAgentTree] = useState<TreeNode[]>([])
  const [crewFiles, setCrewFiles] = useState<FileEntry[]>([])
  const [crewLoading, setCrewLoading] = useState(false)
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  const toggleFolder = (path: string) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }

  useEffect(() => {
    setAgentTree(buildTopLevelTree(agentFiles))
  }, [agentFiles])

  useEffect(() => {
    if (!crewId || !workspaceId) {
      // Clear stale crew tree on context loss — otherwise a swap to an
      // agent without a crew briefly renders the previous crew's files.
      setCrewFiles([])
      setCrewLoading(false)
      return
    }
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
                selectedFile={selectedFile ?? null}
                onToggle={toggleFolder}
                onFileClick={onFileClick}
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
                selectedFile={selectedFile ?? null}
                onToggle={toggleFolder}
                onFileClick={onFileClick}
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
