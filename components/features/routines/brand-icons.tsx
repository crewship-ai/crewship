"use client"

import type { IconType } from "react-icons"
import {
  SiPostgresql,
  SiRedis,
  SiMysql,
  SiMongodb,
  SiAnsible,
  SiTerraform,
  SiKubernetes,
  SiDocker,
  SiPython,
  SiGnubash,
  SiGit,
  SiSlack,
  SiDiscord,
  SiGithub,
  SiNotion,
  SiGooglecalendar,
  SiZapier,
} from "react-icons/si"
import { brandIconKey, type BrandIconKey } from "@/lib/routine-flow"

// brand-icons — single source of truth mapping a BrandIconKey (derived in
// lib/routine-flow, JSX-free) to a real app/brand logo (Simple Icons via
// react-icons/si) plus the brand's tint. Shared by the flow diagram and the
// "What it touches" panel so a Postgres elephant, Redis cube, Ansible, Slack,
// Discord, etc. read identically everywhere. Generic flow nodes (trigger /
// http / agent / transform / output) keep their lucide glyphs and never reach
// this table.

export interface BrandIcon {
  Icon: IconType
  // Hex tint. For brands whose canonical color is near-black (GitHub, Notion)
  // we substitute a light tone so the glyph stays legible on the dark UI.
  color: string
}

export const BRAND_ICONS: Record<BrandIconKey, BrandIcon> = {
  // datastores
  postgresql: { Icon: SiPostgresql, color: "#4169E1" },
  redis: { Icon: SiRedis, color: "#FF4438" },
  mysql: { Icon: SiMysql, color: "#00758F" },
  mongodb: { Icon: SiMongodb, color: "#47A248" },
  // tools / runtimes
  ansible: { Icon: SiAnsible, color: "#EE0000" },
  terraform: { Icon: SiTerraform, color: "#7B42BC" },
  kubernetes: { Icon: SiKubernetes, color: "#326CE5" },
  docker: { Icon: SiDocker, color: "#2496ED" },
  python: { Icon: SiPython, color: "#4B8BBE" },
  bash: { Icon: SiGnubash, color: "#4EAA25" },
  git: { Icon: SiGit, color: "#F05032" },
  // integrations
  slack: { Icon: SiSlack, color: "#36C5F0" },
  discord: { Icon: SiDiscord, color: "#5865F2" },
  github: { Icon: SiGithub, color: "#E6EDF3" }, // brand black → light on dark UI
  notion: { Icon: SiNotion, color: "#D4D4D8" }, // brand black → light on dark UI
  googlecalendar: { Icon: SiGooglecalendar, color: "#4285F4" },
  zapier: { Icon: SiZapier, color: "#FF4F00" },
}

// brandIconByKey resolves an already-derived BrandIconKey (e.g. a FlowNode's
// brandIconKey) to its logo + tint, or null.
export function brandIconByKey(key?: BrandIconKey | null): BrandIcon | null {
  return key ? BRAND_ICONS[key] : null
}

// brandIconForType resolves a raw datastore / tool / integration type string
// (e.g. "postgres", "ansible", "slack") to its logo + tint, or null when no
// real logo is known and the caller should fall back to a lucide glyph.
export function brandIconForType(type?: string | null): BrandIcon | null {
  return brandIconByKey(type ? brandIconKey(type) : null)
}

// BrandGlyph renders the real brand logo (in its tint) when `brand` resolves,
// otherwise the supplied lucide `fallback` (inheriting color from className).
// Keeps the same sizing contract across the flow diagram and "What it touches".
export function BrandGlyph({
  brand,
  fallback: Fallback,
  className,
}: {
  brand: BrandIcon | null
  fallback: IconType | React.ComponentType<{ className?: string }>
  className?: string
}) {
  if (brand) {
    const { Icon, color } = brand
    return <Icon className={className} style={{ color }} aria-hidden />
  }
  return <Fallback className={className} aria-hidden />
}
