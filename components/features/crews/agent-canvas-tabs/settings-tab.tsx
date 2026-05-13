"use client"

import { MoreHorizontal } from "lucide-react"
import { SystemPromptEditor } from "@/components/features/crews/system-prompt-editor"

import { AdvancedSection } from "./advanced-section"
import { RuntimeSection } from "./runtime-section"
import type { AgentRecord } from "./types"

export interface SettingsTabProps {
  agent: AgentRecord
  patch: (body: Record<string, unknown>) => Promise<void>
  safePatch: (body: Record<string, unknown>) => void
  showAdvanced: boolean
  setShowAdvanced: (next: boolean | ((prev: boolean) => boolean)) => void
  customModelOpen: boolean
  setCustomModelOpen: (open: boolean) => void
  customModelDraft: string
  setCustomModelDraft: (draft: string) => void
}

export function SettingsTab({
  agent,
  patch,
  safePatch,
  showAdvanced,
  setShowAdvanced,
  customModelOpen,
  setCustomModelOpen,
  customModelDraft,
  setCustomModelDraft,
}: SettingsTabProps) {
  return (
    <div className="space-y-7">
      {/* System Prompt — top, biggest single setting that matters */}
      <SystemPromptEditor
        value={agent.system_prompt}
        onSave={(v) => patch({ system_prompt: v })}
        updatedHint={`updated ${new Date(agent.updated_at).toLocaleDateString()}`}
      />

      {/* Runtime — provider chips + rich model dropdown */}
      <section className="space-y-3">
        <h2 className="text-lg font-semibold">Runtime</h2>

        <RuntimeSection
          agent={agent}
          safePatch={safePatch}
          customModelOpen={customModelOpen}
          setCustomModelOpen={setCustomModelOpen}
          customModelDraft={customModelDraft}
          setCustomModelDraft={setCustomModelDraft}
        />

        {/* Advanced — collapsible */}
        <AdvancedSection
          agent={agent}
          showAdvanced={showAdvanced}
          setShowAdvanced={setShowAdvanced}
          patch={patch}
        />
      </section>

      <p className="text-xs text-muted-foreground">
        Schedule moved to orchestration · Delete agent moved to the {" "}
        <span className="inline-flex items-center gap-0.5">
          <MoreHorizontal className="h-3 w-3" /> menu
        </span>{" "} next to the Chat button (owners only).
      </p>
    </div>
  )
}
