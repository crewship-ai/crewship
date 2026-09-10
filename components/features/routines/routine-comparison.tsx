"use client"

import { useEffect, useRef, useState } from "react"
import { nanoid } from "nanoid"
import { Button } from "@/components/ui/button"
import { apiFetch } from "@/lib/api-fetch"
import {
  comparisonVerdict,
  parseComparisonCases,
  readComparisonResult,
  type ComparisonJob,
  type ComparisonSide,
  type ComparisonVersion,
} from "@/lib/routine-comparison"

function download(name: string, value: unknown) {
  const url = URL.createObjectURL(
    new Blob([JSON.stringify(value, null, 2)], { type: "application/json" }),
  )
  const link = document.createElement("a")
  link.href = url
  link.download = name
  link.click()
  URL.revokeObjectURL(url)
}

/** Composes existing archived versions and real runs; no second evaluation engine. */
export function RoutineComparison({
  workspaceId,
  slug,
  onBusyChange,
}: {
  workspaceId: string
  slug: string
  onBusyChange?: (busy: boolean) => void
}) {
  const [versions, setVersions] = useState<ComparisonVersion[]>([])
  const [a, setA] = useState("")
  const [b, setB] = useState("")
  const [tierA, setTierA] = useState("fast")
  const [tierB, setTierB] = useState("smart")
  const [dataset, setDataset] = useState('[\n  {"id": "example", "inputs": {}}\n]')
  const [jobs, setJobs] = useState<ComparisonJob[]>([])
  const [busy, setBusy] = useState(false)
  const [loading, setLoading] = useState(false)
  useEffect(() => {
    onBusyChange?.(busy)
  }, [busy, onBusyChange])
  const [error, setError] = useState<string | null>(null)
  const [stopped, setStopped] = useState(false)
  const queue = useRef<ComparisonJob[]>([])
  const cursor = useRef(0)
  const active = useRef(false)
  const stop = useRef(false)
  const mounted = useRef(true)
  const batch = useRef("")
  useEffect(() => {
    const live = mounted
    live.current = true
    return () => {
      live.current = false
      stop.current = true
    }
  }, [])
  const loadVersions = async () => {
    if (loading || active.current) return
    setLoading(true)
    setError(null)
    try {
      const response = await apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(slug)}/versions`,
      )
      if (!response.ok) throw new Error("Could not load published recipe versions.")
      const raw: unknown = await response.json()
      if (
        !Array.isArray(raw) ||
        !raw.every(
          (v) =>
            v &&
            typeof v.version === "number" &&
            v.version > 0 &&
            typeof v.definition_hash === "string" &&
            v.definition_hash,
        )
      )
        throw new Error("No valid archive listing was returned.")
      if (!mounted.current) return
      setVersions(raw)
      const head = raw.find((v) => v.is_head)?.version
      setA(head ? String(head) : "")
      setB(head ? String(head) : "")
    } catch (e) {
      if (mounted.current) setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (mounted.current) setLoading(false)
    }
  }
  const publishProgress = () => {
    if (mounted.current) setJobs(queue.current.map((job) => ({ ...job })))
  }
  const advance = async () => {
    if (active.current) return
    active.current = true
    stop.current = false
    setBusy(true)
    setError(null)
    try {
      while (cursor.current < queue.current.length && !stop.current && mounted.current) {
        const job = queue.current[cursor.current]
        if (!job.run_id) {
          const response = await apiFetch(
            `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(slug)}/run`,
            {
              method: "POST",
              headers: {
                "Content-Type": "application/json",
                "Idempotency-Key": job.key,
                Prefer: "respond-async",
              },
              body: JSON.stringify({
                inputs: job.case.inputs,
                pinned_version: job.config.version.version,
                tier_override: job.config.tier,
                metadata: { comparison_id: batch.current, case_id: job.case.id, side: job.side },
              }),
            },
          )
          const raw = await response.json()
          if (!response.ok || typeof raw.run_id !== "string" || !raw.run_id)
            throw new Error(
              raw.error ||
                "Start was not confirmed. Continue retries the same request key; inspect History if it remains unclear.",
            )
          job.run_id = raw.run_id
          publishProgress()
        }
        if (!mounted.current) return
        const acceptedRunId = job.run_id
        if (!acceptedRunId) throw new Error("Start did not return a run ID.")
        const response = await apiFetch(
          `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipeline-runs/${encodeURIComponent(acceptedRunId)}`,
        )
        if (!response.ok)
          throw new Error(
            "Could not read the accepted run. Continue refreshes it without starting another run.",
          )
        job.result = readComparisonResult(await response.json(), job, workspaceId)
        publishProgress()
        if (comparisonVerdict(job) === "Pending") break
        cursor.current++
      }
    } catch (e) {
      if (mounted.current) setError(e instanceof Error ? e.message : String(e))
    } finally {
      active.current = false
      if (mounted.current) setBusy(false)
    }
  }
  const start = () => {
    if (active.current || (queue.current.length > 0 && cursor.current < queue.current.length))
      return
    try {
      const cases = parseComparisonCases(dataset)
      const va = versions.find((v) => String(v.version) === a)
      const vb = versions.find((v) => String(v.version) === b)
      if (!va || !vb) throw new Error("Select an archived version for both sides.")
      const sides: ["A" | "B", ComparisonSide][] = [
        ["A", { version: va, tier: tierA }],
        ["B", { version: vb, tier: tierB }],
      ]
      setStopped(false)
      batch.current = nanoid()
      queue.current = cases.flatMap((row) =>
        sides.map(([side, config]) => ({ case: row, side, config, key: nanoid() })),
      )
      cursor.current = 0
      publishProgress()
      void advance()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }
  const unfinished = jobs.length > 0 && cursor.current < jobs.length
  return (
    <section className="space-y-4 rounded-2xl border border-hairline p-5">
      <div>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h3 className="font-medium">Compare published recipes and tiers</h3>
          <span className="rounded-full bg-warn/10 px-2.5 py-1 text-xs text-warn">
            Live · real actions and costs
          </span>
        </div>
        <p className="mt-2 text-sm text-muted-foreground">
          Run the same dataset against two archived versions or worker tiers. These are real runs
          with real effects and costs. A completed run alone does not prove output quality. Changes
          in the current draft are not part of this comparison.
        </p>
      </div>
      <Button
        type="button"
        variant="outline"
        disabled={loading || busy || unfinished}
        onClick={() => void loadVersions()}
      >
        {loading ? "Loading…" : "Load published versions"}
      </Button>
      <fieldset disabled={loading || busy || unfinished} className="space-y-3">
        <div className="grid gap-3 sm:grid-cols-2">
          {(["A", "B"] as const).map((side) => (
            <div key={side} className="space-y-2">
              <label className="block text-sm">
                Side {side} recipe version
                <select
                  aria-label={`Side ${side} recipe version`}
                  className="mt-1 w-full rounded-md border bg-card p-2"
                  value={side === "A" ? a : b}
                  onChange={(e) => (side === "A" ? setA : setB)(e.target.value)}
                >
                  <option value="">Select archive…</option>
                  {versions.map((v) => (
                    <option key={v.version} value={v.version}>
                      v{v.version}
                      {v.is_head ? " · published" : ""} · {v.definition_hash.slice(0, 10)}
                    </option>
                  ))}
                </select>
              </label>
              <label className="block text-sm">
                Side {side} worker tier
                <select
                  aria-label={`Side ${side} worker tier`}
                  className="mt-1 w-full rounded-md border bg-card p-2"
                  value={side === "A" ? tierA : tierB}
                  onChange={(e) => (side === "A" ? setTierA : setTierB)(e.target.value)}
                >
                  {["fast", "moderate", "smart"].map((tier) => (
                    <option key={tier}>{tier}</option>
                  ))}
                </select>
              </label>
            </div>
          ))}
        </div>
        <label className="block text-sm">
          Dataset · JSON
          <textarea
            aria-label="Comparison dataset"
            value={dataset}
            onChange={(e) => setDataset(e.target.value)}
            rows={6}
            className="mt-1 w-full rounded-md border bg-card p-2 font-mono text-xs"
          />
        </label>
        <p className="text-xs text-muted-foreground">
          1–20 cases, each with a unique id and typed inputs. Optional expected_output checks exact
          text equality; omit it to leave quality ungraded. Tier overrides use the recipe's existing
          routing policy.
        </p>
        <div className="flex flex-wrap gap-2">
          <Button
            type="button"
            variant="outline"
            onClick={() => {
              try {
                download(`${slug}-dataset.json`, parseComparisonCases(dataset))
              } catch (e) {
                setError(String(e))
              }
            }}
          >
            Export dataset
          </Button>
          <label className="cursor-pointer rounded-md border px-3 py-2 text-sm">
            Import dataset
            <input
              aria-label="Import comparison dataset"
              type="file"
              accept="application/json,.json"
              disabled={busy || unfinished}
              className="sr-only"
              onChange={async (e) => {
                const file = e.target.files?.[0]
                if (!file) return
                setLoading(true)
                try {
                  if (file.size > 1024 * 1024) throw new Error("Dataset file must be under 1 MB.")
                  const text = await file.text()
                  parseComparisonCases(text)
                  if (mounted.current) setDataset(text)
                } catch (err) {
                  if (mounted.current) setError(String(err))
                } finally {
                  if (mounted.current) setLoading(false)
                }
              }}
            />
          </label>
          <Button type="button" disabled={!versions.length} onClick={start}>
            Start live comparison
          </Button>
        </div>
      </fieldset>
      {unfinished && (
        <div className="flex flex-wrap gap-2">
          {busy ? (
            <Button
              type="button"
              variant="outline"
              onClick={() => {
                stop.current = true
              }}
            >
              Pause queue
            </Button>
          ) : (
            <Button type="button" onClick={() => void advance()}>
              Continue / refresh accepted run
            </Button>
          )}
          {!busy && (
            <Button
              type="button"
              variant="outline"
              onClick={() => {
                cursor.current = queue.current.length
                stop.current = true
                setStopped(true)
                publishProgress()
              }}
            >
              Stop remaining comparison
            </Button>
          )}
          <p className="text-xs text-muted-foreground">
            Pending runs may need an Inbox decision. Continue reads the accepted run before starting
            the next case. Leaving this Test view stops the remaining queue, not runs already
            started.
          </p>
        </div>
      )}
      {stopped && (
        <p className="text-sm text-muted-foreground">
          Remaining tests were stopped. Accepted runs continue independently; their links and
          results remain below.
        </p>
      )}
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      {jobs.length > 0 && (
        <div className="space-y-3">
          <p className="text-sm font-medium">Last comparison · saved input snapshot</p>
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs">
              <thead>
                <tr>
                  {["Case", "Side / version / tier", "Result", "Cost", "Time", "Run"].map(
                    (label) => (
                      <th key={label} className="p-2">
                        {label}
                      </th>
                    ),
                  )}
                </tr>
              </thead>
              <tbody>
                {jobs.map((job) => (
                  <tr key={job.key}>
                    <td className="p-2">{job.case.id}</td>
                    <td className="p-2">
                      {job.side} · v{job.config.version.version} · {job.config.tier}
                    </td>
                    <td className="p-2">{comparisonVerdict(job)}</td>
                    <td className="p-2">
                      {job.result ? `$${job.result.cost_usd.toFixed(4)}` : "—"}
                    </td>
                    <td className="p-2">{job.result ? `${job.result.duration_ms} ms` : "—"}</td>
                    <td className="p-2">
                      {job.run_id && (
                        <a
                          className="underline"
                          target="_blank"
                          rel="noreferrer"
                          href={`/routines?run=${encodeURIComponent(job.run_id)}`}
                        >
                          Open run
                        </a>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <Button
            type="button"
            variant="outline"
            onClick={() =>
              download(`${slug}-comparison.json`, {
                comparison_id: batch.current,
                execution_mode: "live",
                workspace_id: workspaceId,
                slug,
                complete:
                  !stopped &&
                  jobs.every(
                    (job) =>
                      !!job.result &&
                      ["completed", "failed", "cancelled"].includes(
                        job.result.status.toLowerCase(),
                      ),
                  ),
                grading: "Exact text when expected_output is supplied; otherwise quality ungraded",
                rows: jobs,
              })
            }
          >
            Export comparison results
          </Button>
        </div>
      )}
    </section>
  )
}
