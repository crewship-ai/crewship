"use client"

import { useMemo, useState } from "react"
import { ChevronDown, ChevronRight, FileText, Folder, FolderOpen } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { formatRelativeTime } from "@/lib/time"
import type { JournalEntry } from "@/lib/types/journal"

interface FilesystemTreeProps {
  entries: JournalEntry[]
}

interface FileWrite {
  id: string
  path: string
  size: number | null
  hashShort: string | null
  agent: string
  ts: string
}

interface TreeNode {
  name: string
  fullPath: string
  children: Map<string, TreeNode>
  file?: FileWrite
}

/** Format a byte count as a compact human string (KB/MB/GB). */
function formatBytes(n: number | null): string {
  if (n === null || n === undefined) return "—"
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`
}

/**
 * Collapsible tree of the last 10 min of `file.written` entries. Files are
 * grouped by their parent directories; expanding a directory reveals the
 * individual writes with size, short hash, and author. No content preview —
 * contents aren't in the journal payload.
 */
export function FilesystemTree({ entries }: FilesystemTreeProps) {
  const files = useMemo<FileWrite[]>(() => {
    const cutoff = Date.now() - 10 * 60 * 1000
    const seenPaths = new Map<string, FileWrite>()
    for (const e of entries) {
      if (e.entry_type !== "file.written") continue
      if (new Date(e.ts).getTime() < cutoff) continue
      const path = typeof e.payload?.path === "string" ? e.payload.path : ""
      if (!path) continue
      // Keep the latest write per path (newest first in `entries`).
      if (seenPaths.has(path)) continue
      const size = typeof e.payload?.size === "number" ? (e.payload.size as number) : null
      const hash = typeof e.payload?.hash === "string" ? (e.payload.hash as string) : null
      seenPaths.set(path, {
        id: e.id,
        path,
        size,
        hashShort: hash ? hash.slice(0, 8) : null,
        agent: e.actor_id ?? "",
        ts: e.ts,
      })
    }
    return Array.from(seenPaths.values())
  }, [entries])

  const tree = useMemo(() => buildTree(files), [files])

  if (files.length === 0) {
    return (
      <div className="p-3 text-[11px] text-muted-foreground/60 italic">
        No file writes in the last 10 minutes.
      </div>
    )
  }

  return (
    <ul className="text-[11px]">
      {Array.from(tree.children.values()).map((node) => (
        <TreeEntry key={node.fullPath} node={node} depth={0} defaultOpen />
      ))}
    </ul>
  )
}

function buildTree(files: FileWrite[]): TreeNode {
  const root: TreeNode = { name: "", fullPath: "", children: new Map() }
  for (const f of files) {
    const parts = f.path.split("/").filter(Boolean)
    let cur = root
    for (let i = 0; i < parts.length; i++) {
      const part = parts[i]
      const next = cur.children.get(part)
      if (next) {
        cur = next
      } else {
        const created: TreeNode = {
          name: part,
          fullPath: "/" + parts.slice(0, i + 1).join("/"),
          children: new Map(),
        }
        cur.children.set(part, created)
        cur = created
      }
    }
    cur.file = f
  }
  return root
}

function TreeEntry({ node, depth, defaultOpen = false }: { node: TreeNode; depth: number; defaultOpen?: boolean }) {
  const [open, setOpen] = useState(defaultOpen)
  const hasChildren = node.children.size > 0
  const isFile = Boolean(node.file) && !hasChildren

  if (isFile && node.file) {
    return (
      <li className="flex items-center gap-1.5 py-0.5" style={{ paddingLeft: depth * 12 + 4 }}>
        <FileText className="h-3 w-3 text-muted-foreground shrink-0" />
        <span className="truncate text-foreground/85 font-mono" title={node.file.path}>
          {node.name}
        </span>
        {node.file.hashShort && (
          <Badge variant="outline" className="text-[10px] font-mono border-border/60 shrink-0">
            {node.file.hashShort}
          </Badge>
        )}
        <span className="text-[10px] text-muted-foreground shrink-0 tabular-nums">{formatBytes(node.file.size)}</span>
        {node.file.agent && (
          <span className="text-[10px] text-muted-foreground shrink-0">
            @{node.file.agent.slice(0, 6)}
          </span>
        )}
        <span className="ml-auto text-[10px] text-muted-foreground font-mono tabular-nums shrink-0">
          {formatRelativeTime(node.file.ts)}
        </span>
      </li>
    )
  }

  return (
    <li>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="flex items-center gap-1.5 py-0.5 w-full text-left hover:bg-muted/30 transition-colors"
        style={{ paddingLeft: depth * 12 + 4 }}
      >
        {hasChildren ? (
          open ? <ChevronDown className="h-3 w-3 text-muted-foreground" /> : <ChevronRight className="h-3 w-3 text-muted-foreground" />
        ) : (
          <span className="w-3" />
        )}
        {open ? <FolderOpen className="h-3 w-3 text-muted-foreground" /> : <Folder className="h-3 w-3 text-muted-foreground" />}
        <span className="truncate text-foreground/80 font-mono">{node.name || "/"}</span>
      </button>
      {open && hasChildren && (
        <ul>
          {Array.from(node.children.values()).map((child) => (
            <TreeEntry key={child.fullPath} node={child} depth={depth + 1} />
          ))}
        </ul>
      )}
    </li>
  )
}
