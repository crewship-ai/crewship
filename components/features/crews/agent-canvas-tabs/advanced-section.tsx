"use client"

import { motion } from "motion/react"
import { ChevronDown } from "lucide-react"
import { EditableField } from "@/components/shared/editable-field"
import { cn } from "@/lib/utils"

import { CanvasRow as Row } from "../canvas-base"
import type { AgentRecord } from "./types"
import { TOOL_PROFILE_OPTIONS } from "./types"

export interface AdvancedSectionProps {
  agent: AgentRecord
  showAdvanced: boolean
  setShowAdvanced: (next: boolean | ((prev: boolean) => boolean)) => void
  patch: (body: Record<string, unknown>) => Promise<void>
}

export function AdvancedSection({
  agent,
  showAdvanced,
  setShowAdvanced,
  patch,
}: AdvancedSectionProps) {
  return (
    <div className="rounded-xl border border-white/8 bg-card">
      <button
        type="button"
        onClick={() => setShowAdvanced((v) => !v)}
        className="w-full px-4 py-2.5 flex items-center gap-2 text-xs text-muted-foreground hover:bg-white/[0.03] hover:text-foreground transition-colors"
      >
        <ChevronDown
          className={cn("h-3 w-3 transition-transform duration-200", !showAdvanced && "-rotate-90")}
        />
        Advanced (LLM tuning, tools, memory, webhook, hooks)
      </button>
      {showAdvanced && (
        <motion.div
          initial={{ opacity: 0, height: 0 }}
          animate={{ opacity: 1, height: "auto" }}
          exit={{ opacity: 0, height: 0 }}
          transition={{ duration: 0.18, ease: "easeOut" }}
          className="divide-y divide-white/5 border-t border-white/5 overflow-hidden"
        >
          <Row label="Timeout (s)">
            <EditableField
              value={String(agent.timeout_seconds)}
              onSave={(v) => {
                const n = parseInt(v, 10)
                if (!Number.isInteger(n) || n < 1) return
                patch({ timeout_seconds: n })
              }}
            />
          </Row>
          <Row label="Tool profile">
            <EditableField
              value={agent.tool_profile}
              onSave={(v) => patch({ tool_profile: v })}
              options={[...TOOL_PROFILE_OPTIONS]}
              format={(v) => TOOL_PROFILE_OPTIONS.find((o) => o.value === v)?.label ?? v}
            />
          </Row>
          <Row label="Tools enabled" align="start">
            <div className="flex flex-wrap items-center gap-1">
              {(agent.cli_tools && agent.cli_tools.length > 0) ? (
                agent.cli_tools.slice(0, 6).map((t) => (
                  <span key={t} className="text-[10px] px-1.5 py-0.5 rounded bg-zinc-800 border border-white/10 text-foreground/80">
                    {t}
                  </span>
                ))
              ) : (
                <em className="text-sm text-muted-foreground italic">(default for tool profile)</em>
              )}
              {agent.cli_tools && agent.cli_tools.length > 6 && (
                <span className="text-[10px] text-muted-foreground">+ {agent.cli_tools.length - 6} more</span>
              )}
            </div>
          </Row>
          <Row label="Memory">
            <button
              type="button"
              onClick={() => patch({ memory_enabled: !agent.memory_enabled })}
              className={cn(
                "relative inline-flex items-center w-9 h-5 rounded-full transition-colors",
                agent.memory_enabled ? "bg-emerald-600/70" : "bg-zinc-700",
              )}
              aria-pressed={agent.memory_enabled}
            >
              <span
                className={cn(
                  "absolute w-4 h-4 rounded-full bg-white transition-transform",
                  agent.memory_enabled ? "translate-x-[18px]" : "translate-x-0.5",
                )}
              />
            </button>
            <span className="text-sm text-muted-foreground ml-2">
              {agent.memory_enabled ? "enabled" : "disabled"}
            </span>
          </Row>
          <Row label="Hooks" align="center">
            <span className="text-sm text-muted-foreground">
              Manage via CLI:{" "}
              <code className="text-foreground/80">crewship hooks list</code>
              {" / "}
              <code className="text-foreground/80">enable</code>
              {" / "}
              <code className="text-foreground/80">disable</code>
            </span>
          </Row>
          <Row label="Webhook" align="center">
            <span className="text-sm text-muted-foreground">
              Manage via CLI: <code className="text-foreground/80">crewship agent webhook {agent.slug}</code>
            </span>
          </Row>
        </motion.div>
      )}
    </div>
  )
}
