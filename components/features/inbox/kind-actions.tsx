"use client"

import { useState } from "react"
import Link from "next/link"
import { CheckCircle2, CircleDot, MessageSquare, ScrollText, XCircle } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { apiFetch } from "@/lib/api-fetch"
import { waitpointDecide } from "@/lib/api/waitpoints"
import { escalationResolve } from "@/lib/api/escalations"
import type { InboxItem } from "@/hooks/use-inbox"

/** Valid in-app chat deep link from a reply notification's payload. */
function chatUrlOf(item: InboxItem): string | null {
  const v = item.payload?.chat_url
  return typeof v === "string" && v.startsWith("/") ? v : null
}

// =============================================================================
// KindActions — the buttons that resolve an item, one branch per source.
//
// Lifted out of inbox-list.tsx unchanged when the surface was rebuilt: every
// branch here is a contract with a specific endpoint (which one, and whether
// the server cascades the inbox row itself), and those were learned the hard
// way — see the comments. The redesign wraps this in a card; it does not
// second-guess what it calls.
//
// New here: `disabled` is now also driven by RBAC. Skill-proposal and
// consolidation approvals are roleManage (OWNER/ADMIN) while the rows carry
// target_role=MANAGER, so a manager used to get a button and a 403. The card
// names who decides instead.
// =============================================================================

export function KindActions({
  item,
  onResolve,
  onRefresh,
  disabled,
}: {
  item: InboxItem
  onResolve: (action: string) => void | Promise<void>
  onRefresh: () => void | Promise<void>
  disabled: boolean
}) {
  const [busy, setBusy] = useState<string | null>(null)
  const wrap = async (action: string, fn: () => Promise<void>) => {
    setBusy(action)
    try {
      await fn()
    } finally {
      setBusy(null)
    }
  }

  switch (item.kind) {
    case "waitpoint": {
      // PR-D hire waitpoints share the inbox kind='waitpoint' shape
      // but live on a different source: source_id is an agent_id, not
      // a pipeline_waitpoints token, and the approve endpoint is
      // /agents/{id}/approve-hire (which resolves the inbox row
      // server-side via inbox.ResolveBySource). The generic
      // waitpointDecide() helper would 404 against the pipeline
      // waitpoints route for these. Disambiguated by payload.kind,
      // which writeInboxItem sets to "hire" for both blocking and
      // non-blocking hire surfaces (blocking lands as kind=waitpoint).
      if (item.payload?.kind === "hire") {
        return (
          <div className="flex items-center gap-2">
            <Button
              size="sm"
              disabled={disabled || busy !== null}
              onClick={() =>
                wrap("approved", async () => {
                  // fetch() rejects on network failure (offline, DNS,
                  // CORS preflight). Without try/catch the user sees
                  // no toast and the action looks like silent success.
                  let res: Response
                  try {
                    res = await apiFetch(
                      `/api/v1/agents/${encodeURIComponent(item.source_id)}/approve-hire`,
                      {
                        method: "POST",
                        headers: { "Content-Type": "application/json" },
                      },
                    )
                  } catch (e) {
                    toast.error(e instanceof Error ? `Approve failed: ${e.message}` : "Approve failed (network error)")
                    return
                  }
                  if (!res.ok) {
                    const body = (await res.json().catch(() => null)) as
                      | { error?: string; reason?: string }
                      | null
                    toast.error(body?.error ?? `Approve failed (${res.status})`)
                    return
                  }
                  toast.success("Hire approved — agent is live")
                  await onRefresh()
                })
              }
              className="gap-1.5 bg-success/20 text-success hover:bg-success/30"
            >
              <CheckCircle2 className="h-3 w-3" />
              {busy === "approved" ? "Approving…" : "Approve hire"}
            </Button>
            {/* No deny counterpart exists for approve-hire yet — the
                PENDING_REVIEW agent stays put until the operator fires
                it from the crew. Surface that explicitly so the
                missing button doesn't read as broken UI. */}
            <span className="text-[11px] text-muted-foreground">
              To deny, fire the agent from its crew page.
            </span>
          </div>
        )
      }
      // Both Approve and Deny hit the same /approve endpoint —
      // the body's `approved` boolean is what disambiguates. An empty
      // body decoded to approved=false because Go's JSON unmarshal
      // gives bools their zero value when absent, so a "{}" body was
      // silently equivalent to denying. The earlier "already decided
      // or expired" complaint was the second click hitting the
      // already-denied row.
      return (
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            disabled={disabled || busy !== null}
            onClick={() =>
              wrap("approved", async () => {
                const res = await waitpointDecide(item.workspace_id, item.source_id, true)
                if (!res.ok) {
                  toast.error(res.error)
                  return
                }
                // Server-side CompleteApproval already cascades the
                // inbox row to resolved via inbox.ResolveBySource, so
                // we must NOT PATCH the inbox ourselves — a waitpoint
                // is a source-managed item and the inbox PATCH rejects
                // any state other than "read" with a 409 ("use the
                // source endpoint for this kind"). Re-fetch instead, to
                // match the escalation branch below.
                await onRefresh()
              })
            }
            className="gap-1.5 bg-success/20 text-success hover:bg-success/30"
          >
            <CheckCircle2 className="h-3 w-3" />
            {busy === "approved" ? "Approving…" : "Approve"}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={disabled || busy !== null}
            onClick={() =>
              wrap("denied", async () => {
                const res = await waitpointDecide(item.workspace_id, item.source_id, false)
                if (!res.ok) {
                  toast.error(res.error)
                  return
                }
                // Same as approve: the server cascades the inbox row;
                // a self-PATCH to "resolved" would 409 (source-managed).
                await onRefresh()
              })
            }
            className="gap-1.5"
          >
            <XCircle className="h-3 w-3" />
            {busy === "denied" ? "Denying…" : "Deny"}
          </Button>
        </div>
      )
    }
    case "escalation": {
      // Agent-authored skill proposals ride the escalation kind (like keeper
      // skill reviews) but resolve through the proposed-skills approve/reject
      // endpoints, not the escalation lifecycle. Disambiguated by payload.kind.
      // Approving here promotes the staged SKILL.md into the crew's registry
      // (tagged GENERATED); the server resolves this inbox row via
      // ResolveBySource so it leaves the queue.
      if (item.payload?.kind === "skill_proposal") {
        const crewId = typeof item.payload?.crew_id === "string" ? (item.payload.crew_id as string) : ""
        const fileName = typeof item.payload?.file_name === "string" ? (item.payload.file_name as string) : ""
        const resolveSkill = (action: "approve" | "reject") =>
          wrap(action, async () => {
            let res: Response
            try {
              res = await apiFetch(
                `/api/v1/skills/proposed/${action}?workspace_id=${encodeURIComponent(item.workspace_id)}`,
                {
                  method: "POST",
                  headers: { "Content-Type": "application/json" },
                  body: JSON.stringify({ crew_id: crewId, file_name: fileName }),
                },
              )
            } catch (e) {
              toast.error(e instanceof Error ? `${action} failed: ${e.message}` : `${action} failed`)
              return
            }
            if (!res.ok) {
              const b = await res.json().catch(() => null)
              toast.error(b?.error ?? `${action} failed (${res.status})`)
              return
            }
            toast.success(action === "approve" ? "Skill approved" : "Skill rejected")
            await onRefresh()
          })
        return (
          <div className="flex items-center gap-2">
            <Button
              size="sm"
              disabled={disabled || busy !== null}
              onClick={() => resolveSkill("approve")}
              className="gap-1.5 bg-success/20 text-success hover:bg-success/30"
            >
              <CheckCircle2 className="h-3 w-3" />
              {busy === "approve" ? "Approving…" : "Approve"}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={disabled || busy !== null}
              onClick={() => resolveSkill("reject")}
              className="gap-1.5"
            >
              <XCircle className="h-3 w-3" />
              {busy === "reject" ? "Rejecting…" : "Reject"}
            </Button>
          </div>
        )
      }
      // An escalation is an agent decision request — resolving it must go
      // through the escalation lifecycle (/escalations/{id}/resolve), NOT
      // a blind inbox flip (that 409s, since escalation is source-managed).
      // Real agent escalations carry escalation_type + a source_id that IS
      // the escalations-row id. Keeper/synthetic escalations don't — for
      // those the inbox can't resolve inline, so we point at the source.
      const escType =
        typeof item.payload?.escalation_type === "string"
          ? (item.payload.escalation_type as string)
          : ""
      const resolveEsc = (action: "approve" | "reject") =>
        wrap(action, async () => {
          const res = await escalationResolve(
            item.source_id,
            action,
            action === "approve" ? "Approved from inbox" : "Rejected from inbox",
            item.workspace_id,
          )
          if (!res.ok) {
            // 404 = no escalations row behind this item (keeper/synthetic):
            // tell the user where to handle it instead of a raw error.
            toast.error(
              res.status === 404
                ? "Resolve this from its source (crew escalations / review panel)."
                : res.error,
            )
            return
          }
          // The lifecycle cascades the inbox row to resolved via
          // ResolveBySource server-side; refresh to pick that up.
          toast.success(`Escalation ${action === "approve" ? "approved" : "rejected"}`)
          await onRefresh()
        })

      // CREDENTIAL escalations: when the agent already proposed a value, it is
      // sitting in the vault as PENDING_APPROVAL, so Approve just activates it
      // (no secret to type here) and Reject discards it — one-click both ways.
      // Legacy CREDENTIAL escalations (no pending credential, the human must
      // supply the secret) keep Reject-only and point at the crew panel.
      if (escType === "CREDENTIAL") {
        if (item.payload?.has_pending_credential === true) {
          return (
            <div className="flex items-center gap-2">
              <Button
                size="sm"
                disabled={disabled || busy !== null}
                onClick={() => resolveEsc("approve")}
                className="gap-1.5 bg-success/20 text-success hover:bg-success/30"
              >
                <CheckCircle2 className="h-3 w-3" />
                {busy === "approve" ? "Approving…" : "Approve"}
              </Button>
              <Button
                size="sm"
                variant="ghost"
                disabled={disabled || busy !== null}
                onClick={() => resolveEsc("reject")}
                className="gap-1.5"
              >
                <XCircle className="h-3 w-3" />
                {busy === "reject" ? "Rejecting…" : "Reject"}
              </Button>
            </div>
          )
        }
        return (
          <div className="flex items-center gap-2">
            <Button
              size="sm"
              variant="ghost"
              disabled={disabled || busy !== null}
              onClick={() => resolveEsc("reject")}
              className="gap-1.5"
            >
              <XCircle className="h-3 w-3" />
              {busy === "reject" ? "Rejecting…" : "Reject"}
            </Button>
            <span className="text-[11px] text-muted-foreground">
              To grant the credential, resolve from the crew’s escalations panel.
            </span>
          </div>
        )
      }
      // Real agent escalation (non-credential): inline approve / reject.
      if (escType !== "") {
        return (
          <div className="flex items-center gap-2">
            <Button
              size="sm"
              disabled={disabled || busy !== null}
              onClick={() => resolveEsc("approve")}
              className="gap-1.5 bg-success/20 text-success hover:bg-success/30"
            >
              <CheckCircle2 className="h-3 w-3" />
              {busy === "approve" ? "Approving…" : "Approve"}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={disabled || busy !== null}
              onClick={() => resolveEsc("reject")}
              className="gap-1.5"
            >
              <XCircle className="h-3 w-3" />
              {busy === "reject" ? "Rejecting…" : "Reject"}
            </Button>
          </div>
        )
      }
      // Keeper / synthetic escalation — no inline decision to make here; the
      // source review tracks the outcome. There's no resolve endpoint behind
      // it, so the inbox row is the only handle: point at Archive (enabled
      // for these) rather than a button that 409s.
      return (
        <span className="text-[11px] text-muted-foreground">
          No decision to make here — its source review tracks the outcome. Use “Archive” to clear it
          from your inbox.
        </span>
      )
    }
    case "failed_run":
      // Retry actually re-fires the routine: POST /pipelines/{slug}/run
      // with the same inputs that produced the failure (replayed from
      // the run's inputs_json so dynamic context is preserved). The
      // payload carries the slug + inputs the writer captured at
      // failure time. If the slug is missing we fall back to just
      // marking the inbox item resolved so the user isn't stuck.
      return (
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            disabled={disabled || busy !== null}
            onClick={() =>
              wrap("retried", async () => {
                const slug = (item.payload?.pipeline_slug ??
                  item.sender_name) as string | undefined
                const inputs = (item.payload?.inputs ?? {}) as Record<string, unknown>
                if (!slug) {
                  toast.error("Cannot retry — pipeline slug missing in payload")
                  await onResolve("cancelled")
                  return
                }
                // Same try/catch pattern as approve-hire above: fetch()
                // rejects on network failure (offline, DNS, CORS); the
                // wrap() helper clears busy state on return, so without
                // explicit error handling the user sees no toast and
                // the retry appears to silently succeed.
                let res: Response
                try {
                  res = await apiFetch(
                    `/api/v1/workspaces/${encodeURIComponent(item.workspace_id)}/pipelines/${encodeURIComponent(slug)}/run`,
                    {
                      method: "POST",
                      headers: { "Content-Type": "application/json" },
                      body: JSON.stringify({ inputs, triggered_via: "manual" }),
                    },
                  )
                } catch (e) {
                  toast.error(e instanceof Error ? `Retry failed: ${e.message}` : "Retry failed (network error)")
                  return
                }
                if (!res.ok) {
                  const body = await res.json().catch(() => null)
                  toast.error(body?.error ?? "Retry failed")
                  return
                }
                toast.success(`Routine ${slug} re-queued — see /activity`)
                await onResolve("retried")
              })
            }
            className="gap-1.5"
          >
            <ScrollText className="h-3 w-3" />
            {busy === "retried" ? "Retrying…" : "Retry"}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={disabled || busy !== null}
            onClick={() => wrap("cancelled", async () => onResolve("cancelled"))}
            className="gap-1.5"
          >
            Cancel
          </Button>
        </div>
      )
    case "memory_consolidation": {
      // /consolidate/proposed/{id}/approve and /reject have existed since the
      // consolidator shipped, and proposal_id is in the payload — the inbox
      // simply never called them, so the only offered action was Dismiss.
      const proposalID = typeof item.payload?.proposal_id === "string" ? item.payload.proposal_id : ""
      if (!proposalID) {
        return (
          <Button
            size="sm"
            disabled={disabled || busy !== null}
            onClick={() => wrap("dismissed", async () => onResolve("dismissed"))}
            className="gap-1.5"
          >
            Dismiss
          </Button>
        )
      }
      const decide = (action: "approve" | "reject") =>
        wrap(action, async () => {
          let res: Response
          try {
            res = await apiFetch(
              `/api/v1/consolidate/proposed/${encodeURIComponent(proposalID)}/${action}?workspace_id=${encodeURIComponent(item.workspace_id)}`,
              { method: "POST", headers: { "Content-Type": "application/json" } },
            )
          } catch (e) {
            toast.error(e instanceof Error ? `${action} failed: ${e.message}` : `${action} failed`)
            return
          }
          if (!res.ok) {
            const b = await res.json().catch(() => null)
            toast.error(b?.error ?? `${action} failed (${res.status})`)
            return
          }
          toast.success(action === "approve" ? "Consolidation accepted" : "Consolidation rejected")
          await onResolve(action === "approve" ? "approved" : "rejected")
        })
      return (
        <div className="flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            disabled={disabled || busy !== null}
            onClick={() => decide("approve")}
            className="gap-1.5 bg-success/20 text-success hover:bg-success/30"
          >
            <CheckCircle2 className="h-3 w-3" />
            {busy === "approve" ? "Accepting…" : "Accept"}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={disabled || busy !== null}
            onClick={() => decide("reject")}
            className="gap-1.5"
          >
            <XCircle className="h-3 w-3" />
            {busy === "reject" ? "Rejecting…" : "Reject"}
          </Button>
        </div>
      )
    }
    case "schedule_circuit_breaker_tripped":
      // The routine disabled itself and nothing in the API can switch it back
      // on — `crewship routine schedules enable` is the only path today. Say
      // that instead of drawing a button that cannot work; the follow-up is an
      // endpoint plus its CLI command, per the API↔CLI parity rule.
      return (
        <div className="flex flex-wrap items-center gap-2">
          <Button asChild size="sm" variant="ghost" className="gap-1.5">
            <Link href="/routines">
              <ScrollText className="h-3 w-3" />
              Open routines
            </Link>
          </Button>
          <code className="rounded bg-white/[0.06] px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">
            crewship routine schedules enable {String(item.payload?.schedule_id ?? "")}
          </code>
        </div>
      )
    case "schedule_missed":
      return (
        <div className="flex flex-wrap items-center gap-2">
          <Button asChild size="sm" variant="ghost" className="gap-1.5">
            <Link href="/routines">
              <ScrollText className="h-3 w-3" />
              Open routines
            </Link>
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={disabled || busy !== null}
            onClick={() => wrap("dismissed", async () => onResolve("dismissed"))}
            className="gap-1.5"
          >
            Dismiss
          </Button>
        </div>
      )
    case "message":
      // Messages from the orchestrator (e.g. "ENG-1 ready for review")
      // carry the issue identifier in payload so the inbox can offer
      // a one-click jump to the issue. Without this the user reads
      // the title and has nowhere to go. "Agent replied" items carry
      // chat_url instead — deep link straight into the session.
      return (
        <div className="flex items-center gap-2">
          {chatUrlOf(item) && (
              <Button asChild size="sm" className="gap-1.5">
                <Link href={chatUrlOf(item) as string}>
                  <MessageSquare className="h-3 w-3" />
                  Open chat
                </Link>
              </Button>
            )}
          {typeof item.payload?.issue_identifier === "string" && (
            <Button asChild size="sm" className="gap-1.5">
              <Link
                href={`/issues/${encodeURIComponent(item.payload.issue_identifier as string)}`}
              >
                <CircleDot className="h-3 w-3" />
                Open {item.payload.issue_identifier}
              </Link>
            </Button>
          )}
          <Button
            size="sm"
            variant="ghost"
            disabled={disabled || busy !== null}
            onClick={() => wrap("dismissed", async () => onResolve("dismissed"))}
            className="gap-1.5"
          >
            Dismiss
          </Button>
        </div>
      )
    default:
      return (
        <Button
          size="sm"
          disabled={disabled || busy !== null}
          onClick={() => wrap("dismissed", async () => onResolve("dismissed"))}
          className="gap-1.5"
        >
          Dismiss
        </Button>
      )
  }
}
