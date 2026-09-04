"use client"

import Link from "next/link"
import { Sparkles } from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { StatusPill } from "@/components/ui/status-pill"
import { formatStatus, type StatusTone } from "@/lib/format-status"
import { entityHref } from "@/lib/entity-links"
import { getModelLabel } from "@/lib/cli-adapters"
import { crewColor } from "@/app/(dashboard)/dashboard-helpers"
import { cn } from "@/lib/utils"

/**
 * Who you are talking to.
 *
 * The chat header used to carry a connection badge, an origin chip, a
 * Commands button and the first eight characters of the session id — and
 * nothing about the agent. This strip is the agent as the crews canvas knows
 * it: face with a status dot, name, role, crew (a link), model (a name, never
 * an id), and the counts that lead to its skills, credentials and runs
 * (README §5: agent → crew, chat, runs, skills, credentials).
 */
export interface AgentStripAgent {
  id: string
  name: string
  slug: string
  status?: string | null
  role_title?: string | null
  llm_model?: string | null
  avatar_seed?: string | null
  avatar_style?: string | null
  avatar_url?: string | null
  crew?: { name: string; slug: string; color?: string | null } | null
  _count?: { skills?: number; credentials?: number; chats?: number } | null
}

/**
 * The agent's status as the shared map spells it (lib/format-status), so the
 * strip cannot call a paused or pending-review agent "Idle" while the crews
 * canvas beside it says otherwise. An agent with no status at all is idle —
 * that is the roster's default.
 */
export function agentStatusPill(status: string | null | undefined): { label: string; tone: StatusTone; live: boolean } {
  const raw = (status ?? "").trim()
  const meta = formatStatus(raw || "IDLE")
  return { label: meta.label, tone: meta.tone, live: raw.toUpperCase() === "RUNNING" }
}

export function AgentStrip({
  agent, size = "md", className, trailing,
}: {
  agent: AgentStripAgent
  /** `md` is the header strip; `lg` is the empty conversation's card. */
  size?: "md" | "lg"
  className?: string
  /** Right-hand controls (Commands, Copy link) on the header strip. */
  trailing?: React.ReactNode
}) {
  const st = agentStatusPill(agent.status)
  const model = agent.llm_model ? getModelLabel(agent.llm_model) : null
  const skills = agent._count?.skills ?? null
  const credentials = agent._count?.credentials ?? null
  const large = size === "lg"
  return (
    <div className={cn("flex min-w-0 items-center gap-3", className)} data-testid="agent-strip">
      <span className="relative shrink-0">
        <AgentAvatar
          seed={agent.avatar_seed || agent.slug}
          style={agent.avatar_style}
          agentId={agent.id}
          avatarUrl={agent.avatar_url}
          alt=""
          className={cn("rounded-lg", large ? "h-14 w-14 rounded-xl" : "h-8 w-8")}
        />
        <span
          aria-hidden
          className={cn(
            "absolute -bottom-0.5 -right-0.5 rounded-full ring-2 ring-card",
            large ? "h-3 w-3" : "h-2.5 w-2.5",
            st.tone === "blue" && "bg-primary shadow-[0_0_0_3px_rgba(30,123,254,0.25)]",
            st.tone === "success" && "bg-success",
            st.tone === "danger" && "bg-destructive",
            st.tone === "warn" && "bg-warn",
            st.tone === "purple" && "bg-purple",
            st.tone === "muted" && "bg-muted-foreground",
          )}
        />
      </span>
      <span className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="flex min-w-0 flex-wrap items-center gap-2">
          <span className={cn("truncate font-semibold", large ? "text-heading" : "text-body")}>{agent.name}</span>
          <StatusPill tone={st.tone} label={st.label} live={st.live} />
        </span>
        <span className="flex min-w-0 flex-wrap items-center gap-1.5 text-label text-muted-foreground">
          {agent.role_title && <span className="truncate">{agent.role_title}</span>}
          {agent.crew && (
            <>
              {agent.role_title && <span className="text-muted-foreground-soft">·</span>}
              <Link href={entityHref({ kind: "crew", slug: agent.crew.slug })} className="flex items-center gap-1.5 hover:underline">
                <span className="h-2 w-2 rounded-full" style={{ background: crewColor(agent.crew.color ?? null) }} aria-hidden />
                {agent.crew.name}
              </Link>
            </>
          )}
          {model && (
            <>
              <span className="text-muted-foreground-soft">·</span>
              <span className="flex items-center gap-1"><Sparkles className="h-3 w-3" aria-hidden />{model}</span>
            </>
          )}
          {skills != null && (
            <>
              <span className="text-muted-foreground-soft">·</span>
              <Link href={entityHref({ kind: "agent", slug: agent.slug, tab: "skills" })} className="hover:underline">
                {skills} {skills === 1 ? "skill" : "skills"}
              </Link>
            </>
          )}
          {credentials != null && (
            <>
              <span className="text-muted-foreground-soft">·</span>
              <Link href={entityHref({ kind: "agent", slug: agent.slug, tab: "credentials" })} className="hover:underline">
                {credentials} {credentials === 1 ? "credential" : "credentials"}
              </Link>
            </>
          )}
          <span className="text-muted-foreground-soft">·</span>
          <Link href={entityHref({ kind: "journal", agentSlug: agent.slug })} className="hover:underline">runs</Link>
        </span>
      </span>
      {trailing && <span className="flex shrink-0 items-center gap-2">{trailing}</span>}
    </div>
  )
}
