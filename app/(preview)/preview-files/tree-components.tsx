"use client"

import { useState } from "react"
import { cn } from "@/lib/utils"
import {
  Copy, Check, Download, ChevronRight, ChevronDown,
  FolderOpen, FolderClosed, FileText, FileCode, FileJson,
  GitBranch, Terminal, Box, Eye, Settings,
  File as FileIcon,
} from "lucide-react"
import {
  TreeNode, MOCK_TREE, MOCK_PREVIEW_CODE,
  formatSize, findNode,
} from "./mocks"

export function getFileTypeIcon(name: string, isDir: boolean, isOpen?: boolean) {
  if (isDir) {
    return isOpen
      ? <FolderOpen className="h-4 w-4 text-amber-500" />
      : <FolderClosed className="h-4 w-4 text-amber-500" />
  }
  const ext = name.split(".").pop()?.toLowerCase() ?? ""
  const n = name.toLowerCase()
  if (n === "dockerfile" || n === "docker-compose.yml") return <Box className="h-4 w-4 text-blue-400" />
  if (n === ".gitignore" || n === ".gitattributes") return <GitBranch className="h-4 w-4 text-orange-500" />
  if (n === "makefile" || n === "cmakelists.txt") return <Terminal className="h-4 w-4 text-green-600" />
  switch (ext) {
    case "py": return <FileCode className="h-4 w-4 text-yellow-500" />
    case "js": case "jsx": return <FileCode className="h-4 w-4 text-yellow-400" />
    case "ts": case "tsx": return <FileCode className="h-4 w-4 text-blue-500" />
    case "go": return <FileCode className="h-4 w-4 text-cyan-500" />
    case "rs": return <FileCode className="h-4 w-4 text-orange-600" />
    case "json": return <FileJson className="h-4 w-4 text-yellow-600" />
    case "yaml": case "yml": return <FileJson className="h-4 w-4 text-red-400" />
    case "md": case "mdx": return <FileText className="h-4 w-4 text-blue-300" />
    case "txt": return <FileText className="h-4 w-4 text-gray-500" />
    case "sh": case "bash": case "zsh": return <Terminal className="h-4 w-4 text-green-500" />
    case "env": return <Settings className="h-4 w-4 text-gray-600" />
    case "html": return <FileCode className="h-4 w-4 text-orange-500" />
    case "css": case "scss": return <FileCode className="h-4 w-4 text-blue-400" />
    case "sql": return <FileCode className="h-4 w-4 text-blue-600" />
    case "toml": return <FileJson className="h-4 w-4 text-gray-500" />
    default: return <FileIcon className="h-4 w-4 text-gray-400" />
  }
}


export function FileTreeNode({ node, depth, selectedPath, onSelect, expandedPaths, onToggle }: {
  node: TreeNode; depth: number; selectedPath: string | null
  onSelect: (path: string) => void; expandedPaths: Set<string>; onToggle: (path: string) => void
}) {
  const isOpen = expandedPaths.has(node.path)
  return (
    <>
      <button
        className={cn(
          "w-full flex items-center gap-1.5 py-1 pr-2 text-xs transition-colors hover:bg-accent/50",
          selectedPath === node.path && "bg-accent text-foreground",
          !node.isDir && selectedPath !== node.path && "text-muted-foreground",
        )}
        style={{ paddingLeft: `${depth * 16 + 8}px` }}
        onClick={() => node.isDir ? onToggle(node.path) : onSelect(node.path)}
      >
        {node.isDir && (isOpen ? <ChevronDown className="h-3 w-3 shrink-0 text-muted-foreground" /> : <ChevronRight className="h-3 w-3 shrink-0 text-muted-foreground" />)}
        {!node.isDir && <span className="w-3" />}
        {getFileTypeIcon(node.name, node.isDir, isOpen)}
        <span className="truncate">{node.name}</span>
        {!node.isDir && <span className="ml-auto text-[10px] text-muted-foreground shrink-0">{formatSize(node.size)}</span>}
      </button>
      {node.isDir && isOpen && node.children.map((child) => (
        <FileTreeNode key={child.path} node={child} depth={depth + 1} selectedPath={selectedPath} onSelect={onSelect} expandedPaths={expandedPaths} onToggle={onToggle} />
      ))}
    </>
  )
}


export function FilePreviewPanel({ selectedPath, className }: { selectedPath: string | null; className?: string }) {
  const [copied, setCopied] = useState(false)
  const file = selectedPath ? findNode(MOCK_TREE, selectedPath) : null

  if (!file || file.isDir) {
    return (
      <div className={cn("flex flex-col items-center justify-center text-muted-foreground", className)}>
        <Eye className="h-8 w-8 mb-2 opacity-30" />
        <p className="text-sm">Select a file to preview</p>
      </div>
    )
  }

  return (
    <div className={cn("flex flex-col", className)}>
      <div className="flex items-center gap-2 px-4 h-[41px] border-b shrink-0">
        <div className="flex items-center gap-1.5 min-w-0 flex-1">
          {getFileTypeIcon(file.name, false)}
          <span className="text-xs font-medium truncate">{file.name}</span>
          <span className="text-[10px] text-muted-foreground shrink-0">{formatSize(file.size)}</span>
        </div>
        <div className="flex items-center gap-1 shrink-0">
          <button className="h-6 w-6 flex items-center justify-center rounded hover:bg-accent" title="Copy path"
            onClick={() => { setCopied(true); setTimeout(() => setCopied(false), 2000) }}>
            {copied ? <Check className="h-3 w-3 text-emerald-500" /> : <Copy className="h-3 w-3 text-muted-foreground" />}
          </button>
          <button className="h-6 w-6 flex items-center justify-center rounded hover:bg-accent" title="Download">
            <Download className="h-3 w-3 text-muted-foreground" />
          </button>
        </div>
      </div>
      <div className="flex-1 overflow-y-auto bg-[#1e1e1e] text-[#d4d4d4] font-mono text-xs leading-5">
        {MOCK_PREVIEW_CODE.split("\n").map((line, i) => (
          <div key={i} className="flex hover:bg-white/5">
            <span className="w-10 shrink-0 text-right pr-3 text-[#858585] select-none">{i + 1}</span>
            <pre className="flex-1 pr-4 whitespace-pre-wrap break-all">
              {colorize(line)}
            </pre>
          </div>
        ))}
      </div>
    </div>
  )
}

export function colorize(line: string) {
  if (line.trimStart().startsWith("#") || line.trimStart().startsWith("//")) return <span className="text-[#6A9955]">{line}</span>
  if (line.trimStart().startsWith('"""') || line.trimStart().startsWith("'''")) return <span className="text-[#6A9955]">{line}</span>
  if (line.match(/^\s*(import |from )/)) return <span className="text-[#C586C0]">{line}</span>
  if (line.match(/^\s*(class |def |async def |function )/)) return <span className="text-[#DCDCAA]">{line}</span>
  if (line.match(/^\s*(return |if |else|elif |for |while |try|except|finally)/)) return <span className="text-[#C586C0]">{line}</span>
  return <span>{line}</span>
}


