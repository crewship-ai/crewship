"use client"

import { useEffect, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { useUrlSelection } from "@/hooks/use-issue-detail"
import { useAbilities } from "@/hooks/use-abilities"
import { roleAtLeast } from "@/lib/routine-governance"
import { DetailCard, Pill } from "@/components/ui/detail"
import { Button } from "@/components/ui/button"
import { RoutineDefinitionCanvas } from "./routine-definition-canvas"
import { RoutineStepDefinition } from "./routine-step-definition"

interface PipelineVersion {
  version: number; is_head?: boolean; parent_version?: number | null
  definition_hash: string; author_type: string; author_id: string
  change_summary?: string; created_at: string
}
interface VersionDetail extends PipelineVersion { definition: Record<string, unknown> }
interface VersionDiff { from_version: number; to_version: number; identical: boolean; unified_diff: string }
interface Props {
  workspaceId: string; slug: string; onRolledBack: () => void
  onPrepareDraft?: (definition: Record<string, unknown>, version: number) => void
}

/** Read-only archives and comparisons. Restoring starts an explicit unsaved draft. */
export function RoutineVersionsTab({ workspaceId, slug, onPrepareDraft }: Props) {
  const [versions, setVersions] = useState<PipelineVersion[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [retry, setRetry] = useState(0)
  const [selected, setSelected] = useUrlSelection("version")
  const [detail, setDetail] = useState<VersionDetail | null>(null)
  const [detailError, setDetailError] = useState<string | null>(null)
  const [diff, setDiff] = useState<VersionDiff | null>(null)
  const [compare, setCompare] = useState(false)
  const [step, setStep] = useState<string | null>(null)
  const { role } = useAbilities()
  const base = `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(slug)}`
  const head = versions.find(v => v.is_head)?.version
  useEffect(() => {
    const c = new AbortController(); setLoading(true); setError(null); setVersions([])
    void (async () => {
      try {
        const res = await apiFetch(`${base}/versions`, { signal: c.signal })
        if (!res.ok) throw new Error("Version history could not be loaded.")
        const rows = await res.json()
        if (!c.signal.aborted) setVersions(rows)
      } catch (e) { if (!c.signal.aborted) setError(e instanceof Error ? e.message : String(e)) }
      finally { if (!c.signal.aborted) setLoading(false) }
    })()
    return () => c.abort()
  }, [base, retry])
  useEffect(() => {
    const c = new AbortController(); setDetail(null); setDiff(null); setDetailError(null); setStep(null)
    if (!selected) return () => c.abort()
    void (async () => {
      try {
        const res = await apiFetch(`${base}/versions/${encodeURIComponent(selected)}`, { signal: c.signal })
        if (!res.ok) throw new Error("This historical version could not be loaded.")
        const data = await res.json()
        if (!c.signal.aborted) setDetail(data)
        if (compare && head != null) {
          const d = await apiFetch(`${base}/diff?from=${encodeURIComponent(selected)}&to=${head}`, { signal: c.signal })
          if (!d.ok) throw new Error("The comparison could not be loaded.")
          const change = await d.json(); if (!c.signal.aborted) setDiff(change)
        }
      } catch (e) { if (!c.signal.aborted) setDetailError(e instanceof Error ? e.message : String(e)) }
    })()
    return () => c.abort()
  }, [base, selected, compare, head, retry])
  const steps = Array.isArray(detail?.definition.steps) ? detail.definition.steps as Record<string, unknown>[] : []
  return <div className="space-y-4">
    <DetailCard title="Version history" subtitle={loading ? "Loading…" : `${versions.length} loaded`}>
      <p className="mb-4 text-xs text-muted-foreground">Versions record changes to the recipe. History records what happened each time it ran. Inspecting a version does not change the active recipe.</p>
      {error && <p role="alert" className="text-sm text-destructive">{error} <button onClick={() => setRetry(v => v + 1)}>Retry</button></p>}
      {!loading && !error && !versions.length && <p className="text-sm text-muted-foreground">No archived versions yet.</p>}
      <ol className="divide-y divide-border/40">{versions.map(v => <li key={v.version} className="flex flex-wrap items-center gap-3 py-3">
        <div className="min-w-0 flex-1"><div className="flex items-center gap-2"><span className="font-medium">Version {v.version}</span>{v.is_head && <Pill tone="blue">Current</Pill>}</div><p className="mt-1 text-xs text-muted-foreground">{new Date(v.created_at).toLocaleString("en-GB")} · {v.change_summary || "Saved recipe"}</p><details className="mt-1 text-[11px] text-muted-foreground"><summary className="cursor-pointer">Author and metadata</summary><p className="break-all">{v.author_type} · {v.author_id || "Not recorded"}</p><p className="break-all">{v.definition_hash}</p></details></div>
        <Button size="sm" variant="outline" onClick={() => { setCompare(false); setSelected(String(v.version)) }}>View version {v.version}</Button>
        {!v.is_head && head != null && <Button size="sm" variant="ghost" onClick={() => { setCompare(true); setSelected(String(v.version)) }}>Compare with current</Button>}
      </li>)}</ol>
      {versions.length >= 100 && <p className="mt-3 text-xs text-muted-foreground">Showing the latest 100 versions. A run’s version link opens its archive directly, including older versions.</p>}
    </DetailCard>
    {selected && <DetailCard title={`Version ${selected}`} subtitle="Read-only archive" action={<button className="text-xs text-muted-foreground" onClick={() => setSelected(null)}>Close</button>}>
      {detailError && <p role="alert" className="mb-3 text-sm text-destructive">{detailError} <button onClick={() => setRetry(v => v + 1)}>Retry</button></p>}
      {!detail && !detailError && <p role="status">Loading version…</p>}
      {detail && <><div className="mb-3 flex flex-wrap items-center justify-between gap-3"><p className="text-xs text-muted-foreground">Historical runs retain their own version and inputs.</p>{onPrepareDraft && roleAtLeast(role, "ADMIN") && <Button size="sm" variant="outline" onClick={() => onPrepareDraft(detail.definition, detail.version)}>Use as draft</Button>}</div>
        {diff && <div className="mb-3 rounded-lg border border-border bg-muted/30 p-3"><p className="text-sm">{diff.identical ? "Definitions are identical." : `Changes from version ${diff.from_version} to version ${diff.to_version}`}</p>{!diff.identical && <pre className="mt-3 max-h-72 overflow-auto whitespace-pre-wrap break-words text-xs">{diff.unified_diff}</pre>}</div>}
        <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_280px]"><div className="h-[56vh] min-h-[380px]"><RoutineDefinitionCanvas definition={detail.definition} slug={slug} name={typeof detail.definition.display_name === "string" ? detail.definition.display_name : slug} selectedStepId={step} onStepSelect={setStep} /></div><aside className="rounded-xl border border-border/60 bg-card p-4"><RoutineStepDefinition step={steps.find(s => s.id === step)} /></aside></div>
        <details className="mt-3 text-xs text-muted-foreground"><summary className="cursor-pointer">Full stored definition</summary><pre className="mt-2 max-h-80 overflow-auto whitespace-pre-wrap break-words">{JSON.stringify(detail.definition, null, 2)}</pre></details>
      </>}
    </DetailCard>}
  </div>
}
