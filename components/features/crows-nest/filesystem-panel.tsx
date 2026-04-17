"use client"

import { FolderOpen } from "lucide-react"
import { FilesystemTree } from "./filesystem-tree"
import type { JournalEntry } from "@/lib/types/journal"

interface FilesystemPanelProps {
  entries: JournalEntry[]
}

/** Card wrapper around <FilesystemTree> — matches the other Crow's Nest panel chrome. */
export function FilesystemPanel({ entries }: FilesystemPanelProps) {
  return (
    <div className="flex flex-col h-full bg-card border border-border/50 rounded-lg overflow-hidden">
      <div className="flex items-center justify-between px-3 py-1.5 bg-muted/40 border-b border-border/50 shrink-0">
        <div className="flex items-center gap-2">
          <FolderOpen className="h-3.5 w-3.5 text-muted-foreground" />
          <span className="text-[11px] text-muted-foreground font-medium">Filesystem (10m)</span>
        </div>
      </div>
      <div className="flex-1 min-h-0 overflow-auto">
        <FilesystemTree entries={entries} />
      </div>
    </div>
  )
}
