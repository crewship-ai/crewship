"use client"

import { Trash2 } from "lucide-react"
import { useAbilities } from "@/hooks/use-abilities"
import { EditableField } from "@/components/shared/editable-field"
import { CrewEscalations } from "@/components/features/crews/crew-escalations"
import { CrewPolicyControls } from "@/components/features/crews/crew-policy-controls"
import { AVATAR_STYLES } from "@/lib/agent-avatar"
import { backfillAgentAvatars } from "@/lib/agent-avatar-persist"
import { AvatarBackfillButton } from "@/components/features/crews/avatar-backfill-button"

import { CanvasRow as Row } from "../canvas-base"
import type { AgentSummary, CrewRecord } from "./types"

const STYLE_OPTIONS = (Object.entries(AVATAR_STYLES) as Array<[
  string,
  { label: string; style: unknown },
]>).map(([value, meta]) => ({ value, label: meta.label }))

export interface SettingsTabProps {
  workspaceId: string
  crew: CrewRecord
  agentsForCrew: AgentSummary[]
  patch: (body: Record<string, unknown>) => Promise<void>
  applyAvatarStyle: (resetOverrides: boolean) => void
  /** Absent for anyone who may not delete the crew (ADMIN+): no Danger zone. */
  onDelete?: () => void
}

export function SettingsTab({
  workspaceId,
  crew,
  agentsForCrew,
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

  return (
    <div className="space-y-7">
      {/* Profile */}
      <section className="space-y-3">
        <h2 className="text-lg font-semibold">Profile</h2>
        <div className="rounded-xl border border-foreground/8 bg-card divide-y divide-foreground/5">
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
                onSave={async (v) => {
                  await patch({ avatar_style: v })
                  // The server drops the renders of agents that inherit the
                  // crew's style; store the new faces now, as part of this
                  // edit, rather than leaving them to whoever looks next
                  // (#2876). Per-agent outcomes are not an error here.
                  await backfillAgentAvatars(workspaceId, { crewId: crew.id }).catch(() => undefined)
                }}
                ariaLabel="Avatar style"
                options={STYLE_OPTIONS}
                format={(v) => STYLE_OPTIONS.find((o) => o.value === v)?.label ?? v}
              />
              {agentsForCrew.length > 0 && (
                <>
                  <button
                    type="button"
                    onClick={() => applyAvatarStyle(false)}
                    className="text-[10px] px-2 py-0.5 rounded border border-foreground/10 text-foreground/80 hover:bg-foreground/5"
                  >
                    Apply to all
                  </button>
                  <button
                    type="button"
                    onClick={() => applyAvatarStyle(true)}
                    className="text-[10px] px-2 py-0.5 rounded border border-foreground/10 text-foreground/80 hover:bg-foreground/5"
                    title="Apply this style and clear per-agent overrides"
                  >
                    Reset overrides
                  </button>
                  {canEditRuntime && agentsForCrew.some((a) => !a.avatar_url) && (
                    <AvatarBackfillButton workspaceId={workspaceId} crewId={crew.id} className="h-6 text-[10px]" />
                  )}
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
          How this crew&rsquo;s agents ask a person for approval, and what happens when an agent
          does something unusual.
        </p>
        <CrewPolicyControls crewId={crew.id} workspaceId={workspaceId} />
      </section>

      {/* Escalations — what agents of this crew asked a person. Container,
          network, image, services and security live in the Environment tab. */}
      <section className="space-y-3">
        <h2 className="text-lg font-semibold">Escalations</h2>
        <CrewEscalations crewId={crew.id} workspaceId={workspaceId} />
      </section>

      {/* Danger */}
      {onDelete && <section className="space-y-3">
        <h2 className="text-lg font-semibold text-destructive">Danger zone</h2>
        <div className="rounded-xl border border-destructive/30 bg-destructive/5 p-4 flex items-center justify-between">
          <div>
            <div className="text-sm font-medium">Delete this crew</div>
            <div className="text-xs text-muted-foreground">
              Deletes the crew and all {agentsForCrew.length} agent{agentsForCrew.length === 1 ? "" : "s"}. Background cleanup removes verified runtimes while preserving mounted volumes and host data. Declared sidecar data volumes are deleted separately.
            </div>
          </div>
          <button
            type="button"
            onClick={onDelete}
            className="text-xs px-3 py-1.5 rounded bg-destructive/20 text-destructive border border-destructive/40 hover:bg-destructive/30 flex items-center gap-1.5"
          >
            <Trash2 className="h-3 w-3" />
            Delete {crew.name}
          </button>
        </div>
      </section>}
    </div>
  )
}
