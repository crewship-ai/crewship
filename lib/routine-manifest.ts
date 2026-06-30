// routine-manifest — pure, JSX-free grouping of a routine's declared
// `manifest` (blast radius) into ordered chip groups. Shared by the
// "What it touches" detail panel (RoutineTouches) and the dry-run "Would
// use" plan panel so both read as one design and stay in lockstep.
//
// The React layer maps each chip's `tone` → a border/text class and its
// `fallback` → a lucide glyph (used only when no real brand logo resolves
// from `type`). Keeping that mapping out of here makes the grouping fully
// unit-testable (lib/__tests__/routine-manifest.test.ts).

import { integrationLabel } from "@/lib/integration-labels"
import type { RoutineManifest } from "@/lib/routine-flow"

export type ManifestChipTone = "integ" | "store" | "tool" | "agent" | "routine" | "risk"

// Stable identifiers the React layer resolves to a generic lucide glyph
// when a chip's `type` doesn't map to a known brand logo.
export type ManifestFallbackIcon =
  | "puzzle"
  | "store-server"
  | "store-db"
  | "terminal"
  | "bot"
  | "routine"
  | "send"
  | "shield"
  | "key"

export interface ManifestChip {
  // React list key (unique within its group).
  key: string
  // Human-readable chip text.
  label: string
  // Raw type used to resolve a real brand logo (Postgres, Slack, Ansible…).
  // Only carried for integrations / datastores / tools — the kinds that can
  // map to a brand. Absent for agents / sub-routines / egress / credentials.
  type?: string
  tone: ManifestChipTone
  fallback: ManifestFallbackIcon
}

export interface ManifestGroup {
  key: string
  label: string
  chips: ManifestChip[]
}

// SQL-ish stores get the "server" glyph; document/kv stores the "db" glyph.
// Mirrors the previous inline storeFallbackIcon() in routine-touches.tsx.
function storeFallback(type: string): ManifestFallbackIcon {
  return /^postgres|^mysql/i.test(type) ? "store-server" : "store-db"
}

// manifestGroups turns a manifest into the ordered groups the chip panels
// render. Empty groups are dropped, so the result is also the source of
// truth for "is there anything to show?" (see isManifestEmpty). Order is
// declared-first → riskiest-last: integrations, datastores, tools, agents,
// sub-routines, egress, credentials.
export function manifestGroups(manifest?: RoutineManifest | null): ManifestGroup[] {
  const m = manifest
  const groups: ManifestGroup[] = []

  const integrations = m?.integrations ?? []
  if (integrations.length > 0) {
    groups.push({
      key: "integrations",
      label: "Integrations",
      chips: integrations.map((s) => ({
        key: s,
        label: integrationLabel(s),
        type: s,
        tone: "integ",
        fallback: "puzzle",
      })),
    })
  }

  const datastores = m?.datastores ?? []
  if (datastores.length > 0) {
    groups.push({
      key: "datastores",
      label: "Datastores",
      chips: datastores.map((d, i) => ({
        key: `${d.type}-${d.name ?? i}`,
        label: d.name ? `${d.type} · ${d.name}` : d.type,
        type: d.type,
        tone: "store",
        fallback: storeFallback(d.type),
      })),
    })
  }

  const tools = m?.tools ?? []
  if (tools.length > 0) {
    groups.push({
      key: "tools",
      label: "Tools / scripts",
      // Tools run arbitrary code (ansible/bash/python) — flag amber as risky.
      chips: tools.map((t, i) => ({
        key: `${t.type}-${t.name ?? i}`,
        label: t.name ? `${t.type} · ${t.name}` : t.type,
        type: t.type,
        tone: "risk",
        fallback: "terminal",
      })),
    })
  }

  const agents = m?.agents ?? []
  if (agents.length > 0) {
    groups.push({
      key: "agents",
      label: "Agents",
      chips: agents.map((a) => ({ key: a, label: `@${a}`, tone: "agent", fallback: "bot" })),
    })
  }

  const routines = m?.routines ?? []
  if (routines.length > 0) {
    groups.push({
      key: "routines",
      label: "Sub-routines",
      chips: routines.map((r) => ({ key: r, label: r, tone: "routine", fallback: "routine" })),
    })
  }

  const egress = m?.egress ?? []
  if (egress.length > 0) {
    groups.push({
      key: "egress",
      label: "Egress",
      // Outbound network reach is the highest-signal "what can it phone home
      // to" — always amber.
      chips: egress.map((host) => ({ key: host, label: host, tone: "risk", fallback: "send" })),
    })
  }

  const credentials = m?.credentials ?? []
  if (credentials.length > 0) {
    groups.push({
      key: "credentials",
      label: "Credentials",
      chips: credentials.map((c, i) => ({
        key: `${c.type}-${i}`,
        label: c.scope ? `${c.type} · ${c.scope}` : c.type,
        tone: "risk",
        fallback: c.type ? "shield" : "key",
      })),
    })
  }

  return groups
}

// isManifestEmpty is true when a manifest declares no external resources at
// all — the panels render their "nothing to touch" empty state instead.
export function isManifestEmpty(manifest?: RoutineManifest | null): boolean {
  return manifestGroups(manifest).length === 0
}
