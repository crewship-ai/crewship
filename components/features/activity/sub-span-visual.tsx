"use client"

import {
  Eye,
  FileText,
  Globe,
  Sparkles,
  Terminal,
  Wrench,
  type LucideIcon,
} from "lucide-react"
import type { SubSpanKind, SubSpanStatus } from "@/lib/trace/types"
import { brandIconForType, BrandGlyph } from "@/components/features/routines/brand-icons"

// sub-span-visual — maps a SubSpan kind to its generic lucide glyph +
// tint, and renders the real brand logo (Ansible/Terraform/Docker/…)
// when the span's `attributes.tool` resolves to a known Simple Icon.
// Reuses the same BrandGlyph + brandIconForType the routine flow uses
// so a Postgres elephant / Ansible logo reads identically everywhere.

interface KindVisual {
  Icon: LucideIcon
  // Tailwind text color for the generic glyph + the icon tile tint.
  tint: string
  // Tailwind classes for the small rounded icon tile background/border.
  tile: string
  label: string
}

const KIND_VISUAL: Record<SubSpanKind, KindVisual> = {
  bash: { Icon: Terminal, tint: "text-emerald-300", tile: "bg-emerald-500/10 border-emerald-500/30", label: "bash" },
  write: { Icon: FileText, tint: "text-amber-300", tile: "bg-amber-500/10 border-amber-500/30", label: "write" },
  edit: { Icon: FileText, tint: "text-amber-300", tile: "bg-amber-500/10 border-amber-500/30", label: "edit" },
  read: { Icon: Eye, tint: "text-sky-300", tile: "bg-sky-500/10 border-sky-500/30", label: "read" },
  mcp_tool: { Icon: Wrench, tint: "text-violet-300", tile: "bg-violet-500/10 border-violet-500/30", label: "mcp" },
  http: { Icon: Globe, tint: "text-cyan-300", tile: "bg-cyan-500/10 border-cyan-500/30", label: "http" },
  tool: { Icon: Wrench, tint: "text-violet-300", tile: "bg-violet-500/10 border-violet-500/30", label: "tool" },
  think: { Icon: Sparkles, tint: "text-indigo-300", tile: "bg-indigo-500/10 border-indigo-500/30", label: "think" },
}

export function subSpanVisual(kind: SubSpanKind): KindVisual {
  return KIND_VISUAL[kind] ?? KIND_VISUAL.tool
}

// SubSpanIcon — the brand logo for `tool` when it resolves (e.g.
// ansible → red Ansible logo), else the generic lucide glyph for the
// kind. `tool` takes precedence because "Bash · ansible-playbook" is
// more recognisable as the Ansible mark than a generic terminal.
export function SubSpanIcon({
  kind,
  tool,
  className,
}: {
  kind: SubSpanKind
  tool?: string
  className?: string
}) {
  const { Icon, tint } = subSpanVisual(kind)
  const brand = brandIconForType(tool)
  return (
    <BrandGlyph
      brand={brand}
      fallback={Icon}
      className={brand ? className : `${className ?? ""} ${tint}`.trim()}
    />
  )
}

export const SUB_SPAN_STATUS_COLOR: Record<SubSpanStatus, string> = {
  ok: "text-emerald-300",
  error: "text-rose-300",
  running: "text-amber-300",
}

// Waterfall bar gradient per kind — mirrors the mockup's lane colors.
export const SUB_SPAN_BAR_CLASS: Record<SubSpanKind, string> = {
  bash: "bg-gradient-to-r from-emerald-800/70 to-emerald-600/70",
  write: "bg-gradient-to-r from-amber-800/70 to-amber-600/70",
  edit: "bg-gradient-to-r from-amber-800/70 to-amber-600/70",
  read: "bg-gradient-to-r from-sky-800/70 to-sky-600/70",
  mcp_tool: "bg-gradient-to-r from-violet-800/70 to-violet-600/70",
  http: "bg-gradient-to-r from-blue-800/70 to-blue-600/70",
  tool: "bg-gradient-to-r from-violet-800/70 to-violet-600/70",
  think: "bg-gradient-to-r from-indigo-800/70 to-indigo-600/70",
}
