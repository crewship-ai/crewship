"use client"

import { useCallback, useMemo } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { FolderOpen, TerminalSquare } from "lucide-react"
import { ToolbarStrip, type ToolbarTab } from "@/components/layout/toolbar-strip"
import { FilesPageClient } from "@/components/features/agents/workspace/files-pane"
import { TerminalPageClient } from "@/components/features/agents/workspace/terminal-pane"

type Pane = "files" | "terminal"

const PANE_TABS: ToolbarTab<Pane>[] = [
  { id: "files", label: "Files", icon: FolderOpen },
  { id: "terminal", label: "Terminal", icon: TerminalSquare },
]

function parsePane(value: string | null): Pane {
  return value === "terminal" ? "terminal" : "files"
}

export function WorkspacePageClient() {
  const searchParams = useSearchParams()
  const router = useRouter()
  const activePane = useMemo(() => parsePane(searchParams.get("pane")), [searchParams])

  const handleChange = useCallback(
    (pane: Pane) => {
      const params = new URLSearchParams(searchParams.toString())
      params.set("pane", pane)
      router.replace(`?${params.toString()}`, { scroll: false })
    },
    [router, searchParams],
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
