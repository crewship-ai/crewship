"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { toast } from "sonner"
import { ChevronDown, Files, Plus } from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import { CrewIcon } from "@/components/ui/crew-icon"
import { EditableField } from "@/components/shared/editable-field"
import { CrewActivityFeed } from "@/components/features/crews/crew-activity-feed"
import { CrewIconPickerDialog } from "@/components/features/crews/crew-icon-picker-dialog"
import { ConfigTextareaEditor } from "@/components/features/crews/config-textarea-editor"
import { AVATAR_STYLES, getAgentAvatarUrl } from "@/lib/agent-avatar"
import { cn } from "@/lib/utils"

interface AgentSummary {
  id: string
  name: string
  slug: string
  status: string
  role_title: string | null
  agent_role: string
  avatar_seed?: string | null
  avatar_style?: string | null
  llm_provider?: string | null
  llm_model?: string | null
  _count?: { skills: number; credentials: number }
}

interface CrewRecord {
  id: string
  workspace_id: string
  name: string
  slug: string
  description: string | null
  color: string | null
  icon: string | null
  avatar_style: string | null
  issue_prefix: string | null
  network_mode: string
  allowed_domains: string[] | string | null
  container_memory_mb: number
  container_cpus: number
  container_ttl_hours: number | null
  runtime_image: string | null
  devcontainer_config: string | null
  mise_config: string | null
  escalation_config: string | null
  cached_image: string | null
  created_at: string
  updated_at: string
  _count?: { agents: number; members: number }
}

interface MissionData {
  id: string
  title: string
  status: string
  crew_id: string
  created_at: string
}

interface IssuesSnapshot {
  Backlog: number
  Todo: number
  InProgress: number
  InReview: number
  Done: number
}

interface CrewIntegration {
  id: string
  integration_id: string
  name: string
  type: string
  status: string
}

// Real DiceBear style slugs from lib/agent-avatar; the previous
// hand-typed labels ("robots", "humans") didn't match anything in the
// catalog so saving an avatar style silently fell back to the default.
const STYLE_OPTIONS = (Object.entries(AVATAR_STYLES) as Array<[
  string,
  { label: string; style: unknown },
]>).map(([value, meta]) => ({ value, label: meta.label }))

export interface CrewCanvasProps {
  workspaceId: string
  crewSlug: string
  agentsForCrew: AgentSummary[]
  missions: MissionData[]
  onCrewChanged: () => void
  onSelectAgent: (slug: string) => void
  onOpenFiles: () => void
  onAddAgent: (defaultCrewSlug: string) => void
}

/**
 * Crew canvas — drives the right pane when ?crew=<slug> is selected (and
 * no ?agent=). Renders all crew configuration inline (no drawer).
 */
export function CrewCanvas({
  workspaceId,
  crewSlug,
  agentsForCrew,
  missions,
  onCrewChanged,
  onSelectAgent,
  onOpenFiles,
  onAddAgent,
}: CrewCanvasProps) {
  const [crew, setCrew] = useState<CrewRecord | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [showContainer, setShowContainer] = useState(false)
  const [issues, setIssues] = useState<IssuesSnapshot | null>(null)
  const [integrations, setIntegrations] = useState<CrewIntegration[] | null>(null)
  const [iconPickerOpen, setIconPickerOpen] = useState(false)

  const fetchCrew = useCallback(async (signal?: AbortSignal) => {
    try {
      const listRes = await fetch(`/api/v1/crews?workspace_id=${workspaceId}`, { signal })
      if (!listRes.ok) throw new Error(`crew fetch failed (${listRes.status})`)
      const list: CrewRecord[] = await listRes.json()
      const match = list.find((c) => c.slug === crewSlug)
      if (!match) throw new Error("crew not found")
      const detailRes = await fetch(`/api/v1/crews/${match.id}?workspace_id=${workspaceId}`, { signal })
      if (!detailRes.ok) throw new Error(`crew detail fetch failed (${detailRes.status})`)
      const detail: CrewRecord = await detailRes.json()
      if (!signal?.aborted) {
        setCrew(detail)
        setError(null)
      }
    } catch (err) {
      if ((err as { name?: string })?.name === "AbortError") return
      setError(err instanceof Error ? err.message : "Failed to load crew")
    } finally {
      if (!signal?.aborted) setLoading(false)
    }
  }, [crewSlug, workspaceId])

  useEffect(() => {
    setLoading(true)
    setShowContainer(false)
    const controller = new AbortController()
    void fetchCrew(controller.signal)
    return () => controller.abort()
  }, [crewSlug, fetchCrew])

  // Issues snapshot
  useEffect(() => {
    if (!crew) return
    let cancelled = false
    fetch(`/api/v1/crews/${crew.id}/issues?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((data: { status?: string }[]) => {
        if (cancelled || !Array.isArray(data)) return
        const buckets: IssuesSnapshot = { Backlog: 0, Todo: 0, InProgress: 0, InReview: 0, Done: 0 }
        for (const i of data) {
          const s = i.status?.toLowerCase() ?? ""
          if (s.includes("backlog")) buckets.Backlog++
          else if (s.includes("todo")) buckets.Todo++
          else if (s.includes("progress")) buckets.InProgress++
          else if (s.includes("review")) buckets.InReview++
          else if (s.includes("done") || s.includes("closed")) buckets.Done++
        }
        setIssues(buckets)
      })
      .catch(() => setIssues({ Backlog: 0, Todo: 0, InProgress: 0, InReview: 0, Done: 0 }))
    return () => { cancelled = true }
  }, [crew, workspaceId])

  // Integrations
  useEffect(() => {
    if (!crew) return
    let cancelled = false
    fetch(`/api/v1/crews/${crew.id}/integrations?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((data: CrewIntegration[]) => {
        if (!cancelled && Array.isArray(data)) setIntegrations(data)
      })
      .catch(() => setIntegrations([]))
    return () => { cancelled = true }
  }, [crew, workspaceId])

  const patch = useCallback(async (body: Record<string, unknown>) => {
    if (!crew) return
    const res = await fetch(`/api/v1/crews/${crew.id}?workspace_id=${workspaceId}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    })
    if (!res.ok) {
      const text = await res.text()
      throw new Error(text || `HTTP ${res.status}`)
    }
    const updated: CrewRecord = await res.json()
    setCrew(updated)
    onCrewChanged()
  }, [crew, workspaceId, onCrewChanged])

  const applyAvatarStyle = useCallback(async () => {
    if (!crew) return
    if (!confirm(`Apply avatar style "${crew.avatar_style ?? "robots"}" to all ${agentsForCrew.length} agents in ${crew.name}?`)) return
    try {
      const res = await fetch(`/api/v1/crews/${crew.id}/apply-avatar-style?workspace_id=${workspaceId}`, { method: "POST" })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      toast.success(`Avatar style applied to ${agentsForCrew.length} agent${agentsForCrew.length === 1 ? "" : "s"}`)
      onCrewChanged()
    } catch (err) {
      toast.error(`Apply failed: ${err instanceof Error ? err.message : err}`)
    }
  }, [crew, agentsForCrew.length, onCrewChanged, workspaceId])

  const handleDelete = useCallback(async () => {
    if (!crew) return
    if (!confirm(`Delete crew "${crew.name}"? All ${agentsForCrew.length} agents will be detached. Container will be torn down. Journal kept 30 days.`)) return
    try {
      const res = await fetch(`/api/v1/crews/${crew.id}?workspace_id=${workspaceId}`, { method: "DELETE" })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      toast.success(`Crew "${crew.name}" deleted`)
      onCrewChanged()
    } catch (err) {
      toast.error(`Delete failed: ${err instanceof Error ? err.message : err}`)
    }
  }, [crew, agentsForCrew.length, onCrewChanged, workspaceId])

  const recentMissions = useMemo(() => {
    if (!crew) return []
    return missions.filter((m) => m.crew_id === crew.id).slice(0, 5)
  }, [missions, crew])

  if (loading) {
    return <div className="px-6 md:px-8 lg:px-12 py-6 max-w-[1180px] mx-auto w-full"><Skeleton className="h-[600px] w-full rounded-xl" /></div>
  }
  if (error || !crew) {
    return (
      <div className="px-6 md:px-8 lg:px-12 py-12 max-w-[1180px] mx-auto w-full text-center">
        <p className="text-sm text-red-300 mb-2">Could not load crew</p>
        <p className="text-xs text-muted-foreground">{error}</p>
      </div>
    )
  }

  const containerSummary = `${crew.runtime_image ?? "debian:trixie-slim"} · ${(crew.container_memory_mb / 1024).toFixed(0)} GB · ${crew.container_cpus} CPU · TTL ${crew.container_ttl_hours ?? "—"}h · network: ${crew.network_mode}`

  return (
    <div className="px-6 md:px-8 lg:px-12 py-6 space-y-7 max-w-[1180px] mx-auto w-full">
      {/* Header */}
      <header className="flex items-start gap-5 pb-5 border-b border-white/8">
        <button
          type="button"
          onClick={() => setIconPickerOpen(true)}
          title="Customize icon and color"
          className="shrink-0 group rounded-2xl transition-transform hover:scale-[1.03]"
        >
          <div className="relative">
            <CrewIcon icon={crew.icon || "briefcase"} color={crew.color} size="xl" />
            <span className="absolute inset-0 rounded-2xl ring-2 ring-blue-400/0 group-hover:ring-blue-400/40 transition-all pointer-events-none" />
          </div>
        </button>
        <CrewIconPickerDialog
          open={iconPickerOpen}
          onOpenChange={setIconPickerOpen}
          crewName={crew.name}
          icon={crew.icon}
          color={crew.color}
          onSave={async ({ icon, color }) => {
            await patch({ icon, color })
            toast.success("Icon updated")
          }}
        />
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 mb-1">
            <h1 className="text-2xl font-semibold">
              <EditableField value={crew.name} onSave={(v) => patch({ name: v })} />
            </h1>
            <span className="text-[11px] flex items-center gap-1.5 px-2 py-0.5 rounded-full bg-zinc-800 text-foreground/80 border border-white/10">
              Crew
            </span>
          </div>
          <div className="flex items-center gap-2 text-sm text-muted-foreground flex-wrap">
            <code className="text-foreground/80 text-xs px-1.5 py-0.5 rounded bg-zinc-900 border border-white/8">
              {crew.slug}
            </code>
            {crew.issue_prefix && (
              <>
                <span className="text-muted-foreground/50">·</span>
                <span className="text-xs">prefix <code className="text-foreground/80 px-1 py-0.5 rounded bg-zinc-900 border border-white/8">{crew.issue_prefix}</code></span>
              </>
            )}
            <span className="text-muted-foreground/50">·</span>
            <span className="text-xs">Created {new Date(crew.created_at).toLocaleDateString()}</span>
          </div>
          <div className="text-xs text-muted-foreground mt-1.5 flex items-center gap-3 flex-wrap">
            <span><span className="text-foreground/80">{crew._count?.agents ?? agentsForCrew.length}</span> agents</span>
            <span><span className="text-foreground/80">{crew._count?.members ?? 0}</span> member{crew._count?.members === 1 ? "" : "s"}</span>
            <span><span className="text-foreground/80">{recentMissions.length}</span> missions</span>
            <span className="text-muted-foreground/50">·</span>
            <span className="truncate">container: <span className="text-foreground/80">{containerSummary}</span></span>
          </div>
        </div>
        <div className="flex items-center gap-2 shrink-0">
          <button
            type="button"
            onClick={onOpenFiles}
            className="px-3 py-2 text-sm rounded-lg border border-white/10 hover:bg-white/5 flex items-center gap-2"
            title="Open files in bottom panel"
          >
            <Files className="h-3.5 w-3.5" />
            Files
          </button>
          <button
            type="button"
            onClick={() => onAddAgent(crew.slug)}
            className="px-3.5 py-2 rounded-lg bg-blue-500 hover:bg-blue-400 text-white text-sm font-medium flex items-center gap-1.5"
          >
            <Plus className="h-3.5 w-3.5" />
            Add agent
          </button>
        </div>
      </header>

      {/* Profile */}
      <section className="space-y-3">
        <h2 className="text-lg font-semibold">Profile</h2>
        <div className="rounded-xl border border-white/8 bg-card divide-y divide-white/5">
          <Row label="Name">
            <EditableField value={crew.name} onSave={(v) => patch({ name: v })} />
          </Row>
          <Row label="Slug">
            <EditableField value={crew.slug} onSave={(v) => patch({ slug: v })} mono />
          </Row>
          <Row label="Description" align="start">
            <EditableField value={crew.description} onSave={(v) => patch({ description: v })} />
          </Row>
          <Row label="Issue prefix">
            <EditableField
              value={crew.issue_prefix ?? ""}
              onSave={(v) => patch({ issue_prefix: v || null })}
              mono
              placeholder="ENG"
            />
          </Row>
          <Row label="Avatar style">
            <div className="flex items-center gap-2">
              <EditableField
                value={crew.avatar_style ?? "bottts-neutral"}
                onSave={(v) => patch({ avatar_style: v })}
                options={STYLE_OPTIONS}
                format={(v) => STYLE_OPTIONS.find((o) => o.value === v)?.label ?? v}
              />
              {agentsForCrew.length > 0 && (
                <button
                  type="button"
                  onClick={applyAvatarStyle}
                  className="text-[10px] px-2 py-0.5 rounded border border-white/10 text-foreground/80 hover:bg-white/5"
                >
                  Apply to all {agentsForCrew.length} agent{agentsForCrew.length === 1 ? "" : "s"}
                </button>
              )}
            </div>
          </Row>
        </div>
      </section>

      {/* Members */}
      <section className="space-y-3">
        <div className="flex items-baseline justify-between">
          <h2 className="text-lg font-semibold">
            Members <span className="text-muted-foreground text-sm font-normal ml-1">{agentsForCrew.length}</span>
          </h2>
          <button
            type="button"
            onClick={() => onAddAgent(crew.slug)}
            className="text-xs px-2.5 py-1 rounded border border-white/10 text-foreground/80 hover:bg-white/5 flex items-center gap-1.5"
          >
            <Plus className="h-3 w-3" />
            Add agent
          </button>
        </div>
        {agentsForCrew.length === 0 ? (
          <div className="rounded-xl border border-white/8 bg-card p-6 text-center text-xs text-muted-foreground">
            No agents in this crew. Click <strong className="text-foreground/80">Add agent</strong> to start.
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
                  <img
                    src={getAgentAvatarUrl(a.avatar_seed || a.name, a.avatar_style || crew.avatar_style)}
                    alt=""
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

      {/* Issues snapshot */}
      <section className="space-y-3">
        <div className="flex items-baseline justify-between">
          <h2 className="text-lg font-semibold">
            Issues
            {crew.issue_prefix && (
              <span className="text-muted-foreground text-sm font-normal ml-2">{crew.issue_prefix}</span>
            )}
          </h2>
          <Link href="/orchestration" className="text-xs text-blue-300 hover:underline">
            Open in /orchestration →
          </Link>
        </div>
        <div className="rounded-xl border border-white/8 bg-card grid grid-cols-5 divide-x divide-white/5">
          {(["Backlog", "Todo", "InProgress", "InReview", "Done"] as const).map((bucket) => (
            <div key={bucket} className="px-4 py-3">
              <div className="text-[10px] text-muted-foreground uppercase">{bucket.replace(/([A-Z])/g, " $1").trim()}</div>
              <div className={cn("text-2xl font-semibold mt-1", issues?.[bucket] ? "text-foreground" : "text-muted-foreground")}>
                {issues?.[bucket] ?? "—"}
              </div>
            </div>
          ))}
        </div>
      </section>

      {/* Recent missions */}
      <section className="space-y-3">
        <div className="flex items-baseline justify-between">
          <h2 className="text-lg font-semibold">Recent missions</h2>
          <Link href="/orchestration" className="text-xs text-blue-300 hover:underline">
            Open in /orchestration →
          </Link>
        </div>
        {recentMissions.length === 0 ? (
          <div className="rounded-xl border border-white/8 bg-card p-6 text-center text-xs text-muted-foreground">
            No missions yet for this crew.
          </div>
        ) : (
          <ul className="rounded-xl border border-white/8 bg-card divide-y divide-white/5">
            {recentMissions.map((m) => (
              <li key={m.id} className="px-4 py-2.5 flex items-center justify-between text-sm">
                <span className="flex items-center gap-2 truncate">
                  <span className={cn(
                    "w-1.5 h-1.5 rounded-full shrink-0",
                    m.status === "RUNNING" ? "bg-emerald-400" : m.status === "FAILED" ? "bg-red-500" : "bg-zinc-500",
                  )} />
                  <span className="truncate">{m.title}</span>
                </span>
                <span className="text-[11px] text-muted-foreground shrink-0">
                  {new Date(m.created_at).toLocaleDateString()}
                </span>
              </li>
            ))}
          </ul>
        )}
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

      {/* Container & runtime */}
      <section className="space-y-3">
        <h2 className="text-lg font-semibold">Container &amp; runtime</h2>
        <div className="rounded-xl border border-white/8 bg-card">
          <button
            type="button"
            onClick={() => setShowContainer((v) => !v)}
            className="w-full px-4 py-2.5 flex items-center gap-2 text-sm hover:bg-white/[0.03]"
          >
            <ChevronDown className={cn("h-3 w-3 text-muted-foreground transition-transform", !showContainer && "-rotate-90")} />
            <span className="text-foreground/80 truncate">{containerSummary}</span>
            <span className="ml-auto text-[10px] text-muted-foreground">click to edit</span>
          </button>
          {showContainer && (
            <div className="divide-y divide-white/5 border-t border-white/5">
              <Row label="Image">
                <EditableField
                  value={crew.runtime_image ?? "debian:trixie-slim"}
                  onSave={(v) => patch({ runtime_image: v })}
                  mono
                />
              </Row>
              <Row label="Memory (MB)">
                <EditableField
                  value={String(crew.container_memory_mb)}
                  onSave={(v) => patch({ container_memory_mb: Number(v) })}
                />
              </Row>
              <Row label="CPU">
                <EditableField
                  value={String(crew.container_cpus)}
                  onSave={(v) => patch({ container_cpus: Number(v) })}
                />
              </Row>
              <Row label="TTL (hours)">
                <EditableField
                  value={crew.container_ttl_hours != null ? String(crew.container_ttl_hours) : ""}
                  onSave={(v) => patch({ container_ttl_hours: v === "" ? null : Number(v) })}
                  placeholder="never"
                />
              </Row>
              <Row label="Network">
                <EditableField
                  value={crew.network_mode}
                  onSave={(v) => patch({ network_mode: v })}
                  options={[
                    { value: "free", label: "Free (full internet)" },
                    { value: "restricted", label: "Restricted (allowlist only)" },
                  ]}
                  format={(v) => (v === "free" ? "Free (full internet)" : "Restricted (allowlist only)")}
                />
              </Row>
              {crew.network_mode === "restricted" && (
                <Row label="Allowed domains" align="start">
                  <EditableField
                    value={Array.isArray(crew.allowed_domains)
                      ? crew.allowed_domains.join(", ")
                      : (crew.allowed_domains ?? "")}
                    onSave={(v) => {
                      const list = v.split(",").map((s) => s.trim()).filter(Boolean)
                      return patch({ allowed_domains: list.length > 0 ? list : null })
                    }}
                    placeholder="api.openai.com, api.anthropic.com, …"
                  />
                </Row>
              )}
              <Row label="MCP servers" align="center">
                <span className="text-sm text-muted-foreground">
                  CLI: <code className="text-foreground/80">crewship mcp {crew.slug}</code>
                </span>
              </Row>
            </div>
          )}
        </div>
      </section>

      {/* Devcontainer + mise — restored from the deleted CrewRuntimeConfig.
          Both fields PATCHable per crews.go::updateCrewRequest. The
          old curated runtime catalog (Ubuntu/debian/mcr.microsoft.com
          presets, language pickers) was 1067 lines and is deferred to
          a follow-up; here we expose the raw devcontainer.json + mise.toml
          editors which is what power users wanted anyway. */}
      <section className="space-y-3">
        <div className="flex items-baseline justify-between">
          <h2 className="text-lg font-semibold">Devcontainer &amp; mise</h2>
          {crew.cached_image && (
            <span className="text-[10px] text-muted-foreground">
              built: <code className="text-foreground/70">{crew.cached_image}</code>
            </span>
          )}
        </div>
        <ConfigTextareaEditor
          format="json"
          filename="devcontainer.json"
          value={crew.devcontainer_config}
          placeholder='{\n  "image": "mcr.microsoft.com/devcontainers/python:1-3.12",\n  "features": { … }\n}'
          hint={<>Standard <code className="text-foreground/70">devcontainer.json</code>. The crew container will be rebuilt from this on the next provision.</>}
          onSave={(v) => patch({ devcontainer_config: v })}
        />
        <ConfigTextareaEditor
          format="toml"
          filename="mise.toml"
          value={crew.mise_config}
          placeholder='[tools]\nnode = "20"\npython = "3.12"\n'
          hint={<>Optional <code className="text-foreground/70">mise.toml</code> for language/runtime versions inside the container.</>}
          onSave={(v) => patch({ mise_config: v })}
        />
        <div className="text-[11px] text-muted-foreground">
          To rebuild the container after a config change:{" "}
          <code className="text-foreground/80">crewship crew provision {crew.slug}</code>
        </div>
      </section>

      {/* Escalation routing — JSON config for who handles what when an
          agent escalates. Backend supports it via PATCH; was missing
          from canvas. */}
      <section className="space-y-3">
        <h2 className="text-lg font-semibold">Escalation routing</h2>
        <ConfigTextareaEditor
          format="json"
          filename="escalation.json"
          value={crew.escalation_config}
          placeholder='{\n  "max_attempts": 3,\n  "default_handler": "lead",\n  "rules": [\n    { "type": "auth_error", "handler": "human" }\n  ]\n}'
          hint={<>JSON describing the escalation chain. Empty = inherit workspace defaults.</>}
          onSave={(v) => patch({ escalation_config: v })}
        />
      </section>

      {/* Activity */}
      <section className="space-y-3">
        <h2 className="text-lg font-semibold">Activity</h2>
        <div className="rounded-xl border border-white/8 bg-card max-h-[400px] overflow-hidden">
          <CrewActivityFeed
            workspaceId={workspaceId}
            crewId={crew.id}
          />
        </div>
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
            onClick={handleDelete}
            className="text-xs px-3 py-1.5 rounded bg-red-500/20 text-red-300 border border-red-500/40 hover:bg-red-500/30"
          >
            Delete {crew.name}
          </button>
        </div>
      </section>
    </div>
  )
}

function Row({
  label,
  align = "center",
  children,
}: {
  label: string
  align?: "center" | "start"
  children: React.ReactNode
}) {
  return (
    <div className={cn(
      "grid grid-cols-[180px_1fr] gap-4 px-4 py-2.5",
      align === "center" ? "items-center" : "items-start",
    )}>
      <span className="text-xs text-muted-foreground">{label}</span>
      <div className="flex items-center gap-2 min-w-0">{children}</div>
    </div>
  )
}
