"use client"

import { useCallback, useMemo } from "react"
import { useMutation, useQuery, useQueryClient, type UseMutationResult, type UseQueryResult } from "@tanstack/react-query"

import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import type { FencedPublishRequest, PublishConflictWire, ReviewSnapshotWire } from "@/lib/pages/editor-contract"
import type { SourceProjectLike } from "@/lib/pages/source-diff"
import type { WirePageDetail } from "@/hooks/use-page-grants"

/**
 * The review screen's data.
 *
 * Three reads, not one, because the server's review snapshot is the
 * *authorization* — the digests the human is about to be shown — and the two
 * source projects are the *evidence*. They come from endpoints that already
 * exist and are verified here:
 *
 *   GET /api/v1/pages/{slug}/project            → internal/api/pages_project.go:79
 *   GET /api/v1/pages/{slug}/project/history/{r} → internal/api/pages_project_history.go:111
 *
 * Both answer the same `pageProjectDraft` body (`pages_project.go:25`):
 * `{git_commit, revision, digest, definition, project}`.
 *
 * The one rule that shapes this file: a baseline that cannot be read is
 * "comparison unavailable", never "no changes" (V05). So the baseline query is
 * not merely allowed to fail quietly — it is never *started* when the server
 * says the retained source is gone, and nothing anywhere substitutes an empty
 * project for it.
 */

/** The body both project reads answer with. `definition` is a `pages.Document`. */
export interface PageProjectDraftWire {
  readonly git_commit: string
  readonly revision: number
  readonly digest: string
  readonly definition: unknown
  readonly project: SourceProjectLike | null
}

/** What a successful publish answers with (`pagePublication`, pages_project_publish.go:24). */
export interface PagePublicationReceiptWire {
  readonly version: number
  readonly build_id: string
  readonly source_revision: number
  readonly source_digest?: string
  readonly git_commit?: string
  readonly artifact_digest?: string
  readonly created_at?: string
}

/**
 * A 409 from publish, carried as a *typed* value rather than flattened into a
 * message string. The UI has to name which base moved and which routines, and
 * it cannot parse that back out of English.
 */
export class PublishFenceError extends Error {
  readonly conflict: PublishConflictWire
  constructor(conflict: PublishConflictWire) {
    super(conflict.error)
    this.name = "PublishFenceError"
    this.conflict = conflict
  }
}

/** Narrow an unknown mutation error to the typed 409 body, or null. */
export function publishConflictOf(error: unknown): PublishConflictWire | null {
  return error instanceof PublishFenceError ? error.conflict : null
}

function normalizeConflict(body: unknown): PublishConflictWire {
  const raw = (body ?? {}) as Partial<PublishConflictWire>
  const kinds = ["definition", "routines", "publication", "draft"] as const
  const conflict = kinds.find(kind => kind === raw.conflict)
  const routines = Array.isArray(raw.routines) ? raw.routines.filter((r): r is string => typeof r === "string") : undefined
  return {
    error: typeof raw.error === "string" && raw.error !== "" ? raw.error : "A base you reviewed changed before this publication was applied.",
    ...(conflict ? { conflict } : {}),
    ...(routines && routines.length > 0 ? { routines } : {}),
  }
}

async function readError(response: Response, fallback: string): Promise<Error> {
  const body = (await response.json().catch(() => null)) as { error?: string } | null
  return Object.assign(new Error(body?.error ?? fallback), { status: response.status })
}

/**
 * Shaping the live Page into something `compareDefinitions` can read.
 *
 * The definition impact is compared against the *current live Page*, not
 * against the candidate's own previous definition (V05). But the two sides
 * arrive in different shapes: the candidate carries a `pages.Document`
 * (`internal/pages/spec.go:67`) and the live Page arrives on the detail route
 * as `pageWire`/`pagePanelWire` (`internal/api/pages_handler.go:139,1308`).
 * Handing those two to the comparator raw would report every panel as
 * rewritten — a false alarm on the one screen that must not cry wolf.
 *
 * So the wire is mapped onto the document's key names here, and both sides'
 * SLA is normalised, because the wire sends `sla_seconds: 300` where the
 * document says `sla: "5m"` and those are the same promise written twice.
 * Nothing else is invented: a key the mapping does not carry over is simply
 * absent, and the comparator reports absence as unmodelled rather than as
 * unchanged.
 */

const DURATION_UNIT: Readonly<Record<string, number>> = { ns: 1e-9, us: 1e-6, "µs": 1e-6, ms: 1e-3, s: 1, m: 60, h: 3600 }
const DURATION_SHAPE = /^-?(?:\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h))+$/
const DURATION_PART = /(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/g

/**
 * One spelling for an SLA on both sides of the comparison. A value that is
 * not a Go duration is returned unchanged rather than guessed at — comparing
 * two strings we do not understand is honest; inventing a number is not.
 */
export function normalizeSla(value: unknown): string | undefined {
  if (typeof value === "number" && Number.isFinite(value)) return `${value}s`
  if (typeof value !== "string") return undefined
  const text = value.trim()
  if (text === "") return undefined
  if (!DURATION_SHAPE.test(text)) return text
  let total = 0
  for (const [, amount, unit] of text.matchAll(DURATION_PART)) total += Number(amount) * DURATION_UNIT[unit]
  return `${text.startsWith("-") ? -total : total}s`
}

function isDict(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

/**
 * Panels on the live Page this viewer may not read. They arrive as sealed
 * placeholders (`pageSealedPanelWire`) carrying an id and nothing else, so
 * they cannot be compared — and leaving them in would report each of them as
 * removed by the candidate, which is a lie about a panel that still exists.
 * They are excluded from both sides and named on the surface instead.
 */
export function hiddenPanelIds(page: WirePageDetail | null): string[] {
  if (!page || !Array.isArray(page.panels)) return []
  return page.panels
    .filter((panel): panel is Record<string, unknown> => isDict(panel) && panel.sealed === true)
    .map(panel => (typeof panel.panel_id === "string" ? panel.panel_id : ""))
    .filter(id => id !== "")
}

function livePanel(panel: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = { id: panel.id, schema: panel.schema, owner: panel.owner, producer: panel.producer }
  for (const key of ["title", "span", "public", "tab", "actions", "refresh", "wake"]) {
    if (panel[key] !== undefined) out[key] = panel[key]
  }
  const sla = normalizeSla(panel.sla ?? panel.sla_seconds)
  if (sla !== undefined) out.sla = sla
  return out
}

export function liveDefinitionFromPage(page: WirePageDetail | null): unknown {
  if (!page) return null
  const panels = Array.isArray(page.panels) ? page.panels : []
  return {
    apiVersion: "crewship.ai/v1",
    kind: "Page",
    metadata: { name: page.name ?? "", slug: page.slug ?? "", description: page.description ?? "" },
    spec: {
      panels: panels.filter((panel): panel is Record<string, unknown> => isDict(panel) && panel.sealed !== true).map(livePanel),
    },
  }
}

/** The candidate side of the same comparison: hidden panels out, SLA normalised. */
export function candidateDefinitionForComparison(definition: unknown, hidden: readonly string[]): unknown {
  if (!isDict(definition)) return definition
  const spec = definition.spec
  if (!isDict(spec) || !Array.isArray(spec.panels)) return definition
  const panels = spec.panels
    .filter(panel => !(isDict(panel) && typeof panel.id === "string" && hidden.includes(panel.id)))
    .map(panel => {
      if (!isDict(panel)) return panel
      const sla = normalizeSla(panel.sla)
      return sla === undefined ? panel : { ...panel, sla }
    })
  return { ...definition, spec: { ...spec, panels } }
}

export interface PageReview {
  /** The authorized snapshot: candidate, both bases, routine hashes, blockers. */
  readonly snapshot: UseQueryResult<ReviewSnapshotWire, Error>
  /** The candidate's own draft (`GET .../project`). */
  readonly candidate: UseQueryResult<PageProjectDraftWire, Error>
  /**
   * The retained source behind the live publication. `isFetching` stays false
   * and `data` stays undefined when the server says the archive is gone — the
   * query is never enabled, so nothing can mistake "not fetched" for "empty".
   */
  readonly baseline: UseQueryResult<PageProjectDraftWire, Error>
  /** Why there is no baseline source to compare with, in a sentence, or null. */
  readonly baselineUnavailable: string | null
  /**
   * The draft moved under the review: the snapshot describes revision N and
   * `GET .../project` now answers M. Publishing would fence-fail; say so here
   * instead of letting the server say it after the click.
   */
  readonly candidateMoved: boolean
  readonly publish: UseMutationResult<PagePublicationReceiptWire, Error, void>
  /** The typed 409 body from the last publish attempt, or null. */
  readonly conflict: PublishConflictWire | null
  /** Re-read every base. The only cure for a moved baseline. */
  readonly refresh: () => void
}

export function usePageReview(workspaceId: string, slug: string, enabled: boolean): PageReview {
  const client = useQueryClient()
  const endpoint = `/api/v1/pages/${encodeURIComponent(slug)}`
  const params = useMemo(() => new URLSearchParams({ workspace_id: workspaceId }).toString(), [workspaceId])

  const invalidate = useCallback(() => {
    void client.invalidateQueries({ queryKey: ["page-review", workspaceId, slug] })
    void client.invalidateQueries({ queryKey: ["page-review-source", workspaceId, slug] })
  }, [client, workspaceId, slug])
  useRealtimeEventSafe("page.updated", invalidate)
  useRealtimeEventSafe("page.deleted", invalidate)
  useRealtimeEventSafe("realtime.reconnected", invalidate)

  const snapshot = useQuery<ReviewSnapshotWire, Error>({
    queryKey: ["page-review", workspaceId, slug],
    enabled,
    retry: false,
    gcTime: 0,
    queryFn: async ({ signal }) => {
      const response = await apiFetch(`${endpoint}/project/review?${params}`, { signal })
      if (!response.ok) throw await readError(response, "Could not load the review of this application.")
      return (await response.json()) as ReviewSnapshotWire
    },
  })

  const candidateRevision = snapshot.data?.candidate?.revision ?? null
  const baselineRevision = snapshot.data?.baseline.source_revision ?? null
  const baselineAvailable = snapshot.data?.baseline.source_available === true

  const candidate = useQuery<PageProjectDraftWire, Error>({
    queryKey: ["page-review-source", workspaceId, slug, "draft"],
    enabled: enabled && candidateRevision !== null,
    retry: false,
    gcTime: 0,
    queryFn: async ({ signal }) => {
      const response = await apiFetch(`${endpoint}/project?${params}`, { signal })
      if (!response.ok) throw await readError(response, "Could not read the candidate's source.")
      return (await response.json()) as PageProjectDraftWire
    },
  })

  const baseline = useQuery<PageProjectDraftWire, Error>({
    queryKey: ["page-review-source", workspaceId, slug, "baseline", baselineRevision],
    // Never fetched when the archive is gone, and there is no fallback path
    // that would hand the diff an empty project (V05).
    enabled: enabled && baselineAvailable && baselineRevision !== null,
    retry: false,
    gcTime: 0,
    queryFn: async ({ signal }) => {
      const response = await apiFetch(`${endpoint}/project/history/${baselineRevision}?${params}`, { signal })
      if (!response.ok) throw await readError(response, "Could not read the source of the live publication.")
      return (await response.json()) as PageProjectDraftWire
    },
  })

  const baselineUnavailable = useMemo(() => {
    const base = snapshot.data?.baseline
    if (!base) return null
    if (snapshot.data?.initial_publication) return null
    if (!base.source_available) {
      return base.source_unavailable_reason ?? "The source retained for the live publication can no longer be read."
    }
    if (base.source_revision === null) {
      return "The live publication does not record which source revision produced it."
    }
    if (baseline.isError) return baseline.error.message
    return null
  }, [snapshot.data, baseline.isError, baseline.error])

  const publish = useMutation<PagePublicationReceiptWire, Error, void>({
    mutationFn: async () => {
      const snap = snapshot.data
      if (!snap || !snap.candidate) throw new Error("There is no candidate to publish.")
      // Every fence value is read off the snapshot the human was shown. Not
      // off a fresh read, and not off the live query cache: a refetch between
      // render and click is exactly the case the fence exists to catch (V04).
      const request: FencedPublishRequest = {
        ...(snap.candidate.build ? { build_id: snap.candidate.build.id } : {}),
        expected_revision: snap.candidate.revision,
        expected_publication: snap.baseline.publication_version,
        reviewed_code: true,
        expected_definition_digest: snap.baseline.definition_digest,
        // Only routines whose current hash the snapshot actually carries. A
        // routine with an unreadable hash cannot be fenced, and inventing an
        // empty string for it would fence against a value nobody reviewed;
        // the screen shows it as `unknown` instead.
        expected_routine_digests: Object.fromEntries(
          snap.routines.filter(r => typeof r.current_digest === "string" && r.current_digest !== "").map(r => [r.routine, r.current_digest as string]),
        ),
      }
      const response = await apiFetch(`${endpoint}/project/publish?${params}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(request),
      })
      const body = await response.json().catch(() => null)
      if (response.status === 409) throw new PublishFenceError(normalizeConflict(body))
      if (!response.ok) throw Object.assign(new Error((body as { error?: string } | null)?.error ?? "Publication failed."), { status: response.status })
      return body as PagePublicationReceiptWire
    },
    onSettled: () => {
      invalidate()
      void client.invalidateQueries({ queryKey: ["page-application", workspaceId, slug] })
      void client.invalidateQueries({ queryKey: ["page-publications", workspaceId, slug] })
      void client.invalidateQueries({ queryKey: ["pages", workspaceId] })
    },
  })

  const refresh = useCallback(() => {
    void snapshot.refetch()
    void candidate.refetch()
    void baseline.refetch()
    publish.reset()
  }, [snapshot, candidate, baseline, publish])

  return {
    snapshot,
    candidate,
    baseline,
    baselineUnavailable,
    candidateMoved: candidateRevision !== null && candidate.data !== undefined && candidate.data.revision !== candidateRevision,
    publish,
    conflict: publishConflictOf(publish.error),
    refresh,
  }
}
