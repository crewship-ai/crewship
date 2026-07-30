"use client"

import { AlertTriangle, Clock } from "lucide-react"

import { Appear, DetailCard, Pill, TickRow } from "@/components/ui/detail"
import { Button } from "@/components/ui/button"
import { MarkdownContent } from "@/components/features/issues/markdown-content"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import { cn } from "@/lib/utils"

import { ActorLabel } from "./actor"
import { canRole, type PreviewInboxItem, type WorkspaceRole } from "./mock-data"
import {
  absolute, categoryOf, decisionFor, expiresIn, jumpFor, payloadNumber, payloadString, subjectOf,
  type DecisionSpec,
} from "./logic"

// The detail blocks, shared by every layout: the split pane renders them in a
// column, the table renders them in a drawer, and the stream renders the
// decision card alone on each card. Same components either way — a layout
// choice must not become a second implementation of an approval.

export function DecisionCard({
  item, role, spec, compact = false,
}: {
  item: PreviewInboxItem
  role: WorkspaceRole
  spec: DecisionSpec
  /** Drops the title + subject, for a card that already shows them. */
  compact?: boolean
}) {
  const allowed = canRole(role, spec.requires)
  const mins = expiresIn(item)

  return (
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
              expires {absolute(payloadString(item, "timeout_at"))} · in {mins} min
            </span>
          )}
        </div>

        {!compact && <div className="text-body font-semibold">{item.title}</div>}
        {!compact && <DecisionSubject item={item} />}

        <div className="flex flex-wrap items-center gap-2">
          {spec.actions.map((a) => (
            <Button
              key={a.label}
              size="sm"
              disabled={!allowed}
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
          {allowed ? (
            <span className="type-meta ml-auto text-muted-foreground-soft">
              anyone in {spec.requires === "manage" ? "OWNER / ADMIN" : "MANAGER+"} can decide
            </span>
          ) : (
            <span className="type-meta text-muted-foreground">
              {spec.requires === "manage" ? "OWNER or ADMIN decides this" : "MANAGER and up decides this"}
            </span>
          )}
        </div>

        {spec.missingEndpoint && (
          <p className="type-meta rounded-md border border-dashed border-border/60 px-2.5 py-1.5 font-mono text-muted-foreground-soft">
            missing on the server: {spec.missingEndpoint}
          </p>
        )}
      </div>
    </DetailCard>
  )
}

/**
 * The one or two payload fields that answer "why should I say yes". Today they
 * sit in the Context key/value dump underneath the actions, at the same weight
 * as request_id.
 */
export function DecisionSubject({ item }: { item: PreviewInboxItem }) {
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
  if (risk != null) chips.push(<Pill key="risk" tone="warn">risk {risk}</Pill>)
  if (scan) chips.push(<Pill key="scan" tone={scan === "clean" ? "success" : "warn"}>scan: {scan}</Pill>)
  if (failures != null) chips.push(<Pill key="fail" tone="destructive">{failures} failures in a row</Pill>)
  if (missed != null) chips.push(<Pill key="miss" tone="warn">{missed} missed runs</Pill>)
  if (rules != null) chips.push(<Pill key="rules" tone="purple">{rules} rules</Pill>)
  if (scanned != null) chips.push(<Pill key="scanned" tone="default">{scanned} entries scanned</Pill>)

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
export function RunProgress({ item }: { item: PreviewInboxItem }) {
  const runId = payloadString(item, "pipeline_run_id")
  const step = payloadString(item, "step_id")
  if (!runId || item.kind !== "waitpoint") return null

  const steps: { label: string; status: "ok" | "running" | "pending"; meta?: string }[] = [
    { label: "checkout", status: "ok", meta: "2s" },
    { label: "build-docs", status: "ok", meta: "48s" },
    { label: "scan", status: "ok", meta: "6s" },
    { label: step || "promote", status: "running", meta: "waiting on you" },
    { label: "notify", status: "pending" },
  ]

  return (
    <DetailCard
      title="How the run got here"
      icon={Clock}
      subtitle={`step 4 of ${steps.length}`}
      footer={<span className="font-mono">pipeline_run_id · step_id</span>}
    >
      {steps.map((s) => (
        <TickRow key={s.label} label={s.label} status={s.status} meta={s.meta} />
      ))}
    </DetailCard>
  )
}

function Definition({
  label, value, field, mono,
}: {
  label: string
  value: React.ReactNode
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

/** The full reading pane — used by the split layout and the table's drawer. */
export function ItemDetail({ item, role }: { item: PreviewInboxItem; role: WorkspaceRole }) {
  const spec = decisionFor(item)
  const jump = jumpFor(item)
  const category = categoryOf(item)
  const subject = subjectOf(item)

  return (
    <div className="flex flex-col gap-3">
      {spec && <Appear order={0}><DecisionCard item={item} role={role} spec={spec} /></Appear>}

      <Appear order={1}>
        <DetailCard bare>
          <div className="grid grid-cols-2 divide-x divide-hairline sm:grid-cols-4">
            <Definition
              label="Subject"
              value={<ActorLabel actor={subject} size={20} />}
              field="payload.agent_name"
            />
            <Definition
              label="Crew"
              value={payloadString(item, "invoking_crew_id") || payloadString(item, "crew_id") || "—"}
              field="crew_id"
            />
            <Definition label="Category" value={category} field="derived from kind" mono />
            <Definition label="Arrived" value={absolute(item.created_at)} field="created_at" />
          </div>
        </DetailCard>
      </Appear>

      <Appear order={2}><RunProgress item={item} /></Appear>

      {item.body_md && (
        <Appear order={3}>
          <DetailCard title="Message" icon={CONCEPT_ICON.inbox}>
            <MarkdownContent compact>{item.body_md}</MarkdownContent>
          </DetailCard>
        </Appear>
      )}

      <Appear order={4}>
        <DetailCard bare>
          <div className="type-meta flex flex-wrap items-center gap-3 px-4 py-2 text-muted-foreground-soft">
            <button type="button" className="hover:text-foreground">Mark unread</button>
            <span>·</span>
            <button type="button" className="hover:text-foreground">Archive</button>
            {jump && (
              <Button size="xs" variant="ghost" className="ml-auto gap-1.5 text-primary">
                <jump.icon className="h-3 w-3" />
                {jump.label}
              </Button>
            )}
            <button type="button" className={cn("text-primary hover:underline", !jump && "ml-auto")}>
              Delivery settings for {category}
            </button>
          </div>
        </DetailCard>
      </Appear>
    </div>
  )
}
