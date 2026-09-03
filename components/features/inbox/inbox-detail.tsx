"use client"

import { useState } from "react"
import Link from "next/link"
import { AlertTriangle, ArrowUpRight, CircleDot, Clock, Eye, EyeOff, Link2, MessageSquare, ScrollText, Users } from "lucide-react"

import { Appear, DetailCard, Pill, type DetailTone } from "@/components/ui/detail"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { StatusPill } from "@/components/ui/status-pill"
import { entityHref } from "@/lib/entity-links"
import { crewColor } from "@/app/(dashboard)/dashboard-helpers"
import type { InboxLookup } from "@/components/features/inbox-v2/inbox-v2-types"
import { RoutineProposalDiff } from "./routine-proposal-diff"
import { Button } from "@/components/ui/button"
import { MarkdownContent } from "@/components/features/issues/markdown-content"
import { FourEyesNotice } from "@/components/features/escalations/four-eyes-notice"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import type { LucideIcon } from "lucide-react"
import { cn } from "@/lib/utils"
import type { InboxItem } from "@/hooks/use-inbox"

import { ActorLabel } from "./inbox-actor"
import { EvidenceFacts } from "./evidence-facts"
import { KindActions } from "./kind-actions"
import { WaitpointRunDetail } from "./waitpoint-run-detail"
import {
  absolute, canRole, deciderCopy, decisionMetaFor, expiresIn, jumpFor, linkToOpen, payloadNumber, remainingLabel, riskLevelOf,
  payloadString, payloadStrings, safeChatURL, since, subjectOf, type WorkspaceRole,
} from "./inbox-derive"
import { entryKindPill, inboxEntry } from "@/components/features/inbox-v2/inbox-v2-derive"

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
      <span className="font-mono text-[11px] text-foreground/80">
        {shown ? value : "••••••••"}
      </span>
      <button
        type="button"
        onClick={() => setShown((s) => !s)}
        className="text-muted-foreground/60 hover:text-foreground"
        aria-label={shown ? "Hide value" : "Reveal value"}
      >
        {shown ? <EyeOff className="h-3 w-3" /> : <Eye className="h-3 w-3" />}
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
    <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1.5 text-[11px]">
      {entries.map(([k, v]) => {
        return (
          <div key={k} className="contents">
            <dt className="text-muted-foreground/70">{humanizeKey(k)}</dt>
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

  if (!long) return <MarkdownContent compact>{body}</MarkdownContent>

  return (
    <div className="flex flex-col gap-2">
      <div
        className={cn(
          "relative",
          !expanded && "max-h-[16rem] overflow-hidden",
        )}
      >
        <MarkdownContent compact>{body}</MarkdownContent>
        {/* Fades rather than cutting mid-glyph, so the clamp reads as
            "there is more" instead of as a rendering fault. */}
        {!expanded && (
          <div className="pointer-events-none absolute inset-x-0 bottom-0 h-12 bg-gradient-to-t from-card to-transparent" />
        )}
      </div>
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        className="self-start type-meta font-medium text-primary hover:underline"
      >
        {expanded ? "Show less" : "Show the whole message"}
      </button>
    </div>
  )
}

export function DecisionCard({
  item, role, onResolve, onRefresh, onDenyHire, crewHref,
}: {
  item: InboxItem
  role: WorkspaceRole | null
  onResolve: (action: string) => void | Promise<void>
  onRefresh: () => void | Promise<void>
  onDenyHire?: () => Promise<void>
  crewHref?: string | null
}) {
  const meta = decisionMetaFor(item)
  if (!meta) return null

  const isResolved = item.state === "resolved"
  const allowed = canRole(role, meta.requires)
  const mins = expiresIn(item)

  return (
    <DetailCard
      tone={!isResolved && meta.tone === "warn" ? "warn" : "default"}
      className={!isResolved && meta.tone === "warn" ? "bg-warn/[.06]" : undefined}
    >
      <div data-testid="decision-card" className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <AlertTriangle className={cn("h-4 w-4", !isResolved && meta.tone === "warn" ? "text-warn" : "text-muted-foreground")} />
          {/* meta.heading is written for a pending gate ("Waiting on your
              decision"). History renders the same card, so a decided row used
              to announce itself as still waiting. */}
          <span className={cn("type-section", !isResolved && meta.tone === "warn" ? "text-warn" : "text-foreground/70")}>
            {isResolved ? "Decision record" : meta.heading}
          </span>
          {/* Author-declared, never inferred. Sits with the heading rather
              than in the Context dump because it changes how the sentence
              below it should be read, and a reader who scrolls past the
              buttons has already decided. */}
          {riskLevelOf(item) === "destructive" && (
            <span className="type-meta rounded bg-destructive/15 px-1.5 py-0.5 font-semibold uppercase tracking-wide text-destructive">
              destructive · cannot be undone by re-running
            </span>
          )}
          {/* A decided row has no deadline left to run. Showing "expires in
              24h" beside a resolved decision — which History does for every
              row — reads as "this is still open", and the greyed Approve
              button underneath reads as a permissions problem rather than a
              decision already made. */}
          {mins != null && !isResolved && (
            <span
              className={cn(
                "type-meta ml-auto font-mono",
                // Expired is not a quieter state than expiring; it is the loud
                // one. It used to render muted, below the pill for a gate that
                // still had time.
                mins > 0 ? "text-destructive" : "font-semibold text-destructive",
              )}
            >
              {mins > 0
                ? `expires ${absolute(payloadString(item, "timeout_at"))} · in ${remainingLabel(mins)}`
                : `expired ${absolute(payloadString(item, "timeout_at"))}`}
            </span>
          )}
          {isResolved && (
            <span className="type-meta ml-auto font-mono text-muted-foreground">
              {item.resolved_action ?? "closed"}
              {item.resolved_at ? ` · ${since(item.resolved_at)}` : ""}
            </span>
          )}
        </div>

        <div className="text-body font-semibold">{item.title}</div>

        <DecisionSubject item={item} />

        {/* Ahead of the buttons: whether this person can resolve it at all
            decides whether pressing one is worth anything. The same component
            the crew escalations panel renders (#1559/#1574) — the inbox is the
            other surface with a one-click Approve on the same escalation, and
            it used to offer it in silence. Every value is the server's
            read-time answer; the row never re-derives it from the payload. */}
        <FourEyesNotice
          required={item.second_approver_required === true}
          byWorkspace={item.second_approver_by_workspace === true}
          byTier={item.second_approver_by_tier === true}
          securityLevelLabel={item.security_level_label ?? null}
          agentSlug={fourEyesAgentOf(item)}
        />

        {/* Above the buttons, below the four-eyes notice: the facts belong where
            they are read before the click, not after it. */}
        <EvidenceFacts item={item} />

        {/* Rendering the buttons disabled was the old behaviour, and it made
            History look like a queue the reader lacked permission to act on.
            A decided row states its outcome instead. */}
        {!isResolved && (
          <KindActions
            item={item}
            onResolve={onResolve}
            onRefresh={onRefresh}
            disabled={!allowed}
            onDenyHire={onDenyHire}
            crewHref={crewHref}
          />
        )}

        {!isResolved && !allowed && (
          <p className="type-meta text-muted-foreground">
            {deciderCopy(meta.requires)}
            {" — you can still archive it."}
          </p>
        )}
        {allowed && !isResolved && (
          <p className="type-meta text-muted-foreground-soft">
            {deciderCopy(meta.requires)} · you can
          </p>
        )}
        {meta.missingEndpoint && (
          <p className="type-meta rounded-md border border-dashed border-border/60 px-2.5 py-1.5 font-mono text-muted-foreground-soft">
            missing on the server: {meta.missingEndpoint}
          </p>
        )}
      </div>
    </DetailCard>
  )
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
  if (credential) chips.push(<Pill key="cred" tone="warn">{credential}</Pill>)
  if (level) chips.push(<Pill key="lvl" tone="destructive">{level}</Pill>)
  if (risk != null) chips.push(<Pill key="risk" tone="warn">risk {risk}</Pill>)
  if (scan) chips.push(<Pill key="scan" tone={scan === "clean" ? "success" : "warn"}>scan: {scan}</Pill>)
  if (failures != null) chips.push(<Pill key="fail" tone="destructive">{failures} failures in a row</Pill>)
  if (missed != null) chips.push(<Pill key="miss" tone="warn">{missed} missed runs</Pill>)
  if (policy) chips.push(<Pill key="pol" tone="default">catchup: {policy}</Pill>)
  if (rules != null) chips.push(<Pill key="rules" tone="purple">{rules} rules</Pill>)
  if (scanned != null) chips.push(<Pill key="scanned" tone="default">{scanned} entries scanned</Pill>)

  if (chips.length === 0 && !intent && reasons.length === 0 && asks.length === 0 && !link) return null

  return (
    <div className="flex flex-col gap-2">
      {/* What is being approved, ahead of everything: a LINK escalation used
          to render "Escalation type LINK" and no address at all. The host is
          spelled out because that is what a person checks before saying yes. */}
      {link && (
        <div className="flex flex-col gap-1">
          <span className="type-meta uppercase tracking-wide text-muted-foreground-soft">What you are approving</span>
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
          <span className="type-meta uppercase tracking-wide text-muted-foreground-soft">
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

/**
 * One cell of the identity strip. No field caption: the strip used to print
 * the payload key under every value (`sender_name`, `crew_id`, `created_at`),
 * which is documentation for a developer, not identity for a client.
 */
function Definition({
  label, value, mono,
}: {
  label: string
  value: React.ReactNode
  mono?: boolean
}) {
  return (
    <div className="min-w-0 px-4 py-2">
      <div className="type-meta uppercase tracking-wide text-muted-foreground-soft">{label}</div>
      <div className={cn("type-row mt-0.5 flex min-w-0 items-center gap-1.5 truncate", mono && "font-mono text-[12px]")}>{value}</div>
    </div>
  )
}

function hostOf(url: string): string {
  try {
    return new URL(url).host
  } catch {
    return url
  }
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

export function InboxDetail({ item, role, onResolve, onArchive, onMarkUnread, onRefresh, lookup, onDenyHire }: InboxDetailProps) {
  const isResolved = item.state === "resolved"
  const decision = decisionMetaFor(item)
  const jump = jumpFor(item)
  const subject = subjectOf(item)
  const runID = payloadString(item, "pipeline_run_id")
  const kind = entryKindPill(inboxEntry(item))
  const crewID = crewIdOf(item)
  const crew = crewID && lookup ? lookup.crewById.get(crewID) ?? null : null
  const agentSlug = agentSlugOf(item)
  const agent = agentSlug && lookup ? lookup.agentBySlug.get(agentSlug) ?? null : null
  const crewName = crew?.name ?? agent?.crew?.name ?? null
  const crewSlug = crew?.slug ?? agent?.crew?.slug ?? null
  const crewHref = crewSlug ? entityHref({ kind: "crew", slug: crewSlug }) : null
  const links = originLinks(item, lookup)

  // Decision items are source-managed: the inbox PATCH rejects anything but
  // "read" while the SOURCE still exists, so they cannot be blind-archived.
  //
  // Whether a source still exists is a question only the server can answer —
  // it is a row in pipeline_waitpoints / escalations / agents. This used to be
  // guessed from the payload ("an escalation with no escalation_type must be
  // synthetic"), and the guess was wrong in both directions: a waitpoint whose
  // run was pruned, and an escalation whose escalations row was deleted, are
  // both resolvable by the server and were both denied an Archive button here,
  // leaving them stuck in the inbox with no way out and no way into History.
  //
  // So stop guessing — but do not guess in the other direction either, by
  // offering an Archive that 409s on every live decision. The server answers
  // the question on the detail read (`source_missing`), using the same probe
  // its PATCH guard uses, so the button and the outcome cannot disagree.
  const archivable =
    !isResolved &&
    ((item.kind !== "waitpoint" && item.kind !== "escalation" && !item.blocking) ||
      item.source_missing === true)
  // A restore is meaningful only for something the user manually archived and
  // whose source does not own a terminal lifecycle. Reopening an approved
  // waitpoint would not re-pend the run; source-less synthetic escalations are
  // also terminal by backend contract. The old unconditional button sent both
  // to PATCH unread and silently received 409.
  const restorable =
    isResolved &&
    item.resolved_action === "archived" &&
    item.kind !== "waitpoint" &&
    item.kind !== "escalation"

  return (
    <div className="flex flex-col gap-3">
      {/* A message and a failed run have no decision to frame, but they DO
          have actions — Open chat, Open ENG-6, Retry, Dismiss. Rendering
          KindActions only inside the decision card is what made Dismiss
          disappear for exactly the rows whose only affordance it was. */}
      <Appear order={0}>
        {decision ? (
          <DecisionCard item={item} role={role} onResolve={onResolve} onRefresh={onRefresh} onDenyHire={onDenyHire} crewHref={crewHref} />
        ) : (
          <DetailCard>
            <div className="flex flex-col gap-3">
              <div className="text-body font-semibold">{item.title}</div>
              <DecisionSubject item={item} />
              <KindActions item={item} onResolve={onResolve} onRefresh={onRefresh} disabled={isResolved} />
            </div>
          </DetailCard>
        )}
      </Appear>

      {/* Identity, as names and links: the agent, its crew, what kind of ask
          this is and when it arrived. It used to print the crew's cuid under
          a `crew_id` caption and the category as `agents.escalation`. */}
      <Appear order={1}>
        <DetailCard bare>
          <div className="grid grid-cols-2 divide-x divide-hairline sm:grid-cols-4">
            <Definition
              label={subject.kind === "agent" ? "Agent" : "From"}
              value={agent ? (
                <Link href={entityHref({ kind: "agent", slug: agent.slug })} className="flex min-w-0 items-center gap-1.5 hover:underline">
                  <AgentAvatar seed={agent.avatar_seed || agent.slug} style={agent.avatar_style} agentId={agent.id} avatarUrl={agent.avatar_url} alt="" className="h-5 w-5 shrink-0 rounded-md" />
                  <span className="truncate">{agent.name}</span>
                  {agent.role_title && <span className="truncate text-[11px] text-muted-foreground">{agent.role_title}</span>}
                </Link>
              ) : (
                <ActorLabel actor={subject} size={20} />
              )}
            />
            <Definition
              label="Crew"
              value={crewName ? (
                <Link href={crewHref ?? "#"} className="flex min-w-0 items-center gap-1.5 hover:underline">
                  <span className="h-2 w-2 shrink-0 rounded-full" style={{ background: crewColor(crew?.color ?? agent?.crew?.color ?? null) }} aria-hidden />
                  <span className="truncate">{crewName}</span>
                </Link>
              ) : (
                <span className="text-muted-foreground">No crew</span>
              )}
            />
            <Definition label="Kind" value={<StatusPill tone={kind.tone} label={kind.label} />} />
            <Definition
              label={isResolved ? "Resolved" : "Arrived"}
              value={isResolved
                ? <><StatusPill status={item.resolved_action ?? "resolved"} /><span className="text-muted-foreground">{since(item.resolved_at)}</span></>
                : <>{absolute(item.created_at)}<span className="text-muted-foreground">· {since(item.created_at)}</span></>}
            />
          </div>
        </DetailCard>
      </Appear>

      {/* Where this came from — the chat it was raised in, the run it
          paused, the issue, the crew — every one a link (README §5). */}
      {links.length > 0 && (
        <Appear order={2}>
          <DetailCard bare>
            <div className="flex flex-wrap items-center gap-2 px-4 py-2.5">
              <span className="type-meta uppercase tracking-wide text-muted-foreground-soft">Where this came from</span>
              {links.map((l) => (
                <Button asChild key={l.href} size="xs" variant="outline" className="gap-1.5">
                  <Link href={l.href}><l.icon className="h-3 w-3" />{l.label}</Link>
                </Button>
              ))}
            </div>
          </DetailCard>
        </Appear>
      )}

      {item.kind === "waitpoint" && runID !== "" && (
        <Appear order={2}>
          <DetailCard title="How the run got here" icon={Clock} bare>
            <div className="px-4 py-3">
              <WaitpointRunDetail
                workspaceId={item.workspace_id}
                pipelineRunId={runID}
                inboxResolved={isResolved}
              />
            </div>
          </DetailCard>
        </Appear>
      )}

      {/* A routine proposal is a decision about a CHANGE, so the change
          is on the item rather than three clicks away in another
          surface. Only for that kind — every other inbox item has no
          versions to compare. */}
      {payloadString(item, "kind") === "routine_proposal" && (
        <RoutineProposalDiff
          workspaceId={item.workspace_id}
          slug={payloadString(item, "slug")}
          fromVersion={payloadNumber(item, "from_version")}
          toVersion={payloadNumber(item, "to_version")}
        />
      )}

      {item.body_md && (
        <Appear order={3}>
          <DetailCard title="Message" icon={CONCEPT_ICON.inbox}>
            <MessageBody body={item.body_md} />
          </DetailCard>
        </Appear>
      )}

      {/* Counted on the VISIBLE keys, not on the payload: a row whose payload
          is nothing but reason/source/step_id would otherwise draw a Context
          heading above an empty box. */}
      {item.payload && visibleContextEntries(item.payload).length > 0 && (
        <Appear order={4}>
          <DetailCard title="Context" subtitle="secrets masked">
            <ContextDetails payload={item.payload} />
          </DetailCard>
        </Appear>
      )}

      <Appear order={5}>
        <DetailCard bare>
          <div className="type-meta flex flex-wrap items-center gap-3 px-4 py-2 text-muted-foreground-soft">
            {!isResolved ? (
              <>
                <button type="button" onClick={onMarkUnread} className="hover:text-foreground">
                  Mark unread
                </button>
                {archivable && (
                  <>
                    <span>·</span>
                    <button type="button" onClick={() => void onArchive()} className="hover:text-foreground">
                      Archive
                    </button>
                  </>
                )}
              </>
            ) : (
              <span>
                {item.resolved_action === "archived" ? "Archived" : "Resolved"} {since(item.resolved_at ?? item.updated_at)}
                {item.resolved_action && item.resolved_action !== "archived" && ` · ${item.resolved_action}`}
              </span>
            )}
            {restorable && (
              <button type="button" onClick={onMarkUnread} className="hover:text-foreground">
                Restore
              </button>
            )}
            {jump && links.length === 0 && (
              <Button asChild size="xs" variant="ghost" className="ml-auto gap-1.5 text-primary">
                <Link href={jump.href}>
                  <jump.icon className="h-3 w-3" />
                  {jump.label}
                </Link>
              </Button>
            )}
          </div>
        </DetailCard>
      </Appear>
    </div>
  )
}
