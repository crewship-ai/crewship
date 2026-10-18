"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { ExternalLink, Lock, Plus, Search } from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { ConceptIcon } from "@/components/ui/concept-icon"
import { StatusPill } from "@/components/ui/status-pill"
import { apiFetch } from "@/lib/api-fetch"
import { entityHref } from "@/lib/entity-links"
import { toast } from "@/lib/toast"

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

type Held = { id: string; name: string; assignmentId?: string; enabled?: boolean; status?: string }
type HeldMap = Map<string, Held[]>

const CARD: Record<Exclude<ResourceKind, "integration">, { title: string; concept: string; add: string; sub: string }> = {
  skill: { title: "Skills", concept: "skills", add: "Add skill", sub: "Skill" },
  credential: { title: "Credentials", concept: "credentials", add: "Share a credential", sub: "Credential" },
}

/** C-TOOLS: one card per kind, resources × agents (lead first), what each agent can use. */
export function CrewTools({ workspaceId, crewSlug, agents, integrations }: {
  workspaceId: string
  crewSlug: string
  agents: AgentSummary[]
  integrations: CrewIntegration[] | null
}) {
  const columns = useMemo(() => orderAgents(agents.filter((a) => !a.expired_at)).slice(0, MATRIX_AGENT_CAP), [agents])
  const [skills, setSkills] = useState<HeldMap>(new Map())
  const [creds, setCreds] = useState<HeldMap>(new Map())
  const [loading, setLoading] = useState(true)
  const [failed, setFailed] = useState(false)
  const [filter, setFilter] = useState<ResourceFilter>("all")
  const [query, setQuery] = useState("")
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState<string | null>(null)
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
        skills: (Array.isArray(s) ? (s as AgentSkillRow[]) : []).map((x): Held => ({ id: x.skill_id, name: x.skill.display_name || x.skill.name, assignmentId: x.id, enabled: x.enabled })),
        creds: (Array.isArray(c?.slots) ? (c.slots as Array<{ credential_id: string; credential_name: string; credential_status?: string }>) : []).map((x): Held => ({ id: x.credential_id, name: x.credential_name, status: x.credential_status })),
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

  // One cell: give the agent the skill, or take it away. Credentials are
  // shared through the Vault, so their cells only show who holds them.
  const toggleSkill = useCallback(async (agent: AgentSummary, skill: { id: string; name: string }) => {
    const held = skills.get(agent.id)?.find((h) => h.id === skill.id)
    const key = `${agent.id}:${skill.id}`
    setBusy(key)
    try {
      const res = held?.assignmentId
        ? await apiFetch(`/api/v1/agents/${agent.id}/skills/${held.assignmentId}`, { method: "DELETE" })
        : await apiFetch(`/api/v1/agents/${agent.id}/skills`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ skill_id: skill.id }) })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const created = held?.assignmentId ? null : ((await res.json().catch(() => null)) as { id?: string } | null)
      setSkills((prev) => {
        const next = new Map(prev)
        const list = (next.get(agent.id) ?? []).filter((h) => h.id !== skill.id)
        if (!held) list.push({ id: skill.id, name: skill.name, assignmentId: created?.id, enabled: true })
        next.set(agent.id, list)
        return next
      })
    } catch (err) {
      toast.error(`Could not change ${agent.name}'s skills`, { description: err instanceof Error ? err.message : String(err) })
    } finally {
      setBusy(null)
    }
  }, [skills])

  const all = useMemo(
    () => buildMatrix({ skills: heldIds(skills), credentials: heldIds(creds), integrations: integrations ?? [], filter: "all", query }),
    [skills, creds, integrations, query],
  )
  const count = (k: ResourceKind) => buildMatrix({ skills: heldIds(skills), credentials: heldIds(creds), integrations: integrations ?? [], filter: k }).length
  const kinds: ResourceKind[] = filter === "all" ? ["skill", "credential", "integration"] : [filter]

  return (
    <div className="space-y-4" data-testid="crew-tools">
      <div className="flex flex-wrap items-center gap-2">
        <label className="flex h-8 min-w-0 flex-1 items-center gap-1.5 rounded-md border border-border bg-card px-2.5 coarse:h-[44px]">
          <Search className="size-3.5 text-muted-foreground-soft" aria-hidden />
          <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder={`Search ${count("skill") + count("credential") + count("integration")} skills, credentials and integrations…`} aria-label="Search tools and access" className="min-w-0 flex-1 bg-transparent text-body outline-none" />
        </label>
        <CrSegment<ResourceFilter>
          label="Type"
          value={filter}
          onChange={setFilter}
          options={[
            { id: "all", label: "All" },
            { id: "skill", label: `Skills ${count("skill")}` },
            { id: "credential", label: `Credentials ${count("credential")}` },
            { id: "integration", label: `Integrations ${count("integration")}` },
          ]}
        />
      </div>

      {failed ? (
        <CrNote tone="error">Tools and access could not be loaded.</CrNote>
      ) : loading ? (
        <CrNote>Loading…</CrNote>
      ) : (
        kinds.map((kind) => {
          const rows = all.filter((r) => r.kind === kind)
          const open = expanded.has(kind)
          const visible = open ? rows : rows.slice(0, MATRIX_FOLD)
          if (kind === "integration") return <IntegrationsCard key={kind} rows={rows} />
          const meta = CARD[kind]
          const held = kind === "skill" ? skills : creds
          return (
            <CrCard
              key={kind}
              title={meta.title}
              concept={meta.concept}
              count={rows.length}
              action={<Button asChild variant="outline" size="sm"><Link href={kind === "skill" ? entityHref({ kind: "skills" }) : entityHref({ kind: "credentials", crewSlug })}><Plus className="size-3.5" aria-hidden /> {meta.add}</Link></Button>}
            >
              {rows.length === 0 ? (
                <CrNote>{query ? "Nothing matches." : "None yet."}</CrNote>
              ) : (
                <div className="overflow-x-auto border-t border-border">
                  <table className="w-full text-body">
                    <thead>
                      <tr className="text-left text-label text-muted-foreground">
                        <th className="px-4 py-2 font-medium">{meta.sub}</th>
                        <th className="px-3 py-2 font-medium">State</th>
                        {columns.map((a) => (
                          <th key={a.id} className="px-2 py-2 text-center font-medium" title={`${a.name}${a.agent_role === "LEAD" ? " · lead" : ""}`}>
                            <span className="inline-flex flex-col items-center gap-0.5">
                              <AgentAvatar seed={a.avatar_seed || a.name} style={a.avatar_style} avatarUrl={a.avatar_url} className="size-5 rounded-sm" />
                              <span className="max-w-16 truncate">{a.name}</span>
                            </span>
                          </th>
                        ))}
                        <th className="w-10" aria-label="Options" />
                      </tr>
                    </thead>
                    <tbody>
                      {visible.map((r) => {
                        const rid = r.key.slice(r.key.indexOf(":") + 1)
                        const any = columns.map((a) => held.get(a.id)?.find((h) => h.id === rid)).find(Boolean)
                        return (
                          <tr key={r.key} className="border-t border-border" data-matrix-row={r.kind}>
                            <td className="px-4 py-2">
                              <span className="flex items-center gap-2.5">
                                <ConceptIcon concept={meta.concept} variant="chip" size="sm" />
                                <span className="min-w-0">
                                  <b className="block truncate font-medium text-foreground">{r.name}</b>
                                  <span className="block text-label text-muted-foreground">{meta.sub}</span>
                                </span>
                              </span>
                            </td>
                            <td className="px-3 py-2">{stateOf(kind, any)}</td>
                            {columns.map((a) => {
                              const h = held.get(a.id)?.find((x) => x.id === rid)
                              const label = `${a.name} ${h ? "uses" : "does not use"} ${r.name}`
                              return (
                                <td key={a.id} className="px-2 py-2 text-center">
                                  <Checkbox
                                    checked={Boolean(h)}
                                    aria-label={label}
                                    disabled={kind === "credential" || busy === `${a.id}:${rid}`}
                                    title={kind === "credential" ? "Share or revoke in the Vault" : undefined}
                                    onCheckedChange={kind === "skill" ? () => void toggleSkill(a, { id: rid, name: r.name }) : undefined}
                                    className="coarse:size-[44px]"
                                  />
                                </td>
                              )
                            })}
                            <td className="px-2 py-2 text-right">
                              <Button asChild variant="ghost" size="icon-sm" aria-label={`Open ${r.name}`}>
                                <Link href={kind === "skill" ? entityHref({ kind: "skills" }) : entityHref({ kind: "credential", id: rid })}><ExternalLink className="size-3.5" aria-hidden /></Link>
                              </Button>
                            </td>
                          </tr>
                        )
                      })}
                    </tbody>
                  </table>
                </div>
              )}
              {rows.length > MATRIX_FOLD && (
                <div className="border-t border-border px-4 py-2">
                  <Button variant="ghost" size="sm" onClick={() => setExpanded((prev) => { const n = new Set(prev); if (n.has(kind)) n.delete(kind); else n.add(kind); return n })}>
                    {open ? "Show fewer" : `Show all ${rows.length}`}
                  </Button>
                </div>
              )}
            </CrCard>
          )
        })
      )}

      {agents.length > MATRIX_AGENT_CAP && (
        <CrNote>Showing the first {MATRIX_AGENT_CAP} agents. Open an agent for its own Tools and access.</CrNote>
      )}
      <p className="text-label text-muted-foreground">
        Each row is one thing the crew can use; each column is an agent, the lead first. Workspaces have hundreds of credentials and a crew a handful of agents, so the list grows down, not sideways. Tick a skill to give it to an agent.
      </p>
      <div className="flex items-start gap-2.5 rounded-card border border-border bg-card px-4 py-3 text-body">
        <Lock className="mt-0.5 size-4 shrink-0 text-chip-info-fg" aria-hidden />
        <span><b className="font-medium text-foreground">Credentials never reach an agent as text</b> <span className="text-muted-foreground">A proxy in the container delivers them; Keeper decides each use and asks a person when a rule says so. All agents of a crew share one container, so keep secrets that must stay apart in different crews.</span></span>
      </div>
    </div>
  )
}

function heldIds(m: HeldMap): ReadonlyMap<string, Array<{ id: string; name: string }>> {
  return m
}

function stateOf(kind: "skill" | "credential", h: Held | undefined) {
  if (kind === "skill") {
    if (!h) return <span className="text-muted-foreground">—</span>
    return h.enabled === false ? <StatusPill className="font-sans" tone="muted" label="Off" /> : <StatusPill className="font-sans" tone="success" label="Ready" />
  }
  if (!h?.status) return <span className="text-muted-foreground">—</span>
  return h.status.toUpperCase() === "ACTIVE" ? <StatusPill className="font-sans" tone="success" label="Ready" /> : <StatusPill className="font-sans" tone="warn" label="Needs attention" />
}

/** Integrations are bound to the crew, not to one agent: a list, not a matrix. */
function IntegrationsCard({ rows }: { rows: MatrixRow[] }) {
  return (
    <CrCard
      title="Integrations"
      concept="integrations"
      count={rows.length}
      action={<Button asChild variant="outline" size="sm"><Link href={entityHref({ kind: "integrations", tab: "tools" })}><Plus className="size-3.5" aria-hidden /> Connect</Link></Button>}
    >
      {rows.length === 0 ? (
        <CrNote>None yet.</CrNote>
      ) : (
        <ul className="border-t border-border">
          {rows.map((r) => (
            <li key={r.key} data-matrix-row="integration" className="flex items-center gap-2.5 border-t border-border px-4 py-2 first:border-t-0">
              <ConceptIcon concept="integrations" variant="chip" size="sm" />
              <span className="min-w-0 flex-1"><b className="block truncate font-medium text-foreground">{r.name}</b><span className="block text-label text-muted-foreground">{r.count === 1 ? "1 agent" : `${r.count ?? 0} agents`}</span></span>
              {r.status === "missing" ? <StatusPill className="font-sans" tone="warn" label="Not connected" /> : <StatusPill className="font-sans" tone="success" label="Ready" />}
            </li>
          ))}
        </ul>
      )}
    </CrCard>
  )
}
