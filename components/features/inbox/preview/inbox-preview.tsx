"use client"

import { useMemo, useState } from "react"
import {
  AlertTriangle, ArrowUpRight, CheckCircle2, CircleDot, Clock, FileDiff, ListChecks,
  MessageSquare, Play, Power, Search, SlidersHorizontal, XCircle,
} from "lucide-react"
import type { LucideIcon } from "lucide-react"

import { Appear, DetailCard, Pill, TickRow } from "@/components/ui/detail"
import { SubBar, SubBarIconButton } from "@/components/layout/sub-bar"
import { Button } from "@/components/ui/button"
import { ListRow } from "@/components/ui/list-row"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { MarkdownContent } from "@/components/features/issues/markdown-content"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import { cn } from "@/lib/utils"

import {
  CATEGORY_BY_KIND, PREVIEW_ARCHIVE, PREVIEW_ITEMS, PREVIEW_NOW, PREVIEW_USER_ID,
  canRole, isVisibleTo, type PreviewInboxItem, type WorkspaceRole,
} from "./mock-data"

// =============================================================================
// /inbox/preview — the 1.0 inbox design rendered against the real kit.
//
// It reads its rows from a fixture set copied out of the Go producers rather
// than from the API, so it can be opened on any instance and still show the
// same screen. Everything else — chrome, tokens, type roles, cards, rows — is
// the production component, so what you see here is what the page would look
// like, not an approximation of it.
//
// The role switch in the sub-bar is the point of the RBAC section: it applies
// the SAME two rules the server does — inboxVisibilityClause for what you see,
// canRole for what you may decide — so a MANAGER watching an OWNER-only
// decision get greyed out is the real behaviour, not a mock-up of it.
// =============================================================================

type Tab = "inbox" | "unread" | "archived"
type Bucket = "decisions" | "replies" | "review" | "routines" | "other"

const ROLES: WorkspaceRole[] = ["OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"]

const BUCKETS: { id: Bucket; label: string; testId: string }[] = [
  { id: "decisions", label: "Rozhodnutí", testId: "facet-bucket-decisions" },
  { id: "replies", label: "Odpovědi", testId: "facet-bucket-replies" },
  { id: "review", label: "K revizi", testId: "facet-bucket-review" },
  { id: "routines", label: "Průběh rutin", testId: "facet-bucket-routines" },
  { id: "other", label: "Ostatní", testId: "facet-bucket-other" },
]

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

/** Fixed-clock relative time — see PREVIEW_NOW. */
function since(iso?: string): string {
  if (!iso) return "—"
  const diff = PREVIEW_NOW - Date.parse(iso)
  const mins = Math.round(diff / 60_000)
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
 * MANAGER-targeted row whose decision needs ADMIN — is what the RBAC column of
 * this preview is for.
 */
function decisionFor(item: PreviewInboxItem): DecisionSpec | null {
  const sub = payloadString(item, "kind")

  if (item.kind === "waitpoint") {
    return {
      heading: "Čeká na rozhodnutí",
      tone: "warn",
      requires: "create",
      actions: [
        { label: "Schválit", icon: CheckCircle2, intent: "approve" },
        { label: "Zamítnout", icon: XCircle, intent: "reject" },
      ],
    }
  }

  if (item.kind === "escalation") {
    if (sub === "skill_proposal") {
      return {
        heading: "Návrh dovednosti",
        tone: "warn",
        requires: "manage",
        actions: [
          { label: "Schválit", icon: CheckCircle2, intent: "approve" },
          { label: "Zamítnout", icon: XCircle, intent: "reject" },
        ],
      }
    }
    if (sub === "routine_proposal") {
      return {
        heading: "Návrh rutiny",
        tone: "warn",
        requires: "create",
        actions: [
          { label: "Schválit", icon: CheckCircle2, intent: "approve" },
          { label: "Zamítnout", icon: XCircle, intent: "reject" },
        ],
      }
    }
    return {
      heading: "Žádost o přístup",
      tone: "warn",
      requires: "create",
      actions: [
        { label: "Schválit", icon: CheckCircle2, intent: "approve" },
        { label: "Zamítnout", icon: XCircle, intent: "reject" },
      ],
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

export interface InboxPreviewProps {
  initialRole?: WorkspaceRole
  initialTab?: Tab
  initialSelectedId?: string
}

export function InboxPreview({
  initialRole = "OWNER",
  initialTab = "inbox",
  initialSelectedId,
}: InboxPreviewProps) {
  const [role, setRole] = useState<WorkspaceRole>(initialRole)
  const [tab, setTab] = useState<Tab>(initialTab)
  const [bucket, setBucket] = useState<Bucket | null>(null)
  const [onlyMine, setOnlyMine] = useState(false)
  const [selectedId, setSelectedId] = useState<string | null>(initialSelectedId ?? null)

  const source = tab === "archived" ? PREVIEW_ARCHIVE : PREVIEW_ITEMS

  const visible = useMemo(
    () => source.filter((it) => isVisibleTo(it, role, PREVIEW_USER_ID)),
    [source, role],
  )

  const bucketCounts = useMemo(() => {
    const counts: Record<Bucket, number> = {
      decisions: 0, replies: 0, review: 0, routines: 0, other: 0,
    }
    for (const it of visible) counts[bucketOf(it)] += 1
    return counts
  }, [visible])

  const rows = useMemo(() => {
    return visible.filter((it) => {
      if (tab === "unread" && it.state !== "unread") return false
      if (bucket && bucketOf(it) !== bucket) return false
      if (onlyMine) {
        const spec = decisionFor(it)
        if (!spec || !canRole(role, spec.requires)) return false
      }
      return true
    })
  }, [visible, tab, bucket, onlyMine, role])

  const selected = useMemo(
    () => rows.find((it) => it.id === selectedId) ?? rows[0] ?? null,
    [rows, selectedId],
  )

  const grouped = useMemo(() => {
    const map = new Map<Bucket, PreviewInboxItem[]>()
    for (const it of rows) {
      const b = bucketOf(it)
      const list = map.get(b)
      if (list) list.push(it)
      else map.set(b, [it])
    }
    return BUCKETS.filter((b) => map.has(b.id)).map((b) => ({
      ...b,
      items: map.get(b.id) ?? [],
    }))
  }, [rows])

  const unreadCount = visible.filter((it) => it.state === "unread").length

  return (
    <div className="flex min-h-[calc(100vh-7rem)] flex-col">
      <SubBar
        icon={CONCEPT_ICON.inbox}
        title="Inbox"
        ariaLabel="Inbox"
        description={`${visible.length} položek · ${unreadCount} nepřečtených`}
        meta={
          <Pill tone="purple" className="ml-2">
            náhled designu
          </Pill>
        }
        tabs={[
          { id: "inbox", label: "Inbox", badge: PREVIEW_ITEMS.length },
          { id: "unread", label: "Nepřečtené", badge: unreadCount },
          { id: "archived", label: "Archiv", badge: PREVIEW_ARCHIVE.length },
        ]}
        activeTab={tab}
        onTabChange={(id) => {
          setTab(id as Tab)
          setSelectedId(null)
        }}
        actions={
          <>
            <RoleSwitch role={role} onChange={setRole} />
            <SubBarIconButton icon={Search} aria-label="Hledat" />
            <SubBarIconButton icon={ListChecks} aria-label="Vybrat" />
            <SubBarIconButton icon={SlidersHorizontal} aria-label="Zobrazení" />
          </>
        }
      />

      {tab === "archived" ? (
        <ArchiveView items={rows} />
      ) : (
        <>
          <FacetBar
            counts={bucketCounts}
            active={bucket}
            onPick={(b) => {
              setBucket(b)
              setSelectedId(null)
            }}
            onlyMine={onlyMine}
            onToggleMine={() => setOnlyMine((v) => !v)}
          />

          <div className="flex min-h-0 flex-1">
            <div className="w-[428px] shrink-0 overflow-y-auto border-r border-hairline bg-card/50">
              {grouped.length === 0 && (
                <p className="type-row px-4 py-10 text-center text-muted-foreground-soft">
                  Tady pro tebe nic není.
                </p>
              )}
              {grouped.map((group) => (
                <div key={group.id}>
                  <div className="flex items-center gap-2 border-b border-hairline bg-surface-subtle px-4 py-1.5">
                    <span className="type-section text-foreground/70">{group.label}</span>
                    <span className="type-meta ml-auto font-mono text-muted-foreground-soft">
                      {group.items.length}
                    </span>
                  </div>
                  <ul>
                    {group.items.map((item) => (
                      <PreviewRow
                        key={item.id}
                        item={item}
                        role={role}
                        selected={selected?.id === item.id}
                        onSelect={() => setSelectedId(item.id)}
                      />
                    ))}
                  </ul>
                </div>
              ))}
              <div className="type-meta flex items-center gap-2 border-t border-hairline px-4 py-2 text-muted-foreground-soft">
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
        </>
      )}
    </div>
  )
}

function RoleSwitch({
  role, onChange,
}: {
  role: WorkspaceRole
  onChange: (r: WorkspaceRole) => void
}) {
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
            role === r
              ? "bg-primary/20 text-primary"
              : "text-muted-foreground hover:text-foreground",
          )}
        >
          {r}
        </button>
      ))}
    </div>
  )
}

function FacetBar({
  counts, active, onPick, onlyMine, onToggleMine,
}: {
  counts: Record<Bucket, number>
  active: Bucket | null
  onPick: (b: Bucket | null) => void
  onlyMine: boolean
  onToggleMine: () => void
}) {
  const total = Object.values(counts).reduce((a, b) => a + b, 0)
  return (
    <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-hairline bg-card/40 px-4 py-2">
      <span className="type-meta uppercase tracking-wide text-muted-foreground-soft">Košík</span>
      <FacetChip label="Vše" count={total} active={active === null} onClick={() => onPick(null)} />
      {BUCKETS.map((b) => (
        <FacetChip
          key={b.id}
          testId={b.testId}
          label={b.label}
          count={counts[b.id]}
          tone={b.id === "decisions" ? "warn" : "default"}
          active={active === b.id}
          onClick={() => onPick(active === b.id ? null : b.id)}
        />
      ))}
      <span className="mx-1 h-4 w-px bg-border/60" />
      <FacetChip label="Jen co můžu rozhodnout" active={onlyMine} onClick={onToggleMine} />
    </div>
  )
}

function FacetChip({
  label, count, active, onClick, tone = "default", testId,
}: {
  label: string
  count?: number
  active: boolean
  onClick: () => void
  tone?: "default" | "warn"
  testId?: string
}) {
  return (
    <button
      type="button"
      data-testid={testId}
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        "type-meta inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 font-medium transition-colors",
        active
          ? tone === "warn"
            ? "bg-warn/20 text-warn"
            : "bg-primary/20 text-primary"
          : "bg-white/[0.04] text-muted-foreground hover:text-foreground",
      )}
    >
      {label}
      {count != null && <span className="font-mono opacity-70">{count}</span>}
    </button>
  )
}

/**
 * The face belongs to the SUBJECT, not the sender.
 *
 * Ten producers write their rows as sender_type=system ("Keeper", "Skill
 * Curator", "Memory Health") while carrying agent_name in the payload — the
 * agent the row is actually about. Keying the avatar off sender_type alone,
 * as the live inbox does, puts a system glyph on "casey žádá GH_TOKEN".
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
      <AgentAvatar
        seed={subject}
        className={cn("shrink-0 rounded-md object-cover", className ?? "h-6 w-6")}
      />
    )
  }
  const Icon =
    item.sender_type === "pipeline" ? CONCEPT_ICON.routines
      : item.sender_type === "crew" ? CONCEPT_ICON.crews
        : CONCEPT_ICON.admin
  const tone =
    item.sender_type === "pipeline" ? "bg-purple/20 text-purple"
      : "bg-notice/20 text-notice"
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
          {mins != null && mins > 0 && (
            <Pill tone="destructive">vyprší za {mins} m</Pill>
          )}
          {blocked && <Pill tone="default">rozhodne ADMIN</Pill>}
          {item.priority === "urgent" && <Pill tone="destructive">urgent</Pill>}
          <span className="type-meta font-mono text-muted-foreground-soft">
            {CATEGORY_BY_KIND[item.kind] ?? item.kind}
          </span>
          <span className="type-meta text-muted-foreground-soft">
            {item.sender_name} · {since(item.created_at)}
          </span>
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
                <AlertTriangle className={cn("h-4 w-4", spec.tone === "warn" ? "text-warn" : "text-muted-foreground")} />
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
                {!canRole(role, spec.requires) && (
                  <span className="type-meta text-muted-foreground">
                    rozhodne {spec.requires === "manage" ? "OWNER nebo ADMIN" : "MANAGER a výš"}
                  </span>
                )}
                {canRole(role, spec.requires) && (
                  <span className="type-meta ml-auto text-muted-foreground-soft">
                    rozhodne kdokoli z {spec.requires === "manage" ? "OWNER / ADMIN" : "MANAGER+"}
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
            <Definition label="Subjekt" value={item.sender_name ?? "—"} field="sender_name" />
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
  if (scanned != null) chips.push(<Pill key="scan2" tone="default">{scanned} záznamů</Pill>)

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

const OUTCOME_TONE: Record<string, "success" | "destructive" | "default"> = {
  approved: "success",
  rejected: "destructive",
  archived: "default",
}

const OUTCOME_LABEL: Record<string, string> = {
  approved: "schváleno",
  rejected: "zamítnuto",
  archived: "archivováno",
  retried: "znovu spuštěno",
  dismissed: "zavřeno",
}

/**
 * Every facet count here is counted off the rows on screen. The real thing has
 * to get them from SQL — the list query is LIMIT 100 with no cursor, so an
 * archive facet computed client-side would be a count of the last hundred rows
 * wearing the label of the whole history.
 */
function ArchiveView({ items }: { items: PreviewInboxItem[] }) {
  const [outcome, setOutcome] = useState<string | null>(null)

  const byOutcome = useMemo(() => {
    const counts = new Map<string, number>()
    for (const it of items) {
      const key = it.resolved_action ?? "—"
      counts.set(key, (counts.get(key) ?? 0) + 1)
    }
    return counts
  }, [items])

  const byActor = useMemo(() => {
    const counts = new Map<string, number>()
    for (const it of items) {
      const key = it.resolved_by_user_id ?? "—"
      counts.set(key, (counts.get(key) ?? 0) + 1)
    }
    return counts
  }, [items])

  const shown = outcome ? items.filter((it) => it.resolved_action === outcome) : items

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-hairline bg-card/40 px-4 py-2">
        <span className="type-meta uppercase tracking-wide text-muted-foreground-soft">Rozhodnutí</span>
        <FacetChip label="Vše" count={items.length} active={outcome === null} onClick={() => setOutcome(null)} />
        {[...byOutcome.entries()].map(([action, count]) => (
          <FacetChip
            key={action}
            label={OUTCOME_LABEL[action] ?? action}
            count={count}
            active={outcome === action}
            onClick={() => setOutcome(outcome === action ? null : action)}
          />
        ))}
        <span className="mx-1 h-4 w-px bg-border/60" />
        <span className="type-meta uppercase tracking-wide text-muted-foreground-soft">Kdo</span>
        {[...byActor.entries()].map(([actor, count]) => (
          <FacetChip key={actor} label={actor} count={count} active={false} onClick={() => {}} />
        ))}
        <span className="mx-1 h-4 w-px bg-border/60" />
        <span className="type-meta uppercase tracking-wide text-muted-foreground-soft">Období</span>
        <FacetChip label="30 dní" active onClick={() => {}} />
      </div>

      <ul className="min-h-0 flex-1 overflow-y-auto">
        {shown.map((item) => {
          const action = item.resolved_action ?? ""
          const responded = item.resolved_at
            ? Math.round((Date.parse(item.resolved_at) - Date.parse(item.created_at)) / 60_000)
            : null
          return (
            <li key={item.id} data-row className="flex items-start gap-2.5 border-b border-hairline px-4 py-2.5">
              <SenderFace item={item} className="mt-0.5 h-6 w-6" />
              <div className="min-w-0 flex-1">
                <div className="type-row truncate text-muted-foreground">{item.title}</div>
                <div className="mt-1 flex flex-wrap items-center gap-1.5">
                  <Pill tone={OUTCOME_TONE[action] ?? "default"}>{OUTCOME_LABEL[action] ?? action}</Pill>
                  <span className="type-meta text-muted-foreground">{item.resolved_by_user_id}</span>
                  <span className="type-meta text-muted-foreground-soft">
                    · {absolute(item.resolved_at)}
                    {responded != null && ` · odezva ${responded < 60 ? `${responded} min` : `${Math.round(responded / 60)} h`}`}
                  </span>
                </div>
              </div>
              <span className="type-meta shrink-0 font-mono text-muted-foreground-soft">
                {CATEGORY_BY_KIND[item.kind]}
              </span>
            </li>
          )
        })}
      </ul>

      <div className="type-meta flex shrink-0 items-center gap-2 border-t border-hairline px-4 py-2 text-muted-foreground-soft">
        <span>{shown.length} z {items.length}</span>
        <span className="ml-auto">stránkování a hledání v těle patří na server</span>
      </div>
    </div>
  )
}
