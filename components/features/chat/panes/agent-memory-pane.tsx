"use client"

// Memory — what the agent actually knows, and what this dashboard cannot
// tell you it knows.
//
// The honest framing first, because it changes how every panel below should
// be read: THIS SURFACE READS THE AUDIT TRAIL, NOT THE LIVE FILES. There is
// no endpoint that returns the current bytes of an agent's .memory/ tree.
// GET /api/v1/memory/versions (router_orchestration.go:625) lists rows in
// the memory_versions table for one exact path, and
// GET /api/v1/memory/versions/{sha} (line 626) returns the blob for one of
// them. "Latest recorded version" is therefore the closest thing to "current
// content" the dashboard can obtain — and when nothing recorded a write, the
// answer is not an empty file, it is silence.
//
// What each tier reads, and whether it can be read at all:
//
//   AGENT.md   agent:<slug>/AGENT.md   — recorded by the audit watcher
//                (internal/memory/audit_watcher.go:458, canonical path built
//                at line 473). Read-only by design: only the agent runtime
//                may write it, and that decision is not relitigated here —
//                see the header of components/features/crews/
//                agent-canvas-tabs/memory-tab.tsx.
//   CREW.md    crew:<crewId>/CREW.md   — same trail, same path the existing
//                memory panel uses (memory-tab.tsx:388). Note, though: no
//                writer in internal/ ever produces that path. CREW.md lives
//                at {base}/crews/{crewID}/shared/.memory/CREW.md
//                (orchestrator/memory_persona.go:61), and the audit watcher
//                only matches crews/{id}/agents/{slug}/.memory/…
//                (audit_watcher.go:449), so the shared tree is never walked.
//                The only other producers of a "crew:" path are the
//                consolidator's pins.md / learned-*.md
//                (consolidator.go:144, :238) and approve.go:193. So this tier
//                is expected to be permanently empty, and the empty state
//                says that rather than implying the crew wrote nothing.
//   PERSONA    GET /api/v1/agents/{id}/persona (router_crews.go:364).
//                Operator-writeable elsewhere; read-only here.
//   Peers      GET /api/v1/agents/{id}/peers  (router_crews.go:384).
//   pins.md    crew:<crewId>/pins.md   — written by the consolidator, which
//                records it at exactly this path
//                (internal/consolidate/consolidator.go:144 →
//                canonicalAuditPath at line 707). Reachable, but only as far
//                as the trail goes: if version blobs are not configured
//                (cfg.BlobRoot == "") the consolidator skips the record
//                entirely and the file exists on disk with no row here.
//
// And the three that a workspace member cannot read at all — rendered as
// explanations, never as empty lists:
//
//   daily/YYYY-MM-DD.md  rows exist (audit_watcher.go:466) but the member
//                        endpoint takes ONE exact path and there is no
//                        enumeration. Listing by prefix exists only on
//                        GET /api/v1/admin/memory/versions
//                        (router_admin.go:191), which is admin/manage-gated.
//   lessons.md           never recorded at all: parseMemoryPath maps
//                        AGENT.md, CREW.md, pins.md, learned-*.md and
//                        daily/*.md, and nothing else (audit_watcher.go:457
//                        -469). No row, no endpoint, no panel.
//   learned-<topic>.md   recorded as crew:<crewId>/learned-<topic>.md, but
//                        the topic names are unknowable without the same
//                        admin prefix listing.

import { useCallback, useEffect, useState } from "react"
import Link from "next/link"
import {
  Brain,
  CalendarDays,
  ExternalLink,
  GraduationCap,
  Lightbulb,
  Pin,
  Users,
} from "lucide-react"

import { Button } from "@/components/ui/button"
import { DetailCard, Pill } from "@/components/ui/detail"
import { apiFetch } from "@/lib/api-fetch"
import { useWorkspace } from "@/hooks/use-workspace"

import { PaneError, PaneLoading, PaneShell, PaneUnreachable } from "./pane-shell"

export interface AgentMemoryPaneProps {
  agentId: string
  agentSlug: string
}

interface VersionEntry {
  id: string
  sha256: string
  bytes: number
  written_at: string
  written_by: string
}

interface PersonaResponse {
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
  updated_at: string
}

type Status = "loading" | "ready" | "error"

interface TierState {
  status: Status
  entries: VersionEntry[]
  content: string
  error: string
}

const TIER_INIT: TierState = { status: "loading", entries: [], content: "", error: "" }

/**
 * One tier read, as two calls: list the audit chain for the path, then pull
 * the blob for the newest sha. Mirrors what memory-tab.tsx does against the
 * same endpoints; it is re-stated here rather than imported because that
 * helper is module-private there and reports neither a loading nor a failure
 * state, which a full-page surface owes its reader.
 */
async function readTier(path: string, workspaceId: string, signal: AbortSignal): Promise<TierState> {
  const ws = encodeURIComponent(workspaceId)
  const list = await apiFetch(
    `/api/v1/memory/versions?path=${encodeURIComponent(path)}&limit=20&workspace_id=${ws}`,
    { signal },
  )
  // A brand-new agent has no rows; the endpoint answers 200 with an empty
  // list, and older deployments 404. Neither is an error — but neither is
  // "the file is empty" either, which is why the caller renders a sentence.
  if (list.status === 404) return { status: "ready", entries: [], content: "", error: "" }
  if (!list.ok) throw new Error(`HTTP ${list.status}`)
  const data = (await list.json()) as { entries?: VersionEntry[] } | null
  const entries = data?.entries ?? []
  if (entries.length === 0) return { status: "ready", entries: [], content: "", error: "" }

  const latest = entries[0]
  const show = await apiFetch(
    `/api/v1/memory/versions/${encodeURIComponent(latest.sha256)}?path=${encodeURIComponent(path)}&workspace_id=${ws}`,
    { signal },
  )
  if (!show.ok) throw new Error(`HTTP ${show.status} reading blob ${latest.sha256.slice(0, 12)}`)
  return { status: "ready", entries, content: await show.text(), error: "" }
}

export function AgentMemoryPane({ agentId, agentSlug }: AgentMemoryPaneProps) {
  const { workspaceId } = useWorkspace()
  const [nonce, setNonce] = useState(0)

  const [status, setStatus] = useState<Status>("loading")
  const [error, setError] = useState("")
  const [crewId, setCrewId] = useState<string | null>(null)
  const [memoryEnabled, setMemoryEnabled] = useState(true)

  const [agentTier, setAgentTier] = useState<TierState>(TIER_INIT)
  const [crewTier, setCrewTier] = useState<TierState>(TIER_INIT)
  const [pinsTier, setPinsTier] = useState<TierState>(TIER_INIT)
  const [persona, setPersona] = useState<PersonaResponse | null>(null)
  const [personaError, setPersonaError] = useState("")
  const [peers, setPeers] = useState<PeerEntry[] | null>(null)
  const [peersError, setPeersError] = useState("")

  const retry = useCallback(() => setNonce((n) => n + 1), [])

  // The agent record is the spine: the crew tier and the pins tier are both
  // keyed by crew ID, which nothing else on this pane carries. If it fails,
  // there is nothing partial worth rendering.
  useEffect(() => {
    if (!workspaceId) {
      setStatus("loading")
      return
    }
    const ac = new AbortController()
    setStatus("loading")
    apiFetch(
      `/api/v1/agents/${encodeURIComponent(agentId)}?workspace_id=${encodeURIComponent(workspaceId)}`,
      { signal: ac.signal },
    )
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        return r.json()
      })
      .then((agent: { crew_id?: string | null; memory_enabled?: boolean } | null) => {
        if (ac.signal.aborted) return
        setCrewId(agent?.crew_id ?? null)
        setMemoryEnabled(agent?.memory_enabled !== false)
        setStatus("ready")
      })
      .catch((e: Error) => {
        if (ac.signal.aborted || e.name === "AbortError") return
        setError(e.message)
        setStatus("error")
      })
    return () => ac.abort()
  }, [agentId, workspaceId, nonce])

  // Tiers fan out once the spine lands. Each keeps its own failure: a 500 on
  // peer cards must not blank out AGENT.md.
  useEffect(() => {
    if (status !== "ready" || !workspaceId) return
    const ac = new AbortController()
    const ws = encodeURIComponent(workspaceId)

    const guard = (set: (s: TierState) => void) => (e: Error) => {
      if (ac.signal.aborted || e.name === "AbortError") return
      set({ status: "error", entries: [], content: "", error: e.message })
    }
    const apply = (set: (s: TierState) => void) => (s: TierState) => {
      if (!ac.signal.aborted) set(s)
    }

    setAgentTier(TIER_INIT)
    readTier(`agent:${agentSlug}/AGENT.md`, workspaceId, ac.signal)
      .then(apply(setAgentTier))
      .catch(guard(setAgentTier))

    if (crewId) {
      setCrewTier(TIER_INIT)
      readTier(`crew:${crewId}/CREW.md`, workspaceId, ac.signal)
        .then(apply(setCrewTier))
        .catch(guard(setCrewTier))
      setPinsTier(TIER_INIT)
      readTier(`crew:${crewId}/pins.md`, workspaceId, ac.signal)
        .then(apply(setPinsTier))
        .catch(guard(setPinsTier))
    }

    setPersona(null)
    setPersonaError("")
    apiFetch(`/api/v1/agents/${encodeURIComponent(agentId)}/persona?workspace_id=${ws}`, {
      signal: ac.signal,
    })
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        return r.json()
      })
      .then((p: PersonaResponse) => {
        if (!ac.signal.aborted) setPersona(p)
      })
      .catch((e: Error) => {
        if (!ac.signal.aborted && e.name !== "AbortError") setPersonaError(e.message)
      })

    setPeers(null)
    setPeersError("")
    apiFetch(`/api/v1/agents/${encodeURIComponent(agentId)}/peers?workspace_id=${ws}`, {
      signal: ac.signal,
    })
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        return r.json()
      })
      .then((d: { peers?: PeerEntry[] }) => {
        if (!ac.signal.aborted) setPeers(d?.peers ?? [])
      })
      .catch((e: Error) => {
        if (!ac.signal.aborted && e.name !== "AbortError") setPeersError(e.message)
      })

    return () => ac.abort()
  }, [status, agentId, agentSlug, crewId, workspaceId, nonce])

  return (
    <PaneShell
      icon={Brain}
      title="Memory"
      subtitle="What this agent has written down. Read from the memory audit trail, not from the live files — a tier with no recorded version is silence, not an empty file."
      actions={
        <Button asChild variant="outline" size="sm">
          <Link href={`/crews?agent=${encodeURIComponent(agentSlug)}`}>
            <ExternalLink className="h-3.5 w-3.5" />
            Full memory panel
          </Link>
        </Button>
      }
      data-testid="memory-pane"
    >
      {status === "loading" && <PaneLoading label="Loading memory…" data-testid="memory-pane-loading" />}

      {status === "error" && (
        <PaneError
          data-testid="memory-error"
          title="Could not load this agent's memory"
          detail={`GET /api/v1/agents/${agentId} failed — ${error}. The crew tiers are keyed by the crew ID on that record, so nothing below can be addressed until it loads.`}
          onRetry={retry}
        />
      )}

      {status === "ready" && (
        <div className="space-y-4">
          {!memoryEnabled && (
            <div className="rounded-xl border border-warn/30 bg-warn/10 p-3">
              <p className="type-meta leading-relaxed text-warn">
                Memory is switched off for {agentSlug}. Anything below is what was recorded before it
                was disabled; nothing new is being written.
              </p>
            </div>
          )}

          <TierCard
            testId="agent"
            title="AGENT.md"
            icon={Brain}
            path={`agent:${agentSlug}/AGENT.md`}
            state={agentTier}
            note="The agent's own canonical memory. Only the agent runtime writes it — operator edits would break the orchestrator's audit chain."
            onRetry={retry}
          />

          {crewId ? (
            <TierCard
              testId="crew"
              title="CREW.md"
              icon={Users}
              path={`crew:${crewId}/CREW.md`}
              state={crewTier}
              badge="shared with the crew"
              note="Every agent in the crew reads this at session start. Agent-managed, like AGENT.md."
              emptyNote="Expect this to stay empty: CREW.md is written into the crew's shared memory directory, and the audit watcher only walks each agent's own. Nothing records this path today, so an absence here says nothing about what the crew has written."
              onRetry={retry}
            />
          ) : (
            <PaneUnreachable icon={Users} title="CREW.md" data-testid="memory-tier-no-crew">
              <p>
                {agentSlug} is not on a crew, so there is no crew-shared memory to read. This is not an
                empty file — the tier does not exist for a solo agent.
              </p>
            </PaneUnreachable>
          )}

          <DetailCard title="PERSONA" icon={Brain} subtitle={persona?.from_default ? "synthesized default" : "agent override"}>
            {personaError ? (
              <InlineError what={`GET /api/v1/agents/${agentId}/persona`} detail={personaError} onRetry={retry} />
            ) : persona === null ? (
              <p className="type-meta text-muted-foreground">Loading persona…</p>
            ) : persona.from_default ? (
              <p className="type-meta leading-relaxed text-muted-foreground">
                No persona of its own. The tone below is synthesized from the crew default and the
                agent&rsquo;s metadata, and changes if either does.
              </p>
            ) : null}
            {persona && (
              <pre className="type-meta mt-2 max-h-64 overflow-auto whitespace-pre-wrap font-mono leading-relaxed text-foreground">
                {persona.content || "(empty)"}
              </pre>
            )}
          </DetailCard>

          <DetailCard
            title="Peer cards"
            icon={Users}
            subtitle={peers ? `${peers.length}` : undefined}
            bare
            footer="One card per person this agent has worked with. Written by the PeerCardSync routine, not by hand — and deleted from the agent's Memory panel, not from here."
          >
            {peersError ? (
              <div className="p-4">
                <InlineError what={`GET /api/v1/agents/${agentId}/peers`} detail={peersError} onRetry={retry} />
              </div>
            ) : peers === null ? (
              <p className="type-meta p-4 text-muted-foreground">Loading peer cards…</p>
            ) : peers.length === 0 ? (
              <p className="type-meta p-4 leading-relaxed text-muted-foreground">
                No peer cards yet. The routine writes one once somebody has had ten messages or a
                five-minute session with {agentSlug}.
              </p>
            ) : (
              <ul className="divide-y divide-hairline">
                {peers.map((p) => (
                  <li key={p.id} className="flex items-baseline gap-3 px-4 py-2.5">
                    <span className="type-row text-foreground">{p.user_slug}</span>
                    <span className="type-meta text-muted-foreground-soft">
                      {p.bytes} B · updated {p.updated_at}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </DetailCard>

          {crewId && (
            <TierCard
              testId="pins"
              title="pins.md"
              icon={Pin}
              path={`crew:${crewId}/pins.md`}
              state={pinsTier}
              note="Written by the consolidator, not the agent. It only appears here once the consolidator has run with version blobs configured — until then the file can exist on disk with nothing recorded."
              onRetry={retry}
            />
          )}

          {/* The tiers with no member-reachable read. Each says why, so that
              nobody reads a missing panel as an agent that knows nothing. */}
          <PaneUnreachable icon={CalendarDays} title="daily/ logs" data-testid="memory-tier-unreachable-daily">
            <p>
              The agent&rsquo;s day logs (<code>daily/YYYY-MM-DD.md</code>) are recorded, but they cannot
              be listed from here: the memory endpoint answers for one exact path at a time and there
              is no way to discover which dates exist.
            </p>
            <p>
              Listing by prefix exists only on <code>GET /api/v1/admin/memory/versions</code>, which is
              admin-gated, or through <code>crewship memory log</code>.
            </p>
          </PaneUnreachable>

          <PaneUnreachable icon={Lightbulb} title="lessons.md" data-testid="memory-tier-unreachable-lessons">
            <p>
              Not readable from the dashboard at all. <code>lessons.md</code> is written into the
              agent&rsquo;s memory directory by the negative-learning writer, but nothing records it to
              the version trail — so there is no row to fetch and no endpoint to fetch it with.
            </p>
            <p>Read it from inside the crew, or through the CLI.</p>
          </PaneUnreachable>

          <PaneUnreachable icon={GraduationCap} title="learned-*.md topics" data-testid="memory-tier-unreachable-learned">
            <p>
              The consolidator&rsquo;s learned topics are recorded one file per topic
              (<code>crew:&lt;crew&gt;/learned-&lt;topic&gt;.md</code>), and the topic names cannot be
              discovered without the admin prefix listing. Nothing here can enumerate them.
            </p>
          </PaneUnreachable>
        </div>
      )}
    </PaneShell>
  )
}

function InlineError({ what, detail, onRetry }: { what: string; detail: string; onRetry: () => void }) {
  return (
    <div role="alert" className="flex flex-wrap items-center gap-2">
      <span className="type-meta text-destructive">
        {what} failed — {detail}.
      </span>
      <Button variant="outline" size="xs" onClick={onRetry}>
        Retry
      </Button>
    </div>
  )
}

/**
 * One audit-trail tier: its latest recorded content plus the chain behind it.
 *
 * The empty branch is the point of this component. "No versions recorded"
 * and "the file is empty" are different facts, and only the first one is
 * knowable from here — so the empty state says which one it is reporting.
 */
function TierCard({
  testId,
  title,
  icon,
  path,
  state,
  badge,
  note,
  emptyNote,
  onRetry,
}: {
  testId: string
  title: string
  icon: React.ComponentType<{ className?: string }>
  path: string
  state: TierState
  badge?: string
  note: string
  /** Appended to the empty state when THIS tier's absence has a known cause. */
  emptyNote?: string
  onRetry: () => void
}) {
  const latest = state.entries[0]
  return (
    <DetailCard
      title={title}
      icon={icon}
      subtitle={path}
      action={badge ? <Pill tone="blue">{badge}</Pill> : undefined}
      footer={note}
      data-testid={`memory-tier-${testId}`}
    >
      {state.status === "loading" && (
        <p className="type-meta text-muted-foreground">Loading {title}…</p>
      )}

      {state.status === "error" && (
        <div data-testid={`memory-tier-error-${testId}`}>
          <InlineError what={`GET /api/v1/memory/versions?path=${path}`} detail={state.error} onRetry={onRetry} />
        </div>
      )}

      {state.status === "ready" && state.entries.length === 0 && (
        <div
          data-testid={`memory-tier-empty-${testId}`}
          className="type-meta space-y-1.5 leading-relaxed text-muted-foreground"
        >
          <p>
            No version of {title} has been recorded in the memory audit trail for this agent. That is
            not the same as an empty file: it means nothing has written here yet, or the writes
            predate version recording on this server.
          </p>
          {emptyNote && <p>{emptyNote}</p>}
        </div>
      )}

      {state.status === "ready" && state.entries.length > 0 && (
        <div className="space-y-3">
          <pre className="type-meta max-h-72 overflow-auto whitespace-pre-wrap font-mono leading-relaxed text-foreground">
            {state.content || "(empty)"}
          </pre>
          <p className="type-meta text-muted-foreground-soft">
            Latest of {state.entries.length} recorded version{state.entries.length === 1 ? "" : "s"} —{" "}
            {latest.bytes} B, written {latest.written_at} by {latest.written_by}.
          </p>
        </div>
      )}
    </DetailCard>
  )
}
