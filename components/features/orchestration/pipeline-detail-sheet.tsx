"use client"

import { useEffect, useRef, useState } from "react"
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

interface DetailScope {
  workspaceId: string
  slug: string
  controller: AbortController
}

function detailBase(scope: DetailScope) {
  return `/api/v1/workspaces/${encodeURIComponent(scope.workspaceId)}/pipelines/${encodeURIComponent(scope.slug)}`
}

async function loadDetails(scope: DetailScope) {
  const base = detailBase(scope)
  const options = { signal: scope.controller.signal }
  const [pRes, vRes] = await Promise.all([apiFetch(base, options), apiFetch(`${base}/versions`, options)])
  if (!pRes.ok) throw new Error(`pipeline: ${pRes.status}`)
  if (!vRes.ok) throw new Error(`versions: ${vRes.status}`)
  const pipeline: PipelineRow = await pRes.json()
  const versions: PipelineVersion[] = await vRes.json()
  return { pipeline, versions: Array.isArray(versions) ? versions : [] }
}

export function PipelineDetailSheet({ workspaceId, slug, open, onClose }: PipelineDetailSheetProps) {
  const [pipeline, setPipeline] = useState<PipelineRow | null>(null)
  const [versions, setVersions] = useState<PipelineVersion[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [pendingAction, setPendingAction] = useState<"rollback" | "export" | null>(null)
  const scopeRef = useRef<DetailScope | null>(null)
  const { runs } = usePipelineRuns(open ? workspaceId : null, open ? slug : null)

  useEffect(() => {
    setPipeline(null)
    setVersions([])
    setError(null)
    setPendingAction(null)
    setLoading(false)
    if (!open || !slug || !workspaceId) return
    const scope = { workspaceId, slug, controller: new AbortController() }
    scopeRef.current = scope
    setLoading(true)
    const active = () => scopeRef.current === scope && !scope.controller.signal.aborted
    void loadDetails(scope).then((details) => {
      if (!active()) return
      setPipeline(details.pipeline)
      setVersions(details.versions)
    }).catch((e: unknown) => {
      if (active()) setError(e instanceof Error ? e.message : String(e))
    }).finally(() => {
      if (active()) setLoading(false)
    })
    return () => {
      scope.controller.abort()
      if (scopeRef.current === scope) scopeRef.current = null
    }
  }, [open, slug, workspaceId])

  const handleRollback = async (version: number) => {
    const scope = scopeRef.current
    if (!scope || pendingAction !== null) return
    if (!confirm(`Rollback to version ${version}? History is preserved; the next save will be version ${(versions[0]?.version ?? 0) + 1}.`)) return
    const active = () => scopeRef.current === scope && !scope.controller.signal.aborted
    setPendingAction("rollback")
    setError(null)
    try {
      const res = await apiFetch(`${detailBase(scope)}/rollback`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ version }), signal: scope.controller.signal,
      })
      if (!active()) return
      if (!res.ok) {
        const body = await res.text()
        if (active()) setError(`Rollback failed: ${body}`)
        return
      }
      const details = await loadDetails(scope)
      if (!active()) return
      setPipeline(details.pipeline)
      setVersions(details.versions)
    } catch (e) {
      if (active()) setError(`Rollback error: ${e instanceof Error ? e.message : String(e)}`)
    } finally {
      if (active()) setPendingAction(null)
    }
  }

  const handleExport = async () => {
    const scope = scopeRef.current
    if (!scope || pendingAction !== null) return
    const active = () => scopeRef.current === scope && !scope.controller.signal.aborted
    setPendingAction("export")
    setError(null)
    let url: string | null = null
    try {
      const res = await apiFetch(`${detailBase(scope)}/export?include_history=1`, { signal: scope.controller.signal })
      if (!active()) return
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const bundle = await res.json()
      if (!active()) return
      const blob = new Blob([JSON.stringify(bundle, null, 2)], { type: "application/json" })
      url = URL.createObjectURL(blob)
      const a = document.createElement("a")
      a.href = url
      a.download = `routine-${scope.slug}-bundle.json`
      a.click()
    } catch (e) {
      if (active()) setError(`Export failed: ${e instanceof Error ? e.message : String(e)}`)
    } finally {
      if (url) URL.revokeObjectURL(url)
      if (active()) setPendingAction(null)
    }
  }

  return (
    <Sheet open={open} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="sm:w-[640px] sm:max-w-[640px] overflow-y-auto">
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
        {error && <div role="alert" className="py-4 text-sm text-destructive">Error: {error}</div>}

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
                <Button size="sm" variant="outline" onClick={handleExport} disabled={pendingAction !== null}>
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
                            disabled={pendingAction !== null}
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
                        <span className={r.severity === "error" ? "text-destructive" : "text-muted-foreground"}>
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
