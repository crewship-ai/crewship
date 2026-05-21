"use client"

// PR-E F6 — Memory tab on the agent canvas.
//
// Two sub-tabs:
//   PERSONA — view + edit + reset + version history for the agent
//             layer (with a separate read-only display of the crew
//             default layer so the operator sees what gets inherited
//             when the agent layer is empty).
//   Peers   — grid of users this agent has cards for; click a row
//             to load the full content + delete button.
//
// Implementation note: this is the slim Phase-1 surface. The
// PRD calls for a full CodeMirror 6 editor; we ship a textarea
// + char counter here so PR-E lands without a CodeMirror
// integration spike. A follow-up PR can swap the textarea for
// `@codemirror/lang-markdown` (already in deps) without touching
// the rest of the tab.

import { useCallback, useEffect, useMemo, useState } from "react"

const PERSONA_CAP_BYTES = 1500
const PEER_CAP_BYTES = 1500

type SubTab = "persona" | "peers"

interface PersonaResponse {
  agent_id?: string
  crew_id?: string
  layer: string
  from_default: boolean
  content: string
  bytes: number
  cap_bytes: number
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

export function MemoryTab({ agentId, crewId, workspaceId }: MemoryTabProps) {
  const [sub, setSub] = useState<SubTab>("persona")
  return (
    <div className="space-y-6">
      <div className="flex gap-2 border-b border-white/10">
        {(["persona", "peers"] as SubTab[]).map((s) => (
          <button
            key={s}
            onClick={() => setSub(s)}
            className={`px-3 py-2 text-sm border-b-2 -mb-px ${
              sub === s
                ? "border-emerald-500 text-emerald-300"
                : "border-transparent text-muted-foreground hover:text-foreground"
            }`}
          >
            {s === "persona" ? "PERSONA" : "Peers"}
          </button>
        ))}
      </div>

      {sub === "persona" && (
        <PersonaPanel agentId={agentId} crewId={crewId} workspaceId={workspaceId} />
      )}
      {sub === "peers" && (
        <PeersPanel agentId={agentId} workspaceId={workspaceId} />
      )}
    </div>
  )
}

// PersonaPanel manages both the agent override editor and a read-only
// view of the crew default layer so the operator can see what gets
// inherited when the agent layer is empty.
function PersonaPanel({ agentId, crewId, workspaceId }: { agentId: string; crewId?: string; workspaceId: string }) {
  const [agentPersona, setAgentPersona] = useState<PersonaResponse | null>(null)
  const [crewPersona, setCrewPersona] = useState<PersonaResponse | null>(null)
  const [history, setHistory] = useState<HistoryEntry[]>([])
  const [editing, setEditing] = useState<string | null>(null)
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

  const save = useCallback(async () => {
    if (editing === null) return
    setSaving(true)
    setErr(null)
    try {
      const r = await fetch(`/api/v1/agents/${encodeURIComponent(agentId)}/persona`, {
        method: "PUT",
        headers: { "Content-Type": "application/json", "X-Workspace-ID": workspaceId },
        body: JSON.stringify({ content: editing }),
      })
      if (!r.ok) {
        setErr(`save failed: ${r.status} ${await r.text()}`)
      } else {
        setEditing(null)
        await load()
      }
    } finally {
      setSaving(false)
    }
  }, [agentId, editing, load, workspaceId])

  const reset = useCallback(async () => {
    if (!confirm("Reset agent PERSONA.md? The crew default + synthesized fallback will be used.")) return
    setSaving(true)
    try {
      await fetch(`/api/v1/agents/${encodeURIComponent(agentId)}/persona`, {
        method: "DELETE",
        headers: { "X-Workspace-ID": workspaceId },
      })
      await load()
    } finally {
      setSaving(false)
    }
  }, [agentId, load, workspaceId])

  return (
    <div className="space-y-6">
      {err && <div className="rounded border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-300">{err}</div>}

      <section className="space-y-3">
        <header className="flex items-center justify-between">
          <h3 className="text-sm font-semibold">Agent override (per-agent PERSONA.md)</h3>
          {agentPersona && (
            <span className="text-xs text-muted-foreground">
              source: {agentPersona.from_default ? "synthesized default" : agentPersona.layer}
              {" · "}
              {agentPersona.bytes}/{PERSONA_CAP_BYTES} B
            </span>
          )}
        </header>
        {editing === null ? (
          <pre className="rounded border border-white/10 bg-zinc-900/60 p-3 text-sm whitespace-pre-wrap min-h-[8rem]">
            {agentPersona?.content || "(no persona configured)"}
          </pre>
        ) : (
          <PersonaEditor value={editing} onChange={setEditing} />
        )}
        <div className="flex gap-2">
          {editing === null ? (
            <>
              <button
                onClick={() => setEditing(agentPersona?.from_default ? "" : agentPersona?.content ?? "")}
                className="rounded bg-emerald-500/20 px-3 py-1.5 text-sm text-emerald-300 hover:bg-emerald-500/30"
              >
                Edit
              </button>
              <button
                onClick={reset}
                disabled={saving || !agentPersona || agentPersona.from_default}
                className="rounded border border-white/10 px-3 py-1.5 text-sm hover:bg-white/5 disabled:opacity-50"
              >
                Reset (drop agent layer)
              </button>
            </>
          ) : (
            <>
              <button
                onClick={save}
                disabled={saving || editing.length === 0 || editing.length > PERSONA_CAP_BYTES}
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
      </section>

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

      <section className="space-y-2">
        <h3 className="text-sm font-semibold">Version history</h3>
        {history.length === 0 ? (
          <p className="text-sm text-muted-foreground">(no history)</p>
        ) : (
          <ul className="space-y-1 text-sm font-mono">
            {history.map((h) => (
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

// PersonaEditor is the textarea + char counter slim editor.
// CodeMirror integration is a follow-up: the textarea preserves the
// markdown source the operator typed without rendering surprises,
// and the counter is the only validation feedback the operator needs
// pre-save (the API enforces the cap server-side too).
function PersonaEditor({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const bytes = useMemo(() => new TextEncoder().encode(value).length, [value])
  const over = bytes > PERSONA_CAP_BYTES
  return (
    <div className="space-y-1">
      <textarea
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="w-full min-h-[10rem] rounded border border-white/10 bg-zinc-900/60 p-3 text-sm font-mono focus:outline-none focus:ring-2 focus:ring-emerald-500/40"
        placeholder="Describe how this agent should address operators (tone, register, language)."
      />
      <div className={`text-xs ${over ? "text-red-400" : "text-muted-foreground"}`}>
        {bytes}/{PERSONA_CAP_BYTES} B {over && "— over cap"}
      </div>
    </div>
  )
}

// PeersPanel renders the per-(agent, user) card grid. Clicking a row
// loads the card content into a detail panel inline.
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
      await fetch(`/api/v1/agents/${encodeURIComponent(agentId)}/peers/${encodeURIComponent(userID)}`, {
        method: "DELETE",
        headers: { "X-Workspace-ID": workspaceId },
      })
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
        {peers.map((p) => (
          <li
            key={p.id}
            onClick={() => loadDetail(p.user_id)}
            className={`cursor-pointer rounded border border-white/10 px-3 py-2 text-sm hover:bg-white/5 ${
              active?.user_id === p.user_id ? "border-emerald-500/40 bg-emerald-500/5" : ""
            }`}
          >
            <div className="font-medium">{p.user_id}</div>
            <div className="text-xs text-muted-foreground">
              {p.bytes} B · slug {p.user_slug} · updated {p.updated_at}
            </div>
          </li>
        ))}
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
