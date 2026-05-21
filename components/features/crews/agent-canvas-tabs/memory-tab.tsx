"use client"

// PR-E F6 — Memory tab on the agent canvas.
//
// Four sub-tabs (PRD §6 F6):
//   AGENT.md — per-agent canonical memory, agent-written. Operator
//              sees the latest content + version history; edits are
//              made by the agent at run time (not via this UI).
//   CREW.md  — per-crew canonical memory, shared across all agents
//              in the crew. Same surface as AGENT.md; "shared with
//              all crew members" badge.
//   PERSONA  — per-agent (layered over crew default) tone/voice
//              profile. Operator-editable via PUT /agents/{id}/persona.
//   Peers    — per-(agent, user) cards. Grid + delete (GDPR SAR).
//
// AGENT.md and CREW.md are read-only in this Phase-1 surface because
// only the agent runtime has the authority to write them — operator
// edits would break the audit chain. CodeRabbit reviewers: this is
// deliberate per the orchestrator's write-gate contract in
// internal/memory/writer_caps.go. Restore-from-history goes through
// the admin restore endpoint (POST /memory/versions/{sha}/restore),
// still gated on OWNER/ADMIN, surfaced separately under the admin
// memory page.
//
// Implementation note: the three "memory tier" panels (AGENT, CREW,
// PERSONA) share the same outer shape — latest content + version
// history list + char counter. We extract MemoryTierEditor as the
// shared shell and parameterize on tier so the three panels stay in
// lockstep when the editor evolves (CodeMirror integration, diff
// view, etc.). PERSONA additionally exposes an Edit button + writer
// because it's the only operator-writeable tier.

import { useCallback, useEffect, useMemo, useState } from "react"

// Char caps — must match server-side enforcement.
//   AGENT.md / CREW.md: 4000 B (PR-A F1)
//   PERSONA.md:        1500 B (PR-E F6)
//   Peer cards:        1500 B per file (PR-E F6)
const AGENT_CAP_BYTES = 4000
const CREW_CAP_BYTES = 4000
const PERSONA_CAP_BYTES = 1500
const PEER_CAP_BYTES = 1500

type SubTab = "agent" | "crew" | "persona" | "peers"

const SUBTAB_LABEL: Record<SubTab, string> = {
  agent: "AGENT.md",
  crew: "CREW.md",
  persona: "PERSONA",
  peers: "Peers",
}

interface PersonaResponse {
  agent_id?: string
  crew_id?: string
  layer: string
  from_default: boolean
  content: string
  bytes: number
  cap_bytes: number
}

interface VersionEntry {
  id: string
  sha256: string
  bytes: number
  written_at: string
  written_by: string
  parent_sha?: string
}

interface PeerEntry {
  id: string
  user_id: string
  user_slug: string
  bytes: number
  created_at: string
  updated_at: string
  content?: string
}

interface HistoryEntry {
  id: string
  sha256: string
  bytes: number
  written_at: string
  written_by: string
}

export interface MemoryTabProps {
  agentId: string
  agentSlug: string
  crewId?: string
  workspaceId: string
}

export function MemoryTab({ agentId, agentSlug, crewId, workspaceId }: MemoryTabProps) {
  const [sub, setSub] = useState<SubTab>("agent")
  const tabs: SubTab[] = useMemo(() => {
    // CREW.md is only meaningful when the agent belongs to a crew.
    // Solo agents (no crew_id) hide the CREW tab outright rather than
    // showing an empty pane that confuses the operator.
    return crewId
      ? ["agent", "crew", "persona", "peers"]
      : ["agent", "persona", "peers"]
  }, [crewId])

  return (
    <div className="space-y-6">
      {/* Linear-style underline tab bar (PRD §9 UI guidelines). */}
      <div className="flex gap-2 border-b border-white/10">
        {tabs.map((s) => (
          <button
            key={s}
            data-testid={`memory-subtab-${s}`}
            onClick={() => setSub(s)}
            className={`px-3 py-2 text-sm border-b-2 -mb-px ${
              sub === s
                ? "border-emerald-500 text-emerald-300"
                : "border-transparent text-muted-foreground hover:text-foreground"
            }`}
          >
            {SUBTAB_LABEL[s]}
          </button>
        ))}
      </div>

      {sub === "agent" && (
        <AgentMemoryPanel
          agentId={agentId}
          agentSlug={agentSlug}
          workspaceId={workspaceId}
        />
      )}
      {sub === "crew" && crewId && (
        <CrewMemoryPanel crewId={crewId} workspaceId={workspaceId} />
      )}
      {sub === "persona" && (
        <PersonaPanel
          agentId={agentId}
          crewId={crewId}
          workspaceId={workspaceId}
        />
      )}
      {sub === "peers" && (
        <PeersPanel agentId={agentId} workspaceId={workspaceId} />
      )}
    </div>
  )
}

// MemoryTierEditor is the shared shell for the three tier panels.
// Renders a header (with optional shared-with-crew badge), the latest
// content pane (read-only by default; editor when editing), the per-
// tier char counter, and the version-history list.
//
// Edit semantics:
//   - readOnly=true  → no Edit button; content stays in <pre>.
//   - readOnly=false → Edit toggles to <textarea>; onSave is called
//                      with the new content. Char cap enforced
//                      client-side; server-side cap is the source of
//                      truth and would 4xx an over-cap write anyway.
//
// Version history is always rendered; the timeline answers "when did
// this change last?" for both writeable + read-only tiers.
function MemoryTierEditor({
  title,
  content,
  bytes,
  capBytes,
  versions,
  readOnly,
  badge,
  hint,
  saving,
  err,
  onSave,
  onReset,
  resetDisabled,
  resetLabel,
}: {
  title: string
  content: string
  bytes: number
  capBytes: number
  versions: VersionEntry[]
  readOnly: boolean
  badge?: string
  hint?: string
  saving?: boolean
  err?: string | null
  onSave?: (next: string) => Promise<void> | void
  onReset?: () => Promise<void> | void
  resetDisabled?: boolean
  resetLabel?: string
}) {
  const [editing, setEditing] = useState<string | null>(null)
  const editingBytes = useMemo(
    () => (editing === null ? 0 : new TextEncoder().encode(editing).length),
    [editing],
  )
  const over = editingBytes > capBytes

  return (
    <div className="space-y-6">
      {err && (
        <div className="rounded border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-300">
          {err}
        </div>
      )}

      <section className="space-y-3">
        <header className="flex items-center justify-between gap-3">
          <div className="flex items-center gap-2">
            <h3 className="text-sm font-semibold">{title}</h3>
            {badge && (
              <span className="rounded bg-blue-500/15 px-2 py-0.5 text-[10px] uppercase tracking-wide text-blue-300">
                {badge}
              </span>
            )}
          </div>
          <span className="text-xs text-muted-foreground">
            {editing !== null ? editingBytes : bytes}/{capBytes} B
          </span>
        </header>

        {editing === null ? (
          <pre className="rounded border border-white/10 bg-zinc-900/60 p-3 text-sm whitespace-pre-wrap min-h-[8rem]">
            {content || "(empty)"}
          </pre>
        ) : (
          <div className="space-y-1">
            <textarea
              value={editing}
              onChange={(e) => setEditing(e.target.value)}
              className="w-full min-h-[10rem] rounded border border-white/10 bg-zinc-900/60 p-3 text-sm font-mono focus:outline-none focus:ring-2 focus:ring-emerald-500/40"
            />
            <div className={`text-xs ${over ? "text-red-400" : "text-muted-foreground"}`}>
              {editingBytes}/{capBytes} B {over && "— over cap"}
            </div>
          </div>
        )}

        {hint && (
          <p className="text-xs text-muted-foreground">{hint}</p>
        )}

        {!readOnly && (
          <div className="flex gap-2">
            {editing === null ? (
              <>
                <button
                  onClick={() => setEditing(content ?? "")}
                  className="rounded bg-emerald-500/20 px-3 py-1.5 text-sm text-emerald-300 hover:bg-emerald-500/30"
                >
                  Edit
                </button>
                {onReset && (
                  <button
                    onClick={onReset}
                    disabled={saving || resetDisabled}
                    className="rounded border border-white/10 px-3 py-1.5 text-sm hover:bg-white/5 disabled:opacity-50"
                  >
                    {resetLabel ?? "Reset"}
                  </button>
                )}
              </>
            ) : (
              <>
                <button
                  onClick={async () => {
                    if (!onSave || editing === null) return
                    await onSave(editing)
                    setEditing(null)
                  }}
                  disabled={saving || editing.length === 0 || over}
                  className="rounded bg-emerald-500 px-3 py-1.5 text-sm text-zinc-950 hover:bg-emerald-400 disabled:opacity-50"
                >
                  {saving ? "Saving..." : "Save"}
                </button>
                <button
                  onClick={() => setEditing(null)}
                  className="rounded border border-white/10 px-3 py-1.5 text-sm hover:bg-white/5"
                >
                  Cancel
                </button>
              </>
            )}
          </div>
        )}
      </section>

      <section className="space-y-2">
        <h3 className="text-sm font-semibold">Version history</h3>
        {versions.length === 0 ? (
          <p className="text-sm text-muted-foreground">(no history)</p>
        ) : (
          <ul className="space-y-1 text-sm font-mono">
            {versions.map((h) => (
              <li key={h.id} className="flex gap-3 text-xs">
                <span className="text-muted-foreground w-44">{h.written_at}</span>
                <span className="text-muted-foreground">{h.sha256.slice(0, 12)}</span>
                <span className="w-16 text-right">{h.bytes} B</span>
                <span className="text-muted-foreground">by {h.written_by}</span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}

// AgentMemoryPanel reads the canonical path `agent:<slug>/AGENT.md`
// from the memory_versions endpoint and shows the latest content
// read-only. The agent runtime is the only authorised writer.
function AgentMemoryPanel({
  agentId: _agentId,
  agentSlug,
  workspaceId,
}: {
  agentId: string
  agentSlug: string
  workspaceId: string
}) {
  const path = `agent:${agentSlug}/AGENT.md`
  const { content, bytes, versions, err } = useMemoryTierLatest(path, workspaceId)
  return (
    <MemoryTierEditor
      title="AGENT.md — per-agent canonical memory"
      content={content}
      bytes={bytes}
      capBytes={AGENT_CAP_BYTES}
      versions={versions}
      readOnly
      hint="Agent-managed file. The agent writes to this during runs; operators can audit history here but cannot edit directly. Use the admin restore endpoint to roll back a row."
      err={err}
    />
  )
}

// CrewMemoryPanel reads `crew:<crewID>/CREW.md` and renders the same
// shell with a "shared with crew" badge. We key by crewID (not slug)
// to match the canonical path layout used by the writer caps.
function CrewMemoryPanel({
  crewId,
  workspaceId,
}: {
  crewId: string
  workspaceId: string
}) {
  const path = `crew:${crewId}/CREW.md`
  const { content, bytes, versions, err } = useMemoryTierLatest(path, workspaceId)
  return (
    <MemoryTierEditor
      title="CREW.md — shared crew memory"
      content={content}
      bytes={bytes}
      capBytes={CREW_CAP_BYTES}
      versions={versions}
      readOnly
      badge="shared with all crew members"
      hint="All agents in this crew read this file at session start. Agent-managed; operator edits would break the orchestrator audit chain."
      err={err}
    />
  )
}

// useMemoryTierLatest pulls the version history for the given path
// and returns the latest entry's content (fetched lazily via the
// /content endpoint). Renders an empty body when no history exists
// rather than erroring out — that's the expected state on a fresh
// agent that hasn't been run yet.
function useMemoryTierLatest(path: string, workspaceId: string) {
  const [versions, setVersions] = useState<VersionEntry[]>([])
  const [content, setContent] = useState("")
  const [bytes, setBytes] = useState(0)
  const [err, setErr] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    const headers = { "X-Workspace-ID": workspaceId }
    async function load() {
      setErr(null)
      try {
        const r = await fetch(
          `/api/v1/memory/versions?path=${encodeURIComponent(path)}&limit=20`,
          { headers },
        )
        if (!r.ok) {
          if (r.status === 404) {
            // Brand-new agent with no memory writes yet — surface as
            // an empty state, not as an error.
            if (!cancelled) {
              setVersions([])
              setContent("")
              setBytes(0)
            }
            return
          }
          throw new Error(`list versions failed: ${r.status}`)
        }
        const data = (await r.json()) as { entries?: VersionEntry[] }
        const entries = data.entries ?? []
        if (cancelled) return
        setVersions(entries)
        if (entries.length === 0) {
          setContent("")
          setBytes(0)
          return
        }
        const latest = entries[0]
        // Show endpoint returns the raw blob bytes for the latest sha.
        const cr = await fetch(
          `/api/v1/memory/versions/${encodeURIComponent(latest.sha256)}?path=${encodeURIComponent(path)}`,
          { headers },
        )
        if (!cr.ok) throw new Error(`load latest content failed: ${cr.status}`)
        const text = await cr.text()
        if (cancelled) return
        setContent(text)
        setBytes(latest.bytes)
      } catch (e) {
        if (!cancelled) setErr((e as Error).message)
      }
    }
    load()
    return () => {
      cancelled = true
    }
  }, [path, workspaceId])

  return { content, bytes, versions, err }
}

// PersonaPanel manages both the agent override editor and a read-
// only view of the crew default layer so the operator can see what
// gets inherited when the agent layer is empty. PERSONA is the only
// tier the operator can write directly (separate cap: 1500 B).
function PersonaPanel({
  agentId,
  crewId,
  workspaceId,
}: {
  agentId: string
  crewId?: string
  workspaceId: string
}) {
  const [agentPersona, setAgentPersona] = useState<PersonaResponse | null>(null)
  const [crewPersona, setCrewPersona] = useState<PersonaResponse | null>(null)
  const [history, setHistory] = useState<HistoryEntry[]>([])
  const [saving, setSaving] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const load = useCallback(async () => {
    setErr(null)
    const headers = { "X-Workspace-ID": workspaceId }
    try {
      const [pr, hist] = await Promise.all([
        fetch(`/api/v1/agents/${encodeURIComponent(agentId)}/persona`, { headers }),
        fetch(`/api/v1/agents/${encodeURIComponent(agentId)}/persona/history?limit=20`, { headers }),
      ])
      if (pr.ok) setAgentPersona(await pr.json())
      if (hist.ok) {
        const h = await hist.json()
        setHistory(h.entries || [])
      }
      if (crewId) {
        const cr = await fetch(`/api/v1/crews/${encodeURIComponent(crewId)}/persona`, { headers })
        if (cr.ok) setCrewPersona(await cr.json())
      }
    } catch (e) {
      setErr((e as Error).message)
    }
  }, [agentId, crewId, workspaceId])

  useEffect(() => {
    load()
  }, [load])

  const save = useCallback(
    async (next: string) => {
      setSaving(true)
      setErr(null)
      try {
        const r = await fetch(`/api/v1/agents/${encodeURIComponent(agentId)}/persona`, {
          method: "PUT",
          headers: { "Content-Type": "application/json", "X-Workspace-ID": workspaceId },
          body: JSON.stringify({ content: next }),
        })
        if (!r.ok) {
          setErr(`save failed: ${r.status} ${await r.text()}`)
        } else {
          await load()
        }
      } finally {
        setSaving(false)
      }
    },
    [agentId, load, workspaceId],
  )

  const reset = useCallback(async () => {
    if (!confirm("Reset agent PERSONA.md? The crew default + synthesized fallback will be used.")) return
    setSaving(true)
    setErr(null)
    try {
      const r = await fetch(`/api/v1/agents/${encodeURIComponent(agentId)}/persona`, {
        method: "DELETE",
        headers: { "X-Workspace-ID": workspaceId },
      })
      if (!r.ok) {
        setErr(`reset failed: ${r.status} ${await r.text()}`)
        return
      }
      await load()
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setSaving(false)
    }
  }, [agentId, load, workspaceId])

  const personaContent = agentPersona?.from_default ? "" : agentPersona?.content ?? ""
  const personaBytes = agentPersona?.bytes ?? 0

  return (
    <div className="space-y-6">
      <MemoryTierEditor
        title="Agent override (per-agent PERSONA.md)"
        content={personaContent}
        bytes={personaBytes}
        capBytes={PERSONA_CAP_BYTES}
        versions={history}
        readOnly={false}
        badge={agentPersona?.from_default ? "synthesized default" : undefined}
        hint={
          agentPersona?.from_default
            ? "No persona configured for this agent yet — content above is synthesized from the crew default + agent metadata. Click Edit to create an explicit override."
            : undefined
        }
        saving={saving}
        err={err}
        onSave={save}
        onReset={reset}
        resetDisabled={!agentPersona || agentPersona.from_default}
        resetLabel="Reset (drop agent layer)"
      />

      {crewId && crewPersona && (
        <section className="space-y-2 opacity-80">
          <header className="flex items-center justify-between">
            <h3 className="text-sm font-semibold">Crew default (read-only here)</h3>
            <span className="text-xs text-muted-foreground">
              {crewPersona.bytes}/{PERSONA_CAP_BYTES} B
            </span>
          </header>
          <pre className="rounded border border-white/10 bg-zinc-900/40 p-3 text-sm whitespace-pre-wrap min-h-[4rem]">
            {crewPersona.content || "(no crew persona configured)"}
          </pre>
          <p className="text-xs text-muted-foreground">
            Edit via the crew page or <code>crewship persona crew &lt;slug&gt; edit</code>.
          </p>
        </section>
      )}
    </div>
  )
}

// PeersPanel renders the per-(agent, user) card grid. Clicking a row
// loads the card content into a detail panel inline. Delete fires
// the GDPR SAR endpoint.
function PeersPanel({ agentId, workspaceId }: { agentId: string; workspaceId: string }) {
  const [peers, setPeers] = useState<PeerEntry[]>([])
  const [active, setActive] = useState<PeerEntry | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  const load = useCallback(async () => {
    setErr(null)
    setLoading(true)
    try {
      const r = await fetch(`/api/v1/agents/${encodeURIComponent(agentId)}/peers`, {
        headers: { "X-Workspace-ID": workspaceId },
      })
      if (!r.ok) throw new Error(`list peers failed: ${r.status}`)
      const data = await r.json()
      setPeers(data.peers || [])
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setLoading(false)
    }
  }, [agentId, workspaceId])

  useEffect(() => {
    load()
  }, [load])

  const loadDetail = useCallback(
    async (userID: string) => {
      try {
        const r = await fetch(
          `/api/v1/agents/${encodeURIComponent(agentId)}/peers/${encodeURIComponent(userID)}`,
          { headers: { "X-Workspace-ID": workspaceId } },
        )
        if (!r.ok) throw new Error(`load peer failed: ${r.status}`)
        const data = await r.json()
        setActive(data as PeerEntry)
      } catch (e) {
        setErr((e as Error).message)
      }
    },
    [agentId, workspaceId],
  )

  const deleteCard = useCallback(
    async (userID: string) => {
      if (!confirm("Delete this peer card? The next routine sweep may rebuild it.")) return
      const r = await fetch(`/api/v1/agents/${encodeURIComponent(agentId)}/peers/${encodeURIComponent(userID)}`, {
        method: "DELETE",
        headers: { "X-Workspace-ID": workspaceId },
      })
      if (!r.ok) {
        setErr(`delete peer failed: ${r.status}`)
        return
      }
      setActive(null)
      await load()
    },
    [agentId, load, workspaceId],
  )

  if (loading) return <p className="text-sm text-muted-foreground">Loading peers...</p>
  if (err) return <div className="rounded border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-300">{err}</div>
  if (peers.length === 0)
    return (
      <p className="text-sm text-muted-foreground">
        No peer cards yet. The PeerCardSync routine writes them once an operator has had ≥10 messages or a ≥5 min session with this agent.
      </p>
    )

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <ul className="space-y-2">
        {peers.map((p) => {
          const isActive = active?.user_id === p.user_id
          return (
            <li key={p.id}>
              <button
                type="button"
                onClick={() => loadDetail(p.user_id)}
                aria-pressed={isActive}
                aria-label={`Open peer card for ${p.user_id}`}
                className={`w-full text-left cursor-pointer rounded border border-white/10 px-3 py-2 text-sm hover:bg-white/5 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-500/60 ${
                  isActive ? "border-emerald-500/40 bg-emerald-500/5" : ""
                }`}
              >
                <div className="font-medium">{p.user_id}</div>
                <div className="text-xs text-muted-foreground">
                  {p.bytes} B · slug {p.user_slug} · updated {p.updated_at}
                </div>
              </button>
            </li>
          )
        })}
      </ul>

      <div className="rounded border border-white/10 p-3">
        {active ? (
          <div className="space-y-3">
            <header className="flex items-center justify-between">
              <h4 className="text-sm font-semibold">{active.user_id}</h4>
              <button
                onClick={() => deleteCard(active.user_id)}
                className="rounded bg-red-500/15 px-2 py-1 text-xs text-red-300 hover:bg-red-500/25"
              >
                Delete
              </button>
            </header>
            <pre className="text-sm whitespace-pre-wrap">{active.content}</pre>
            <p className="text-xs text-muted-foreground">
              {active.bytes}/{PEER_CAP_BYTES} B
            </p>
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">Select a peer to view their card.</p>
        )}
      </div>
    </div>
  )
}
