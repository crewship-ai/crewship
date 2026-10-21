"use client"

import { Box, FolderGit2, Library, Sparkles } from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { cn } from "@/lib/utils"
import { skillIcon, skillSource, type SkillAgentRef, type SkillRow, type SkillSourceKind } from "./skills-model"

// The small marks the Skills area draws everywhere: a skill's own tile, the
// mark of the source it came from, and the faces of the agents that hold it.

// Pixel sizes from v67. The spacing unit here is 0.23rem (globals.css), so
// size-6.5 is the 24 px box, not size-6.
const BOX: Record<number, string> = {
  16: "size-4.5 rounded-sm [&>svg]:size-2.5",
  20: "size-5.5 rounded-sm [&>svg]:size-3",
  24: "size-6.5 rounded-md [&>svg]:size-3.5",
  28: "size-7.5 rounded-md [&>svg]:size-4",
  52: "size-14 rounded-lg [&>svg]:size-6.5",
}

/** The skill's own icon (its domain's when it has none) in a Harbor icon tile. */
export function SkillTile({
  category,
  icon,
  size = "md",
  className,
}: {
  category: string
  icon?: string | null
  size?: "sm" | "md" | "lg"
  className?: string
}) {
  const Icon = skillIcon({ icon, category })
  const box = size === "lg" ? "h-11 w-11 rounded-lg" : size === "sm" ? "h-6 w-6 rounded-md" : "h-8 w-8 rounded-lg"
  const glyph = size === "lg" ? "h-5 w-5" : size === "sm" ? "h-3 w-3" : "h-4 w-4"
  return (
    <span aria-hidden className={cn("icon-tile inline-flex shrink-0 items-center justify-center", box, className)}>
      <Icon className={glyph} />
    </span>
  )
}

const SOURCE_ICON: Record<SkillSourceKind, typeof Box> = {
  builtin: Library,
  workspace: Sparkles,
  repo: FolderGit2,
  vendor: Box,
}

/**
 * Where a skill comes from, as a mark: built-in and made-here wear the
 * Skills tile, a repository or vendor a neutral tile with its glyph. There are
 * no third-party logos in the product, so a repository is a folder-with-git
 * glyph rather than its owner's logo.
 */
export function SourceMark({
  kind,
  size = 24,
  className,
}: {
  kind: SkillSourceKind
  size?: 16 | 20 | 24 | 28 | 52
  className?: string
}) {
  const Icon = SOURCE_ICON[kind]
  const ours = kind === "builtin" || kind === "workspace"
  return (
    <span
      aria-hidden
      data-source-kind={kind}
      className={cn(
        "inline-flex shrink-0 items-center justify-center",
        ours ? "icon-tile" : "border border-border bg-muted text-foreground/80",
        BOX[size],
        className,
      )}
    >
      <Icon />
    </span>
  )
}

export function SkillSourceMark({ skill, size = 24, className }: { skill: Pick<SkillRow, "source" | "vendor" | "homepage">; size?: 16 | 20 | 24 | 28 | 52; className?: string }) {
  return <SourceMark kind={skillSource(skill).kind} size={size} className={className} />
}

/** Up to `max` overlapping agent faces and "+N" for the rest. */
export function HolderStack({ agents, max = 4, size = 20 }: { agents: SkillAgentRef[]; max?: number; size?: 16 | 18 | 20 }) {
  const box = size === 16 ? "h-4 w-4" : size === 20 ? "h-5 w-5" : "h-[18px] w-[18px]"
  return (
    <span className="inline-flex shrink-0 items-center gap-1">
      <span className="flex -space-x-1.5">
        {agents.slice(0, max).map((a) => (
          <AgentAvatar
            key={a.agent_id}
            seed={a.avatar_seed ?? a.agent_slug}
            style={a.avatar_style ?? a.crew_avatar_style}
            avatarUrl={a.avatar_url}
            alt={a.agent_name}
            title={`${a.agent_name}${a.crew_name ? ` · ${a.crew_name}` : ""}`}
            width={size}
            height={size}
            className={cn("rounded-full bg-foreground/[0.04] ring-2 ring-card", box)}
          />
        ))}
      </span>
      {agents.length > max && <span className="text-label tabular-nums text-muted-foreground">+{agents.length - max}</span>}
    </span>
  )
}
