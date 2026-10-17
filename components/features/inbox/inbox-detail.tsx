"use client"

import { useState } from "react"
import Link from "next/link"
import {
  Activity, AlertTriangle, ArrowUpRight, Brain, CalendarClock, CircleDot, Eye, EyeOff, HelpCircle, KeyRound,
  Link2, ListChecks, MessageSquare, MoreHorizontal, PauseCircle, ScrollText, ShieldCheck, Sparkles, UserPlus, Users,
} from "lucide-react"
import type { LucideIcon } from "lucide-react"

import { Pill, type DetailTone } from "@/components/ui/detail"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { StatusPill } from "@/components/ui/status-pill"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { toStatusTone } from "@/lib/status-tone"
import { entityHref } from "@/lib/entity-links"
import { hostOf } from "@/lib/routine-step-describe"
import { isDecisionForm } from "@/lib/decision-form"
import { cn } from "@/lib/utils"
import { EMPTY_INBOX_LOOKUP, type InboxLookup } from "@/components/features/inbox-v2/inbox-v2-types"
import { entryIdentity } from "@/components/features/inbox-v2/inbox-entry-identity"
import { inboxEntry } from "@/components/features/inbox-v2/inbox-v2-derive"
import {
  DecisionBlock, DetailExpand, DetailFrame, Dock, HistoryReceipt, OriginChips, RailPart, RoleNote, outcomeLabel, type DecisionTone,
} from "@/components/features/inbox-v2/inbox-detail-frame"
import { MarkdownContent } from "@/components/features/issues/markdown-content"
import { FourEyesNotice } from "@/components/features/escalations/four-eyes-notice"
import type { InboxItem } from "@/hooks/use-inbox"

import { RoutineProposalDiff } from "./routine-proposal-diff"
import { EvidenceFacts } from "./evidence-facts"
import { ActReceipt, receiptOf } from "./run-needs-human-actions"
import { KindActions } from "./kind-actions"
import type { InboxActFn } from "./run-needs-human-actions"
import { WaitpointRunDetail } from "./waitpoint-run-detail"
import {
  attentionBadge, canRole, deciderCopy, decisionMetaFor, expiresIn, linkToOpen, payloadNumber, remainingLabel, riskLevelOf,
  payloadString, payloadStrings, safeChatURL, since, type WorkspaceRole,
} from "./inbox-derive"

/**
 * The §12 attention class, as a pill in the detail head (#2398). One place
 * for every card shape so the decision block and the plain card cannot spell
 * it differently.
 */
function AttentionBadge({ item }: { item: InboxItem }) {
  const badge = attentionBadge(item)
  if (!badge) return null
  return <StatusPill tone={toStatusTone(badge.tone)} label={badge.label} data-testid="attention-badge" />
}

// =============================================================================
// The reading pane.
//
// Ordered by the question someone opens an item with: what is being asked of
// me, on what evidence, by whom, and what happens if I do nothing. The
// decision card is first and carries the countdown, because a waitpoint that
// expires is the only thing here with a deadline and the old pane rendered the
// item's AGE instead.
//
// The buttons inside it are KindActions — the same branches that shipped, with
// the endpoints they learned the hard way. This file frames them; it does not
// replace them.
// =============================================================================

// ── Secret redaction (client-side display) ──────────────────────────
// The backend already redacts secrets before they hit body_md, but
// payload values can still carry credential-ish material an agent put
// there. Mask anything that looks like a secret in the Context card and
// reveal it only on explicit click — defense in depth + don't shoulder-
// surf-leak a token sitting in someone's inbox.
// Display-only defense in depth: the backend already redacts real secrets
// out of body_md before they ever reach the client (see inbox.RedactSecrets
// / lookout.Redact — the source of truth). This just hides a credential-
// looking *Context value* behind a reveal toggle. Keep the key vocabulary
// in sync with the backend's kvSecretRe so the two agree on "looks secret"
// (same keys + "credential"). Mask ONLY a credential-named key or a
// connection string with inline creds — a bare skill_id / run_id / crew_id
// is an identifier, not a secret (the thing the user flagged).
const SECRET_KEY_RE =
  /(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|auth|bearer|credential)/i
const SECRET_VAL_RE = /:\/\/[^/@\s]+:[^/@\s]+@/

/**
 * Does anything inside this value look like a credential?
 *
 * Walks a nested object rather than trusting the top-level key: the masking
 * rule keys off the NAME, and a name like "metadata" or "inputs" says nothing
 * about what is underneath it.
 */
function looksSecretNested(value: unknown, depth = 0): boolean {
  if (depth > 4) return false
  if (typeof value === "string") return SECRET_VAL_RE.test(value)
  if (Array.isArray(value)) return value.some((v) => looksSecretNested(v, depth + 1))
  if (value && typeof value === "object") {
    return Object.entries(value as Record<string, unknown>).some(
      ([k, v]) => SECRET_KEY_RE.test(k) || looksSecretNested(v, depth + 1),
    )
  }
  return false
}

function looksSecret(key: string, value: string): boolean {
  return SECRET_KEY_RE.test(key) || SECRET_VAL_RE.test(value)
}

function RevealableValue({ value }: { value: string }) {
  const [shown, setShown] = useState(false)
  return (
    <span className="inline-flex items-center gap-1.5">
      <span className="font-mono text-label text-foreground/80">
        {shown ? value : "••••••••"}
      </span>
      {/* v67: the word, not a bare eye — what the click does is the label. */}
      <button
        type="button"
        onClick={() => setShown((s) => !s)}
        className="inline-flex items-center gap-1 text-label text-primary-hover hover:underline"
        aria-label={shown ? "Hide value" : "Reveal value"}
      >
        {shown ? <EyeOff className="h-3 w-3" aria-hidden /> : <Eye className="h-3 w-3" aria-hidden />}
        {shown ? "Hide" : "Reveal"}
      </button>
    </span>
  )
}

// Keys that duplicate what's already shown (body/title) or are pure
// plumbing — hidden from the human Context card so it reads like a
// summary, not a JSON dump. The data is still on the wire for anything
// that needs it programmatically.
const CONTEXT_HIDE_KEYS = new Set([
  "reason",
  "raw_reason",
  "source",
  "kind",
  "inputs",
  "step_id",
  // Rendered as a badge beside the decision, so repeating it here as a raw
  // key/value reads as debug output rather than as the warning it is.
  "risk_level",
  // Identifiers the pane renders as NAMES and LINKS (the identity strip and
  // the "Where this came from" row). A cuid in a Context card is exactly the
  // leak README §6 rules out, and it was the first thing a client saw.
  "crew_id",
  "invoking_crew_id",
  "chat_id",
  "agent_id",
  "agent_slug",
  "approval_id",
  "credential_id",
  "has_pending_credential",
  "escalation_type",
  "link_url",
  "credential_name",
  "pipeline_run_id",
  "run_id",
  "mission_id",
  "issue_identifier",
  "chat_url",
  "schedule_id",
  "pipeline_slug",
  "request_id",
  "request_type",
])

function humanizeKey(k: string): string {
  return k
    .replace(/_/g, " ")
    .replace(/\bid\b/i, "ID")
    .replace(/^\w/, (c) => c.toUpperCase())
}

// ContextDetails renders payload as a clean key/value summary instead of
// a raw <pre>{JSON}</pre> block. Strings that look like secrets are
// masked with a reveal toggle; nested objects fall back to compact JSON.
/** The payload keys worth showing — plumbing and duplicates are dropped. */
export function visibleContextEntries(payload: Record<string, unknown>): [string, unknown][] {
  return Object.entries(payload).filter(
    ([k, v]) => !CONTEXT_HIDE_KEYS.has(k) && v !== null && v !== undefined && v !== "",
  )
}

function ContextDetails({ payload }: { payload: Record<string, unknown> }) {
  const entries = visibleContextEntries(payload)
  if (entries.length === 0) return null
  return (
    <dl className="grid grid-cols-1 gap-x-4 gap-y-1.5 sm:grid-cols-[minmax(0,1fr)_minmax(0,2fr)] text-[11px]">
      {entries.map(([k, v]) => {
        return (
          <div key={k} className="contents">
            <dt className="text-muted-foreground">{humanizeKey(k)}</dt>
            <dd className="min-w-0 break-words text-foreground/80">
              {typeof v === "string" ? (
                looksSecret(k, v) ? (
                  <RevealableValue value={v} />
                ) : (
                  <span>{v}</span>
                )
              ) : looksSecretNested(v) ? (
                // A nested object was stringified whole, so an api_key one
                // level down printed in full while the top-level key check —
                // which only sees "metadata" — waved it through. Anything that
                // contains something secret-looking is masked as a unit.
                <RevealableValue value={JSON.stringify(v)} />
              ) : (
                <span className="font-mono text-[10px]">{JSON.stringify(v)}</span>
              )}
            </dd>
          </div>
        )
      })}
    </dl>
  )
}


/**
 * The message body, bounded.
 *
 * A waitpoint's body_md is the whole approval prompt — for a model-drafted
 * change plan that is several hundred words of unbroken prose, and the card
 * had no height limit of any kind, so it pushed the identifiers, the run
 * ladder and the footer off the bottom of the pane. The reader who most
 * needs the buttons is the one who has to scroll furthest from them.
 *
 * So: clamp, and offer the rest. The threshold is deliberately generous —
 * a short body must not grow a control that does nothing — and the collapsed
 * height is enough to read the first paragraph, which is where a plan states
 * what it is going to do.
 */
const BODY_CLAMP_CHARS = 600

function MessageBody({ body }: { body: string }) {
  const [expanded, setExpanded] = useState(false)
  // Character count, not rendered height: the decision has to be made before
  // paint, and a body this long is long whatever it renders to.
  const long = body.length > BODY_CLAMP_CHARS

  if (!long) return <MarkdownContent>{body}</MarkdownContent>

  return (
    <div className="flex flex-col gap-2">
      <div
        className={cn(
          "relative",
          !expanded && "max-h-[16rem] overflow-hidden",
        )}
      >
        <MarkdownContent>{body}</MarkdownContent>
        {/* Fades rather than cutting mid-glyph, so the clamp reads as
            "there is more" instead of as a rendering fault. */}
        {!expanded && (
          <div className="pointer-events-none absolute inset-x-0 bottom-0 h-12 bg-gradient-to-t from-card to-transparent" />
        )}
      </div>
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        aria-expanded={expanded}
        className="self-start type-meta font-medium text-primary hover:underline"
      >
        {expanded ? "Show less" : "Show the whole message"}
      </button>
    </div>
  )
}

export function DecisionCard({
  item, role, onResolve, onRefresh, onDenyHire, crewHref, onAct,
}: {
  /** Kept for callers of the old card shape; the block is the same either way. */
  embedded?: boolean
  item: InboxItem
  role: WorkspaceRole | null
  onResolve: (action: string) => void | Promise<void>
  onRefresh: () => void | Promise<void>
  onDenyHire?: () => Promise<void>
  crewHref?: string | null
  onAct?: InboxActFn
}) {
  const meta = decisionMetaFor(item)
  if (!meta) return null

  const isResolved = item.state === "resolved"
  const allowed = canRole(role, meta.requires)
  const mins = expiresIn(item)
  const destructive = riskLevelOf(item) === "destructive"
  // Colour carries the stakes only: a destructive approval is red, a schedule
  // that stopped is amber, every other ask is the plain block.
  const tone: DecisionTone = isResolved
    ? "default"
    : destructive
      ? "danger"
      : item.kind === "schedule_missed" || item.kind === "schedule_circuit_breaker_tripped"
        ? "warn"
        : "default"
  const authorForm = item.kind === "waitpoint" && isDecisionForm(item.payload?.decision_form)

  return (
    <DecisionBlock
      testId="decision-card"
      icon={decisionIcon(item)}
      // meta.heading is written for a pending gate ("Waiting on your
      // decision"). History renders the same block, so a decided row used to
      // announce itself as still waiting.
      heading={isResolved ? "Decision record" : meta.heading}
      tone={tone}
      // A decided row has no deadline left to run: "expires in 24h" beside a
      // resolved decision reads as "this is still open".
      expires={mins != null && !isResolved ? remainingLabel(mins) === "expired" ? "expired" : `in ${remainingLabel(mins)}` : null}
      badge={isResolved ? (
        <span className="type-meta font-mono text-muted-foreground">
          {item.resolved_action ?? "closed"}
          {item.resolved_at ? ` · ${since(item.resolved_at)}` : ""}
        </span>
      ) : null}
      // Who decides sits beside the buttons it disables; otherwise the note
      // says whose words the buttons are.
      note={isResolved ? undefined : !allowed ? <RoleNote>{deciderCopy(meta.requires)}</RoleNote> : authorForm ? "Buttons named by the routine author" : undefined}
      actions={!isResolved ? (
        // Rendering the buttons disabled for History made it look like a
        // queue the reader lacked permission to act on; a decided row states
        // its outcome instead.
        <KindActions
          item={item}
          onResolve={onResolve}
          onRefresh={onRefresh}
          disabled={!allowed}
          onDenyHire={onDenyHire}
          crewHref={crewHref}
          onAct={onAct}
        />
      ) : undefined}
    >
      {/* Author-declared, never inferred. Sits in the block rather than in
          the Context dump because it changes how the ask should be read. */}
      {destructive && (
        <div className="flex flex-wrap gap-1.5">
          <span className="type-meta rounded-full border border-destructive/40 px-2 py-0.5 font-semibold text-destructive">
            destructive · cannot be undone by re-running
          </span>
        </div>
      )}
      <DecisionSubject item={item} />
      {payloadString(item, "kind") === "routine_proposal" && <RoutineProposalDiff workspaceId={item.workspace_id} slug={payloadString(item, "slug")} fromVersion={payloadNumber(item, "from_version")} toVersion={payloadNumber(item, "to_version")} />}
      {/* Ahead of the buttons: whether this person can resolve it at all
          decides whether pressing one is worth anything. Every value is the
          server's read-time answer; the row never re-derives it. */}
      <FourEyesNotice
        required={item.second_approver_required === true}
        byWorkspace={item.second_approver_by_workspace === true}
        byTier={item.second_approver_by_tier === true}
        securityLevelLabel={item.security_level_label ?? null}
        agentSlug={fourEyesAgentOf(item)}
      />
      <EvidenceFacts item={item} />
      {meta.missingEndpoint && (
        <p className="type-meta rounded-md border border-dashed border-border/60 px-2.5 py-1.5 font-mono text-muted-foreground">
          missing on the server: {meta.missingEndpoint}
        </p>
      )}
    </DecisionBlock>
  )
}

function decisionIcon(item: InboxItem): LucideIcon {
  if (riskLevelOf(item) === "destructive") return AlertTriangle
  switch (item.kind) {
    case "waitpoint": return isDecisionForm(item.payload?.decision_form) ? ListChecks : payloadString(item, "kind") === "hire" ? UserPlus : ShieldCheck
    case "escalation": {
      const sub = payloadString(item, "kind")
      if (sub === "routine_proposal") return ScrollText
      if (sub === "skill_proposal") return Sparkles
      return payloadString(item, "escalation_type") === "TEXT" ? HelpCircle : KeyRound
    }
    case "schedule_missed": return CalendarClock
    case "schedule_circuit_breaker_tripped": return PauseCircle
    case "memory_consolidation": return Brain
  }
  return ShieldCheck
}

/**
 * The one or two payload fields that answer "why should I say yes". They used
 * to sit in the Context key/value dump below the actions, at the same weight
 * as request_id.
 */
export function DecisionSubject({ item }: { item: InboxItem }) {
  const intent = payloadString(item, "intent")
  const credential = payloadString(item, "credential_name")
  const level = payloadString(item, "security_level")
  const risk = payloadNumber(item, "risk_score")
  const scan = payloadString(item, "scan_status")
  const reasons = Array.isArray(item.payload?.risk_reasons)
    ? (item.payload?.risk_reasons as unknown[]).filter((r): r is string => typeof r === "string")
    : []
  // What a proposed routine declares it will reach for. `risk_reasons`
  // says "credentials_required", which is the category; these are the
  // things themselves, and they are the reviewer's actual question.
  const asks: { key: string; label: string; values: string[]; tone: DetailTone }[] = [
    { key: "credentials_required", label: "Credentials", values: payloadStrings(item, "credentials_required"), tone: "warn" as DetailTone },
    { key: "integrations_required", label: "Integrations", values: payloadStrings(item, "integrations_required"), tone: "purple" as DetailTone },
    { key: "egress_targets", label: "Egress", values: payloadStrings(item, "egress_targets"), tone: "destructive" as DetailTone },
  ].filter((a) => a.values.length > 0)
  const failures = payloadNumber(item, "consecutive_failures")
  const missed = payloadNumber(item, "missed_count")
  const policy = payloadString(item, "catchup_policy")
  const rules = payloadNumber(item, "rules_count")
  const scanned = payloadNumber(item, "entries_scanned")

  const link = linkToOpen(item)
  const chips: React.ReactNode[] = []
  // Severity facts are status pills; plain counts and settings are Badges.
  if (credential) chips.push(<StatusPill key="cred" size="md" tone="warn" label={credential} />)
  if (level) chips.push(<StatusPill key="lvl" size="md" tone="danger" label={level} />)
  if (risk != null) chips.push(<StatusPill key="risk" size="md" tone="warn" label={`risk ${risk}`} />)
  if (scan) chips.push(<StatusPill key="scan" size="md" tone={scan === "clean" ? "success" : "warn"} label={`scan: ${scan}`} />)
  if (failures != null) chips.push(<StatusPill key="fail" size="md" tone="danger" label={`${failures} failures in a row`} />)
  if (missed != null) chips.push(<StatusPill key="miss" size="md" tone="warn" label={`${missed} missed runs`} />)
  if (policy) chips.push(<Badge key="pol" variant="secondary">catchup: {policy}</Badge>)
  if (rules != null) chips.push(<Badge key="rules" variant="secondary">{rules} rules</Badge>)
  if (scanned != null) chips.push(<Badge key="scanned" variant="secondary">{scanned} entries scanned</Badge>)

  if (chips.length === 0 && !intent && reasons.length === 0 && asks.length === 0 && !link) return null

  return (
    <div className="flex flex-col gap-2">
      {/* What is being approved, ahead of everything: a LINK escalation used
          to render "Escalation type LINK" and no address at all. The host is
          spelled out because that is what a person checks before saying yes. */}
      {link && (
        <div className="flex flex-col gap-1">
          <span className="type-meta uppercase tracking-wide text-muted-foreground">What you are approving</span>
          <div className="flex items-center gap-2.5 rounded-lg border border-primary/35 bg-primary/[0.06] px-3 py-2" data-testid="decision-link">
            <Link2 className="h-4 w-4 shrink-0 text-primary-hover" />
            <span className="flex min-w-0 flex-1 flex-col">
              <a href={link} target="_blank" rel="noopener noreferrer" className="truncate font-mono text-[12px] text-primary-hover hover:underline">{link}</a>
              <span className="text-[11px] text-muted-foreground">{hostOf(link)} · the agent wants to open this address</span>
            </span>
            <Button asChild size="xs" variant="outline" className="gap-1">
              <a href={link} target="_blank" rel="noopener noreferrer">Open link <ArrowUpRight className="h-3 w-3" /></a>
            </Button>
          </div>
        </div>
      )}
      {chips.length > 0 && <div className="flex flex-wrap items-center gap-1.5">{chips}</div>}
      {asks.map((a) => (
        <div key={a.key} className="flex flex-wrap items-baseline gap-1.5">
          <span className="type-meta uppercase tracking-wide text-muted-foreground">
            {a.label}
          </span>
          {a.values.map((v) => (
            <Pill key={v} tone={a.tone}>
              {v}
            </Pill>
          ))}
        </div>
      ))}
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

/** The crew id a row names, whichever key the producer used. */
export function crewIdOf(item: InboxItem): string | null {
  return payloadString(item, "invoking_crew_id") || payloadString(item, "crew_id") || null
}

/** The agent slug a row names — the key the roster is keyed by. */
export function agentSlugOf(item: InboxItem): string | null {
  return payloadString(item, "agent_slug") || (item.sender_type === "agent" ? item.sender_name ?? null : null) || null
}

/**
 * Where this row came from, as links (README §5: an inbox item links to the
 * object that raised it and the crew). Built through entityHref so none of
 * them can point at a route that does not exist.
 */
export function originLinks(
  item: InboxItem,
  lookup?: InboxLookup,
): { label: string; href: string; icon: LucideIcon }[] {
  const out: { label: string; href: string; icon: LucideIcon }[] = []
  const agentSlug = agentSlugOf(item)
  const chatID = payloadString(item, "chat_id")
  const chatURL = safeChatURL(item)
  if (chatURL) out.push({ label: "Open chat", href: chatURL, icon: MessageSquare })
  else if (chatID && agentSlug) out.push({ label: "Open chat", href: entityHref({ kind: "chat", agentSlug, sessionId: chatID }), icon: MessageSquare })
  const run = payloadString(item, "pipeline_run_id") || payloadString(item, "run_id")
  if (run) out.push({ label: "Open run", href: entityHref({ kind: "run", runId: run, pipelineSlug: payloadString(item, "pipeline_slug") || undefined }), icon: ArrowUpRight })
  const issue = payloadString(item, "issue_identifier")
  if (issue) out.push({ label: `Open ${issue}`, href: entityHref({ kind: "issue", identifier: issue }), icon: CircleDot })
  const routine = payloadString(item, "pipeline_slug")
  if (routine) out.push({ label: "Open routine", href: entityHref({ kind: "routine", slug: routine }), icon: ScrollText })
  // The quiet-runs digest counts runs that completed in the window; they are
  // the Completed bucket of Activity over the same 24h. Without this the card
  // named where they were and offered only Dismiss (#2979).
  if (item.kind === "message" && payloadString(item, "subkind") === "digest") {
    out.push({ label: "View in Activity", href: "/activity?status=done", icon: Activity })
  }
  const crewID = crewIdOf(item)
  const crew = crewID && lookup ? lookup.crewById.get(crewID) : null
  if (crew) out.push({ label: `Open ${crew.name}`, href: entityHref({ kind: "crew", slug: crew.slug }), icon: Users })
  return out
}

export interface InboxDetailProps {
  item: InboxItem
  role: WorkspaceRole | null
  onResolve: (action: string) => void | Promise<void>
  onArchive: () => void | Promise<void>
  onMarkUnread: () => void
  onRefresh: (action?: string) => void | Promise<void>
  /** Crews and agents by id/slug, so the strip can name them and link them. */
  lookup?: InboxLookup
  /** Deny a staged hire through its approvals-queue twin. */
  onDenyHire?: () => Promise<void>
  /** Act on a run_needs_human card through POST /inbox/{id}/act (#2398). */
  onAct?: InboxActFn
  /** Render the ⋯ menu in the pane. The Inbox page puts it in the top bar. */
  menu?: boolean
}

/**
 * The agent the four-eyes rule is about: whoever OWNS it cannot resolve.
 *
 * On a keeper credential escalation that is not the sender. The keeper raises
 * the item, so `sender_name` is "Keeper" — while the server compares the
 * approver against `agents.created_by_user_id` of the agent that ASKED, which
 * the payload carries as `agent_name`. Falls back to the sender for the
 * escalations-backed flow, where the two are the same agent.
 */
function fourEyesAgentOf(item: InboxItem): string | null {
  const fromPayload = item.payload?.agent_name
  if (typeof fromPayload === "string" && fromPayload !== "") return fromPayload
  return item.sender_name ?? null
}

/**
 * Can this row leave the inbox by Archive?
 *
 * Decision items are source-managed: the inbox PATCH rejects anything but
 * "read" while the SOURCE still exists, so they cannot be blind-archived.
 * Whether a source still exists is a question only the server can answer —
 * it is a row in pipeline_waitpoints / escalations / agents. This used to be
 * guessed from the payload, and the guess was wrong in both directions: a
 * waitpoint whose run was pruned, and an escalation whose escalations row was
 * deleted, are both resolvable by the server and were both denied an Archive
 * button, leaving them stuck in the inbox with no way into History.
 *
 * So stop guessing — but do not guess in the other direction either, by
 * offering an Archive that 409s on every live decision. The server answers
 * the question on the detail read (`source_missing`), using the same probe
 * its PATCH guard uses, so the button and the outcome cannot disagree.
 */
export function inboxArchivable(item: InboxItem): boolean {
  return (
    item.state !== "resolved" &&
    ((item.kind !== "waitpoint" && item.kind !== "escalation" && !item.blocking) || item.source_missing === true)
  )
}

/**
 * A restore is meaningful only for something the user manually archived and
 * whose source does not own a terminal lifecycle. Reopening an approved
 * waitpoint would not re-pend the run; source-less synthetic escalations are
 * also terminal by backend contract. The old unconditional button sent both
 * to PATCH unread and silently received 409.
 */
export function inboxRestorable(item: InboxItem): boolean {
  return (
    item.state === "resolved" &&
    item.resolved_action === "archived" &&
    item.kind !== "waitpoint" &&
    item.kind !== "escalation"
  )
}

/**
 * The ⋯ menu: Mark unread · Archive · Restore. On the Inbox page it sits in
 * the top bar (Harbor 2); surfaces without one render it in the pane.
 */
export function InboxMessageMenu({ item, onArchive, onMarkUnread, className }: {
  item: InboxItem
  onArchive: () => void | Promise<void>
  onMarkUnread: () => void
  className?: string
}) {
  const isResolved = item.state === "resolved"
  const archivable = inboxArchivable(item)
  const restorable = inboxRestorable(item)
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" aria-label="Message options" className={cn("h-8 w-8 shrink-0", className)}>
          <MoreHorizontal className="h-4 w-4" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {!isResolved && <DropdownMenuItem onSelect={onMarkUnread}>Mark unread</DropdownMenuItem>}
        {archivable && <DropdownMenuItem onSelect={() => void onArchive()}>Archive</DropdownMenuItem>}
        {!isResolved && !archivable && <DropdownMenuItem disabled>Archive · decide first</DropdownMenuItem>}
        {restorable && <DropdownMenuItem onSelect={onMarkUnread}>Restore</DropdownMenuItem>}
        {isResolved && !restorable && <DropdownMenuItem disabled>Saved in History</DropdownMenuItem>}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

export function InboxDetail({ item, role, onResolve, onArchive, onMarkUnread, onRefresh, lookup, onDenyHire, onAct, menu = true }: InboxDetailProps) {
  const isResolved = item.state === "resolved"
  const actReceipt = isResolved && item.kind === "run_needs_human" ? receiptOf(item) : null
  const decision = decisionMetaFor(item)
  const entry = inboxEntry(item)
  const known = lookup ?? EMPTY_INBOX_LOOKUP
  const identity = entryIdentity(entry, known)
  const runID = payloadString(item, "pipeline_run_id")
  const crew = identity.crew
  const agent = identity.agent
  const crewSlug = crew?.slug ?? agent?.crew?.slug ?? null
  const crewHref = crewSlug ? entityHref({ kind: "crew", slug: crewSlug }) : null
  const links = originLinks(item, lookup)
  // Message navigation is rendered once, in the dock beside Dismiss.
  const messageLinks = item.kind === "message" ? links.filter((link) => link.href !== crewHref) : []
  const relatedLinks = item.kind === "message" ? links.filter((link) => link.href === crewHref) : links
  const body = item.body_md ? <div className="text-default leading-relaxed text-foreground/90"><MessageBody key={item.id} body={item.body_md} /></div> : null
  const badge = item.attention_class ? <AttentionBadge item={item} /> : undefined

  return (
    <DetailFrame entry={entry} lookup={known} attention={badge}>
      {menu && (
        <div className="-mt-2 flex justify-end">
          <InboxMessageMenu item={item} onArchive={onArchive} onMarkUnread={onMarkUnread} />
        </div>
      )}

      {/* History (v67): the receipt first; the request it settled stays below it. */}
      {isResolved && (
        <HistoryReceipt
          outcome={outcomeLabel(item.resolved_action)}
          at={item.resolved_at ?? item.updated_at}
          activityHref={runID ? entityHref({ kind: "run", runId: runID, pipelineSlug: payloadString(item, "pipeline_slug") || undefined }) : "/activity"}
        />
      )}

      {decision ? (
        <>
          {body}
          <DecisionCard item={item} role={role} onResolve={onResolve} onRefresh={onRefresh} onDenyHire={onDenyHire} crewHref={crewHref} onAct={onAct} />
        </>
      ) : item.kind === "failed_run" || item.kind === "run_needs_human" ? (
        <>
          <DecisionSubject item={item} />
          <DecisionBlock
            testId="inbox-ask"
            icon={item.kind === "failed_run" ? AlertTriangle : MessageSquare}
            heading={item.kind === "failed_run" ? "What happened" : "Your input is needed"}
            tone={item.kind === "failed_run" && !isResolved ? "danger" : "default"}
            note={item.kind === "failed_run" && !isResolved ? "Re-runs with the same inputs" : undefined}
            actions={actReceipt ? <ActReceipt receipt={actReceipt} /> : !isResolved ? (
              <KindActions item={item} onResolve={onResolve} onRefresh={onRefresh} disabled={false} onAct={onAct} hideMessageLinks />
            ) : undefined}
          >
            {body ?? <p className="text-body text-muted-foreground">No message content was included.</p>}
          </DecisionBlock>
        </>
      ) : (
        <>
          <DecisionSubject item={item} />
          {body ?? <p className="text-body text-muted-foreground">No message content was included.</p>}
          {(messageLinks.length > 0 || !isResolved) && (
            <Dock standalone>
              {!isResolved && <KindActions item={item} onResolve={onResolve} onRefresh={onRefresh} disabled={false} onAct={onAct} hideMessageLinks />}
              {messageLinks.map((l, index) => (
                <Button asChild key={l.href} size="sm" variant={index === messageLinks.length - 1 ? "default" : "outline"}>
                  <Link href={l.href}><l.icon className="mr-1.5 h-3.5 w-3.5 shrink-0" />{l.label}</Link>
                </Button>
              ))}
            </Dock>
          )}
        </>
      )}

      <RailPart><OriginChips links={relatedLinks} /></RailPart>

      {item.kind === "waitpoint" && runID !== "" && (
        <RailPart><DetailExpand title="How the run got here" open={!isResolved}>
          <WaitpointRunDetail workspaceId={item.workspace_id} pipelineRunId={runID} inboxResolved={isResolved} />
        </DetailExpand></RailPart>
      )}
      {item.payload && visibleContextEntries(item.payload).length > 0 && (
        <RailPart><DetailExpand title="Additional context">
          <ContextDetails payload={item.payload} />
        </DetailExpand></RailPart>
      )}
      {isResolved && (
        <p className="m-0 text-label text-muted-foreground">
          {item.resolved_action === "archived" ? "Archived" : "Resolved"} {since(item.resolved_at ?? item.updated_at)}{item.resolved_action && item.resolved_action !== "archived" && ` · ${item.resolved_action}`}
        </p>
      )}
    </DetailFrame>
  )
}
