"use client"

import { useEffect, useState } from "react"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { GitBranch, History, FileJson, RotateCcw, Download } from "lucide-react"
import { usePipelineRuns } from "@/hooks/use-pipelines"
import { apiFetch } from "@/lib/api-fetch"

// PipelineDetailSheet is the right-side drawer that opens when a
// PipelineRunNode is clicked in the Orchestration → Graph view.
// Three tabs: Overview (slug, description, author, run stats),
// Versions (immutable history with rollback button), Runs
// (journal-backed activity).
//
// Why a sheet rather than a full page: matches Crewship's existing
// "click anything → side drawer" pattern (Approvals, Issues all
// use this), keeps the graph context visible behind the sheet so
// users can correlate run-detail with workflow position.

export interface PipelineDetailSheetProps {
  workspaceId: string
  /** Pipeline slug; null/undefined = sheet closed */
  slug: string | null
  open: boolean
  onClose: () => void
}

interface PipelineRow {
  id: string
  slug: string
  name: string
  description?: string
  dsl_version: string
  definition_hash: string
  invocation_count: number
  last_invoked_at?: string
  last_invocation_status?: string
  author_crew_id?: string
  author_agent_id?: string
  authored_via: string
  created_at: string
  updated_at: string
  definition?: unknown
}

interface PipelineVersion {
  version: number
  definition_hash: string
  author_type: string
  author_id: string
  parent_version?: number
  change_summary?: string
  created_at: string
}

export function PipelineDetailSheet({ workspaceId, slug, open, onClose }: PipelineDetailSheetProps) {
  const [pipeline, setPipeline] = useState<PipelineRow | null>(null)
  const [versions, setVersions] = useState<PipelineVersion[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const { runs } = usePipelineRuns(workspaceId, slug)

  useEffect(() => {
    if (!open || !slug || !workspaceId) {
      setPipeline(null)
      setVersions([])
      return
    }
    let cancelled = false
    const ctrl = new AbortController()
    const load = async () => {
      setLoading(true)
      setError(null)
      try {
        const [pRes, vRes] = await Promise.all([
          apiFetch(`/api/v1/workspaces/${workspaceId}/pipelines/${slug}`, { signal: ctrl.signal }),
          apiFetch(`/api/v1/workspaces/${workspaceId}/pipelines/${slug}/versions`, { signal: ctrl.signal }),
        ])
        if (!pRes.ok) throw new Error(`pipeline: ${pRes.status}`)
        if (!vRes.ok) throw new Error(`versions: ${vRes.status}`)
        const p: PipelineRow = await pRes.json()
        const v: PipelineVersion[] = await vRes.json()
        if (cancelled) return
        setPipeline(p)
        setVersions(Array.isArray(v) ? v : [])
      } catch (e) {
        if (!cancelled && (e as Error).name !== "AbortError") {
          setError((e as Error).message)
        }
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    load()
    return () => {
      cancelled = true
      ctrl.abort()
    }
  }, [open, slug, workspaceId])

  const handleRollback = async (version: number) => {
    if (!workspaceId || !slug) return
    if (!confirm(`Rollback to version ${version}? History is preserved; the next save will be version ${(versions[0]?.version ?? 0) + 1}.`)) return
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceId}/pipelines/${slug}/rollback`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ version }),
        },
      )
      if (!res.ok) {
        const body = await res.text()
        alert(`Rollback failed: ${body}`)
        return
      }
      // Reload — quick and dirty; a stale-fetch guard would be
      // overkill for an explicit user action like this
      const reload = await apiFetch(`/api/v1/workspaces/${workspaceId}/pipelines/${slug}`)
      if (reload.ok) {
        setPipeline(await reload.json())
      }
      const vReload = await apiFetch(`/api/v1/workspaces/${workspaceId}/pipelines/${slug}/versions`)
      if (vReload.ok) {
        setVersions(await vReload.json())
      }
    } catch (e) {
      alert(`Rollback error: ${(e as Error).message}`)
    }
  }

  const handleExport = async () => {
    if (!workspaceId || !slug) return
    const res = await apiFetch(`/api/v1/workspaces/${workspaceId}/pipelines/${slug}/export?include_history=1`)
    if (!res.ok) return
    const bundle = await res.json()
    // Trigger download. Browsers require an anchor click for File
    // System Access fallback; this is the lowest-friction route.
    const blob = new Blob([JSON.stringify(bundle, null, 2)], { type: "application/json" })
    const url = URL.createObjectURL(blob)
    const a = document.createElement("a")
    a.href = url
    a.download = `routine-${slug}-bundle.json`
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <Sheet open={open} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-[640px] sm:max-w-[640px] overflow-y-auto">
        <SheetHeader>
          <SheetTitle className="flex items-center gap-2">
            <GitBranch className="h-4 w-4" />
            {pipeline?.name ?? slug ?? "Routine"}
          </SheetTitle>
          {pipeline?.description && (
            <p className="text-sm text-muted-foreground">{pipeline.description}</p>
          )}
        </SheetHeader>

        {loading && <div className="py-8 text-center text-sm text-muted-foreground">Loading…</div>}
        {error && <div className="py-4 text-sm text-red-500">Error: {error}</div>}

        {pipeline && !loading && (
          <Tabs defaultValue="overview" className="mt-4">
            <TabsList className="grid w-full grid-cols-3">
              <TabsTrigger value="overview">Overview</TabsTrigger>
              <TabsTrigger value="versions">
                Versions
                <Badge variant="secondary" className="ml-1.5 px-1.5 text-[10px]">
                  {versions.length}
                </Badge>
              </TabsTrigger>
              <TabsTrigger value="runs">
                Runs
                <Badge variant="secondary" className="ml-1.5 px-1.5 text-[10px]">
                  {runs.length}
                </Badge>
              </TabsTrigger>
            </TabsList>

            <TabsContent value="overview" className="mt-4 space-y-3 text-sm">
              <Row label="Slug" value={pipeline.slug} mono />
              <Row label="DSL version" value={pipeline.dsl_version} />
              <Row label="Definition hash" value={pipeline.definition_hash.slice(0, 16) + "…"} mono />
              <Row label="Invocations" value={String(pipeline.invocation_count)} />
              {pipeline.last_invoked_at && (
                <Row
                  label="Last invoked"
                  value={`${new Date(pipeline.last_invoked_at).toLocaleString()}${
                    pipeline.last_invocation_status ? ` (${pipeline.last_invocation_status})` : ""
                  }`}
                />
              )}
              <Row label="Author crew" value={pipeline.author_crew_id || "—"} mono />
              <Row label="Author agent" value={pipeline.author_agent_id || "—"} mono />
              <Row label="Authored via" value={pipeline.authored_via} />
              <Row label="Created" value={new Date(pipeline.created_at).toLocaleString()} />
              <Row label="Updated" value={new Date(pipeline.updated_at).toLocaleString()} />

              <div className="mt-4 flex gap-2">
                <Button size="sm" variant="outline" onClick={handleExport}>
                  <Download className="mr-1.5 h-3.5 w-3.5" />
                  Export bundle
                </Button>
              </div>

              {pipeline.definition !== undefined && (
                <details className="mt-3">
                  <summary className="cursor-pointer text-xs text-muted-foreground">
                    <FileJson className="mr-1 inline h-3 w-3" />
                    Show DSL
                  </summary>
                  <pre className="mt-2 max-h-80 overflow-auto rounded bg-muted p-2 text-[10px]">
                    {(() => {
                      try {
                        return JSON.stringify(pipeline.definition, null, 2) ?? ""
                      } catch {
                        return ""
                      }
                    })()}
                  </pre>
                </details>
              )}
            </TabsContent>

            <TabsContent value="versions" className="mt-4">
              {versions.length === 0 ? (
                <div className="py-6 text-center text-sm text-muted-foreground">
                  No version history yet.
                </div>
              ) : (
                <ul className="space-y-2">
                  {versions.map((v) => (
                    <li
                      key={v.version}
                      className="rounded border border-border bg-card/50 p-3 text-sm"
                    >
                      <div className="flex items-center justify-between">
                        <div className="flex items-center gap-2">
                          <Badge variant="outline" className="font-mono">
                            v{v.version}
                          </Badge>
                          <span className="text-muted-foreground text-xs">
                            {v.author_type}/{v.author_id.slice(0, 12)}
                          </span>
                        </div>
                        {v.version !== versions[0]?.version && (
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() => handleRollback(v.version)}
                            className="h-6 px-2 text-xs"
                          >
                            <RotateCcw className="mr-1 h-3 w-3" />
                            Rollback
                          </Button>
                        )}
                      </div>
                      {v.change_summary && (
                        <p className="mt-1.5 text-xs">{v.change_summary}</p>
                      )}
                      <div className="mt-1 flex items-center gap-2 text-[10px] text-muted-foreground">
                        <span>{new Date(v.created_at).toLocaleString()}</span>
                        <span>·</span>
                        <span className="font-mono">{v.definition_hash.slice(0, 12)}…</span>
                        {v.parent_version && (
                          <>
                            <span>·</span>
                            <span>parent v{v.parent_version}</span>
                          </>
                        )}
                      </div>
                    </li>
                  ))}
                </ul>
              )}
            </TabsContent>

            <TabsContent value="runs" className="mt-4">
              {runs.length === 0 ? (
                <div className="py-6 text-center text-sm text-muted-foreground">
                  No runs yet — invoke the routine to see activity here.
                </div>
              ) : (
                <ul className="space-y-1.5">
                  {runs.map((r) => (
                    <li key={r.id} className="rounded border border-border bg-card/50 p-2.5 text-xs">
                      <div className="flex items-center justify-between">
                        <span className="font-mono">{r.entry_type}</span>
                        <span className={r.severity === "error" ? "text-red-400" : "text-muted-foreground"}>
                          {r.severity}
                        </span>
                      </div>
                      <p className="mt-1 truncate">{r.summary}</p>
                      <div className="mt-1 flex items-center gap-2 text-[10px] text-muted-foreground">
                        <History className="h-3 w-3" />
                        <span>{new Date(r.ts).toLocaleString()}</span>
                        {r.run_id && <span className="font-mono">{r.run_id.slice(0, 16)}…</span>}
                      </div>
                    </li>
                  ))}
                </ul>
              )}
            </TabsContent>
          </Tabs>
        )}
      </SheetContent>
    </Sheet>
  )
}

function Row({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex items-baseline justify-between gap-3">
      <span className="text-muted-foreground text-xs">{label}</span>
      <span className={mono ? "font-mono text-xs" : ""}>{value}</span>
    </div>
  )
}
