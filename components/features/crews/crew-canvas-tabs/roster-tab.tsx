"use client"

import Link from "next/link"
import { Plus } from "lucide-react"
import { AgentAvatar } from "@/components/ui/agent-avatar"

import type { AgentSummary, CrewMemberRow, CrewRecord } from "./types"

export interface RosterTabProps {
  crew: CrewRecord
  agentsForCrew: AgentSummary[]
  members: CrewMemberRow[] | null
  onSelectAgent: (slug: string) => void
}

export function RosterTab({ crew, agentsForCrew, members, onSelectAgent }: RosterTabProps) {
  return (
    <div className="space-y-7">
      {/* Agents */}
      <section className="space-y-3">
        <div className="flex items-baseline justify-between">
          <h2 className="text-lg font-semibold">
            Agents <span className="text-muted-foreground text-sm font-normal ml-1">{agentsForCrew.length}</span>
          </h2>
        </div>
        {agentsForCrew.length === 0 ? (
          <div className="rounded-xl border border-white/8 bg-card p-6 text-center text-xs text-muted-foreground">
            No agents in this crew. Use <strong className="text-foreground/80">+ Agent</strong> in the toolbar to add one.
          </div>
        ) : (
          <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
            {agentsForCrew.map((a) => (
              <button
                key={a.id}
                type="button"
                onClick={() => onSelectAgent(a.slug)}
                className="rounded-xl border border-white/8 bg-card p-3.5 text-left hover:border-white/15 transition-colors"
              >
                <div className="flex items-center gap-3">
                  <AgentAvatar
                    seed={a.avatar_seed || a.name}
                    style={a.avatar_style || crew.avatar_style}
                    className="w-10 h-10 rounded-xl"
                  />
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="font-medium truncate">{a.name}</span>
                      <span className="text-[10px] text-muted-foreground">{a.status?.toLowerCase()}</span>
                      {a.agent_role !== "AGENT" && (
                        <span className="text-[8px] px-1 rounded bg-violet-500/20 text-violet-300">{a.agent_role}</span>
                      )}
                    </div>
                    <div className="text-xs text-muted-foreground truncate">{a.role_title || "—"}</div>
                  </div>
                </div>
                <div className="flex items-center gap-3 mt-3 text-[11px] text-muted-foreground">
                  {a.llm_model && (
                    <span className="px-1.5 py-0.5 rounded bg-zinc-800 border border-white/10 truncate">
                      {a.llm_model}
                    </span>
                  )}
                  {a._count?.skills !== undefined && <span>{a._count.skills} skills</span>}
                  {a._count?.credentials !== undefined && <span>{a._count.credentials} keys</span>}
                </div>
              </button>
            ))}
          </div>
        )}
      </section>

      {/* Workspace users — humans with crew access (different from agents) */}
      <section className="space-y-3">
        <div className="flex items-baseline justify-between">
          <h2 className="text-lg font-semibold">
            Workspace users <span className="text-muted-foreground text-sm font-normal ml-1">{members?.length ?? 0}</span>
          </h2>
          <Link
            href="/settings?tab=members"
            className="text-xs px-2.5 py-1 rounded border border-white/10 hover:bg-white/5 text-foreground/80 flex items-center gap-1.5"
          >
            <Plus className="h-3 w-3" />
            Manage in settings
          </Link>
        </div>
        <div className="rounded-xl border border-white/8 bg-card overflow-hidden divide-y divide-white/5">
          {members === null ? (
            <div className="px-4 py-6 text-xs text-muted-foreground">Loading…</div>
          ) : members.length === 0 ? (
            <div className="px-4 py-6 text-xs text-muted-foreground italic">
              No workspace users assigned yet. By default, OWNERs and ADMINs of the workspace already have full access — assign individual MEMBERs here to scope their reach to this crew only.
            </div>
          ) : (
            members.map((m) => (
              <div key={m.id} className="px-4 py-2.5 flex items-center gap-3">
                {m.user?.avatar_url ? (
                  <img src={m.user.avatar_url} alt="" className="w-8 h-8 rounded-full" />
                ) : (
                  <div className="w-8 h-8 rounded-full bg-violet-600 grid place-items-center text-[11px]">
                    {(m.user?.full_name ?? m.user?.email ?? "?").slice(0, 2).toUpperCase()}
                  </div>
                )}
                <div className="flex-1 min-w-0">
                  <div className="text-sm text-foreground truncate">
                    {m.user?.full_name ?? m.user?.email ?? "Unknown user"}
                  </div>
                  <div className="text-[10px] text-muted-foreground truncate">
                    {m.user?.email}
                    {m.created_at && ` · joined ${new Date(m.created_at).toLocaleDateString()}`}
                  </div>
                </div>
              </div>
            ))
          )}
        </div>
      </section>
    </div>
  )
}
