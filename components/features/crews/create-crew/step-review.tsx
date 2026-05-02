"use client"

import { CrewIcon } from "@/components/ui/crew-icon"
import { cn } from "@/lib/utils"
import type { WizardState } from "./types"

interface Props {
  state: WizardState
  /** Optional jump-back callback to allow click-to-edit on a row */
  onEdit?: (step: 1 | 2 | 3 | 4) => void
  /** Lineup summary derived from wizard mode */
  lineupSummary: { count: number; source: string; agents?: { name: string; agent_role: string }[] }
}

export function StepReview({ state, onEdit, lineupSummary }: Props) {
  return (
    <div className="rounded-lg border border-white/10 bg-card/50 p-4 space-y-1">
      <Row label="Identity" onEdit={onEdit && (() => onEdit(1))}>
        <CrewIcon icon={state.icon} color={state.color} size="sm" />
        <strong>{state.name}</strong>
        <span className="text-muted-foreground font-mono text-xs">@{state.slug}</span>
        <Pill>icon: {state.icon}</Pill>
        <Pill>color: {state.color}</Pill>
      </Row>

      {state.description && (
        <Row label="Description" onEdit={onEdit && (() => onEdit(1))}>
          <span className="text-foreground/80 text-[12.5px]">{state.description}</span>
        </Row>
      )}

      <Row label="Lineup" onEdit={onEdit && (() => onEdit(2))}>
        {lineupSummary.count > 0 ? (
          <>
            <Pill>{lineupSummary.count} agents</Pill>
            <Pill>{lineupSummary.source}</Pill>
            {lineupSummary.agents && (
              <div className="flex flex-wrap gap-1 ml-2">
                {lineupSummary.agents.slice(0, 6).map((a) => (
                  <span
                    key={a.name}
                    className={cn(
                      "h-5 w-5 rounded text-[9px] font-bold flex items-center justify-center",
                      a.agent_role === "LEAD" ? "bg-amber-500/20 text-amber-300" : "bg-violet-500/15 text-violet-300",
                    )}
                    title={`${a.name} (${a.agent_role})`}
                  >
                    {a.name.slice(0, 1).toUpperCase()}
                  </span>
                ))}
              </div>
            )}
          </>
        ) : (
          <>
            <span className="text-amber-300">●</span>
            <span className="text-foreground/80 text-[12.5px]">Empty crew — agents added later</span>
          </>
        )}
      </Row>

      <Row label="Container" onEdit={onEdit && (() => onEdit(3))}>
        <Pill>{prettyMemory(state.memoryMB)}</Pill>
        <Pill>{state.cpus} CPU</Pill>
        <Pill>TTL: {state.ttlHours === null ? "never" : `${state.ttlHours} h`}</Pill>
      </Row>

      <Row label="Network" onEdit={onEdit && (() => onEdit(3))}>
        <span className={cn(
          "h-2 w-2 rounded-full inline-block",
          state.networkMode === "free" ? "bg-emerald-400" : "bg-amber-400",
        )} />
        <span className="capitalize text-foreground/90">{state.networkMode}</span>
        {state.networkMode === "restricted" && state.allowedDomains.length > 0 && (
          <div className="flex flex-wrap gap-1">
            {state.allowedDomains.slice(0, 6).map((d) => <Pill key={d}>{d}</Pill>)}
            {state.allowedDomains.length > 6 && <Pill>+{state.allowedDomains.length - 6} more</Pill>}
          </div>
        )}
      </Row>

      {hasContainerOverrides(state) && (
        <Row label="Image" onEdit={onEdit && (() => onEdit(4))}>
          <Pill>{summaryBaseImage(state)}</Pill>
          {summaryFeatureCount(state.devcontainerConfig) > 0 && (
            <Pill>{summaryFeatureCount(state.devcontainerConfig)} feature{summaryFeatureCount(state.devcontainerConfig) === 1 ? "" : "s"}</Pill>
          )}
          {summaryRuntimeCount(state.miseConfig) > 0 && (
            <Pill>{summaryRuntimeCount(state.miseConfig)} runtime{summaryRuntimeCount(state.miseConfig) === 1 ? "" : "s"}</Pill>
          )}
        </Row>
      )}

      {summaryMCPCount(state.mcpConfig) > 0 && (
        <Row label="MCP" onEdit={onEdit && (() => onEdit(4))}>
          <span className="h-2 w-2 rounded-full inline-block bg-violet-400" />
          <Pill>{summaryMCPCount(state.mcpConfig)} server{summaryMCPCount(state.mcpConfig) === 1 ? "" : "s"}</Pill>
        </Row>
      )}

      <Row label="After create">
        <span className="text-muted-foreground text-[12px] leading-relaxed">
          Container <code className="text-[11px] font-mono bg-black/30 px-1 py-0.5 rounded">crewship-team-{state.slug}</code> built in background.
          {lineupSummary.count > 0 && ` ${lineupSummary.count} agents auto-assigned credentials, ready in ~2 min.`}
        </span>
      </Row>
    </div>
  )
}

function Row({ label, onEdit, children }: { label: string; onEdit?: (() => void) | undefined; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[110px_1fr] gap-2.5 items-center py-1.5 border-b border-dashed border-white/5 last:border-b-0">
      <div className="text-[11.5px] text-muted-foreground flex items-center gap-1">
        {label}
        {onEdit && (
          <button
            type="button"
            onClick={onEdit}
            className="text-[10px] text-blue-400/80 hover:text-blue-300 ml-auto"
            title="Edit"
          >
            edit
          </button>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-2 text-[12.5px]">
        {children}
      </div>
    </div>
  )
}

function Pill({ children }: { children: React.ReactNode }) {
  return <span className="text-[10px] font-mono px-1.5 py-0.5 rounded bg-white/5 text-foreground/80">{children}</span>
}

function prettyMemory(mb: number): string {
  if (mb >= 1024) return `${(mb / 1024).toFixed(mb % 1024 === 0 ? 0 : 1)} GB`
  return `${mb} MB`
}

// Container summary helpers — same parsers as step-container, kept local because
// step-container.tsx is a sibling and we don't want a 3rd file just for these.

function hasContainerOverrides(s: WizardState): boolean {
  return (
    s.runtimeImage.trim() !== "" ||
    s.devcontainerConfig.trim() !== "" ||
    s.miseConfig.trim() !== ""
  )
}

function summaryBaseImage(s: WizardState): string {
  if (s.devcontainerConfig.trim()) {
    try {
      const parsed = JSON.parse(s.devcontainerConfig) as { image?: unknown }
      if (typeof parsed.image === "string" && parsed.image) return parsed.image
    } catch { /* fallthrough */ }
  }
  return s.runtimeImage.trim() || "debian:bookworm-slim"
}

function summaryFeatureCount(devcontainerConfig: string): number {
  if (!devcontainerConfig.trim()) return 0
  try {
    const parsed = JSON.parse(devcontainerConfig) as { features?: Record<string, unknown> }
    return parsed.features ? Object.keys(parsed.features).length : 0
  } catch { return 0 }
}

function summaryRuntimeCount(miseConfig: string): number {
  if (!miseConfig.trim()) return 0
  let inTools = false
  let count = 0
  for (const raw of miseConfig.split(/\r?\n/)) {
    const line = raw.trim()
    if (line.startsWith("[")) { inTools = line === "[tools]"; continue }
    if (inTools && /^[\w-]+\s*=/.test(line)) count++
  }
  return count
}

function summaryMCPCount(mcpConfig: string): number {
  if (!mcpConfig.trim()) return 0
  try {
    const parsed = JSON.parse(mcpConfig) as { mcpServers?: Record<string, unknown> }
    return parsed.mcpServers ? Object.keys(parsed.mcpServers).length : 0
  } catch { return 0 }
}
