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
//
// WHAT THIS PANEL IS READING, and why it says so out loud
// (§4.5 of the 2026-08-13 chat-surface audit):
//
// The tier panels read the memory AUDIT TRAIL — rows in
// memory_versions — not the live .memory files. No endpoint returns
// the current bytes of an agent's memory directory; "latest recorded
// version" is the closest thing this surface can obtain. That matters
// because memory_versions is a projection: a file only appears if some
// writer records it, and until the audit watcher learned the crew's
// shared tree, CREW.md never did. A real shared memory file therefore
// rendered here as "(no history)" — the panel stating an absence it
// had no way to establish.
//
// So every empty list now has to declare which kind of empty it is.
// GET /api/v1/memory/versions returns a `projection` object saying
// whether the path is recorded at all, and the history section renders
// "nothing has been written" ONLY when the server says the path is
// watched. Unrecorded, unavailable, still-loading and failed all say
// what they are instead. The client does not hard-code which tiers are
// projected — that list has been wrong in three separate documents,
// and the server owns it.

import { useCallback, useEffect, useMemo, useState } from "react"
import { MarkdownEditor } from "@/components/shared/markdown-editor"
import { apiFetch } from "@/lib/api-fetch"
import { MemoryExportButton } from "./memory-export-button"

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

// Projection mirrors internal/memory/projection.go. `recorded` is the
// only state in which an empty entry list may be read as "nothing has
// been written"; the other two mean the trail cannot be collected for
// this path (nothing projects it) or on this server (versioning off).
interface Projection {
  state: "recorded" | "unrecorded" | "unavailable"
  reason: string
}

type HistoryStatus = "loading" | "ready" | "error"

// HistoryState is what the version-history section renders from. It
// carries the load status alongside the rows on purpose: an in-flight
// request and a failed one both have zero entries, and neither is an
// empty history.
interface HistoryState {
  status: HistoryStatus
  entries: VersionEntry[]
  projection: Projection
  error?: string
}

const RECORDED: Projection = { state: "recorded", reason: "" }

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
      {/* Linear-style underline tab bar (PRD §9 UI guidelines). The
          export sits on the bar rather than inside a pane: it takes the
          whole scope, not the sub-tab being viewed. */}
      <div className="flex items-center gap-2 border-b border-white/10">
        {tabs.map((s) => (
          <button
            key={s}
            data-testid={`memory-subtab-${s}`}
            onClick={() => setSub(s)}
            className={`px-3 py-2 text-sm border-b-2 -mb-px ${
              sub === s
                ? "border-success text-success"
                : "border-transparent text-muted-foreground hover:text-foreground"
            }`}
          >
            {SUBTAB_LABEL[s]}
          </button>
        ))}
        {/* Export follows the SELECTED sub-tab: on CREW the operator is
            looking at the crew-shared tier, and sending agent_slug would
            have handed them the agent's memory under a crew label.
            Without a crew there is nothing to scope on — the export API
            keys on crew_id — so the button is not offered at all. */}
        {crewId && (
          <div className="ml-auto pb-1.5">
            <MemoryExportButton
              crewId={crewId}
              agentSlug={sub === "crew" ? undefined : agentSlug}
              workspaceId={workspaceId}
            />
          </div>
        )}
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

      <OtherTiers agentSlug={agentSlug} hasCrew={Boolean(crewId)} />
    </div>
  )
}

// OtherTiers names the memory files this panel has no sub-tab for.
//
// The four tabs above are not the whole of an agent's memory, and a
// reader who assumes they are will draw exactly the wrong conclusion
// from them. Each entry below states what is true today rather than
// what we would like to be true — the 2026-08-13 audit found that
// three separate documents had this wrong, so the wording is
// deliberately about mechanism ("who writes it, who records it") and
// not about intent.
function OtherTiers({ agentSlug, hasCrew }: { agentSlug: string; hasCrew: boolean }) {
  return (
    <section
      data-testid="memory-other-tiers"
      className="space-y-3 rounded border border-white/10 p-3"
    >
      <h3 className="text-sm font-semibold">Other memory this panel does not show</h3>
      <p className="text-xs text-muted-foreground">
        &ldquo;Recorded&rdquo; below means a writer puts the file into the version trail —
        the same trail the panels above read. If a panel above reports that versioning is
        off on this server, none of these are being recorded either.
      </p>
      <dl className="space-y-3 text-xs text-muted-foreground">
        <div>
          <dt className="font-mono text-foreground">daily/YYYY-MM-DD.md</dt>
          <dd>
            Recorded. Every write lands in the audit trail as{" "}
            <code>agent:{agentSlug}/daily/&lt;date&gt;.md</code>, but the member endpoint
            answers for one exact path at a time and there is no way to discover which
            dates exist. Listing by prefix is admin-only (
            <code>GET /api/v1/admin/memory/versions</code>), or{" "}
            <code>crewship memory log</code>.
          </dd>
        </div>
        <div>
          <dt className="font-mono text-foreground">lessons.md</dt>
          <dd>
            Not recorded at all. The negative-learning evaluator writes it straight into
            the memory directory (<code>consolidate.WriteLesson</code>) and no writer
            projects it into the version trail — no row, no endpoint. An absence here is
            this panel&rsquo;s blind spot, not an empty file. Read it from inside the crew
            or over the CLI.
          </dd>
        </div>
        <div>
          <dt className="font-mono text-foreground">learned-&lt;topic&gt;.md</dt>
          <dd>
            {hasCrew ? (
              <>
                Recorded by the consolidator, one file per topic, as{" "}
                <code>crew:&lt;crew&gt;/learned-&lt;topic&gt;.md</code> — but the topic
                names cannot be enumerated without the same admin prefix listing, so
                nothing here can offer them.
              </>
            ) : (
              <>
                Crew-scoped, and this agent is on no crew — the consolidator writes these
                per crew, so there are none to show.
              </>
            )}
          </dd>
        </div>
        <div>
          <dt className="font-mono text-foreground">pins.md</dt>
          <dd>
            {hasCrew ? (
              <>
                Recorded at <code>crew:&lt;crew&gt;/pins.md</code> by the consolidator and
                by the audit watcher. Reachable through the version endpoints and{" "}
                <code>crewship memory log</code>; it simply has no tab here yet.
              </>
            ) : (
              <>
                Recorded at <code>agent:{agentSlug}/pins.md</code> when the agent keeps
                pinned facts. Reachable through the version endpoints and{" "}
                <code>crewship memory log</code>; it has no tab here yet.
              </>
            )}
          </dd>
        </div>
      </dl>
    </section>
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
// this change last?" for both writeable + read-only tiers — or says
// why it cannot answer.
function MemoryTierEditor({
  title,
  content,
  bytes,
  capBytes,
  history,
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
  history: HistoryState
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
        <div className="rounded border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive">
          {err}
        </div>
      )}

      <section className="space-y-3">
        <header className="flex items-center justify-between gap-3">
          <div className="flex items-center gap-2">
            <h3 className="text-sm font-semibold">{title}</h3>
            {badge && (
              <span className="rounded bg-info/15 px-2 py-0.5 text-[10px] uppercase tracking-wide text-info">
                {badge}
              </span>
            )}
          </div>
          <span className="text-xs text-muted-foreground">
            {editing !== null ? editingBytes : bytes}/{capBytes} B
          </span>
        </header>

        {editing === null ? (
          // Read-only display uses the same CodeMirror editor (with
          // readOnly=true) so markdown syntax highlighting is visible
          // to the operator even when they cannot edit. With no content
          // the editor looks awkward at zero bytes, so a plain pre
          // carries the placeholder — and the placeholder says which
          // kind of nothing this is, since the content shown here is
          // the latest RECORDED version, not the file on disk.
          content ? (
            <MarkdownEditor
              value={content}
              onChange={() => { /* read-only */ }}
              readOnly
              minHeight="8rem"
              ariaLabel={`${title} (read-only)`}
            />
          ) : (
            <pre className="rounded border border-white/10 bg-muted/60 p-3 text-sm whitespace-pre-wrap min-h-[8rem]">
              {emptyContentPlaceholder(history)}
            </pre>
          )
        ) : (
          <div className="space-y-1">
            <MarkdownEditor
              value={editing}
              onChange={(next) => setEditing(next)}
              minHeight="10rem"
              autoFocus
              ariaLabel={`${title} editor`}
            />
            <div className={`text-xs ${over ? "text-destructive" : "text-muted-foreground"}`}>
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
                  className="rounded bg-success/20 px-3 py-1.5 text-sm text-success hover:bg-success/30"
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
                  className="rounded bg-success px-3 py-1.5 text-sm text-muted-foreground hover:bg-success disabled:opacity-50"
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

      <VersionHistory title={title} history={history} />
    </div>
  )
}

// emptyContentPlaceholder keeps the content pane from claiming a file
// is empty when all that is known is that no version was recorded.
function emptyContentPlaceholder(history: HistoryState): string {
  if (history.status === "loading") return "(loading…)"
  if (history.status === "error") return "(could not be read — see below)"
  if (history.projection.state !== "recorded") return "(not readable here — see below)"
  return "(no version recorded)"
}

// VersionHistory is the section the audit finding is about. Four
// outcomes, four sentences — the one thing it must never do is render
// the same blank line for all of them.
function VersionHistory({ title, history }: { title: string; history: HistoryState }) {
  const { status, entries, projection } = history
  return (
    <section className="space-y-2">
      <h3 className="text-sm font-semibold">Version history</h3>

      {status === "loading" && (
        <p data-testid="memory-history-loading" className="text-sm text-muted-foreground">
          Loading version history…
        </p>
      )}

      {status === "error" && (
        <div
          data-testid="memory-history-error"
          role="alert"
          className="rounded border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive"
        >
          Could not read the version history for {title}
          {history.error ? ` — ${history.error}` : ""}. This is a failed read, not an empty
          history: nothing here says anything about what the agent has written.
        </div>
      )}

      {status === "ready" && entries.length === 0 && projection.state !== "recorded" && (
        <div
          data-testid="memory-history-unreadable"
          className="space-y-1.5 rounded border border-warn/30 bg-warn/10 p-3 text-sm text-warn"
        >
          <p>This tier cannot be read here.</p>
          <p>{projection.reason}</p>
          <p className="text-muted-foreground">
            The file may well have content — this panel reads the audit trail, and nothing
            puts this path into it.
          </p>
        </div>
      )}

      {status === "ready" && entries.length === 0 && projection.state === "recorded" && (
        <p data-testid="memory-history-empty" className="text-sm text-muted-foreground">
          No version of {title} has been recorded yet. This path is watched, so that means
          nothing has been written to it — not that the trail is missing it.
        </p>
      )}

      {status === "ready" && entries.length > 0 && (
        <ul className="space-y-1 text-sm font-mono">
          {entries.map((h) => (
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
  const { content, bytes, history, err } = useMemoryTierLatest(path, workspaceId)
  return (
    <MemoryTierEditor
      title="AGENT.md — per-agent canonical memory"
      content={content}
      bytes={bytes}
      capBytes={AGENT_CAP_BYTES}
      history={history}
      readOnly
      hint="Agent-managed file. The agent writes to this during runs; operators can audit history here but cannot edit directly. Use the admin restore endpoint to roll back a row. Shown from the audit trail, not from the live file."
      err={err}
    />
  )
}

// CrewMemoryPanel reads `crew:<crewID>/CREW.md` and renders the same
// shell with a "shared with crew" badge. We key by crewID (not slug)
// to match the canonical path layout used by the writer caps — and by
// the audit watcher, which since the §4.5 fix walks the crew's shared
// tree ({base}/crews/{id}/shared/.memory) and records writes there
// under exactly this path. Before that this tier was structurally
// empty no matter what the crew had written.
function CrewMemoryPanel({
  crewId,
  workspaceId,
}: {
  crewId: string
  workspaceId: string
}) {
  const path = `crew:${crewId}/CREW.md`
  const { content, bytes, history, err } = useMemoryTierLatest(path, workspaceId)
  return (
    <MemoryTierEditor
      title="CREW.md — shared crew memory"
      content={content}
      bytes={bytes}
      capBytes={CREW_CAP_BYTES}
      history={history}
      readOnly
      badge="shared with all crew members"
      hint="All agents in this crew read this file at session start. Agent-managed (memory.write, tier CREW); operator edits would break the orchestrator audit chain. Shown from the audit trail, not from the live file."
      err={err}
    />
  )
}

// useMemoryTierLatest pulls the version history for the given path and
// returns the latest entry's content (fetched lazily via the /content
// endpoint), plus the server's verdict on whether this path is
// projected into the trail at all.
//
// The old version of this hook collapsed four outcomes into one empty
// array — in flight, failed, nothing written, and nothing recording
// this path — which is precisely how a populated CREW.md rendered as
// "(no history)". Each now keeps its own state, and the projection
// comes from the server rather than from a client-side list of which
// tiers are watched.
const LOADING_HISTORY: HistoryState = {
  status: "loading",
  entries: [],
  projection: RECORDED,
}

function useMemoryTierLatest(path: string, workspaceId: string) {
  const [history, setHistory] = useState<HistoryState>(LOADING_HISTORY)
  const [content, setContent] = useState("")
  const [bytes, setBytes] = useState(0)
  const [err, setErr] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    const headers = { "X-Workspace-ID": workspaceId }
    async function load() {
      setErr(null)
      setHistory(LOADING_HISTORY)
      try {
        const r = await apiFetch(
          `/api/v1/memory/versions?path=${encodeURIComponent(path)}&limit=20`,
          { headers },
        )
        if (!r.ok) {
          if (r.status === 404) {
            // No such surface on this server. That is a gap in what we
            // can read, not evidence about the file, so it is reported
            // as unreadable rather than as an empty tier.
            if (!cancelled) {
              setHistory({
                status: "ready",
                entries: [],
                projection: {
                  state: "unavailable",
                  reason:
                    "This server does not serve the memory version trail (404), so no history can be read for any tier.",
                },
              })
              setContent("")
              setBytes(0)
            }
            return
          }
          throw new Error(`list versions failed: ${r.status}`)
        }
        const data = (await r.json()) as {
          entries?: VersionEntry[]
          projection?: Projection
        }
        const entries = data.entries ?? []
        // A server that predates the projection field is treated as
        // "recorded": the rows it returns are real, and inventing an
        // unreadable state for it would be its own kind of lie.
        const projection = data.projection ?? RECORDED
        if (cancelled) return
        setHistory({ status: "ready", entries, projection })
        if (entries.length === 0) {
          setContent("")
          setBytes(0)
          return
        }
        const latest = entries[0]
        // Show endpoint returns the raw blob bytes for the latest sha.
        const cr = await apiFetch(
          `/api/v1/memory/versions/${encodeURIComponent(latest.sha256)}?path=${encodeURIComponent(path)}`,
          { headers },
        )
        if (!cr.ok) throw new Error(`load latest content failed: ${cr.status}`)
        const text = await cr.text()
        if (cancelled) return
        setContent(text)
        setBytes(latest.bytes)
      } catch (e) {
        if (cancelled) return
        const message = (e as Error).message
        setErr(message)
        setHistory({
          status: "error",
          entries: [],
          projection: RECORDED,
          error: message,
        })
      }
    }
    load()
    return () => {
      cancelled = true
    }
  }, [path, workspaceId])

  return { content, bytes, history, err }
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
  // PERSONA has its own history surface (persona/history), so its
  // projection is never in doubt — but a failed fetch of it must still
  // not render as "no edits have ever been made".
  const [history, setHistory] = useState<HistoryState>(LOADING_HISTORY)
  const [saving, setSaving] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const load = useCallback(async () => {
    setErr(null)
    const headers = { "X-Workspace-ID": workspaceId }
    try {
      const [pr, hist] = await Promise.all([
        apiFetch(`/api/v1/agents/${encodeURIComponent(agentId)}/persona`, { headers }),
        apiFetch(`/api/v1/agents/${encodeURIComponent(agentId)}/persona/history?limit=20`, { headers }),
      ])
      if (pr.ok) setAgentPersona(await pr.json())
      if (hist.ok) {
        const h = (await hist.json()) as { entries?: HistoryEntry[] }
        setHistory({ status: "ready", entries: h.entries || [], projection: RECORDED })
      } else {
        setHistory({
          status: "error",
          entries: [],
          projection: RECORDED,
          error: `GET persona/history returned ${hist.status}`,
        })
      }
      if (crewId) {
        const cr = await apiFetch(`/api/v1/crews/${encodeURIComponent(crewId)}/persona`, { headers })
        if (cr.ok) setCrewPersona(await cr.json())
      }
    } catch (e) {
      const message = (e as Error).message
      setErr(message)
      setHistory({ status: "error", entries: [], projection: RECORDED, error: message })
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
        const r = await apiFetch(`/api/v1/agents/${encodeURIComponent(agentId)}/persona`, {
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
      const r = await apiFetch(`/api/v1/agents/${encodeURIComponent(agentId)}/persona`, {
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
        history={history}
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
          <pre className="rounded border border-white/10 bg-muted/40 p-3 text-sm whitespace-pre-wrap min-h-[4rem]">
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
      const r = await apiFetch(`/api/v1/agents/${encodeURIComponent(agentId)}/peers`, {
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
        const r = await apiFetch(
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
      const r = await apiFetch(`/api/v1/agents/${encodeURIComponent(agentId)}/peers/${encodeURIComponent(userID)}`, {
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
  if (err) return <div className="rounded border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive">{err}</div>
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
                className={`w-full text-left cursor-pointer rounded border border-white/10 px-3 py-2 text-sm hover:bg-white/5 focus-visible:outline focus-visible:outline-2 focus-visible:outline-success/60 ${
                  isActive ? "border-success/40 bg-success/5" : ""
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
                className="rounded bg-destructive/15 px-2 py-1 text-xs text-destructive hover:bg-destructive/25"
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
