"use client"

import { useEffect, useState } from "react"
import { Activity, AlertTriangle } from "lucide-react"

import { DetailCard } from "@/components/ui/detail"
import { apiFetch } from "@/lib/api-fetch"

/**
 * What the Keeper has been deciding lately.
 *
 * The Keeper had no metric on its own decisions at all until #1664, and the
 * cost of that is on the record: the #1624 bug made the judge deny everything,
 * and it survived several milestones because nothing was watching the shape of
 * the output. A gate that refuses every request looks identical to a gate
 * nobody is testing.
 *
 * The readout has existed on the API and in the CLI since then, and nowhere in
 * the product — which is the same failure one level up. An operator does not
 * run `crewship keeper health` on a hunch; they notice on a screen they already
 * have open, or they do not notice.
 *
 * # Why progressed rate rather than allow rate
 *
 * ESCALATE is not a refusal. A judge that escalates everything is cautious and
 * possibly annoying; a judge that DENIES everything is broken. Pooling them
 * would hide exactly the failure this is here to catch, so the headline counts
 * allow + escalate — requests that went somewhere — against the total.
 *
 * # Counts travel with rates
 *
 * "0% progressed" over four samples is noise; over four hundred it is an
 * outage. The server withholds its alarm below a minimum sample count and this
 * says the count out loud rather than letting a percentage stand alone.
 */
interface KeeperHealth {
  samples: number
  allow: number
  deny: number
  escalate: number
  judge_failures: number
  progressed_rate: number
  judge_failure_rate: number
  p95_latency_ms: number
  min_samples: number
  alarm_progressed_rate: number
  alarm_judge_failure_rate: number
  alarm?: { kind: string; summary: string; at?: string }
  oldest?: string
}

export function KeeperHealthCard() {
  const [health, setHealth] = useState<KeeperHealth | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let live = true
    const load = async () => {
      try {
        const res = await apiFetch("/api/v1/admin/keeper/health")
        if (!res.ok) {
          // A server without the endpoint degrades to saying nothing, not to
          // claiming health. Silence and "all clear" must not look the same.
          if (live) setError(res.status === 404 ? "" : `HTTP ${res.status}`)
          return
        }
        const body = (await res.json()) as KeeperHealth
        if (live) { setHealth(body); setError(null) }
      } catch {
        if (live) setError("unreachable")
      }
    }
    void load()
    return () => { live = false }
  }, [])

  if (error) return null
  if (!health) return null

  // An empty window is a real answer and must read as one. Rendering 0% over
  // zero samples would say the judge refused everything when it decided nothing.
  const empty = health.samples === 0
  const belowMin = health.samples > 0 && health.samples < health.min_samples
  const pct = (n: number) => `${Math.round(n * 100)}%`

  return (
    <DetailCard tone={health.alarm ? "warn" : "default"}>
      <div data-testid="keeper-health" className="flex flex-col gap-2.5">
        <div className="flex items-center gap-2">
          <Activity className="h-4 w-4 text-muted-foreground" />
          <span className="type-section">Recent decisions</span>
        </div>

        {empty ? (
          <p className="type-meta text-muted-foreground">
            The window is empty — the Keeper has not decided anything yet. Nothing
            here is a claim about its health.
          </p>
        ) : (
          <>
            <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-[11px]">
              <dt className="text-muted-foreground/70">Progressed</dt>
              <dd className={health.progressed_rate < health.alarm_progressed_rate ? "text-warn" : "text-foreground/80"}>
                {pct(health.progressed_rate)} of {health.samples} — allow {health.allow},
                escalate {health.escalate}, deny {health.deny}
              </dd>

              <dt className="text-muted-foreground/70">Judge failures</dt>
              <dd className={health.judge_failure_rate > health.alarm_judge_failure_rate ? "text-warn" : "text-foreground/80"}>
                {pct(health.judge_failure_rate)} ({health.judge_failures}) — a
                failure is a DENY the judge never made
              </dd>

              <dt className="text-muted-foreground/70">p95 latency</dt>
              <dd className="text-foreground/80">{health.p95_latency_ms} ms</dd>

              {health.oldest ? (
                <>
                  <dt className="text-muted-foreground/70">Window opens</dt>
                  <dd className="text-foreground/80">{health.oldest}</dd>
                </>
              ) : null}
            </dl>

            {belowMin ? (
              <p className="type-meta text-muted-foreground-soft">
                Below {health.min_samples} samples the server withholds its alarm,
                and so should you: a rate over {health.samples} decisions is noise,
                not a signal.
              </p>
            ) : null}
          </>
        )}

        {health.alarm ? (
          <div className="flex items-start gap-2 rounded-md border border-warn/30 bg-warn/5 dark:bg-warn/20 p-2.5">
            <AlertTriangle className="h-3.5 w-3.5 text-warn mt-0.5 shrink-0" />
            <div className="space-y-0.5">
              <span className="text-body font-medium">{health.alarm.kind}</span>
              <p className="text-body text-muted-foreground">{health.alarm.summary}</p>
            </div>
          </div>
        ) : null}
      </div>
    </DetailCard>
  )
}
