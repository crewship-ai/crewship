"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { ExternalLink, Loader2 } from "lucide-react"
import type { RoutineDetail } from "./routines-detail-panel"
import { Badge } from "@/components/ui/badge"
import { JSONViewer } from "@/components/features/activity/json-viewer"
import { relTime, formatDuration } from "@/lib/activity/format-time"
import { statusTint } from "@/lib/activity/run-status"
import { cn } from "@/lib/utils"

// RoutineOverviewTab — read-only meta inspector. Mirrors what
// orchestration's pipeline-detail-sheet showed under Overview, expanded
// with declared egress / credentials / inputs blocks pulled from the
// routine's DSL (when available). The trailing "Last run result"
// section makes the panel useful at a glance — opening a routine
// without a recent output forces the user to drill into the Runs tab
// to answer the obvious "did this work?" question.

// asArrayOfObjects defensively extracts an array-of-records from a
// DSL field. Older routines or hand-edited JSON can ship arrays
// containing scalars, or a non-array value where an array is
// expected; without this guard the .map() calls below would crash
// the whole tab. Returning [] keeps the section silent for malformed
// fields (the Editor sub-tab still surfaces the raw JSON for
// diagnosis).
function asArrayOfObjects(v: unknown): Array<Record<string, unknown>> {
  if (!Array.isArray(v)) return []
  const out: Array<Record<string, unknown>> = []
  for (const item of v) {
    if (item && typeof item === "object" && !Array.isArray(item)) {
      out.push(item as Record<string, unknown>)
    }
  }
  return out
}

function asArrayOfStrings(v: unknown): string[] {
  if (!Array.isArray(v)) return []
  return v.filter((x): x is string => typeof x === "string")
}

export function RoutineOverviewTab({
  routine,
  workspaceId,
}: {
  routine: RoutineDetail
  workspaceId?: string
}) {
  const def = routine.definition as Record<string, unknown> | undefined
  const inputs = asArrayOfObjects(def?.["inputs"])
  const outputs = asArrayOfObjects(def?.["outputs"])
  const egress = asArrayOfStrings(def?.["egress_targets"])
  const creds = asArrayOfObjects(def?.["credentials_required"])
  const tier = def?.["execution_tier"] as Record<string, unknown> | undefined
  const steps = asArrayOfObjects(def?.["steps"])

  return (
    <div className="space-y-4 text-xs">
      {/* Description */}
      {routine.description && (
        <p className="text-foreground/90">{routine.description}</p>
      )}

      {/* Metadata grid */}
      <Section title="Identity">
        <Row label="Slug" value={routine.slug} mono />
        <Row label="DSL version" value={routine.dsl_version} />
        <Row label="Definition hash" value={routine.definition_hash.slice(0, 16) + "…"} mono />
        <Row label="Visibility" value={routine.workspace_visible ? "workspace-visible" : "private"} />
        {routine.ephemeral && <Row label="Type" value="ephemeral (auto-generated)" />}
      </Section>

      <Section title="Authorship">
        <Row label="Authored via" value={routine.authored_via.replace(/_/g, " ")} />
        <Row label="Author crew" value={routine.author_crew_id || "—"} mono />
        <Row label="Author agent" value={routine.author_agent_id || "—"} mono />
        <Row label="Created" value={new Date(routine.created_at).toLocaleString()} />
        <Row label="Updated" value={new Date(routine.updated_at).toLocaleString()} />
      </Section>

      <Section title="Activity">
        <Row label="Total invocations" value={String(routine.invocation_count)} />
        {routine.last_invoked_at && (
          <Row
            label="Last invoked"
            value={`${new Date(routine.last_invoked_at).toLocaleString()}${
              routine.last_invocation_status ? ` (${routine.last_invocation_status})` : ""
            }`}
          />
        )}
        <Row label="Step count" value={String(steps.length)} />
      </Section>

      {tier && (
        <Section title="Execution tier">
          {tier["preferred"] != null && <Row label="Preferred" value={String(tier["preferred"])} />}
          {Array.isArray(tier["fallback"]) && (tier["fallback"] as unknown[]).length > 0 && (
            <Row label="Fallback chain" value={(tier["fallback"] as string[]).join(" → ")} />
          )}
        </Section>
      )}

      {inputs.length > 0 && (
        <Section title={`Inputs (${inputs.length})`}>
          <ul className="space-y-1">
            {inputs.map((inp, i) => (
              <li key={i} className="rounded bg-muted/30 px-2 py-1 font-mono">
                <span className="text-blue-300">{String(inp["name"])}</span>
                <span className="text-muted-foreground"> · {String(inp["type"])}</span>
                {inp["required"] === true && (
                  <Badge variant="outline" className="ml-1.5 px-1 py-0 text-[9px]">required</Badge>
                )}
                {"default" in inp && (
                  <span className="ml-1.5 text-muted-foreground">= {JSON.stringify(inp["default"])}</span>
                )}
                {typeof inp["description"] === "string" && (
                  <p className="mt-0.5 text-[11px] font-sans text-muted-foreground/80">
                    {String(inp["description"])}
                  </p>
                )}
              </li>
            ))}
          </ul>
        </Section>
      )}

      {outputs.length > 0 && (
        <Section title={`Outputs (${outputs.length})`}>
          <ul className="space-y-1">
            {outputs.map((out, i) => (
              <li key={i} className="rounded bg-muted/30 px-2 py-1 font-mono">
                <span className="text-emerald-300">{String(out["name"])}</span>
                <span className="text-muted-foreground"> · {String(out["type"])}</span>
              </li>
            ))}
          </ul>
        </Section>
      )}

      {egress.length > 0 && (
        <Section title="Declared egress">
          <div className="flex flex-wrap gap-1.5">
            {egress.map((host) => (
              <Badge key={host} variant="outline" className="font-mono text-[10px]">
                {host}
              </Badge>
            ))}
          </div>
        </Section>
      )}

      {creds.length > 0 && (
        <Section title="Credentials required">
          <ul className="space-y-1">
            {creds.map((c, i) => (
              <li key={i} className="rounded bg-muted/30 px-2 py-1 font-mono">
                <span className="text-amber-300">{String(c["type"])}</span>
                {typeof c["scope"] === "string" && (
                  <span className="ml-1.5 text-muted-foreground">scope: {String(c["scope"])}</span>
                )}
              </li>
            ))}
          </ul>
        </Section>
      )}

      {workspaceId && (
        <LastRunSection workspaceId={workspaceId} slug={routine.slug} />
      )}
    </div>
  )
}

// LastRunSection fetches the most recent run for this routine and
// surfaces its final output + step outputs inline. Mirrors what the
// /activity rail's RoutinePreviewCard shows on hover; here we pin it
// to the panel because the user already committed to opening the
// routine — they want to see what it produced last time without
// switching to the Runs tab.
//
// Two-step fetch: list-records gives us the most recent run id, the
// per-run endpoint gives us step_outputs (which the list DTO omits to
// keep the table small). The cost is one extra request per panel
// open; the alternative is duplicating step_outputs into the list
// DTO which would balloon every Runs-tab response.
interface LatestRunRecord {
  id: string
  status: string
  started_at: string
  ended_at: string
  duration_ms: number
  cost_usd: number
  output: string
  error_message: string
  failed_at_step: string
}

interface LatestRunDetail {
  step_outputs?: Record<string, unknown> | null
  output?: string
}

function LastRunSection({ workspaceId, slug }: { workspaceId: string; slug: string }) {
  const [record, setRecord] = useState<LatestRunRecord | null>(null)
  const [detail, setDetail] = useState<LatestRunDetail | null>(null)
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState<string | null>(null)

  useEffect(() => {
    // AbortController so a fast routine switch (or unmount) actually
    // cancels in-flight fetches instead of just discarding their
    // results. The `cancelled` flag still guards setState calls in
    // case the fetch resolves between abort() and the next tick.
    const ctrl = new AbortController()
    let cancelled = false
    setLoading(true)
    setErr(null)
    setRecord(null)
    setDetail(null)

    const wsEnc = encodeURIComponent(workspaceId)
    const slugEnc = encodeURIComponent(slug)
    ;(async () => {
      try {
        const recRes = await fetch(
          `/api/v1/workspaces/${wsEnc}/pipelines/${slugEnc}/run-records?limit=1`,
          { signal: ctrl.signal },
        )
        if (!recRes.ok) {
          if (recRes.status === 503) {
            // Older deployments without the runStore wired — silently
            // hide the section rather than showing an alarming error.
            if (!cancelled) setLoading(false)
            return
          }
          throw new Error(`run-records: ${recRes.status}`)
        }
        const records = (await recRes.json()) as LatestRunRecord[]
        if (cancelled) return
        if (!Array.isArray(records) || records.length === 0) {
          setLoading(false)
          return
        }
        const rec = records[0]
        setRecord(rec)
        // Fire the detail fetch in the same effect so step_outputs
        // arrive together with the record. Failure here is non-fatal
        // — we still render the record-level summary.
        try {
          const detRes = await fetch(
            `/api/v1/workspaces/${wsEnc}/pipeline-runs/${encodeURIComponent(rec.id)}`,
            { signal: ctrl.signal },
          )
          if (detRes.ok && !cancelled) {
            setDetail((await detRes.json()) as LatestRunDetail)
          }
        } catch {
          /* ignore — the record-level summary is enough */
        }
      } catch (e) {
        // AbortError lands here as a DOMException — ignore it; the
        // unmount cleanup is the cause and the component is gone.
        if (ctrl.signal.aborted) return
        if (!cancelled) setErr(e instanceof Error ? e.message : String(e))
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()

    return () => {
      ctrl.abort()
      cancelled = true
    }
  }, [workspaceId, slug])

  if (loading) {
    return (
      <Section title="Last run result">
        <div className="flex items-center gap-1.5 text-muted-foreground/60">
          <Loader2 className="h-3 w-3 animate-spin" /> Loading…
        </div>
      </Section>
    )
  }
  if (err) {
    return (
      <Section title="Last run result">
        <p className="text-muted-foreground/60">Couldn't load: {err}</p>
      </Section>
    )
  }
  if (!record) {
    return (
      <Section title="Last run result">
        <p className="text-muted-foreground/60">Routine hasn't been invoked yet.</p>
      </Section>
    )
  }

  const tint = statusTint(record.status)
  const stepOutputs = detail?.step_outputs ?? {}
  const stepEntries = Object.entries(stepOutputs)
  const finalOutput = detail?.output ?? record.output

  return (
    <Section title="Last run result">
      <div className="space-y-2 rounded border border-white/[0.06] bg-muted/20 p-2">
        <div className="flex items-center gap-2 text-[11px]">
          <span className={cn("rounded px-1.5 py-0 capitalize", tint.bg, tint.text)}>
            {record.status}
          </span>
          <span className="font-mono text-[10px] text-muted-foreground/70">{record.id}</span>
          <span className="ml-auto flex items-center gap-2 text-muted-foreground/60">
            <span>{relTime(record.started_at)}</span>
            {record.duration_ms > 0 && <span>· {formatDuration(record.duration_ms)}</span>}
            {record.cost_usd > 0 && <span>· ${record.cost_usd.toFixed(4)}</span>}
          </span>
        </div>

        {record.error_message && (
          <div className="rounded border border-rose-500/30 bg-rose-500/10 px-2 py-1.5 font-mono text-[10px] text-rose-300">
            {record.failed_at_step && <span className="opacity-70">{record.failed_at_step}: </span>}
            {record.error_message}
          </div>
        )}

        {stepEntries.length > 1 && (
          <div className="space-y-1.5">
            <div className="text-[10px] uppercase tracking-wider text-muted-foreground/60">
              Step outputs ({stepEntries.length})
            </div>
            {stepEntries.map(([stepId, value]) => (
              <details key={stepId} className="rounded border border-white/[0.04]">
                <summary className="cursor-pointer px-2 py-1 font-mono text-[11px] text-blue-300 hover:bg-white/[0.03]">
                  {stepId}
                </summary>
                <div className="border-t border-white/[0.04] p-1.5">
                  <JSONViewer value={value} />
                </div>
              </details>
            ))}
          </div>
        )}

        {/* Final output. Hidden when there are >1 step outputs above
          * (final == last step, would just duplicate); shown for
          * single-step routines or when step_outputs is unavailable. */}
        {finalOutput && stepEntries.length <= 1 && (
          <div className="space-y-1">
            <div className="text-[10px] uppercase tracking-wider text-muted-foreground/60">
              Output
            </div>
            <JSONViewer value={finalOutput} />
          </div>
        )}

        <div className="border-t border-white/[0.04] pt-1">
          <Link
            href={`/activity?run=${encodeURIComponent(record.id)}`}
            className="inline-flex items-center gap-1 rounded bg-blue-500/15 px-2 py-1 text-[10px] text-blue-300 hover:bg-blue-500/25"
          >
            <ExternalLink className="h-3 w-3" />
            Open full trace in Activity
          </Link>
        </div>
      </div>
    </Section>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div>
      <h3 className="mb-1.5 text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
        {title}
      </h3>
      <div className="space-y-1">{children}</div>
    </div>
  )
}

function Row({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex items-baseline gap-3">
      <span className="w-36 shrink-0 text-muted-foreground">{label}</span>
      <span className={mono ? "font-mono break-all" : ""}>{value}</span>
    </div>
  )
}
