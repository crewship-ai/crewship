"use client"

import Link from "next/link"
import { Bot, Box, Clock, Inbox, KeyRound, Plug, Plus } from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
import { entityHref } from "@/lib/entity-links"
import { cn } from "@/lib/utils"

import { CrCard, CrKv, CrLine, CrNote, LeadMark } from "./cr-card"
import { CrewActivityFeed } from "./crew-activity-feed"
import type { CrewNeed } from "./crew-needs"
import type { ContainerStatus } from "./bottom-panel/types"
import type { ProvisioningState } from "./explorer-groups"
import { imageLabel } from "./crew-header"
import { orderAgents, tempHireLabel } from "./crews-panel-model"
import { formatSpend, type CrewSpend } from "./crew-spend"
import type { CrewTab } from "./crews-tabs"
import type { AgentSummary, CrewIntegration, CrewRecord } from "./crew-canvas-tabs/types"

const NEED_ICON = { build: Box, credential: KeyRound, integration: Plug, agent: Bot, decision: Inbox } as const

/** Tools pinned in mise.toml's [tools] table: `node = "22.11.0"`. */
export function miseTools(mise: string | null | undefined): Array<{ name: string; version: string }> {
  if (!mise) return []
  const out: Array<{ name: string; version: string }> = []
  let inTools = false
  for (const raw of mise.split("\n")) {
    const line = raw.trim()
    if (line.startsWith("[")) { inTools = line === "[tools]"; continue }
    if (!inTools) continue
    const m = /^"?([\w@/.:-]+)"?\s*=\s*"?([^"#\s]+)"?/.exec(line)
    if (m) out.push({ name: m[1], version: m[2] })
  }
  return out
}

function domains(value: CrewRecord["allowed_domains"]): string[] {
  if (Array.isArray(value)) return value
  if (!value) return []
  try { const p: unknown = JSON.parse(value); return Array.isArray(p) ? p.filter((d): d is string => typeof d === "string") : [] } catch { return [] }
}

export interface CrewOverviewProps {
  workspaceId: string
  crew: CrewRecord
  agents: AgentSummary[]
  needs: CrewNeed[]
  busyNeedId?: string | null
  onBuild: () => void
  onInstall: (need: CrewNeed & { action: { kind: "install" } }) => void
  /** GET /crews/{id}/containers; null while it loads or when it failed. */
  containers: ContainerStatus[] | null
  provisioning?: ProvisioningState
  /** Credential bindings whose CLI is missing from the image. */
  gapCount: number
  integrations: CrewIntegration[] | null
  onSelectAgent: (slug: string) => void
  onTab: (tab: CrewTab) => void
  onNewAgent: () => void
  onHire?: () => void
  /** Last 30 days of this crew; null = unknown or not for this reader, so the row is left out. */
  spend?: CrewSpend | null
  now?: number
}

/**
 * C-OV: what needs a person (one verb per row), what the crew is doing with
 * its recent activity, Health, the Team and what the crew works on.
 */
export function CrewOverview({
  workspaceId,
  crew,
  agents,
  needs,
  busyNeedId = null,
  onBuild,
  onInstall,
  containers,
  provisioning,
  gapCount,
  integrations,
  onSelectAgent,
  onTab,
  onNewAgent,
  onHire,
  spend = null,
  now = Date.now(),
}: CrewOverviewProps) {
  const team = orderAgents(agents.filter((a) => !a.expired_at))
  const running = team.filter((a) => a.status === "RUNNING")
  const runtime = containers?.find((c) => c.kind === "crew") ?? containers?.[0] ?? null
  const tools = miseTools(crew.mise_config)
  const allowed = domains(crew.allowed_domains)
  const missingIntegrations = (integrations ?? []).filter((i) => i.auth_status === "missing" || i.auth_status === "expired").length

  const containerValue = runtime == null
    ? <span className="text-muted-foreground">{containers == null ? "Checking…" : "Not created yet · starts on the next run"}</span>
    : runtime.status === "running"
      ? <><StatusPill tone="success" label="Running" />{" "}<span className="text-label text-muted-foreground">
          {[runtime.cpu_percent != null ? `${Math.round(runtime.cpu_percent)} % CPU` : null, runtime.memory_mb != null ? `${(runtime.memory_mb / 1024).toFixed(1)} of ${(crew.container_memory_mb / 1024).toFixed(0)} GB` : null].filter(Boolean).join(" · ")}
        </span></>
      : <span className="text-muted-foreground">Stopped · starts on the next run</span>

  const imageValue = provisioning === "running"
    ? <StatusPill tone="blue" label="Building" live />
    : provisioning === "failed"
      ? <StatusPill tone="danger" label="Build failed" />
      : provisioning === "needs_provision"
        ? <StatusPill tone="warn" label="Behind · rebuild" />
        : <span>{imageLabel(crew.runtime_image)} · <span className="text-muted-foreground">current</span></span>

  const accessValue = gapCount > 0 || missingIntegrations > 0
    ? <StatusPill tone="warn" label={[gapCount ? `${gapCount} credential ${gapCount === 1 ? "gap" : "gaps"}` : null, missingIntegrations ? `${missingIntegrations} integration${missingIntegrations === 1 ? "" : "s"} not connected` : null].filter(Boolean).join(" · ")} />
    : <span>No gaps found</span>

  const networkValue = crew.network_mode === "restricted"
    ? <span>Restricted <span className="text-label text-muted-foreground">· {allowed.length} allowed</span></span>
    : <span className="text-chip-warn-fg">Open · any site</span>

  return (
    <div className="space-y-4" data-testid="crew-overview">
      {needs.length > 0 && (
        <CrCard title="Needs you" concept="inbox" count={needs.length} link={{ label: "Inbox", href: entityHref({ kind: "inbox" }) }} data-testid="crew-needs-you">
          {needs.map((n) => {
            const Icon = NEED_ICON[n.icon]
            const busy = busyNeedId === n.id
            return (
              <CrLine
                key={n.id}
                lead={<span className={cn("grid size-7 shrink-0 place-items-center rounded-md", n.tone === "danger" ? "bg-chip-danger-bg text-chip-danger-fg" : "bg-chip-warn-bg text-chip-warn-fg")}><Icon className="size-3.5" aria-hidden /></span>}
                title={n.title}
                sub={n.detail}
                right={
                  n.action.kind === "link" ? (
                    <Button asChild size="sm"><Link href={n.action.href}>{n.action.label}</Link></Button>
                  ) : n.action.kind === "build" ? (
                    <Button size="sm" disabled={busy} onClick={onBuild}>{busy ? "Starting…" : n.action.label}</Button>
                  ) : (
                    <Button size="sm" disabled={busy} onClick={() => onInstall(n as CrewNeed & { action: { kind: "install" } })}>{busy ? "Installing…" : n.action.label}</Button>
                  )
                }
              />
            )
          })}
        </CrCard>
      )}

      <div className="grid gap-4 xl:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
        <CrCard title="What the crew is doing" concept="activity">
          {running.length > 0 ? running.map((a) => (
            <CrLine
              key={a.id}
              lead={<AgentAvatar seed={a.avatar_seed || a.name} style={a.avatar_style} avatarUrl={a.avatar_url} className="size-7 rounded-md" />}
              title={<>{a.name}{a.agent_role === "LEAD" && <> <LeadMark /></>}</>}
              sub={a.role_title ?? "Working"}
              right={<Button variant="ghost" size="sm" onClick={() => onSelectAgent(a.slug)}>Open</Button>}
            />
          )) : (
            <CrNote>Nobody is working right now.{runtime?.status !== "running" ? " The container starts on the next run." : ""}</CrNote>
          )}
          <div className="border-t border-border px-4 py-3">
            <p className="mb-1.5 text-label font-medium text-muted-foreground">Recent activity</p>
            <CrewActivityFeed workspaceId={workspaceId} crewId={crew.id} limit={4} />
          </div>
        </CrCard>

        <CrCard title="Health" concept="crews" link={{ label: "Environment", onClick: () => onTab("env") }} data-testid="crew-health">
          <CrKv rows={[
            ["Container", containerValue],
            ["Tools", tools.length ? <span className="flex flex-wrap gap-1">{tools.map((t) => <span key={t.name} className="rounded-sm bg-muted px-1.5 font-mono text-label">{t.name} {t.version.split(".").slice(0, 2).join(".")}</span>)}</span> : <span className="text-muted-foreground">From the image</span>],
            ["Image", imageValue],
            ["Access", accessValue],
            ["Network", networkValue],
            ...(spend ? [["Spend · 30 d", <span key="spend"><span className="font-mono tabular-nums">{formatSpend(spend.costUsd)}</span> <span className="text-label text-muted-foreground">· {spend.calls.toLocaleString("en-GB")} model {spend.calls === 1 ? "call" : "calls"}</span></span>] as [string, React.ReactNode]] : []),
          ]} />
        </CrCard>
      </div>

      <div className="grid gap-4 xl:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
        <CrCard title="Team" concept="crews" count={team.length} link={{ label: "Team", onClick: () => onTab("team") }}>
          {team.length === 0 ? (
            <CrNote>No agents yet. Add a lead first; it can hire the rest.</CrNote>
          ) : (
            <div className="grid gap-2 border-t border-border p-3 sm:grid-cols-2">
              {team.map((a) => {
                const temp = tempHireLabel(a, now)
                return (
                  <button
                    key={a.id}
                    type="button"
                    onClick={() => onSelectAgent(a.slug)}
                    className="flex min-w-0 items-start gap-2.5 rounded-lg border border-border p-2.5 text-left transition-colors hover:bg-foreground/[0.03] coarse:min-h-[44px]"
                  >
                    <AgentAvatar seed={a.avatar_seed || a.name} style={a.avatar_style} avatarUrl={a.avatar_url} className="size-9 shrink-0 rounded-lg" />
                    <span className="min-w-0 flex-1">
                      <b className="flex items-center gap-1.5 truncate text-body font-medium text-foreground">{a.name}{a.agent_role === "LEAD" && <LeadMark />}</b>
                      <span className="block truncate text-label text-muted-foreground">{[a.role_title, a.llm_model].filter(Boolean).join(" · ") || "Agent"}</span>
                      <span className={cn("block truncate text-label", a.status === "ERROR" ? "text-chip-danger-fg" : temp ? "text-chip-warn-fg" : "text-muted-foreground")}>
                        {temp ? `Temporary · ${temp.label}` : a.status === "RUNNING" ? "Working" : a.status === "ERROR" ? "Last run failed" : "Idle"}
                      </span>
                    </span>
                  </button>
                )
              })}
            </div>
          )}
          <div className="flex flex-wrap gap-2 border-t border-border px-4 py-2.5">
            <Button variant="outline" size="sm" onClick={onNewAgent}><Plus className="size-3.5" aria-hidden /> New agent</Button>
            {onHire && <Button variant="ghost" size="sm" onClick={onHire}><Clock className="size-3.5" aria-hidden /> Hire a temporary agent</Button>}
          </div>
        </CrCard>

        <CrCard title="Works on" concept="routines" data-testid="crew-works-on" id="crew-works-on">
          <CrLine title="Routines" sub="Recurring work this crew runs" right={<Button asChild variant="ghost" size="sm"><Link href={entityHref({ kind: "routines", crewSlug: crew.slug })}>Open</Link></Button>} />
          <CrLine title="Issues" sub={crew.issue_prefix ? `${crew.issue_prefix}-1, ${crew.issue_prefix}-2 …` : "Issues assigned to this crew"} right={<Button asChild variant="ghost" size="sm"><Link href={entityHref({ kind: "issues", crewId: crew.id })}>Open</Link></Button>} />
        </CrCard>
      </div>
    </div>
  )
}
