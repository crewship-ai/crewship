"use client"

import { EditableField } from "@/components/shared/editable-field"
import { InboxBanner } from "@/components/features/crews/inbox-banner"

import { PeersCard, RecentRunsCard, RecentSessionsCard } from "../agent-canvas-cards"
import { CanvasRow as Row } from "../canvas-base"
import type {
  AgentRecord,
  ChatRow,
  InboxSummary,
  PeerMessageRow,
  RunRow,
} from "./types"
import { ROLE_OPTIONS } from "./types"

export interface OverviewTabProps {
  agent: AgentRecord
  crews: { id: string; name: string; slug: string }[]
  inbox: InboxSummary
  chats: ChatRow[] | null
  runs: RunRow[] | null
  peerMessages: PeerMessageRow[]
  patch: (body: Record<string, unknown>) => Promise<void>
}

export function OverviewTab({
  agent,
  crews,
  inbox,
  chats,
  runs,
  peerMessages,
  patch,
}: OverviewTabProps) {
  const isLead = agent.agent_role === "LEAD"
  const crewOptions = [
    { value: "", label: "(no crew)" },
    ...crews.map((c) => ({ value: c.id, label: c.name })),
  ]

  return (
    <div className="space-y-7">
      <InboxBanner agentId={agent.id} count={inbox.count} summary={inbox.summary} />

      {/* Profile */}
      <section className="space-y-3">
        <div className="flex items-baseline justify-between">
          <h2 className="text-lg font-semibold">Profile</h2>
          <span className="text-[10px] text-muted-foreground">
            updated {new Date(agent.updated_at).toLocaleDateString()}
          </span>
        </div>
        <div className="rounded-xl border border-white/8 bg-card divide-y divide-white/5">
          <Row label="Name">
            <EditableField value={agent.name} onSave={(v) => patch({ name: v })} />
          </Row>
          <Row label="Slug">
            <EditableField value={agent.slug} onSave={(v) => patch({ slug: v })} mono />
          </Row>
          <Row label="Role title">
            <EditableField value={agent.role_title} onSave={(v) => patch({ role_title: v })} />
          </Row>
          <Row label="Description" align="start">
            <EditableField value={agent.description} onSave={(v) => patch({ description: v })} />
          </Row>
          <Row label="Crew">
            <EditableField
              value={agent.crew_id ?? ""}
              onSave={(v) => patch({ crew_id: v || null })}
              options={crewOptions}
              format={(_v) => agent.crew?.name ?? "(no crew)"}
            />
          </Row>
          <Row label="Agent role">
            <EditableField
              value={agent.agent_role}
              onSave={(v) => patch({ agent_role: v })}
              options={[...ROLE_OPTIONS]}
              format={(v) => ROLE_OPTIONS.find((o) => o.value === v)?.label ?? v}
            />
          </Row>
          {isLead && (
            <Row label="Lead mode" align="center">
              <EditableField
                value={agent.lead_mode || "active"}
                onSave={(v) => patch({ lead_mode: v })}
                options={[
                  { value: "active", label: "Active (orchestrates crew)" },
                  { value: "passive", label: "Passive (frontend only)" },
                ]}
                format={(v) => (v === "active" ? "Active" : "Passive")}
              />
            </Row>
          )}
        </div>
      </section>

      {/* Recent sessions + Recent runs */}
      <section className="grid md:grid-cols-2 gap-4">
        <RecentSessionsCard agentSlug={agent.slug} chats={chats} />
        <RecentRunsCard agentId={agent.id} runs={runs} />
      </section>

      {/* Crew peers (LEAD only — uses inbox.peer_messages) */}
      {isLead && peerMessages.length > 0 && (
        <PeersCard messages={peerMessages} />
      )}
    </div>
  )
}
