"use client"

import { useCallback, useMemo } from "react"
import { FolderOpen, TerminalSquare } from "lucide-react"
import { ToolbarStrip, type ToolbarTab } from "@/components/layout/toolbar-strip"
import { FilesPageClient } from "@/components/features/agents/workspace/files-pane"
import { TerminalPageClient } from "@/components/features/agents/workspace/terminal-pane"
import { useShallowSearchParam } from "@/hooks/use-shallow-search-param"

type Pane = "files" | "terminal"

const PANE_TABS: ToolbarTab<Pane>[] = [
  { id: "files", label: "Files", icon: FolderOpen },
  { id: "terminal", label: "Terminal", icon: TerminalSquare },
]

function parsePane(value: string | null): Pane {
  return value === "terminal" ? "terminal" : "files"
}

export function WorkspacePageClient() {
  const [paneRaw, setPaneRaw] = useShallowSearchParam("pane", "files")
  const activePane = useMemo(() => parsePane(paneRaw), [paneRaw])

  const handleChange = useCallback(
    (pane: Pane) => {
      setPaneRaw(pane)
    },
    [setPaneRaw],
  )

  return (
    <div className="flex flex-col h-full min-h-0">
      <ToolbarStrip
        tabs={PANE_TABS}
        activeTab={activePane}
        onTabChange={handleChange}
        ariaLabel="Workspace panes"
      />
      <div className="flex-1 min-h-0 overflow-hidden">
        {activePane === "files" ? <FilesPageClient /> : <TerminalPageClient />}
      </div>
    </div>
  )
}
