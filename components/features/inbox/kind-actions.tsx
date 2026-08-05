"use client"

import { useState } from "react"
import Link from "next/link"
import { CheckCircle2, CircleDot, MessageSquare, Play, Power, ScrollText, XCircle } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { apiFetch } from "@/lib/api-fetch"
import { isAlreadyDecidedError, waitpointDecide } from "@/lib/api/waitpoints"
import { escalationResolve } from "@/lib/api/escalations"
import type { InboxItem } from "@/hooks/use-inbox"

/**
 * Valid in-app chat deep link from a reply notification's payload.
 *
 * The guard has to reject "//evil.example/x" as well as "https://…": a
 * protocol-relative URL starts with "/" and the browser resolves it against
 * the current scheme, so a payload an agent controls could navigate a manager
 * off-origin from a link that looks internal. One leading slash, not two, and
 * no backslash either — some parsers fold "/\" onto "//".
 */
function chatUrlOf(item: InboxItem): string | null {
  const v = item.payload?.chat_url
  if (typeof v !== "string") return null
  if (!v.startsWith("/")) return null
  if (v.startsWith("//") || v.startsWith("/\\")) return null
  return v
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
                  // Somebody approved it from /activity, or it timed out. That
                  // is a resolution, not an error — the trace surface has
                  // always shown it as one, and a red toast here made the same
                  // outcome look like a broken button.
                  if (isAlreadyDecidedError(res.status, res.error)) {
                    toast.info("Already decided elsewhere — refreshing")
                    await onRefresh()
                    return
                  }
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
                  if (isAlreadyDecidedError(res.status, res.error)) {
                    toast.info("Already decided elsewhere — refreshing")
                    await onRefresh()
                    return
                  }
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
      // Routine proposals ride the escalation kind too, and resolve
      // through the routine's own lifecycle endpoints — approve flips it
      // to active, reject soft-deletes the proposal. Both are MANAGER+,
      // the same tier that saved it.
      //
      // Without this branch the row fell through to the "no decision to
      // make here" note at the bottom: the inbox told a reviewer there
      // was nothing to do while the routine sat at `proposed`, unable to
      // run, with both endpoints waiting on the server. The queue said
      // "your move" and the item said "not here".
      if (item.payload?.kind === "routine_proposal") {
        const slug = typeof item.payload?.slug === "string" ? (item.payload.slug as string) : ""
        // No slug, no endpoint. A button that cannot work is worse than
        // none — it turns a data problem into a mystery failure.
        if (slug === "") {
          return (
            <span className="text-[11px] text-muted-foreground">
              This proposal carries no routine to act on. Open the routine to decide.
            </span>
          )
        }
        const resolveRoutine = (action: "approve" | "reject") =>
          wrap(action, async () => {
            let res: Response
            try {
              res = await apiFetch(
                `/api/v1/workspaces/${encodeURIComponent(item.workspace_id)}/pipelines/${encodeURIComponent(slug)}/${action}`,
                { method: "POST" },
              )
            } catch (e) {
              toast.error(e instanceof Error ? `${action} failed: ${e.message}` : `${action} failed`)
              return
            }
            if (!res.ok) {
              const b = await res.json().catch(() => null)
              // 409 is the other reviewer having got there first. That is
              // the queue working, not a failure, so it must not read as
              // one.
              toast.error(
                res.status === 409
                  ? `Already decided — this routine is ${b?.status ?? "no longer awaiting approval"}.`
                  : (b?.error ?? `${action} failed (${res.status})`),
              )
              await onRefresh()
              return
            }
            toast.success(action === "approve" ? "Routine approved" : "Routine rejected")
            await onRefresh()
          })

        return (
          <div className="flex items-center gap-2">
            <Button
              size="sm"
              disabled={disabled || busy !== null}
              onClick={() => resolveRoutine("approve")}
              className="gap-1.5 bg-success/20 text-success hover:bg-success/30"
            >
              <CheckCircle2 className="h-3 w-3" />
              {busy === "approve" ? "Approving…" : "Approve"}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={disabled || busy !== null}
              onClick={() => resolveRoutine("reject")}
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
      // A keeper CREDENTIAL request is not an escalations row — it is a
      // keeper_requests row, which is why resolving it here used to 404 and the
      // card admitted "a keeper request has no resolve endpoint yet". It has one
      // now, and this is the branch that uses it.
      //
      // The decision the whole tier system defers to a person was, until this,
      // the one decision the product could not accept: L4 is never granted by
      // the model alone, and the human had nowhere to say yes.
      const keeperRequestID =
        typeof item.payload?.request_id === "string" ? (item.payload.request_id as string) : ""
      if (item.payload?.request_type === "access" && keeperRequestID) {
        const resolveKeeper = (action: "approve" | "reject") =>
          wrap(action, async () => {
            // apiFetch REJECTS on a transport failure rather than resolving with
            // a non-ok Response, and the click handler discards this promise — so
            // an uncaught rejection would leave the operator staring at a button
            // that did nothing. On a decision this consequential, "no feedback"
            // is the worst possible answer: they cannot tell a refused approval
            // from an unsent one, and the honest recovery is to try again.
            let res: Response
            try {
              res = await apiFetch(
                `/api/v1/admin/keeper/requests/${encodeURIComponent(keeperRequestID)}/resolve` +
                  `?workspace_id=${encodeURIComponent(item.workspace_id)}`,
                {
                  method: "POST",
                  headers: { "Content-Type": "application/json" },
                  body: JSON.stringify({
                    decision: action === "approve" ? "ALLOW" : "DENY",
                    reason: action === "approve" ? "Approved from inbox" : "Denied from inbox",
                  }),
                },
              )
            } catch (e) {
              toast.error(
                e instanceof Error
                  ? `Could not reach the server: ${e.message}`
                  : "Could not reach the server",
              )
              return
            }
            if (!res.ok) {
              // 403 here is usually four-eyes, not a permissions mistake, and
              // the server's message says which — so it is shown rather than
              // replaced with a generic refusal.
              let detail = `Could not resolve (HTTP ${res.status})`
              try {
                const body = (await res.json()) as { detail?: string; error?: string }
                detail = body.detail ?? body.error ?? detail
              } catch {
                // Keep the status-code message.
              }
              toast.error(detail)
              return
            }
            toast.success(action === "approve" ? "Credential approved" : "Credential denied")
            await onRefresh()
          })

        return (
          <div className="flex items-center gap-2">
            <Button size="sm" variant="soft" disabled={disabled || busy !== null}
              onClick={() => void resolveKeeper("approve")}>
              {busy === "approve" ? "Approving…" : "Approve"}
            </Button>
            <Button size="sm" variant="soft" disabled={disabled || busy !== null}
              onClick={() => void resolveKeeper("reject")}>
              {busy === "reject" ? "Denying…" : "Deny"}
            </Button>
          </div>
        )
      }

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
    case "schedule_circuit_breaker_tripped": {
      // The schedule turned itself off after N consecutive failures. Turning it
      // back on is PATCH pipeline-schedules/{id} {enabled:true} — the same call
      // `crewship routine schedules enable` makes, and OWNER/ADMIN like it.
      const scheduleID = typeof item.payload?.schedule_id === "string" ? item.payload.schedule_id : ""
      if (!scheduleID) {
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
      return (
        <div className="flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            disabled={disabled || busy !== null}
            onClick={() =>
              wrap("reenabled", async () => {
                let res: Response
                try {
                  res = await apiFetch(
                    `/api/v1/workspaces/${encodeURIComponent(item.workspace_id)}/pipeline-schedules/${encodeURIComponent(scheduleID)}`,
                    {
                      method: "PATCH",
                      headers: { "Content-Type": "application/json" },
                      body: JSON.stringify({ enabled: true }),
                    },
                  )
                } catch (e) {
                  toast.error(e instanceof Error ? `Re-enable failed: ${e.message}` : "Re-enable failed (network error)")
                  return
                }
                if (!res.ok) {
                  const b = await res.json().catch(() => null)
                  toast.error(b?.error ?? `Re-enable failed (${res.status})`)
                  return
                }
                toast.success("Schedule re-enabled — it fires on the next tick")
                await onResolve("reenabled")
              })
            }
            className="gap-1.5 bg-success/20 text-success hover:bg-success/30"
          >
            <Power className="h-3 w-3" />
            {busy === "reenabled" ? "Enabling…" : "Re-enable schedule"}
          </Button>
          <Button asChild size="sm" variant="ghost" className="gap-1.5">
            <Link href="/routines">
              <ScrollText className="h-3 w-3" />
              Open routines
            </Link>
          </Button>
        </div>
      )
    }
    case "schedule_missed": {
      // The occurrences are gone; what a person wants is to fire it now, which
      // is the same out-of-cycle run the CLI's `schedules now` performs.
      const scheduleID = typeof item.payload?.schedule_id === "string" ? item.payload.schedule_id : ""
      return (
        <div className="flex flex-wrap items-center gap-2">
          {scheduleID !== "" && (
            <Button
              size="sm"
              disabled={disabled || busy !== null}
              onClick={() =>
                wrap("ran", async () => {
                  let res: Response
                  try {
                    res = await apiFetch(
                      `/api/v1/workspaces/${encodeURIComponent(item.workspace_id)}/pipeline-schedules/${encodeURIComponent(scheduleID)}/run`,
                      { method: "POST", headers: { "Content-Type": "application/json" } },
                    )
                  } catch (e) {
                    toast.error(e instanceof Error ? `Run failed: ${e.message}` : "Run failed (network error)")
                    return
                  }
                  if (!res.ok) {
                    const b = await res.json().catch(() => null)
                    toast.error(b?.error ?? `Run failed (${res.status})`)
                    return
                  }
                  toast.success("Schedule fired — see /activity")
                  await onResolve("ran")
                })
              }
              className="gap-1.5"
            >
              <Play className="h-3 w-3" />
              {busy === "ran" ? "Running…" : "Run now"}
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
    }
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
