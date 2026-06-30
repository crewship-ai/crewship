"use client"

import type { ReactNode } from "react"
import {
  Puzzle,
  Database,
  Server,
  Terminal,
  Bot,
  Send,
  KeyRound,
  ShieldAlert,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { integrationLabel } from "@/lib/integration-labels"
import type { RoutineManifest } from "@/lib/routine-flow"

// RoutineTouches — the "What it touches" capability manifest panel. Renders
// the routine's blast radius as chips grouped by kind (Integrations,
// Datastores, Tools, Agents, Egress, Credentials). Risky rows — egress
// (outbound network), credentials, and code/script tools — are highlighted
// amber so a reviewer can see "what can this thing reach + what secrets does
// it hold" at a glance. Read-only, derived entirely from `manifest`.

type ChipTone = "integ" | "store" | "tool" | "agent" | "neutral" | "risk"

const TONE: Record<ChipTone, string> = {
  integ: "border-indigo-500/30 text-indigo-300",
  store: "border-cyan-500/30 text-cyan-300",
  tool: "border-violet-500/30 text-violet-300",
  agent: "border-emerald-500/30 text-emerald-300",
  neutral: "border-border text-muted-foreground",
  risk: "border-amber-500/35 text-amber-400",
}

function Chip({
  tone,
  icon: Icon,
  children,
}: {
  tone: ChipTone
  icon?: typeof Puzzle
  children: ReactNode
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-[7px] border bg-card px-2 py-[3px] text-[11px]",
        TONE[tone],
      )}
    >
      {Icon && <Icon className="h-3 w-3" aria-hidden />}
      {children}
    </span>
  )
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-start gap-2 border-t border-white/[0.04] py-2 first:border-t-0">
      <div className="w-[88px] shrink-0 pt-[3px] text-[10.5px] text-muted-foreground-soft">{label}</div>
      <div className="flex flex-wrap gap-1.5">{children}</div>
    </div>
  )
}

function storeIcon(type: string) {
  return /^postgres|^mysql/i.test(type) ? Server : Database
}

export function RoutineTouches({ manifest }: { manifest?: RoutineManifest | null }) {
  const m = manifest
  const integrations = m?.integrations ?? []
  const datastores = m?.datastores ?? []
  const tools = m?.tools ?? []
  const agents = m?.agents ?? []
  const egress = m?.egress ?? []
  const credentials = m?.credentials ?? []

  const isEmpty =
    integrations.length === 0 &&
    datastores.length === 0 &&
    tools.length === 0 &&
    agents.length === 0 &&
    egress.length === 0 &&
    credentials.length === 0

  if (isEmpty) {
    return (
      <div className="px-1 py-3 text-center text-xs text-muted-foreground">
        This routine declares no external resources.
        <br />
        <span className="text-muted-foreground-soft">Nothing to touch beyond its own steps.</span>
      </div>
    )
  }

  return (
    <div className="px-1">
      {integrations.length > 0 && (
        <Row label="Integrations">
          {integrations.map((s) => (
            <Chip key={s} tone="integ" icon={Puzzle}>
              {integrationLabel(s)}
            </Chip>
          ))}
        </Row>
      )}

      {datastores.length > 0 && (
        <Row label="Datastores">
          {datastores.map((d, i) => {
            const Icon = storeIcon(d.type)
            return (
              <Chip key={`${d.type}-${d.name ?? i}`} tone="store" icon={Icon}>
                {d.type}
                {d.name ? ` · ${d.name}` : ""}
              </Chip>
            )
          })}
        </Row>
      )}

      {tools.length > 0 && (
        <Row label="Tools / scripts">
          {tools.map((t, i) => (
            // Tools run arbitrary code (ansible/bash/python) — flag amber as risky.
            <Chip key={`${t.type}-${t.name ?? i}`} tone="risk" icon={Terminal}>
              {t.type}
              {t.name ? ` · ${t.name}` : ""}
            </Chip>
          ))}
        </Row>
      )}

      {agents.length > 0 && (
        <Row label="Agents">
          {agents.map((a) => (
            <Chip key={a} tone="agent" icon={Bot}>
              @{a}
            </Chip>
          ))}
        </Row>
      )}

      {egress.length > 0 && (
        <Row label="Egress">
          {egress.map((host) => (
            // Outbound network reach is the highest-signal "what can it phone
            // home to" — always amber.
            <Chip key={host} tone="risk" icon={Send}>
              {host}
            </Chip>
          ))}
        </Row>
      )}

      {credentials.length > 0 && (
        <Row label="Credentials">
          {credentials.map((c, i) => (
            <Chip key={`${c.type}-${i}`} tone="risk" icon={c.type ? ShieldAlert : KeyRound}>
              {c.type}
              {c.scope ? ` · ${c.scope}` : ""}
            </Chip>
          ))}
        </Row>
      )}
    </div>
  )
}
