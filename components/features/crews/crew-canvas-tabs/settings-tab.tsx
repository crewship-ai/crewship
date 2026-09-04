"use client"

import Link from "next/link"
import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
import { entityHref } from "@/lib/entity-links"
import { Trash2 } from "lucide-react"
import { toast } from "sonner"
import { apiFetch } from "@/lib/api-fetch"
import { useAbilities } from "@/hooks/use-abilities"
import { EditableField } from "@/components/shared/editable-field"
import { CrewRuntimeConfig } from "@/components/features/crews/crew-runtime-config"
import { CrewImageFreshness } from "@/components/features/crews/crew-image-freshness"
import { CrewContainerConfig } from "@/components/features/crews/crew-container-config"
import { CrewNetworkPolicy } from "@/components/features/crews/crew-network-policy"
import { CrewMCPConfig } from "@/components/features/crews/crew-mcp-config"
import { CrewEscalations } from "@/components/features/crews/crew-escalations"
import { CrewPolicyControls } from "@/components/features/crews/crew-policy-controls"
import { AVATAR_STYLES } from "@/lib/agent-avatar"

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
  // Private-endpoint egress is an ADMIN ("manage") capability server-side —
  // mirror that gate in the UI so a MANAGER sees the posture but can't flip it
  // and eat a 403 on save.
  const { role } = useAbilities()
  const isAdmin = role === "OWNER" || role === "ADMIN"
  // #1845: POST /refresh-image is roleCreate server-side ("update" — MANAGER+).
  // Mirrored here so a MEMBER or VIEWER sees the verdict, which is a read, but
  // is not offered a button that would 403.
  const canEditRuntime = isAdmin || role === "MANAGER"

  // Saving a network policy stops the crew container so it's recreated with
  // the new policy on the next agent run. Without feedback that restart was
  // invisible — the save just returned and the container quietly cycled. This
  // polls the container-status endpoint for a few seconds and surfaces the
  // observed state, so the operator sees the policy actually take.
  async function pollContainerRestart(crewId: string) {
    const toastId = toast.loading("Applying network policy — restarting crew container…")
    const url = `/api/v1/crews/${crewId}/container-status?workspace_id=${encodeURIComponent(workspaceId)}`
    let lastStatus = ""
    try {
      // ~15s budget: 10 polls × 1.5s. The container is stopped then lazily
      // recreated on next run, so we report the settled state rather than
      // block on it reaching "running".
      for (let i = 0; i < 10; i++) {
        await new Promise((r) => setTimeout(r, 1500))
        const res = await apiFetch(url)
        if (!res.ok) continue
        const body = (await res.json()) as { status?: string }
        lastStatus = body.status ?? ""
        // Once the container has cycled away from a live "running" state the
        // restart has taken effect — stop early.
        if (lastStatus === "stopped" || lastStatus === "creating" || lastStatus === "not_configured") {
          break
        }
      }
      toast.success("Network policy saved", {
        id: toastId,
        description:
          lastStatus === "creating"
            ? "Crew container is restarting with the new policy."
            : "Crew container will start with the new policy on the next run.",
      })
    } catch {
      // The policy patch already succeeded; a status-poll hiccup shouldn't read
      // as a save failure. Resolve the toast without implying an error.
      toast.success("Network policy saved", {
        id: toastId,
        description: "Crew container will pick up the new policy on the next run.",
      })
    }
  }

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
              // `""` is the documented clear (matches the CLI and the
              // server's `^[A-Za-z0-9_-]{1,16}$` contract, #2035) — a JSON
              // `null` decodes server-side as "field absent" and the PATCH
              // silently no-ops (#2118).
              onSave={(v) => patch({ issue_prefix: v ? v.toUpperCase().slice(0, 16) : "" })}
              ariaLabel="Issue prefix"
              mono
              placeholder="ENG"
            />
            <span className="text-[10px] text-muted-foreground ml-1">max 16 · uppercase</span>
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
          monitor responds to anti-patterns.
        </p>
        <CrewPolicyControls crewId={crew.id} workspaceId={workspaceId} />
      </section>

      {/* Runtime &amp; security — collapsibles per wireframe spec */}
      <section className="space-y-3">
        <h2 className="text-lg font-semibold">Runtime &amp; security</h2>
        <Collapsible
          title="Container resources"
          summary={`${formatMemory(crew.container_memory_mb)} · ${crew.container_cpus} CPU${crew.container_ttl_hours ? ` · stops after ${crew.container_ttl_hours}h idle` : ""}`}
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
          summary={`${crew.network_mode}${crew.network_mode_enforced === false ? " · not enforced" : ""}${Array.isArray(crew.allowed_domains) && crew.allowed_domains.length > 0 ? ` · ${crew.allowed_domains.length} allowed` : ""}`}
        >
          <CrewNetworkPolicy
            networkMode={crew.network_mode === "restricted" ? "restricted" : "free"}
            enforced={crew.network_mode_enforced}
            unenforcedReason={crew.network_mode_unenforced_reason}
            allowedDomains={Array.isArray(crew.allowed_domains)
              ? crew.allowed_domains
              : (crew.allowed_domains ? String(crew.allowed_domains).split(",").map((s) => s.trim()).filter(Boolean) : [])}
            allowPrivateEndpoints={crew.allow_private_endpoints}
            canEdit
            canEditPrivateEndpoints={isAdmin}
            onSave={async (mode, domains, allowPrivate) => {
              const body: Record<string, unknown> = {
                network_mode: mode,
                allowed_domains: domains.length > 0 ? domains : null,
              }
              // Only send the field when the crew record carried it — a bare
              // `undefined` would still serialize as an explicit JSON null on
              // some paths and clear a flag the operator never touched.
              if (allowPrivate !== undefined) body.allow_private_endpoints = allowPrivate
              await patch(body)
              // Fire-and-forget: surface the container restart the policy
              // change triggers. Not awaited so the editor closes immediately.
              void pollContainerRestart(crew.id)
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
          {/* #1845: whether the RUNNING container is still on the image this
              section configures. It belongs here rather than next to the cache
              status above it — that one answers "was the devcontainer built?",
              this one answers "has the base image moved since?", and the two
              have different fixes. */}
          <div className="mb-4">
            <CrewImageFreshness crewId={crew.id} workspaceId={workspaceId} canEdit={canEditRuntime} />
          </div>
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
          summary="who an escalation reaches, and what happens when nobody answers"
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
          <Link href={entityHref({ kind: "integrations", tab: "tools", section: "crew-tools" })} className="text-xs text-primary hover:underline">
            Manage workspace integrations →
          </Link>
        </div>
        {!integrations || integrations.length === 0 ? (
          <div className="rounded-xl border border-white/8 bg-card p-4 text-xs text-muted-foreground">
            No integrations bound to this crew.
          </div>
        ) : (
          <div className="rounded-xl border border-white/8 bg-card divide-y divide-white/5">
            {integrations.map((i) => {
              const gap = i.auth_status === "missing" || i.auth_status === "expired"
              return (
                <div key={i.id} className="px-4 py-2.5 flex items-center gap-3">
                  <div className="w-7 h-7 rounded bg-purple/20 text-purple grid place-items-center text-xs font-semibold">
                    {(i.display_name || i.name).charAt(0).toUpperCase()}
                  </div>
                  <div className="flex-1 min-w-0">
                    <div className="text-sm truncate">{i.display_name || i.name}</div>
                    <div className="text-[11px] text-muted-foreground truncate">
                      {i.transport} · {i.agent_binding_count} {i.agent_binding_count === 1 ? "agent" : "agents"}{i.enabled ? "" : " · disabled"}
                    </div>
                  </div>
                  <StatusPill
                    status={i.auth_status === "none" ? "no_auth" : i.auth_status}
                    label={i.auth_status === "missing" ? "No credential" : i.auth_status === "none" ? "No auth needed" : undefined}
                    tone={i.auth_status === "none" ? "muted" : undefined}
                  />
                  {gap && (
                    <Link
                      href={entityHref({ kind: "integrations", tab: "tools", section: "crew-tools", server: i.id })}
                      className="text-xs text-primary-hover hover:underline shrink-0"
                    >
                      {i.auth_status === "expired" ? "Reconnect" : "Connect"}
                    </Link>
                  )}
                </div>
              )
            })}
          </div>
        )}
      </section>

      {/* Danger */}
      <section className="space-y-3">
        <h2 className="text-lg font-semibold text-destructive">Danger zone</h2>
        <div className="rounded-xl border border-destructive/30 bg-destructive/5 p-4 flex items-center justify-between">
          <div>
            <div className="text-sm font-medium">Delete this crew</div>
            <div className="text-xs text-muted-foreground">
              All {agentsForCrew.length} agent{agentsForCrew.length === 1 ? "" : "s"} will be detached. Container torn down. Journal kept 30 days.
            </div>
          </div>
          <Button variant="destructive" size="sm" onClick={onDelete}>
            <Trash2 className="h-3 w-3" />
            Delete {crew.name}
          </Button>
        </div>
      </section>
    </div>
  )
}
