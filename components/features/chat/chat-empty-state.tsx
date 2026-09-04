"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { Layers, MessageSquare } from "lucide-react"

import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { apiFetch } from "@/lib/api-fetch"
import { entityHref } from "@/lib/entity-links"

import { AgentStrip, type AgentStripAgent } from "./agent-strip"

/**
 * The empty conversation, with context.
 *
 * "Start a conversation · Send a message to Riley" plus four generic chips
 * told a client nothing about who Riley is. This is the agent card — role,
 * crew, model, what it holds — and "What Riley can do", built from the
 * agent's own skills, each with a prompt that starts the conversation from
 * that skill (docs/ux/audit-conversations.md P1-6). An agent with no skills
 * gets one line and a link to add some, never a blank card.
 */
export interface AgentSkillRow {
  id: string
  skill: { slug: string; display_name?: string | null; description?: string | null }
  enabled?: boolean
}

export function skillPrompt(skill: AgentSkillRow["skill"]): string {
  const name = skill.display_name || skill.slug
  return `Use your ${name} skill: what can you do for me with it right now?`
}

export function ChatEmptyState({
  agent, workspaceId, onPick, kind = "direct",
}: {
  agent: AgentStripAgent
  workspaceId: string | null
  onPick: (prompt: string) => void
  /** A routine step or an issue chat is a transcript, not something to start. */
  kind?: "direct" | "routine" | "issue" | "agent"
}) {
  const [skills, setSkills] = useState<AgentSkillRow[] | null>(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    if (!workspaceId || kind !== "direct") return
    let cancelled = false
    apiFetch(`/api/v1/agents/${encodeURIComponent(agent.id)}/skills?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(String(r.status)))))
      .then((rows: unknown) => {
        if (cancelled) return
        setSkills(Array.isArray(rows) ? (rows as AgentSkillRow[]).filter((s) => s.enabled !== false) : [])
      })
      .catch(() => {
        if (!cancelled) setFailed(true)
      })
    return () => {
      cancelled = true
    }
  }, [agent.id, workspaceId, kind])

  if (kind !== "direct") {
    return (
      <div className="mx-auto w-full max-w-3xl px-4 pt-8 md:px-6" data-testid="chat-empty-state">
        <InlineEmpty
          icon={MessageSquare}
          text={kind === "routine" ? "This step has not written anything yet." : kind === "issue" ? "This issue's work has not started yet." : "Nothing has been said here yet."}
          action={<Link href={entityHref({ kind: "chat", agentSlug: agent.slug })} className="text-primary-hover hover:underline">Message {agent.name} →</Link>}
        />
      </div>
    )
  }

  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-4 px-4 pt-8 md:px-6" data-testid="chat-empty-state">
      <AgentStrip agent={agent} size="lg" />
      <DashboardCard
        title={`What ${agent.name} can do`}
        icon={Layers}
        hint={skills ? `${skills.length} ${skills.length === 1 ? "skill" : "skills"}` : undefined}
        action={<Link href={entityHref({ kind: "agent", slug: agent.slug, tab: "skills" })} className="text-primary-hover hover:underline">Manage →</Link>}
      >
        {failed ? (
          <InlineEmpty icon={Layers} text={`Could not load ${agent.name}'s skills.`} />
        ) : skills === null ? (
          <div className="h-10 animate-pulse rounded-md bg-foreground/[0.04]" aria-hidden />
        ) : skills.length === 0 ? (
          <InlineEmpty
            icon={Layers}
            text={`${agent.name} has no skills yet — it answers from its role and prompt alone.`}
            action={<Link href={entityHref({ kind: "agent", slug: agent.slug, tab: "skills" })} className="text-primary-hover hover:underline">Add a skill →</Link>}
          />
        ) : (
          <div className="flex flex-col">
            {skills.slice(0, 6).map((s) => {
              const name = s.skill.display_name || s.skill.slug
              return (
                <div key={s.id} className="flex items-center gap-3 border-t border-border/50 py-2.5 first:border-0">
                  <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary-hover">
                    <Layers className="h-3.5 w-3.5" />
                  </span>
                  <span className="flex min-w-0 flex-1 flex-col">
                    <span className="truncate text-body font-medium">{name}</span>
                    {s.skill.description && <span className="truncate text-label text-muted-foreground">{s.skill.description}</span>}
                  </span>
                  <button
                    type="button"
                    onClick={() => onPick(skillPrompt(s.skill))}
                    className="kit-tap inline-flex h-7 shrink-0 items-center rounded-full border border-border px-3 text-label hover:bg-foreground/[0.04]"
                  >
                    Try it
                  </button>
                </div>
              )
            })}
            {skills.length > 6 && (
              <Link href={entityHref({ kind: "agent", slug: agent.slug, tab: "skills" })} className="pt-2 text-label text-primary-hover hover:underline">
                {skills.length - 6} more →
              </Link>
            )}
          </div>
        )}
      </DashboardCard>
    </div>
  )
}
