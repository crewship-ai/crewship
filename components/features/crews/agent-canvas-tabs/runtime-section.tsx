"use client"

import { CLI_ADAPTERS, getProviderLabel } from "@/lib/cli-adapters"
import { ModelLibraryPicker, getCompatibleAdapters } from "../model-library-picker"
import { cn } from "@/lib/utils"

import type { AgentRecord } from "./types"

export interface RuntimeSectionProps {
  agent: AgentRecord
  safePatch: (body: Record<string, unknown>) => void
  customModelOpen: boolean
  setCustomModelOpen: (open: boolean) => void
  customModelDraft: string
  setCustomModelDraft: (draft: string) => void
}

export function RuntimeSection({
  agent,
  safePatch,
  customModelOpen,
  setCustomModelOpen,
  customModelDraft,
  setCustomModelDraft,
}: RuntimeSectionProps) {
  const compat = agent.llm_provider
    ? getCompatibleAdapters(agent.llm_provider)
    : []

  return (
    <>
      <div className="rounded-xl border border-white/8 bg-card p-4 space-y-4">
        {/* Model — primary, model-first picker. Adapter is auto-resolved
            and shown only when the choice is meaningful (Anthropic ↔
            Claude Code / OpenCode). */}
        <div className="space-y-2">
          <div className="text-xs text-muted-foreground">Model</div>
          <ModelLibraryPicker
            cliAdapter={agent.cli_adapter}
            llmModel={agent.llm_model ?? ""}
            onPick={(next) => safePatch(next)}
            onCustom={() => setCustomModelOpen(true)}
          />
        </div>

        {/* Adapter — inline pill selector. Renders only when the
            current provider has more than one compatible CLI binary
            (Anthropic ↔ Claude Code / OpenCode). For OpenAI / Google
            the row is hidden so the UI stays focused on the model. */}
        {compat.length > 1 && (
          <div className="space-y-2">
            <div className="text-xs text-muted-foreground">CLI adapter</div>
            <div className="grid grid-cols-2 gap-2">
              {compat.map((key) => {
                const cfg = CLI_ADAPTERS[key]
                if (!cfg) return null
                const Icon = cfg.icon
                const isActive = agent.cli_adapter === key
                return (
                  <button
                    key={key}
                    type="button"
                    onClick={() => {
                      if (!isActive) safePatch({ cli_adapter: key })
                    }}
                    className={cn(
                      "flex items-center gap-2.5 rounded-lg border px-3 py-2 text-left transition-colors",
                      isActive
                        ? "border-blue-400 bg-blue-500/10 ring-1 ring-blue-500/30"
                        : "border-white/10 hover:bg-white/[0.03]",
                    )}
                  >
                    <Icon className={cn("h-4 w-4 shrink-0", isActive ? "text-blue-300" : "text-muted-foreground")} />
                    <div className="min-w-0 flex-1">
                      <div className="text-sm font-medium leading-tight">{cfg.label}</div>
                      <div className="text-[11px] text-muted-foreground truncate leading-tight mt-0.5">
                        {cfg.description}
                      </div>
                    </div>
                  </button>
                )
              })}
            </div>
            <p className="text-[11px] text-muted-foreground pl-1">
              Both adapters run {getProviderLabel(agent.llm_provider ?? "")} models — stick with the default unless you have a reason to switch.
            </p>
          </div>
        )}
      </div>

      {/* Custom model name — modal swap on the picker */}
      {customModelOpen && (
        <div className="rounded-xl border border-amber-500/40 bg-amber-500/5 p-3 space-y-2">
          <div className="text-xs text-amber-300">Custom model identifier</div>
          <div className="flex gap-2">
            <input
              autoFocus
              type="text"
              value={customModelDraft}
              onChange={(e) => setCustomModelDraft(e.target.value)}
              placeholder="e.g. claude-3-7-sonnet or my-fine-tuned-llama"
              className="flex-1 px-3 py-1.5 rounded-md border border-white/10 bg-zinc-900 text-sm font-mono outline-none focus:border-blue-400"
              onKeyDown={(e) => {
                if (e.key === "Enter" && customModelDraft.trim()) {
                  safePatch({ llm_model: customModelDraft.trim() })
                  setCustomModelOpen(false)
                  setCustomModelDraft("")
                } else if (e.key === "Escape") {
                  setCustomModelOpen(false)
                  setCustomModelDraft("")
                }
              }}
            />
            <button
              type="button"
              onClick={() => {
                if (customModelDraft.trim()) {
                  safePatch({ llm_model: customModelDraft.trim() })
                  setCustomModelOpen(false)
                  setCustomModelDraft("")
                }
              }}
              disabled={!customModelDraft.trim()}
              className="px-3 py-1.5 rounded-md bg-blue-500 hover:bg-blue-400 disabled:opacity-40 text-sm text-white"
            >
              Save
            </button>
            <button
              type="button"
              onClick={() => {
                setCustomModelOpen(false)
                setCustomModelDraft("")
              }}
              className="px-3 py-1.5 rounded-md border border-white/10 hover:bg-white/5 text-sm text-muted-foreground"
            >
              Cancel
            </button>
          </div>
          <p className="text-[11px] text-muted-foreground">
            ⏎ to save · Esc to cancel · keeps current adapter and provider.
          </p>
        </div>
      )}
    </>
  )
}
