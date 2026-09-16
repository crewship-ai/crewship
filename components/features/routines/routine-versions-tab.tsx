"use client"

import { formatRoutineTime } from "@/lib/routine-time"

import { useEffect, useState } from "react"
import { toast } from "sonner"
import { apiFetch } from "@/lib/api-fetch"
import { useUrlSelection } from "@/hooks/use-issue-detail"
import { useAbilities } from "@/hooks/use-abilities"
import { useSessionSafe } from "@/hooks/use-auth"
import { roleAtLeast } from "@/lib/routine-governance"
import { relTime } from "@/lib/time"
import { discardRoutineDraft, loadRoutineDraft, saveRoutineDraft } from "@/lib/routine-drafts"
import type { PipelineDraftSummary } from "@/hooks/use-pipelines"
import { DetailCard, Pill } from "@/components/ui/detail"
import { Button } from "@/components/ui/button"
import { RoutineDefinitionCanvas } from "./routine-definition-canvas"
import { RoutineStepDefinition } from "./routine-step-definition"
import { draftAuthorLabel } from "./routine-identity-header"

interface PipelineVersion {
  version: number
  is_head?: boolean
  parent_version?: number | null
  definition_hash: string
  author_type: string
  author_id: string
  change_summary?: string
  created_at: string
}
interface VersionDetail extends PipelineVersion {
  definition: Record<string, unknown>
}
interface VersionDiff {
  from_version: number
  to_version: number
  identical: boolean
  unified_diff: string
}
interface Props {
  workspaceId: string
  slug: string
  /** The routine's saved draft, when there is one — drawn on top. */
  draft?: PipelineDraftSummary
  /** Identity carried into a restored draft's document. */
  routine?: { name?: string; description?: string; author_crew_id?: string; icon?: string; color?: string }
  onPublish?: () => void
  /** The draft row changed (discarded, or a version was restored as one). */
  onChanged?: () => void
}

/**
 * Read-only archives and comparisons.
 *
 * The draft, when there is one, sits on top: it is the one thing here that is
 * not history. Restoring a version creates a draft from it through the same
 * drafts API the CLI uses — never a rollback that bypasses the publish review
 * (oponentura 12. 9.). Publishing that draft is the one way a live version
 * changes from the web.
 */
export function RoutineVersionsTab({ workspaceId, slug, draft, routine, onPublish, onChanged }: Props) {
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
  const [busy, setBusy] = useState<string | null>(null)
  const { role } = useAbilities()
  const { data: session } = useSessionSafe()
  const manager = roleAtLeast(role, "MANAGER")
  const base = `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(slug)}`
  const head = versions.find((v) => v.is_head)?.version
  useEffect(() => {
    const c = new AbortController()
    setLoading(true)
    setError(null)
    setVersions([])
    void (async () => {
      try {
        const res = await apiFetch(`${base}/versions`, { signal: c.signal })
        if (!res.ok) throw new Error("Version history could not be loaded.")
        const rows = await res.json()
        if (!c.signal.aborted) setVersions(rows)
      } catch (e) {
        if (!c.signal.aborted) setError(e instanceof Error ? e.message : String(e))
      } finally {
        if (!c.signal.aborted) setLoading(false)
      }
    })()
    return () => c.abort()
  }, [base, retry])
  useEffect(() => {
    const c = new AbortController()
    setDetail(null)
    setDiff(null)
    setDetailError(null)
    setStep(null)
    if (!selected) return () => c.abort()
    void (async () => {
      try {
        const res = await apiFetch(`${base}/versions/${encodeURIComponent(selected)}`, {
          signal: c.signal,
        })
        if (!res.ok) throw new Error("This historical version could not be loaded.")
        const data = await res.json()
        if (!c.signal.aborted) setDetail(data)
        if (compare && head != null) {
          const d = await apiFetch(
            `${base}/diff?from=${encodeURIComponent(selected)}&to=${head}`,
            { signal: c.signal },
          )
          if (!d.ok) throw new Error("The comparison could not be loaded.")
          const change = await d.json()
          if (!c.signal.aborted) setDiff(change)
        }
      } catch (e) {
        if (!c.signal.aborted) setDetailError(e instanceof Error ? e.message : String(e))
      }
    })()
    return () => c.abort()
  }, [base, selected, compare, head, retry])

  /** Load the archived definition and save it as the routine's draft. */
  const restoreAsDraft = async (version: number) => {
    if (busy) return
    const expectedDraft = draft ? { id: draft.id, revision: draft.revision } : null
    if (draft && !window.confirm(`Replace draft r${draft.revision} with the recipe of v${version}? The draft's current content is lost; nothing published changes.`)) return
    setBusy(`restore:${version}`)
    try {
      const res = await apiFetch(`${base}/versions/${encodeURIComponent(String(version))}`)
      if (!res.ok) throw new Error("This historical version could not be loaded.")
      const archived: VersionDetail = await res.json()
      const baseline = await loadRoutineDraft(workspaceId, slug)
      if (expectedDraft
        ? baseline.id !== expectedDraft.id || baseline.revision !== expectedDraft.revision
        : !!baseline.id) {
        throw new Error("The draft changed while you were restoring this version. Refresh and review the current draft before trying again.")
      }
      const definition = archived.definition
      const document: Record<string, unknown> = {
        ...(baseline.id ? baseline.document : {}),
        slug,
        name: routine?.name || (typeof definition.display_name === "string" ? definition.display_name : slug),
        description: routine?.description ?? (typeof definition.description === "string" ? definition.description : ""),
        definition,
      }
      if (routine?.author_crew_id) document.author_crew_id = routine.author_crew_id
      if (routine?.icon) document.icon = routine.icon
      if (routine?.color) document.color = routine.color
      const saved = await saveRoutineDraft(workspaceId, { ...baseline, slug }, document)
      toast.success(`Draft r${saved.revision} restored from v${version} · publish to make it live`)
      onChanged?.()
    } catch (e) {
      toast.error("Could not restore this version as a draft", { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy(null)
    }
  }

  const discard = async () => {
    if (!draft || busy) return
    if (!window.confirm(`Discard draft r${draft.revision}? The published version stays as it is.`)) return
    setBusy("discard")
    try {
      await discardRoutineDraft(workspaceId, { slug, id: draft.id, revision: draft.revision })
      toast.success("Draft discarded")
      onChanged?.()
    } catch (e) {
      toast.error("Could not discard the draft", { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy(null)
    }
  }

  const steps = Array.isArray(detail?.definition.steps)
    ? (detail.definition.steps as Record<string, unknown>[])
    : []
  return (
    <div className="space-y-4">
      <DetailCard
        title="Versions"
        subtitle={loading ? "Loading…" : `${versions.length} loaded`}
        bare
      >
        {draft && (
          <div data-testid="routine-draft-row" className="flex flex-wrap items-center gap-3 border-b border-hairline bg-purple/10 px-4 py-3">
            <span className="w-16 text-[15px] font-semibold text-purple">Draft</span>
            <div className="min-w-0 flex-1">
              <div className="text-sm">
                <b className="font-medium">r{draft.revision}</b> · saved {relTime(draft.updated_at)} by {draftAuthorLabel(draft.updated_by, session?.user?.id)}
              </div>
              <p className="text-xs text-muted-foreground">Not used by Run or schedules until published.</p>
            </div>
            {manager && (
              <div className="flex shrink-0 gap-1.5">
                <Button size="sm" onClick={onPublish} disabled={!onPublish || !!busy}>Review and publish</Button>
                <Button size="sm" variant="ghost" onClick={() => void discard()} disabled={!!busy}>Discard</Button>
              </div>
            )}
          </div>
        )}
        <div className="px-4 py-3">
          <p className="mb-3 text-xs text-muted-foreground">
            Versions record changes to the recipe. History records what happened each time it ran.
            Inspecting a version does not change the published one.
          </p>
          {error && (
            <p role="alert" className="text-sm text-destructive">
              {error} <Button variant="ghost" size="sm" onClick={() => setRetry((v) => v + 1)}>Retry</Button>
            </p>
          )}
          {!loading && !error && !versions.length && (
            <p className="text-sm text-muted-foreground">Nothing published yet.</p>
          )}
          <ol className="divide-y divide-border/40">
            {versions.map((v) => (
              <li key={v.version} data-testid={`routine-version-${v.version}`} className="flex flex-wrap items-center gap-3 py-3">
                <span className="w-16 text-[15px] font-semibold">v{v.version}</span>
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    {v.is_head && <Pill tone="success">Published · Run uses this</Pill>}
                    <span className="text-xs text-muted-foreground">{formatRoutineTime(v.created_at)}</span>
                  </div>
                  <p className="mt-1 text-xs">{v.change_summary || "Saved recipe"}</p>
                  <details className="mt-1 text-[11px] text-muted-foreground">
                    <summary className="cursor-pointer">Author and metadata</summary>
                    <p className="break-all">
                      {v.author_type} · {v.author_id || "Not recorded"}
                    </p>
                    <p className="break-all">{v.definition_hash}</p>
                  </details>
                </div>
                <div className="flex shrink-0 flex-wrap gap-1.5">
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => {
                      setCompare(false)
                      setSelected(String(v.version))
                    }}
                  >
                    View
                  </Button>
                  {!v.is_head && head != null && (
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => {
                        setCompare(true)
                        setSelected(String(v.version))
                      }}
                    >
                      Compare with published
                    </Button>
                  )}
                  {!v.is_head && manager && (
                    <Button
                      size="sm"
                      variant="ghost"
                      title="Creates a draft from this version; publishing it makes a new version, history is not rewritten"
                      disabled={!!busy}
                      onClick={() => void restoreAsDraft(v.version)}
                    >
                      {busy === `restore:${v.version}` ? "Restoring…" : "Restore as draft"}
                    </Button>
                  )}
                </div>
              </li>
            ))}
          </ol>
          {versions.length >= 100 && (
            <p className="mt-3 text-xs text-muted-foreground">
              Showing the latest 100 versions. A run’s version link opens its archive directly,
              including older versions.
            </p>
          )}
        </div>
      </DetailCard>
      {selected && (
        <DetailCard
          title={`Version ${selected}`}
          subtitle="Read-only archive"
          action={
            <Button variant="ghost" size="sm" className="text-xs text-muted-foreground" onClick={() => setSelected(null)}>
              Close
            </Button>
          }
        >
          {detailError && (
            <p role="alert" className="mb-3 text-sm text-destructive">
              {detailError} <Button variant="ghost" size="sm" onClick={() => setRetry((v) => v + 1)}>Retry</Button>
            </p>
          )}
          {!detail && !detailError && <p role="status">Loading version…</p>}
          {detail && (
            <>
              <p className="mb-3 text-xs text-muted-foreground">
                Historical runs retain their own version and inputs.
              </p>
              {diff && (
                <div className="mb-3 rounded-lg border border-border bg-muted/30 p-3">
                  <p className="text-sm">
                    {diff.identical
                      ? "Recipes are identical."
                      : `Changes from version ${diff.from_version} to version ${diff.to_version}`}
                  </p>
                  {!diff.identical && (
                    <pre className="mt-3 max-h-72 overflow-auto whitespace-pre-wrap break-words text-xs">
                      {diff.unified_diff}
                    </pre>
                  )}
                </div>
              )}
              <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_280px]">
                <div className="h-[56dvh] min-h-[380px]">
                  <RoutineDefinitionCanvas
                    definition={detail.definition}
                    slug={slug}
                    name={
                      typeof detail.definition.display_name === "string"
                        ? detail.definition.display_name
                        : slug
                    }
                    selectedStepId={step}
                    onStepSelect={setStep}
                  />
                </div>
                <aside className="rounded-xl border border-border/60 bg-card p-4">
                  <RoutineStepDefinition step={steps.find((s) => s.id === step)} />
                </aside>
              </div>
              <details className="mt-3 text-xs text-muted-foreground">
                <summary className="cursor-pointer">Full stored definition</summary>
                <pre className="mt-2 max-h-80 overflow-auto whitespace-pre-wrap break-words">
                  {JSON.stringify(detail.definition, null, 2)}
                </pre>
              </details>
            </>
          )}
        </DetailCard>
      )}
    </div>
  )
}
