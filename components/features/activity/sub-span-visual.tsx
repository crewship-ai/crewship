"use client"

import {
  Database,
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
// A `db` span carries its engine there ("postgres", "redis", …) rather
// than the harness tool, which is what puts the elephant on a psql row.
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
  bash: { Icon: Terminal, tint: "text-success", tile: "bg-success/10 border-success/30", label: "bash" },
  // Datastore work is a shell/MCP call the backend named — tinted apart from
  // `bash` so "wrote to Postgres" and "ran ls" don't read as the same action.
  // Engines with a real logo (Postgres/Redis/MySQL/Mongo) resolve it from
  // `attributes.tool`; the rest fall back to this generic Database glyph.
  db: { Icon: Database, tint: "text-cyan-300", tile: "bg-cyan-500/10 border-cyan-500/30", label: "db" },
  write: { Icon: FileText, tint: "text-warn", tile: "bg-warn/10 border-warn/30", label: "write" },
  edit: { Icon: FileText, tint: "text-warn", tile: "bg-warn/10 border-warn/30", label: "edit" },
  read: { Icon: Eye, tint: "text-sky-300", tile: "bg-sky-500/10 border-sky-500/30", label: "read" },
  mcp_tool: { Icon: Wrench, tint: "text-purple", tile: "bg-purple/10 border-purple/30", label: "mcp" },
  http: { Icon: Globe, tint: "text-notice", tile: "bg-notice/10 border-notice/30", label: "http" },
  tool: { Icon: Wrench, tint: "text-purple", tile: "bg-purple/10 border-purple/30", label: "tool" },
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
  ok: "text-success",
  error: "text-destructive",
  running: "text-warn",
}
