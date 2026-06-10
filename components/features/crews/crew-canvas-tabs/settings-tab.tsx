"use client"

import Link from "next/link"
import { Trash2 } from "lucide-react"
import { EditableField } from "@/components/shared/editable-field"
import { CrewRuntimeConfig } from "@/components/features/crews/crew-runtime-config"
import { CrewContainerConfig } from "@/components/features/crews/crew-container-config"
import { CrewNetworkPolicy } from "@/components/features/crews/crew-network-policy"
import { CrewMCPConfig } from "@/components/features/crews/crew-mcp-config"
import { CrewEscalations } from "@/components/features/crews/crew-escalations"
import { CrewPolicyControls } from "@/components/features/crews/crew-policy-controls"
import { AVATAR_STYLES } from "@/lib/agent-avatar"
import { cn } from "@/lib/utils"

import { Collapsible } from "../crew-canvas-banner"
import { CanvasRow as Row } from "../canvas-base"
import type { AgentSummary, CrewIntegration, CrewRecord } from "./types"
import { formatMemory } from "./types"

const STYLE_OPTIONS = (Object.entries(AVATAR_STYLES) as Array<[
  string,
  { label: string; style: unknown },
]>).map(([value, meta]) => ({ value, label: meta.label }))

export interface SettingsTabProps {
  workspaceId: string
  crew: CrewRecord
  agentsForCrew: AgentSummary[]
  integrations: CrewIntegration[] | null
  patch: (body: Record<string, unknown>) => Promise<void>
  applyAvatarStyle: (resetOverrides: boolean) => void
  onDelete: () => void
}

export function SettingsTab({
  workspaceId,
  crew,
  agentsForCrew,
  integrations,
  patch,
  applyAvatarStyle,
  onDelete,
}: SettingsTabProps) {
  return (
    <div className="space-y-7">
      {/* Profile */}
      <section className="space-y-3">
        <h2 className="text-lg font-semibold">Profile</h2>
        <div className="rounded-xl border border-white/8 bg-card divide-y divide-white/5">
          <Row label="Name">
            <EditableField value={crew.name} onSave={(v) => patch({ name: v })} ariaLabel="Name" />
          </Row>
          <Row label="Slug">
            <EditableField value={crew.slug} onSave={(v) => patch({ slug: v })} ariaLabel="Slug" mono />
          </Row>
          <Row label="Description" align="start">
            <EditableField value={crew.description} onSave={(v) => patch({ description: v })} ariaLabel="Description" />
          </Row>
          <Row label="Issue prefix">
            <EditableField
              value={crew.issue_prefix ?? ""}
              onSave={(v) => patch({ issue_prefix: (v || null) && v.toUpperCase().slice(0, 5) })}
              ariaLabel="Issue prefix"
              mono
              placeholder="ENG"
            />
            <span className="text-[10px] text-muted-foreground ml-1">max 5 · uppercase</span>
          </Row>
          <Row label="Avatar style">
            <div className="flex items-center gap-2 flex-wrap">
              <EditableField
                value={crew.avatar_style ?? "bottts-neutral"}
                onSave={(v) => patch({ avatar_style: v })}
                ariaLabel="Avatar style"
                options={STYLE_OPTIONS}
                format={(v) => STYLE_OPTIONS.find((o) => o.value === v)?.label ?? v}
              />
              {agentsForCrew.length > 0 && (
                <>
                  <button
                    type="button"
                    onClick={() => applyAvatarStyle(false)}
                    className="text-[10px] px-2 py-0.5 rounded border border-white/10 text-foreground/80 hover:bg-white/5"
                  >
                    Apply to all
                  </button>
                  <button
                    type="button"
                    onClick={() => applyAvatarStyle(true)}
                    className="text-[10px] px-2 py-0.5 rounded border border-white/10 text-foreground/80 hover:bg-white/5"
                    title="Apply this style and clear per-agent overrides"
                  >
                    Reset overrides
                  </button>
                </>
              )}
            </div>
          </Row>
        </div>
      </section>

      {/* Policy — PR-G F2 / F4.2 surface. Lives between Profile and Runtime
          because policy decisions (autonomy, behavior_mode) govern every
          subsequent downstream behaviour (HITL, hire, behavior monitor).
          Read-visible to all members; only ADMIN+ can flip server-side. */}
      <section className="space-y-3">
        <h2 className="text-lg font-semibold">Autonomy &amp; behavior</h2>
        <p className="text-xs text-muted-foreground -mt-1">
          Governs how this crew&rsquo;s agents request operator approval and how the behavior
          monitor responds to anti-patterns. (PRD §6 F2 / F4.2)
        </p>
        <CrewPolicyControls crewId={crew.id} workspaceId={workspaceId} />
      </section>

      {/* Runtime &amp; security — collapsibles per wireframe spec */}
      <section className="space-y-3">
        <h2 className="text-lg font-semibold">Runtime &amp; security</h2>
        <Collapsible
          title="Container resources"
          summary={`${formatMemory(crew.container_memory_mb)} · ${crew.container_cpus} CPU · TTL ${crew.container_ttl_hours ?? "—"}h`}
        >
          <CrewContainerConfig
            memoryMb={crew.container_memory_mb}
            cpus={crew.container_cpus}
            ttlHours={crew.container_ttl_hours}
            canEdit
            onSave={async (config) => { await patch(config) }}
          />
        </Collapsible>

        <Collapsible
          title="Network policy"
          summary={`${crew.network_mode}${Array.isArray(crew.allowed_domains) && crew.allowed_domains.length > 0 ? ` · ${crew.allowed_domains.length} allowed` : ""}`}
        >
          <CrewNetworkPolicy
            networkMode={crew.network_mode === "restricted" ? "restricted" : "free"}
            allowedDomains={Array.isArray(crew.allowed_domains)
              ? crew.allowed_domains
              : (crew.allowed_domains ? String(crew.allowed_domains).split(",").map((s) => s.trim()).filter(Boolean) : [])}
            canEdit
            onSave={async (mode, domains) => {
              await patch({ network_mode: mode, allowed_domains: domains.length > 0 ? domains : null })
            }}
          />
        </Collapsible>

        <Collapsible
          title="MCP servers"
          summary="crew-wide model context protocol servers"
        >
          <CrewMCPConfig crewId={crew.id} workspaceId={workspaceId} />
        </Collapsible>

        <Collapsible
          title="Container image &amp; features"
          summary={crew.runtime_image ?? "debian:trixie-slim"}
        >
          <CrewRuntimeConfig
            crewId={crew.id}
            workspaceId={workspaceId}
            runtimeImage={crew.runtime_image}
            devcontainerConfig={crew.devcontainer_config}
            miseConfig={crew.mise_config}
            cachedImage={crew.cached_image}
            canEdit
            onSave={async (config) => { await patch(config) }}
          />
        </Collapsible>

        <Collapsible
          title="Escalations"
          summary="harbormaster sync · deny on miss"
        >
          <CrewEscalations crewId={crew.id} workspaceId={workspaceId} />
        </Collapsible>
      </section>

      {/* Integrations */}
      <section className="space-y-3">
        <div className="flex items-baseline justify-between">
          <h2 className="text-lg font-semibold">
            Integrations
            <span className="text-muted-foreground text-sm font-normal ml-1">{integrations?.length ?? 0}</span>
          </h2>
          <Link href="/integrations" className="text-xs text-blue-300 hover:underline">
            Manage workspace integrations →
          </Link>
        </div>
        {!integrations || integrations.length === 0 ? (
          <div className="rounded-xl border border-white/8 bg-card p-4 text-xs text-muted-foreground">
            No integrations bound to this crew.
          </div>
        ) : (
          <div className="rounded-xl border border-white/8 bg-card divide-y divide-white/5">
            {integrations.map((i) => (
              <div key={i.id} className="px-4 py-2.5 flex items-center gap-3">
                <div className="w-7 h-7 rounded bg-violet-500/20 text-violet-300 grid place-items-center text-xs font-semibold">
                  {i.name.charAt(0).toUpperCase()}
                </div>
                <div className="flex-1">
                  <div className="text-sm">{i.name}</div>
                  <div className="text-[11px] text-muted-foreground">{i.type}</div>
                </div>
                <span className={cn(
                  "text-[10px]",
                  i.status === "connected" ? "text-emerald-400" : "text-muted-foreground",
                )}>
                  {i.status}
                </span>
              </div>
            ))}
          </div>
        )}
      </section>

      {/* Danger */}
      <section className="space-y-3">
        <h2 className="text-lg font-semibold text-red-400">Danger zone</h2>
        <div className="rounded-xl border border-red-500/30 bg-red-500/5 p-4 flex items-center justify-between">
          <div>
            <div className="text-sm font-medium">Delete this crew</div>
            <div className="text-xs text-muted-foreground">
              All {agentsForCrew.length} agent{agentsForCrew.length === 1 ? "" : "s"} will be detached. Container torn down. Journal kept 30 days.
            </div>
          </div>
          <button
            type="button"
            onClick={onDelete}
            className="text-xs px-3 py-1.5 rounded bg-red-500/20 text-red-300 border border-red-500/40 hover:bg-red-500/30 flex items-center gap-1.5"
          >
            <Trash2 className="h-3 w-3" />
            Delete {crew.name}
          </button>
        </div>
      </section>
    </div>
  )
}
