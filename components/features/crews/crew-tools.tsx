"use client"

import { useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { Check, KeyRound, Search, ShieldCheck } from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Button } from "@/components/ui/button"
import { apiFetch } from "@/lib/api-fetch"
import { entityHref } from "@/lib/entity-links"
import { cn } from "@/lib/utils"

import { CrCard, CrNote, CrSegment } from "./cr-card"
import { orderAgents } from "./crews-panel-model"
import type { AgentSkillRow } from "./agent-canvas-tabs/types"
import type { AgentSummary, CrewIntegration } from "./crew-canvas-tabs/types"

export type ResourceKind = "skill" | "credential" | "integration"
export type ResourceFilter = "all" | ResourceKind

export interface MatrixRow {
  key: string
  kind: ResourceKind
  name: string
  /** Agent ids that hold it; null when the server reports only a count. */
  holders: Set<string> | null
  /** For integrations: how many agents are bound. */
  count?: number
  status?: "ready" | "missing"
}

/** Agents at most fetched for the matrix; a larger crew links to each agent. */
export const MATRIX_AGENT_CAP = 12
/** Rows shown before "Show all". */
export const MATRIX_FOLD = 6

/**
 * Rows of the Tools and access matrix: every skill and credential any agent
 * of the crew holds, and the crew's integrations, filtered by type and text.
 */
export function buildMatrix({
  skills,
  credentials,
  integrations,
  filter = "all",
  query = "",
}: {
  skills: ReadonlyMap<string, Array<{ id: string; name: string }>>
  credentials: ReadonlyMap<string, Array<{ id: string; name: string }>>
  integrations: CrewIntegration[]
  filter?: ResourceFilter
  query?: string
}): MatrixRow[] {
  const rows = new Map<string, MatrixRow>()
  const add = (kind: ResourceKind, agentId: string, r: { id: string; name: string }) => {
    const key = `${kind}:${r.id}`
    const row = rows.get(key) ?? { key, kind, name: r.name, holders: new Set<string>() }
    row.holders?.add(agentId)
    rows.set(key, row)
  }
  for (const [agentId, list] of skills) for (const s of list) add("skill", agentId, s)
  for (const [agentId, list] of credentials) for (const c of list) add("credential", agentId, c)
  for (const i of integrations) {
    rows.set(`integration:${i.id}`, {
      key: `integration:${i.id}`,
      kind: "integration",
      name: i.display_name || i.name,
      holders: null,
      count: i.agent_binding_count,
      status: i.auth_status === "missing" || i.auth_status === "expired" ? "missing" : "ready",
    })
  }
  const q = query.trim().toLowerCase()
  const order: Record<ResourceKind, number> = { skill: 0, credential: 1, integration: 2 }
  return [...rows.values()]
    .filter((r) => (filter === "all" || r.kind === filter) && (!q || r.name.toLowerCase().includes(q)))
    .sort((a, b) => order[a.kind] - order[b.kind] || a.name.localeCompare(b.name))
}

const KIND_LABEL: Record<ResourceKind, string> = { skill: "Skill", credential: "Credential", integration: "Integration" }

/** C-TOOLS: resources × agents (lead first), what each agent can use. */
export function CrewTools({ workspaceId, crewSlug, agents, integrations }: {
  workspaceId: string
  crewSlug: string
  agents: AgentSummary[]
  integrations: CrewIntegration[] | null
}) {
  const columns = useMemo(() => orderAgents(agents.filter((a) => !a.expired_at)).slice(0, MATRIX_AGENT_CAP), [agents])
  const [skills, setSkills] = useState<Map<string, Array<{ id: string; name: string }>>>(new Map())
  const [creds, setCreds] = useState<Map<string, Array<{ id: string; name: string }>>>(new Map())
  const [loading, setLoading] = useState(true)
  const [failed, setFailed] = useState(false)
  const [filter, setFilter] = useState<ResourceFilter>("all")
  const [query, setQuery] = useState("")
  const [showAll, setShowAll] = useState(false)
  const ids = columns.map((a) => a.id).join(",")

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setFailed(false)
    const ws = encodeURIComponent(workspaceId)
    Promise.all(columns.map(async (a) => {
      const [s, c] = await Promise.all([
        apiFetch(`/api/v1/agents/${a.id}/skills?workspace_id=${ws}`).then((r) => (r.ok ? r.json() : Promise.reject(r.status))),
        apiFetch(`/api/v1/agents/${a.id}/credential-bindings?workspace_id=${ws}`).then((r) => (r.ok ? r.json() : Promise.reject(r.status))),
      ])
      return {
        id: a.id,
        skills: (Array.isArray(s) ? (s as AgentSkillRow[]) : []).map((x) => ({ id: x.skill_id, name: x.skill.display_name || x.skill.name })),
        creds: (Array.isArray(c?.slots) ? (c.slots as Array<{ credential_id: string; credential_name: string }>) : []).map((x) => ({ id: x.credential_id, name: x.credential_name })),
      }
    }))
      .then((rows) => {
        if (cancelled) return
        setSkills(new Map(rows.map((r) => [r.id, r.skills])))
        setCreds(new Map(rows.map((r) => [r.id, r.creds])))
      })
      .catch(() => { if (!cancelled) setFailed(true) })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- keyed on the agent ids
  }, [ids, workspaceId])

  const rows = useMemo(
    () => buildMatrix({ skills, credentials: creds, integrations: integrations ?? [], filter, query }),
    [skills, creds, integrations, filter, query],
  )
  const visible = showAll ? rows : rows.slice(0, MATRIX_FOLD)

  return (
    <div className="space-y-4" data-testid="crew-tools">
      <div className="flex items-start gap-2.5 rounded-card border border-border bg-card px-4 py-3 text-body">
        <ShieldCheck className="mt-0.5 size-4 shrink-0 text-chip-ok-fg" aria-hidden />
        <span><b className="font-medium text-foreground">Credentials never reach an agent as text.</b> <span className="text-muted-foreground">They are delivered into the crew container when a run needs them.</span></span>
      </div>
      <CrCard
        title="Tools and access"
        concept="skills"
        count={rows.length}
        link={{ label: "Vault", href: entityHref({ kind: "credentials", crewSlug }) }}
      >
        <div className="flex flex-wrap items-center gap-2 border-t border-border px-4 py-2.5">
          <label className="flex h-8 min-w-0 flex-1 items-center gap-1.5 rounded-md border border-border bg-card px-2.5 coarse:h-11">
            <Search className="size-3.5 text-muted-foreground-soft" aria-hidden />
            <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search skills, credentials and integrations" aria-label="Search tools and access" className="min-w-0 flex-1 bg-transparent text-body outline-none" />
          </label>
          <CrSegment<ResourceFilter>
            label="Type"
            value={filter}
            onChange={setFilter}
            options={[{ id: "all", label: "All" }, { id: "skill", label: "Skills" }, { id: "credential", label: "Credentials" }, { id: "integration", label: "Integrations" }]}
          />
        </div>
        {failed ? (
          <CrNote tone="error">Tools and access could not be loaded.</CrNote>
        ) : loading ? (
          <CrNote>Loading…</CrNote>
        ) : rows.length === 0 ? (
          <CrNote>{query || filter !== "all" ? "Nothing matches." : "No agent of this crew holds a skill or a credential yet."}</CrNote>
        ) : (
          <div className="overflow-x-auto border-t border-border">
            <table className="w-full text-body">
              <thead>
                <tr className="text-left text-label text-muted-foreground">
                  <th className="px-4 py-2 font-medium">Resource</th>
                  {columns.map((a) => (
                    <th key={a.id} className="px-2 py-2 text-center font-medium" title={a.name}>
                      <span className="inline-flex flex-col items-center gap-0.5">
                        <AgentAvatar seed={a.avatar_seed || a.name} style={a.avatar_style} avatarUrl={a.avatar_url} className="size-5 rounded-sm" />
                        <span className="max-w-16 truncate">{a.name}</span>
                      </span>
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {visible.map((r) => (
                  <tr key={r.key} className="border-t border-border" data-matrix-row={r.kind}>
                    <td className="px-4 py-2">
                      <span className="flex items-center gap-2">
                        {r.kind === "credential" && <KeyRound className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />}
                        <span className="min-w-0 truncate">{r.name}</span>
                        <span className="shrink-0 text-micro text-muted-foreground">{KIND_LABEL[r.kind]}</span>
                        {r.status === "missing" && <span className="shrink-0 text-micro text-chip-warn-fg">not connected</span>}
                      </span>
                    </td>
                    {r.holders == null ? (
                      <td colSpan={columns.length} className="px-2 py-2 text-center text-label text-muted-foreground">
                        {r.count === 1 ? "1 agent" : `${r.count ?? 0} agents`}
                      </td>
                    ) : columns.map((a) => (
                      <td key={a.id} className="px-2 py-2 text-center">
                        {r.holders?.has(a.id)
                          ? <Check className="mx-auto size-3.5 text-chip-ok-fg" aria-label={`${a.name} has it`} />
                          : <span className="text-muted-foreground-soft" aria-label={`${a.name} does not have it`}>·</span>}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {rows.length > MATRIX_FOLD && (
          <div className="border-t border-border px-4 py-2">
            <Button variant="ghost" size="sm" onClick={() => setShowAll(!showAll)}>{showAll ? "Show fewer" : `Show all ${rows.length}`}</Button>
          </div>
        )}
        {agents.length > MATRIX_AGENT_CAP && (
          <CrNote>Showing the first {MATRIX_AGENT_CAP} agents. Open an agent for its own Tools and access.</CrNote>
        )}
        <div className={cn("flex flex-wrap gap-2 border-t border-border px-4 py-2.5")}>
          <Button asChild variant="ghost" size="sm"><Link href={entityHref({ kind: "skills" })}>Skills</Link></Button>
          <Button asChild variant="ghost" size="sm"><Link href={entityHref({ kind: "integrations", tab: "tools" })}>Integrations</Link></Button>
        </div>
      </CrCard>
    </div>
  )
}
