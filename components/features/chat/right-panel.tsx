"use client"

import React, { useEffect, useState } from "react"
import { LayoutGrid, ListTodo } from "lucide-react"
import { cn } from "@/lib/utils"
import { useDrawerStore } from "@/stores/drawer-store"
import { useChatAgent } from "./chat-agent-context"
import { AgentArtifactsTab } from "./right-panel-tabs/agent-artifacts-tab"
import { AgentWorkTab } from "./right-panel-tabs/agent-work-tab"
import type { FileEntry } from "./chat-tree-row"
import type { EditorScope } from "./hooks/use-file-editor"

const TABS = [
  { id: "artifacts", label: "Artifacts", icon: LayoutGrid },
  { id: "work", label: "Work", icon: ListTodo },
] as const

type PanelTab = typeof TABS[number]["id"]
const validTab = (tab?: string): PanelTab => tab === "work" ? "work" : "artifacts"

interface RightPanelProps {
  agentId: string
  workspaceId: string | null
  initialTab?: string
  hideTabs?: boolean
  style?: React.CSSProperties
  /** Legacy Files props remain accepted while transcript callers migrate. */
  files?: FileEntry[]
  filesLoading?: boolean
  filesError?: string | null
  onRetryFiles?: () => void
  previewFile?: { path: string } | null
  onPreviewHandled?: (request: { path: string }) => void
  onOpenFile?: (file: { path: string; name: string; scope: EditorScope }) => void
  selectedFile?: string | null
}

/** Chat exposes deliverables and work; agent configuration lives on the agent card. */
export const RightPanel = React.memo(function RightPanel({ agentId, workspaceId, initialTab, hideTabs, style }: RightPanelProps) {
  const chatAgent = useChatAgent()
  const [activeTab, setActiveTab] = useState<PanelTab>(() => validTab(initialTab))
  useEffect(() => { setActiveTab(validTab(initialTab)) }, [initialTab])
  const agentName = chatAgent?.name || "this agent"
  const subtitle = activeTab === "artifacts" ? `Documents and outputs for ${agentName}` : `Issues and routines connected to ${agentName}`

  return <div className="flex min-h-0 flex-col overflow-hidden bg-accent/30" style={style}>
    <header className="shrink-0 border-b px-3 py-3">
      <h2 className="text-sm font-semibold">{activeTab === "artifacts" ? "Artifacts" : "Work"}</h2>
      <p className="mt-0.5 truncate text-xs text-muted-foreground">{subtitle}</p>
    </header>
    {!hideTabs && <div className="flex h-[41px] shrink-0 items-end border-b">
      {TABS.map((tab) => <button key={tab.id} type="button" role="tab" aria-selected={activeTab === tab.id}
        onClick={() => { setActiveTab(tab.id); useDrawerStore.getState().setActiveTab(tab.id) }}
        className={cn("mb-[-1px] flex flex-1 items-center justify-center gap-1.5 border-b-2 pb-2.5 text-micro font-medium transition-colors", activeTab === tab.id ? "border-primary text-foreground" : "border-transparent text-muted-foreground hover:text-foreground")}>
        <tab.icon className="size-3.5" />{tab.label}
      </button>)}
    </div>}
    <div className="min-h-0 flex-1 overflow-y-auto">
      {activeTab === "artifacts" ? <AgentArtifactsTab agentId={agentId} workspaceId={workspaceId} crewId={chatAgent?.crewId} agentSlug={chatAgent?.slug} />
        : <AgentWorkTab agentId={agentId} workspaceId={workspaceId} />}
    </div>
  </div>
})
