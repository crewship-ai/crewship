"use client"

import React from "react"
import {
  ChevronRight,
  ChevronDown,
  FolderOpen,
  FolderClosed,
  FileCode,
  FileJson,
  FileText,
  Terminal,
  Box,
  File as FileIcon,
  Loader2,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { isPreviewable } from "@/lib/file-format"

export interface TreeNode {
  path: string; name: string; size: number; is_dir: boolean
  children: TreeNode[]; childrenLoaded?: boolean
}

export interface FileEntry {
  path: string
  name: string
  size: number
  is_dir: boolean
  mod_time: string
}

export function buildTopLevelTree(files: FileEntry[]): TreeNode[] {
  const nodes = files.map((f) => ({ ...f, children: [] as TreeNode[], childrenLoaded: !f.is_dir }))
  nodes.sort((a, b) => { if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1; return a.name.localeCompare(b.name) })
  return nodes
}

export function insertTreeChildren(tree: TreeNode[], parentPath: string, children: FileEntry[]): TreeNode[] {
  return tree.map((n) => {
    if (n.path === parentPath) {
      const c = children.map((f) => ({ ...f, children: [] as TreeNode[], childrenLoaded: !f.is_dir }))
      c.sort((a, b) => { if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1; return a.name.localeCompare(b.name) })
      return { ...n, children: c, childrenLoaded: true }
    }
    if (n.is_dir && n.children.length > 0) return { ...n, children: insertTreeChildren(n.children, parentPath, children) }
    return n
  })
}

export function findTreeNode(node: TreeNode, path: string): TreeNode | undefined {
  if (node.path === path) return node
  for (const c of node.children) { const found = findTreeNode(c, path); if (found) return found }
  return undefined
}

export function getChatFileIcon(name: string, isDir: boolean, isOpen?: boolean) {
  if (isDir) return isOpen ? <FolderOpen className="h-3.5 w-3.5 text-amber-500" /> : <FolderClosed className="h-3.5 w-3.5 text-amber-500" />
  const ext = name.split(".").pop()?.toLowerCase() ?? ""
  switch (ext) {
    case "py": return <FileCode className="h-3.5 w-3.5 text-yellow-500" />
    case "js": case "jsx": case "ts": case "tsx": return <FileCode className="h-3.5 w-3.5 text-blue-500" />
    case "json": return <FileJson className="h-3.5 w-3.5 text-yellow-600" />
    case "yaml": case "yml": return <FileJson className="h-3.5 w-3.5 text-red-400" />
    case "md": return <FileText className="h-3.5 w-3.5 text-blue-300" />
    case "sh": case "bash": return <Terminal className="h-3.5 w-3.5 text-green-500" />
    case "zip": case "tar": case "gz": return <Box className="h-3.5 w-3.5 text-purple-400" />
    default: return <FileIcon className="h-3.5 w-3.5 text-gray-400" />
  }
}

export function getEditorLanguage(name: string): string {
  const ext = name.split(".").pop()?.toLowerCase() ?? ""
  const map: Record<string, string> = {
    ts: "typescript", tsx: "tsx", js: "javascript", jsx: "jsx",
    py: "python", go: "go", rs: "rust", sh: "bash", bash: "bash",
    json: "json", yaml: "yaml", yml: "yaml", xml: "xml", svg: "xml",
    html: "html", css: "css", scss: "css", less: "css",
    md: "markdown", txt: "text", sql: "sql", toml: "toml",
  }
  return map[ext] ?? "text"
}

function formatFileSize(bytes: number): string {
  if (bytes === 0) return "0 B"
  const units = ["B", "KB", "MB", "GB"]
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  const value = bytes / Math.pow(1024, i)
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[i]}`
}

interface ChatTreeRowProps {
  node: TreeNode
  depth: number
  expanded: Set<string>
  loadingDirs: Set<string>
  selectedFile: string | null
  onToggle: (p: string) => void
  onFileClick: (node: TreeNode) => void
}

export const ChatTreeRow = React.memo(function ChatTreeRow({ node, depth, expanded, loadingDirs, selectedFile, onToggle, onFileClick }: ChatTreeRowProps) {
  const isOpen = expanded.has(node.path)
  const isLoading = loadingDirs.has(node.path)
  const isSelected = !node.is_dir && selectedFile === node.path
  const canPreview = !node.is_dir && isPreviewable(node.name)
  return (
    <>
      <button
        className={cn(
          "w-full flex items-center gap-1.5 py-1 pr-3 text-label transition-colors",
          isSelected
            ? "bg-blue-50 text-blue-700 dark:bg-blue-950/30 dark:text-blue-300"
            : canPreview
              ? "text-muted-foreground hover:text-foreground hover:bg-accent/50 cursor-pointer"
              : "text-muted-foreground hover:text-foreground hover:bg-accent/50",
        )}
        style={{ paddingLeft: `${depth * 14 + 8}px` }}
        onClick={() => {
          if (node.is_dir) onToggle(node.path)
          else if (canPreview) onFileClick(node)
        }}
      >
        {node.is_dir ? (
          isLoading ? <Loader2 className="h-3 w-3 shrink-0 animate-spin" /> :
          isOpen ? <ChevronDown className="h-3 w-3 shrink-0" /> : <ChevronRight className="h-3 w-3 shrink-0" />
        ) : <span className="w-3" />}
        {getChatFileIcon(node.name, node.is_dir, isOpen)}
        <span className="truncate">{node.name}</span>
        {!node.is_dir && <span className="ml-auto text-micro text-muted-foreground-soft shrink-0">{formatFileSize(node.size)}</span>}
      </button>
      {node.is_dir && isOpen && node.children.map((child) => (
        <ChatTreeRow key={child.path} node={child} depth={depth + 1} expanded={expanded} loadingDirs={loadingDirs} selectedFile={selectedFile} onToggle={onToggle} onFileClick={onFileClick} />
      ))}
    </>
  )
})
