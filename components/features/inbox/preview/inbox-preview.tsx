"use client"

import { useMemo, useState } from "react"
import {
  AlertTriangle, ArrowUpRight, CheckCircle2, CircleDot, Clock, FileDiff,
  MessageSquare, Play, Power, XCircle,
} from "lucide-react"
import type { LucideIcon } from "lucide-react"

import { Appear, DetailCard, Pill, TickRow } from "@/components/ui/detail"
import { SubBar } from "@/components/layout/sub-bar"
import { SidebarCollapseButton } from "@/components/layout/sidebar-kit"
import { Button } from "@/components/ui/button"
import { ListRow } from "@/components/ui/list-row"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { MarkdownContent } from "@/components/features/issues/markdown-content"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import { cn } from "@/lib/utils"

import { InboxExplorer } from "./inbox-explorer"
import type { Bucket, InboxView, SubjectFacet } from "./types"
import {
  CATEGORY_BY_KIND, PREVIEW_ARCHIVE, PREVIEW_ITEMS, PREVIEW_NOW, PREVIEW_USER_ID,
  canRole, isVisibleTo, type PreviewInboxItem, type WorkspaceRole,
} from "./mock-data"

// =============================================================================
// /inbox/preview — the 1.0 inbox design rendered against the real kit.
//
// Chrome is the product's, not this page's: SubBar on top, the sidebar-kit
// explorer rail on the left at 280px, list + detail to the right of it. That
// is the /issues and /routines shape, and an inbox that invented its own would
// be the eleventh near-miss the 1.0 cleanup exists to remove.
//
// Rows come from a fixture set copied out of the Go producers rather than from
// the API, so the page can be opened on any instance and still show the same
// screen. The role switch applies the SAME two rules the server does —
// inboxVisibilityClause for what is listed, canRole for what is decidable — so
// a MANAGER watching an OWNER-only decision grey out is real behaviour.
// =============================================================================

const ROLES: WorkspaceRole[] = ["OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"]

function payloadString(item: PreviewInboxItem, key: string): string {
  const v = item.payload?.[key]
  return typeof v === "string" ? v : ""
}

function payloadNumber(item: PreviewInboxItem, key: string): number | null {
  const v = item.payload?.[key]
  return typeof v === "number" ? v : null
}

function bucketOf(item: PreviewInboxItem): Bucket {
  if (item.blocking || item.kind === "waitpoint" || item.kind === "escalation") return "decisions"
  if (item.kind === "message" && payloadString(item, "chat_url")) return "replies"
  if (item.kind === "message" && payloadString(item, "issue_identifier")) return "review"
  if (item.kind === "message" && payloadString(item, "subkind") === "routine_update") return "routines"
  if (item.kind.startsWith("schedule_")) return "routines"
  return "other"
}

/** The agent or routine a row is ABOUT — payload first, sender as fallback. */
function subjectOf(item: PreviewInboxItem): SubjectFacet | null {
  const agent = payloadString(item, "agent_name") || payloadString(item, "agent_slug")
  if (agent) return { id: agent, label: agent, kind: "agent", count: 0 }
  if (item.sender_type === "agent" && item.sender_name) {
    return { id: item.sender_name, label: item.sender_name, kind: "agent", count: 0 }
  }
  if (item.sender_type === "pipeline" && item.sender_name) {
    return { id: item.sender_name, label: item.sender_name, kind: "pipeline", count: 0 }
  }
  return null
}

/** Fixed-clock relative time — see PREVIEW_NOW. */
function since(iso?: string): string {
  if (!iso) return "—"
  const mins = Math.round((PREVIEW_NOW - Date.parse(iso)) / 60_000)
  if (mins < 1) return "právě teď"
  if (mins < 60) return `před ${mins} min`
  const hrs = Math.round(mins / 60)
  if (hrs < 24) return `před ${hrs} h`
  return new Date(iso).toLocaleDateString("cs-CZ", { day: "numeric", month: "numeric" })
}

function absolute(iso?: string): string {
  if (!iso) return "—"
  return new Date(iso).toLocaleString("cs-CZ", {
    day: "numeric", month: "numeric", hour: "2-digit", minute: "2-digit",
  })
}

/** Minutes left on a waitpoint, from the payload key the current UI drops. */
function expiresIn(item: PreviewInboxItem): number | null {
  const raw = payloadString(item, "timeout_at")
  if (!raw) return null
  const mins = Math.round((Date.parse(raw) - PREVIEW_NOW) / 60_000)
  return Number.isFinite(mins) ? mins : null
}

interface DecisionAction {
  label: string
  icon?: LucideIcon
  intent?: "approve" | "reject" | "neutral"
}

interface DecisionSpec {
  heading: string
  tone: "warn" | "default"
  /** Which canRole action the server demands for this decision. */
  requires: "create" | "manage"
  actions: DecisionAction[]
  /** Rendered when the endpoint this card implies does not exist yet. */
  missingEndpoint?: string
}

/**
 * What this row asks of a human, and which role the server lets do it.
 *
 * The `requires` values are read off the router: waitpoint approve, escalation
 * resolve and routine approve are roleCreate (MANAGER+), while skill-proposal
 * and consolidation approve are roleManage (OWNER/ADMIN). That mismatch — a
 * MANAGER-targeted row whose decision needs ADMIN — is what the role switch in
 * the sub-bar exists to show.
 */
function decisionFor(item: PreviewInboxItem): DecisionSpec | null {
  const sub = payloadString(item, "kind")
  const approveReject: DecisionAction[] = [
    { label: "Schválit", icon: CheckCircle2, intent: "approve" },
    { label: "Zamítnout", icon: XCircle, intent: "reject" },
  ]

  if (item.kind === "waitpoint") {
    return { heading: "Čeká na rozhodnutí", tone: "warn", requires: "create", actions: approveReject }
  }

  if (item.kind === "escalation") {
    if (sub === "skill_proposal") {
      return { heading: "Návrh dovednosti", tone: "warn", requires: "manage", actions: approveReject }
    }
    if (sub === "routine_proposal") {
      return { heading: "Návrh rutiny", tone: "warn", requires: "create", actions: approveReject }
    }
    return {
      heading: "Žádost o přístup",
      tone: "warn",
      requires: "create",
      actions: approveReject,
      missingEndpoint: payloadString(item, "request_type") === "access"
        ? "keeperova žádost nemá dnes resolve endpoint — nutné doplnit"
        : undefined,
    }
  }

  if (item.kind === "schedule_circuit_breaker_tripped") {
    return {
      heading: "Rutina je vypnutá",
      tone: "warn",
      requires: "create",
      actions: [
        { label: "Zapnout rozvrh", icon: Power, intent: "approve" },
        { label: "Poslední běhy", icon: ArrowUpRight, intent: "neutral" },
      ],
      missingEndpoint: "znovuzapnutí rozvrhu potřebuje API + CLI",
    }
  }

  if (item.kind === "schedule_missed") {
    return {
      heading: "Zameškaná spuštění",
      tone: "warn",
      requires: "create",
      actions: [
        { label: "Spustit teď", icon: Play, intent: "approve" },
        { label: "Otevřít rozvrh", icon: ArrowUpRight, intent: "neutral" },
      ],
    }
  }

  if (item.kind === "memory_consolidation") {
    return {
      heading: "Návrh konsolidace paměti",
      tone: "default",
      requires: "manage",
      actions: [
        { label: "Přijmout", icon: CheckCircle2, intent: "approve" },
        { label: "Zamítnout", icon: XCircle, intent: "reject" },
        { label: "Diff", icon: FileDiff, intent: "neutral" },
      ],
    }
  }

  return null
}

/** Non-decision rows still have somewhere to go. */
function jumpFor(item: PreviewInboxItem): { label: string; icon: LucideIcon } | null {
  if (payloadString(item, "chat_url")) return { label: "Otevřít chat", icon: MessageSquare }
  const issue = payloadString(item, "issue_identifier")
  if (issue) return { label: `Otevřít ${issue}`, icon: CircleDot }
  if (payloadString(item, "pipeline_run_id")) return { label: "Otevřít běh", icon: ArrowUpRight }
  return null
}

const OUTCOME_LABEL: Record<string, string> = {
  approved: "schváleno",
  rejected: "zamítnuto",
  archived: "archivováno",
  retried: "znovu spuštěno",
  dismissed: "zavřeno",
  expired: "vyprchalo",
}

const OUTCOME_TONE: Record<string, "success" | "destructive" | "warn" | "blue" | "default"> = {
  approved: "success",
  rejected: "destructive",
  archived: "default",
  retried: "blue",
  expired: "warn",
}

export interface InboxPreviewProps {
  initialRole?: WorkspaceRole
  initialView?: InboxView
  initialSelectedId?: string
}

export function InboxPreview({
  initialRole = "OWNER",
  initialView = "inbox",
  initialSelectedId,
}: InboxPreviewProps) {
  const [role, setRole] = useState<WorkspaceRole>(initialRole)
  const [view, setView] = useState<InboxView>(initialView)
  const [bucket, setBucket] = useState<Bucket | null>(null)
  const [subject, setSubject] = useState<string | null>(null)
  const [search, setSearch] = useState("")
  const [outcome, setOutcome] = useState<string | null>(null)
  const [actor, setActor] = useState<string | null>(null)
  const [period, setPeriod] = useState("30")
  const [selectedId, setSelectedId] = useState<string | null>(initialSelectedId ?? null)
  const [leftCollapsed, setLeftCollapsed] = useState(false)

  const archive = view === "archived"
  const source = archive ? PREVIEW_ARCHIVE : PREVIEW_ITEMS

  const visible = useMemo(
    () => source.filter((it) => isVisibleTo(it, role, PREVIEW_USER_ID)),
    [source, role],
  )

  const viewCounts = useMemo<Record<InboxView, number>>(() => {
    const live = PREVIEW_ITEMS.filter((it) => isVisibleTo(it, role, PREVIEW_USER_ID))
    return {
      inbox: live.length,
      unread: live.filter((it) => it.state === "unread").length,
      archived: PREVIEW_ARCHIVE.filter((it) => isVisibleTo(it, role, PREVIEW_USER_ID)).length,
    }
  }, [role])

  const bucketCounts = useMemo(() => {
    const counts: Record<Bucket, number> = { decisions: 0, replies: 0, review: 0, routines: 0, other: 0 }
    for (const it of visible) counts[bucketOf(it)] += 1
    return counts
  }, [visible])

  const subjects = useMemo<SubjectFacet[]>(() => {
    const map = new Map<string, SubjectFacet>()
    for (const it of visible) {
      const s = subjectOf(it)
      if (!s) continue
      const found = map.get(s.id)
      if (found) found.count += 1
      else map.set(s.id, { ...s, count: 1 })
    }
    return [...map.values()].sort((a, b) => b.count - a.count || a.label.localeCompare(b.label))
  }, [visible])

  const outcomeCounts = useMemo(() => {
    const map = new Map<string, number>()
    for (const it of visible) {
      const key = it.resolved_action ?? "—"
      map.set(key, (map.get(key) ?? 0) + 1)
    }
    return [...map.entries()].map(([id, count]) => ({ id, label: OUTCOME_LABEL[id] ?? id, count }))
  }, [visible])

  const actorCounts = useMemo(() => {
    const map = new Map<string, number>()
    for (const it of visible) {
      if (!it.resolved_by_user_id) continue
      map.set(it.resolved_by_user_id, (map.get(it.resolved_by_user_id) ?? 0) + 1)
    }
    return [...map.entries()].map(([id, count]) => ({ id, count }))
  }, [visible])

  const rows = useMemo(() => {
    const q = search.trim().toLowerCase()
    return visible.filter((it) => {
      if (view === "unread" && it.state !== "unread") return false
      if (!archive && bucket && bucketOf(it) !== bucket) return false
      if (archive && outcome && it.resolved_action !== outcome) return false
      if (archive && actor && it.resolved_by_user_id !== actor) return false
      if (subject && subjectOf(it)?.id !== subject) return false
      if (q) {
        const hay = `${it.title} ${it.sender_name ?? ""} ${it.body_md ?? ""}`.toLowerCase()
        if (!hay.includes(q)) return false
      }
      return true
    })
  }, [visible, view, archive, bucket, outcome, actor, subject, search])

  const selected = useMemo(
    () => rows.find((it) => it.id === selectedId) ?? rows[0] ?? null,
    [rows, selectedId],
  )

  const explorer = (
    <InboxExplorer
      view={view}
      onViewChange={(v) => {
        setView(v)
        setBucket(null)
        setOutcome(null)
        setActor(null)
        setSubject(null)
        setSelectedId(null)
      }}
      viewCounts={viewCounts}
      bucket={bucket}
      onBucketChange={(b) => { setBucket(b); setSelectedId(null) }}
      bucketCounts={bucketCounts}
      subjects={subjects}
      selectedSubject={subject}
      onSubjectChange={(s) => { setSubject(s); setSelectedId(null) }}
      outcome={outcome}
      onOutcomeChange={(o) => { setOutcome(o); setSelectedId(null) }}
      outcomeCounts={outcomeCounts}
      actor={actor}
      onActorChange={(a) => { setActor(a); setSelectedId(null) }}
      actorCounts={actorCounts}
      period={period}
      onPeriodChange={setPeriod}
      search={search}
      onSearchChange={setSearch}
      onToggleCollapse={() => setLeftCollapsed(true)}
    />
  )

  return (
    <div className="flex h-[calc(100vh-3rem)] flex-col">
      <SubBar
        icon={CONCEPT_ICON.inbox}
        title="Inbox"
        section={archive ? "Archiv" : undefined}
        ariaLabel="Inbox"
        description={
          archive
            ? `${rows.length} z ${visible.length} vyřízených`
            : `${rows.length} položek · ${visible.filter((i) => i.state === "unread").length} nepřečtených`
        }
        meta={<Pill tone="purple" className="ml-2">náhled designu</Pill>}
        actions={<RoleSwitch role={role} onChange={setRole} />}
      />

      <div className="flex flex-1 overflow-hidden">
        <aside
          className={cn(
            "shrink-0 overflow-hidden border-r border-white/[0.06] bg-card transition-all",
            leftCollapsed ? "w-9" : "w-[280px]",
          )}
        >
          {leftCollapsed ? (
            <div className="flex h-full flex-col items-center pt-1.5">
              <SidebarCollapseButton collapsed onToggle={() => setLeftCollapsed(false)} />
            </div>
          ) : (
            explorer
          )}
        </aside>

        {archive ? (
          <ArchiveTable rows={rows} total={visible.length} />
        ) : (
          <div className="flex min-w-0 flex-1 overflow-hidden">
            <div className="flex w-[380px] shrink-0 flex-col overflow-y-auto border-r border-white/[0.06] bg-card/50">
              {rows.length === 0 && (
                <p className="type-row px-4 py-10 text-center text-muted-foreground-soft">
                  Tady pro tebe nic není.
                </p>
              )}
              <ul>
                {rows.map((item) => (
                  <PreviewRow
                    key={item.id}
                    item={item}
                    role={role}
                    selected={selected?.id === item.id}
                    onSelect={() => setSelectedId(item.id)}
                  />
                ))}
              </ul>
              <div className="type-meta mt-auto flex items-center gap-2 border-t border-hairline px-4 py-2 text-muted-foreground-soft">
                <span>{rows.length} z {visible.length}</span>
                <span className="ml-auto">vše načteno</span>
              </div>
            </div>

            <div className="min-w-0 flex-1 overflow-y-auto p-4">
              {selected ? (
                <ItemDetail key={selected.id} item={selected} role={role} />
              ) : (
                <p className="type-row px-4 py-10 text-center text-muted-foreground-soft">
                  Vyber položku vlevo.
                </p>
              )}
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

function RoleSwitch({ role, onChange }: { role: WorkspaceRole; onChange: (r: WorkspaceRole) => void }) {
  return (
    <div className="flex items-center gap-1 rounded-md border border-border/60 bg-surface-subtle p-0.5">
      <span className="type-meta px-1.5 text-muted-foreground-soft">Role</span>
      {ROLES.map((r) => (
        <button
          key={r}
          type="button"
          onClick={() => onChange(r)}
          aria-pressed={role === r}
          className={cn(
            "type-meta rounded px-1.5 py-0.5 font-medium transition-colors",
            role === r ? "bg-primary/20 text-primary" : "text-muted-foreground hover:text-foreground",
          )}
        >
          {r}
        </button>
      ))}
    </div>
  )
}

/**
 * The face belongs to the SUBJECT, not the sender.
 *
 * Ten producers write their rows as sender_type=system ("Keeper", "Skill
 * Curator", "Memory Health") while carrying agent_name in the payload — the
 * agent the row is actually about. Keying the avatar off sender_type alone, as
 * the live inbox does, puts a system glyph on "casey žádá GH_TOKEN".
 */
function SenderFace({ item, className }: { item: PreviewInboxItem; className?: string }) {
  const subject = payloadString(item, "agent_name") || payloadString(item, "agent_slug")

  if (item.sender_type === "agent" && (item.avatar_seed || item.sender_name)) {
    return (
      <AgentAvatar
        seed={item.avatar_seed || item.sender_name || "agent"}
        style={item.avatar_style}
        className={cn("shrink-0 rounded-md object-cover", className ?? "h-6 w-6")}
      />
    )
  }
  if (subject) {
    return (
      <AgentAvatar seed={subject} className={cn("shrink-0 rounded-md object-cover", className ?? "h-6 w-6")} />
    )
  }
  const Icon =
    item.sender_type === "pipeline" ? CONCEPT_ICON.routines
      : item.sender_type === "crew" ? CONCEPT_ICON.crews
        : CONCEPT_ICON.admin
  const tone = item.sender_type === "pipeline" ? "bg-purple/20 text-purple" : "bg-notice/20 text-notice"
  return (
    <span className={cn("grid shrink-0 place-items-center rounded-md", tone, className ?? "h-6 w-6")}>
      <Icon className="h-3.5 w-3.5" />
    </span>
  )
}

function PreviewRow({
  item, role, selected, onSelect,
}: {
  item: PreviewInboxItem
  role: WorkspaceRole
  selected: boolean
  onSelect: () => void
}) {
  const spec = decisionFor(item)
  const blocked = spec != null && !canRole(role, spec.requires)
  const mins = expiresIn(item)

  return (
    <ListRow selected={selected} onSelect={onSelect} className="items-start gap-2.5 px-4 py-2.5">
      <span
        className={cn(
          "mt-2 h-1.5 w-1.5 shrink-0 rounded-full",
          item.state === "unread" ? "bg-primary" : "bg-transparent",
        )}
      />
      <SenderFace item={item} className="mt-0.5 h-6 w-6" />
      <span className="min-w-0 flex-1">
        <span
          className={cn(
            "type-row block truncate",
            item.state === "unread" ? "font-medium text-foreground" : "text-muted-foreground",
          )}
        >
          {item.title}
        </span>
        <span className="mt-1 flex flex-wrap items-center gap-1.5">
          {mins != null && mins > 0 && <Pill tone="destructive">vyprší za {mins} m</Pill>}
          {blocked && <Pill tone="default">rozhodne ADMIN</Pill>}
          <span className="type-meta font-mono text-muted-foreground-soft">
            {CATEGORY_BY_KIND[item.kind] ?? item.kind}
          </span>
          <span className="type-meta text-muted-foreground-soft">{since(item.created_at)}</span>
        </span>
      </span>
    </ListRow>
  )
}

function ItemDetail({ item, role }: { item: PreviewInboxItem; role: WorkspaceRole }) {
  const spec = decisionFor(item)
  const jump = jumpFor(item)
  const mins = expiresIn(item)
  const category = CATEGORY_BY_KIND[item.kind] ?? "—"

  return (
    <div className="flex flex-col gap-3">
      {spec && (
        <Appear order={0}>
          <DetailCard
            tone={spec.tone === "warn" ? "warn" : "default"}
            className={spec.tone === "warn" ? "bg-warn/[.06]" : undefined}
          >
            <div data-testid="decision-card" className="flex flex-col gap-3">
              <div className="flex items-center gap-2">
                <AlertTriangle
                  className={cn("h-4 w-4", spec.tone === "warn" ? "text-warn" : "text-muted-foreground")}
                />
                <span className={cn("type-section", spec.tone === "warn" ? "text-warn" : "text-foreground/70")}>
                  {spec.heading}
                </span>
                {mins != null && mins > 0 && (
                  <span className="type-meta ml-auto font-mono text-destructive">
                    vyprší {absolute(payloadString(item, "timeout_at"))} · za {mins} min
                  </span>
                )}
              </div>

              <div className="text-body font-semibold">{item.title}</div>

              <DecisionSubject item={item} />

              <div className="flex flex-wrap items-center gap-2">
                {spec.actions.map((a) => (
                  <Button
                    key={a.label}
                    size="sm"
                    disabled={!canRole(role, spec.requires)}
                    variant={a.intent === "approve" ? "soft" : "outline"}
                    className={cn(
                      "gap-1.5",
                      a.intent === "approve" &&
                        "border-success/30 bg-success/15 text-success hover:bg-success/25 hover:text-success",
                    )}
                  >
                    {a.icon && <a.icon className="h-3 w-3" />}
                    {a.label}
                  </Button>
                ))}
                {canRole(role, spec.requires) ? (
                  <span className="type-meta ml-auto text-muted-foreground-soft">
                    rozhodne kdokoli z {spec.requires === "manage" ? "OWNER / ADMIN" : "MANAGER+"}
                  </span>
                ) : (
                  <span className="type-meta text-muted-foreground">
                    rozhodne {spec.requires === "manage" ? "OWNER nebo ADMIN" : "MANAGER a výš"}
                  </span>
                )}
              </div>

              {spec.missingEndpoint && (
                <p className="type-meta rounded-md border border-dashed border-border/60 px-2.5 py-1.5 font-mono text-muted-foreground-soft">
                  chybí na serveru: {spec.missingEndpoint}
                </p>
              )}
            </div>
          </DetailCard>
        </Appear>
      )}

      <Appear order={1}>
        <DetailCard bare>
          <div className="grid grid-cols-2 divide-x divide-hairline sm:grid-cols-4">
            <Definition label="Subjekt" value={subjectOf(item)?.label ?? item.sender_name ?? "—"} field="payload.agent_name" />
            <Definition
              label="Crew"
              value={payloadString(item, "invoking_crew_id") || payloadString(item, "crew_id") || "—"}
              field="crew_id"
            />
            <Definition label="Kategorie" value={category} field="odvozeno z kind" mono />
            <Definition label="Přišlo" value={absolute(item.created_at)} field="created_at" />
          </div>
        </DetailCard>
      </Appear>

      <RunProgress item={item} />

      {item.body_md && (
        <Appear order={3}>
          <DetailCard title="Zpráva" icon={CONCEPT_ICON.inbox}>
            <MarkdownContent compact>{item.body_md}</MarkdownContent>
          </DetailCard>
        </Appear>
      )}

      <Appear order={4}>
        <DetailCard bare>
          <div className="type-meta flex flex-wrap items-center gap-3 px-4 py-2 text-muted-foreground-soft">
            <button type="button" className="hover:text-foreground">Označit nepřečtené</button>
            <span>·</span>
            <button type="button" className="hover:text-foreground">Archivovat</button>
            {jump && (
              <Button size="xs" variant="ghost" className="ml-auto gap-1.5 text-primary">
                <jump.icon className="h-3 w-3" />
                {jump.label}
              </Button>
            )}
            <button type="button" className={cn("text-primary hover:underline", !jump && "ml-auto")}>
              Nastavit doručování {category}
            </button>
          </div>
        </DetailCard>
      </Appear>
    </div>
  )
}

/**
 * The one or two payload fields that answer "why should I say yes". Today they
 * sit in the Context key/value dump underneath the actions, at the same weight
 * as request_id.
 */
function DecisionSubject({ item }: { item: PreviewInboxItem }) {
  const intent = payloadString(item, "intent")
  const credential = payloadString(item, "credential_name")
  const level = payloadString(item, "security_level")
  const risk = payloadNumber(item, "risk_score")
  const scan = payloadString(item, "scan_status")
  const reasons = Array.isArray(item.payload?.risk_reasons)
    ? (item.payload?.risk_reasons as unknown[]).filter((r): r is string => typeof r === "string")
    : []
  const failures = payloadNumber(item, "consecutive_failures")
  const missed = payloadNumber(item, "missed_count")
  const rules = payloadNumber(item, "rules_count")
  const scanned = payloadNumber(item, "entries_scanned")

  const chips: React.ReactNode[] = []
  if (credential) chips.push(<Pill key="cred" tone="warn">{credential}</Pill>)
  if (level) chips.push(<Pill key="lvl" tone="destructive">{level}</Pill>)
  if (risk != null) chips.push(<Pill key="risk" tone="warn">riziko {risk}</Pill>)
  if (scan) chips.push(<Pill key="scan" tone={scan === "clean" ? "success" : "warn"}>scan: {scan}</Pill>)
  if (failures != null) chips.push(<Pill key="fail" tone="destructive">{failures}× selhalo v řadě</Pill>)
  if (missed != null) chips.push(<Pill key="miss" tone="warn">{missed} zameškaná spuštění</Pill>)
  if (rules != null) chips.push(<Pill key="rules" tone="purple">{rules} pravidel</Pill>)
  if (scanned != null) chips.push(<Pill key="scanned" tone="default">{scanned} záznamů</Pill>)

  if (chips.length === 0 && !intent && reasons.length === 0) return null

  return (
    <div className="flex flex-col gap-2">
      {chips.length > 0 && <div className="flex flex-wrap items-center gap-1.5">{chips}</div>}
      {intent && <p className="type-row leading-snug text-muted-foreground">{intent}</p>}
      {reasons.length > 0 && (
        <ul className="type-meta flex flex-col gap-0.5 text-muted-foreground">
          {reasons.map((r) => (
            <li key={r} className="flex items-center gap-1.5">
              <AlertTriangle className="h-3 w-3 text-warn" />
              {r}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

/** Static stand-in for WaitpointRunDetail, which fetches the real run. */
function RunProgress({ item }: { item: PreviewInboxItem }) {
  const runId = payloadString(item, "pipeline_run_id")
  const step = payloadString(item, "step_id")
  if (!runId || item.kind !== "waitpoint") return null

  const steps: { label: string; status: "ok" | "running" | "pending"; meta?: string }[] = [
    { label: "checkout", status: "ok", meta: "2 s" },
    { label: "build-docs", status: "ok", meta: "48 s" },
    { label: "scan", status: "ok", meta: "6 s" },
    { label: step || "promote", status: "running", meta: "čeká na tebe" },
    { label: "notify", status: "pending" },
  ]

  return (
    <Appear order={2}>
      <DetailCard
        title="Jak běh došel sem"
        icon={Clock}
        subtitle={`krok 4 z ${steps.length}`}
        footer={<span className="font-mono">pipeline_run_id · step_id</span>}
      >
        {steps.map((s) => (
          <TickRow key={s.label} label={s.label} status={s.status} meta={s.meta} />
        ))}
      </DetailCard>
    </Appear>
  )
}

function Definition({
  label, value, field, mono,
}: {
  label: string
  value: string
  field: string
  mono?: boolean
}) {
  return (
    <div className="px-4 py-2">
      <div className="type-meta uppercase tracking-wide text-muted-foreground-soft">{label}</div>
      <div className={cn("type-row mt-0.5 truncate", mono && "font-mono text-[12px]")}>{value}</div>
      <div className="type-meta truncate font-mono text-muted-foreground-soft">{field}</div>
    </div>
  )
}

/**
 * Archive = the whole history, full width, in the catalog table shape /routines
 * uses. Not the split list+detail: the question here is "what did we decide and
 * who decided it", which is a table of outcomes, not one item at a time.
 */
function ArchiveTable({ rows, total }: { rows: PreviewInboxItem[]; total: number }) {
  return (
    <div className="min-w-0 flex-1 overflow-y-auto">
      <div className="flex items-center gap-2 border-b border-hairline px-5 py-3">
        <span className="text-body font-medium">Archiv</span>
        <span className="type-meta text-muted-foreground-soft">
          {rows.length === total ? `${total} vyřízených položek` : `${rows.length} z ${total}`}
        </span>
        <span className="type-meta ml-auto font-mono text-muted-foreground-soft">
          filtry vlevo · na serveru = SQL fasety + kurzor
        </span>
      </div>

      <table className="w-full">
        <thead>
          <tr className="border-b border-hairline">
            {["Položka", "Rozhodnutí", "Kdo", "Kdy", "Odezva", "Kategorie"].map((h) => (
              <th
                key={h}
                className="type-meta px-5 py-2 text-left font-medium uppercase tracking-wide text-muted-foreground-soft"
              >
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((item) => {
            const action = item.resolved_action ?? ""
            const respondedMin = item.resolved_at
              ? Math.round((Date.parse(item.resolved_at) - Date.parse(item.created_at)) / 60_000)
              : null
            return (
              <tr key={item.id} data-row className="border-b border-hairline/60 hover:bg-white/[0.02]">
                <td className="px-5 py-2.5">
                  <div className="flex items-center gap-2.5">
                    <SenderFace item={item} className="h-6 w-6" />
                    <div className="min-w-0">
                      <div className="type-row truncate">{item.title}</div>
                      <div className="type-meta truncate font-mono text-muted-foreground-soft">
                        {item.sender_name}
                      </div>
                    </div>
                  </div>
                </td>
                <td className="px-5 py-2.5">
                  <Pill tone={OUTCOME_TONE[action] ?? "default"}>{OUTCOME_LABEL[action] ?? action}</Pill>
                </td>
                <td className="type-row px-5 py-2.5 text-muted-foreground">
                  {item.resolved_by_user_id ?? "—"}
                </td>
                <td className="type-meta px-5 py-2.5 font-mono text-muted-foreground-soft">
                  {absolute(item.resolved_at)}
                </td>
                <td className="type-meta px-5 py-2.5 font-mono tabular-nums text-muted-foreground-soft">
                  {respondedMin == null
                    ? "—"
                    : respondedMin < 60
                      ? `${respondedMin} min`
                      : `${Math.round(respondedMin / 60)} h`}
                </td>
                <td className="type-meta px-5 py-2.5 font-mono text-muted-foreground-soft">
                  {CATEGORY_BY_KIND[item.kind]}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>

      {rows.length === 0 && (
        <p className="type-row px-5 py-10 text-center text-muted-foreground-soft">
          Tomuhle filtru nic neodpovídá.
        </p>
      )}
    </div>
  )
}
