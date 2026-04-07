"use client"

import { memo } from "react"
import { type NodeProps } from "@xyflow/react"
import { Brain, Crown, Cpu } from "lucide-react"
import { cn } from "@/lib/utils"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"

export interface AgentCardData {
  name: string
  slug: string
  avatarSeed: string | null
  avatarStyle: string | null
  role: string
  isLead: boolean
  status: "active" | "idle" | "blocked" | "error"
  model: string
  tokenCount: number
  cost: number
  skills: string[]
  memoryEnabled: boolean
  currentTask: string | null
  onAgentClick?: (agentSlug: string) => void
  [key: string]: unknown
}

const statusConfig = {
  active: {
    dot: "bg-green-500",
    pulse: true,
    border: "border-green-500/20",
    bg: "bg-[#0a1f0f]/60",
  },
  idle: {
    dot: "bg-slate-400",
    pulse: false,
    border: "border-border",
    bg: "bg-[#0f1115]/60",
  },
  blocked: {
    dot: "bg-amber-500",
    pulse: false,
    border: "border-amber-500/20",
    bg: "bg-[#1f1a0a]/60",
  },
  error: {
    dot: "bg-red-500",
    pulse: false,
    border: "border-red-500/20",
    bg: "bg-[#1f0a0a]/60",
  },
} as const

function formatTokens(count: number): string {
  if (count >= 1_000_000) return `${(count / 1_000_000).toFixed(1)}M`
  if (count >= 1_000) return `${(count / 1_000).toFixed(1)}k`
  return `${count}`
}

function AgentCardNodeInner({ data }: NodeProps) {
  const d = data as unknown as AgentCardData
  const cfg = statusConfig[d.status] ?? statusConfig.idle
  const maxVisibleSkills = 3
  const visibleSkills = d.skills.slice(0, maxVisibleSkills)
  const extraSkillCount = d.skills.length - maxVisibleSkills

  return (
    <div
      className={cn(
        "rounded-lg border backdrop-blur-sm cursor-pointer transition-all duration-200",
        "hover:brightness-110",
        cfg.border,
        cfg.bg,
      )}
      style={{ width: 200, minHeight: 100 }}
      onClick={() => d.onAgentClick?.(d.slug)}
    >
      <div className="px-2.5 py-2">
        {/* Top row: status dot + name + badges */}
        <div className="flex items-center gap-2">
          {/* Status dot */}
          <div className="relative shrink-0">
            <div className={cn("w-2 h-2 rounded-full", cfg.dot)} />
            {cfg.pulse && (
              <div className={cn("absolute inset-0 w-2 h-2 rounded-full animate-ping", cfg.dot, "opacity-40")} />
            )}
          </div>

          {/* Avatar */}
          <img src={getAgentAvatarUrl(d.avatarSeed || d.slug, d.avatarStyle)} alt="" className="w-5 h-5 rounded-full shrink-0" />

          {/* Name */}
          <span className="text-[12px] font-semibold text-foreground truncate flex-1">
            {d.name}
          </span>

          {/* Lead badge */}
          {d.isLead && (
            <span className="flex items-center gap-0.5 px-1.5 py-0.5 rounded-full bg-violet-500/20 border border-violet-500/30 text-[8px] font-semibold text-violet-400 uppercase tracking-wider shrink-0">
              <Crown className="h-2.5 w-2.5" />
              Lead
            </span>
          )}

          {/* Memory icon */}
          {d.memoryEnabled && (
            <Brain className="h-3 w-3 text-cyan-400/60 shrink-0" />
          )}
        </div>

        {/* Role subtitle */}
        <div className="text-[10px] text-muted-foreground mt-0.5 truncate pl-11">
          {d.role}
        </div>

        {/* Model + metrics row */}
        <div className="flex items-center gap-2 mt-1.5 pl-11">
          <div className="flex items-center gap-1 text-[9px] text-muted-foreground/70 font-mono">
            <Cpu className="h-2.5 w-2.5" />
            <span className="truncate max-w-[80px]">{d.model}</span>
          </div>
          <span className="text-[9px] text-muted-foreground/50">|</span>
          <span className="text-[9px] text-muted-foreground/70 font-mono tabular-nums">
            {formatTokens(d.tokenCount)} tok
          </span>
          <span className="text-[9px] text-muted-foreground/70 font-mono tabular-nums">
            ${d.cost.toFixed(2)}
          </span>
        </div>

        {/* Skill tags */}
        {d.skills.length > 0 && (
          <div className="flex flex-wrap items-center gap-1 mt-1.5 pl-11">
            {visibleSkills.map((skill) => (
              <span
                key={skill}
                className="px-1.5 py-0.5 rounded text-[8px] font-medium text-muted-foreground bg-accent/50 border border-border truncate max-w-[70px]"
              >
                {skill}
              </span>
            ))}
            {extraSkillCount > 0 && (
              <span className="text-[8px] text-muted-foreground/50 font-mono">
                +{extraSkillCount} more
              </span>
            )}
          </div>
        )}

        {/* Current task */}
        {d.currentTask && (
          <div className="mt-1.5 pt-1.5 border-t border-border pl-11">
            <p className="text-[9px] text-blue-300/50 italic truncate">
              {d.currentTask}
            </p>
          </div>
        )}
      </div>
    </div>
  )
}

export const AgentCardNode = memo(AgentCardNodeInner)
