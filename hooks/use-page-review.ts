"use client"

import { useCallback, useMemo } from "react"
import { useMutation, useQuery, useQueryClient, type UseMutationResult, type UseQueryResult } from "@tanstack/react-query"

import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import type { FencedPublishRequest, PublishConflictWire, ReviewSnapshotWire } from "@/lib/pages/editor-contract"
import type { SourceProjectLike } from "@/lib/pages/source-diff"

/**
 * The review screen's data.
 *
 * Three reads, not one, because the server's review snapshot is the
 * *authorization* — the digests the human is about to be shown — and the two
 * source projects are the *evidence*. They come from endpoints that already
 * exist and are verified here:
 *
 *   GET /api/v1/pages/{slug}/project/source     → internal/api/pages_project.go:79
 *   GET /api/v1/pages/{slug}/project/history/{r}/source → internal/api/pages_project_history.go:111
 *
 * Both answer the same source-only body (`pages_project_authoring.go`):
 * `{git_commit, revision, digest, project}`.
 *
 * The live Page definition the candidate is compared against is NOT a fourth
 * read: it arrives inside the snapshot, on the same row and in the same
 * statement as the digest the publish fence sends. A definition fetched from
 * the Page detail route instead is a second endpoint on a second cache entry,
 * and the two are free to disagree — which is how a consent could attest to a
 * digest for a document the screen never rendered.
 *
 * The one rule that shapes this file: a baseline that cannot be read is
 * "comparison unavailable", never "no changes" (V05). So the baseline query is
 * not merely allowed to fail quietly — it is never *started* when the server
 * says the retained source is gone, and nothing anywhere substitutes an empty
 * project for it.
 */

/** Source evidence intentionally carries no panel definition. */
export interface PageProjectDraftWire {
  readonly git_commit: string
  readonly revision: number
  readonly digest: string
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

export interface PageReview {
  /** The authorized snapshot: candidate, both bases, routine hashes, blockers. */
  readonly snapshot: UseQueryResult<ReviewSnapshotWire, Error>
  /** The candidate's source (`GET .../project/source`). */
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
   * `GET .../project/source` now answers M. Publishing would fence-fail; say so here
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
      const response = await apiFetch(`${endpoint}/project/source?${params}`, { signal })
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
      const response = await apiFetch(`${endpoint}/project/history/${baselineRevision}/source?${params}`, { signal })
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
        // Two filters, and they exclude different mistakes.
        //
        // `in_candidate` keeps out routines that only the LIVE publication
        // calls. The list is a union so a reviewer can see a routine being
        // dropped; fencing on a dropped one asks the server to compare a key
        // it does not recompute, which answers 409 naming a routine nobody
        // moved — and refetching never clears it, because the next snapshot
        // says exactly the same thing.
        //
        // The digest check keeps out a routine whose current hash could not be
        // read. Inventing an empty string would fence against a value nobody
        // reviewed; the row on screen says it is uncovered instead.
        expected_routine_digests: Object.fromEntries(
          snap.routines
            .filter(r => r.in_candidate && typeof r.current_digest === "string" && r.current_digest !== "")
            .map(r => [r.routine, r.current_digest as string]),
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
