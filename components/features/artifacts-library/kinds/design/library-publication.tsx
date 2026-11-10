"use client"

import * as React from "react"
import { useSessionSafe } from "@/hooks/use-auth"
import { Button } from "@/components/ui/button"
import { artifactRequest, type ArtifactRequestError } from "@/components/features/chat/artifact-context/projects/client"
import { useArtifactViewer } from "@/components/features/chat/artifact-context/projects/artifact-viewer-context"

/*
 * Library publication and consumer pins (#5189). Publishing is the Library
 * owner's right, separate from editing; a consumer project keeps its own copy
 * of the published design-system.json and updates only when someone reviews
 * and applies a newer publication.
 */

export interface LibraryPublication { published_version: number; can_publish: boolean }
export interface LibraryPin { library_page_id: string; library_version: number; library_name: string; copied_at: string }
export interface LibraryChanges { added: string[]; removed: string[]; changed: string[]; truncated?: boolean }
export interface LibraryUpdate { library_page_id: string; pinned_version: number; published_version: number; library_available: boolean; update_available: boolean; changes: LibraryChanges }

const path = (workspaceId: string, slug: string, rest: string) =>
  `/api/v1/pages/${encodeURIComponent(slug)}/artifact${rest}?${new URLSearchParams({ workspace_id: workspaceId })}`
const isObject = (raw: unknown): raw is Record<string, unknown> => typeof raw === "object" && raw !== null
const isInt = (raw: unknown): raw is number => Number.isSafeInteger(raw) && (raw as number) >= 0
const strings = (raw: unknown): string[] | null => Array.isArray(raw) && raw.every(item => typeof item === "string") ? raw : null

export function parsePublication(raw: unknown): LibraryPublication | null {
  return isObject(raw) && isInt(raw.published_version) && typeof raw.can_publish === "boolean" ? { published_version: raw.published_version, can_publish: raw.can_publish } : null
}
export function parsePin(raw: unknown): LibraryPin | null {
  return isObject(raw) && typeof raw.library_page_id === "string" && isInt(raw.library_version) && typeof raw.library_name === "string" && typeof raw.copied_at === "string"
    ? { library_page_id: raw.library_page_id, library_version: raw.library_version, library_name: raw.library_name, copied_at: raw.copied_at } : null
}
export function parseUpdate(raw: unknown): LibraryUpdate | null {
  if (!isObject(raw) || typeof raw.library_page_id !== "string" || !raw.library_page_id || !isInt(raw.pinned_version) || !isInt(raw.published_version) || typeof raw.library_available !== "boolean" || typeof raw.update_available !== "boolean" || !isObject(raw.changes)) return null
  const added = strings(raw.changes.added), removed = strings(raw.changes.removed), changed = strings(raw.changes.changed)
  if (!added || !removed || !changed) return null
  return { library_page_id: raw.library_page_id, pinned_version: raw.pinned_version, published_version: raw.published_version, library_available: raw.library_available, update_available: raw.update_available, changes: { added, removed, changed, truncated: raw.changes.truncated === true } }
}

export const libraryClient = {
  publication: (ws: string, slug: string, signal?: AbortSignal) => artifactRequest(path(ws, slug, "/library"), parsePublication, { signal }),
  publish: (ws: string, slug: string, version: number) => artifactRequest(path(ws, slug, "/library/publish"), parsePublication, { method: "POST", body: { version } }),
  pin: (ws: string, slug: string, signal?: AbortSignal) => artifactRequest(path(ws, slug, "/library-pin"), parsePin, { signal }),
  update: (ws: string, slug: string, signal?: AbortSignal) => artifactRequest(path(ws, slug, "/library-pin/update"), parseUpdate, { signal }),
  /** Applies exactly the reviewed state: the published version shown, over the pin shown. */
  apply: (ws: string, slug: string, library: string, version: number, ifPinned: number) => artifactRequest(path(ws, slug, "/library-pin"), parsePin, { method: "PUT", body: { library: "@" + library, version, if_pinned_library: "@" + library, if_pinned_version: ifPinned } }),
}

const message = (error: unknown) => (error as ArtifactRequestError)?.message || "Something went wrong. Try again."

/** "Published vN" for every reader, and Publish for the Library's owner only. */
export function LibraryPublishControl({ shown, pageId }: { shown: number; pageId?: string }) {
  const viewer = useArtifactViewer()
  const principal = useSessionSafe()?.data?.user?.id
  if (!viewer?.target) return null
  const ref = pageId ? "@" + pageId : viewer.target.slug
  return <PublishControl key={JSON.stringify([principal, viewer.workspaceId, ref])} workspaceId={viewer.workspaceId} slug={ref} shown={shown} />
}

function PublishControl({ workspaceId, slug, shown }: { workspaceId: string; slug: string; shown: number }) {
  const [state, setState] = React.useState<LibraryPublication | null>(null)
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)
  React.useEffect(() => {
    const abort = new AbortController()
    libraryClient.publication(workspaceId, slug, abort.signal).then(found => { if (!abort.signal.aborted) setState(found) }, () => { if (!abort.signal.aborted) setState(null) })
    return () => abort.abort()
  }, [workspaceId, slug])
  if (!state) return null
  const publish = () => {
    setBusy(true); setError(null)
    libraryClient.publish(workspaceId, slug, shown).then(setState, failure => setError(message(failure))).finally(() => setBusy(false))
  }
  return <span className="flex items-center gap-1.5 text-xs text-muted-foreground" data-library-publication>
    <span>{state.published_version ? `Published v${state.published_version}` : "Not published"}</span>
    {state.can_publish && shown > 0 && shown !== state.published_version &&
      <Button type="button" size="sm" variant="secondary" className="h-7" disabled={busy} onClick={publish}>Publish v{shown}</Button>}
    {error && <span role="alert" className="text-destructive">{error}</span>}
  </span>
}

/** The consumer's pinned Library copy, and an explicit review before an update is applied. */
export function LibraryPinNotice({ pageId, canWrite = true }: { pageId?: string; canWrite?: boolean }) {
  const viewer = useArtifactViewer()
  const principal = useSessionSafe()?.data?.user?.id
  if (!viewer?.target) return null
  const ref = pageId ? "@" + pageId : viewer.target.slug
  return <PinNotice key={JSON.stringify([principal, viewer.workspaceId, ref, canWrite])} workspaceId={viewer.workspaceId} slug={ref} canWrite={canWrite} />
}

function PinNotice({ workspaceId, slug, canWrite }: { workspaceId: string; slug: string; canWrite: boolean }) {
  const [pin, setPin] = React.useState<LibraryPin | null>(null)
  const [update, setUpdate] = React.useState<LibraryUpdate | null>(null)
  const [reviewing, setReviewing] = React.useState(false)
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)
  const [round, setRound] = React.useState(0)
  const [reviewStale, setReviewStale] = React.useState(false)
  React.useEffect(() => {
    const abort = new AbortController()
    const load = async () => {
      for (let attempt = 0; attempt < 2 && !abort.signal.aborted; attempt++) {
        const found = await libraryClient.pin(workspaceId, slug, abort.signal)
        if (abort.signal.aborted) return
        const review = await libraryClient.update(workspaceId, slug, abort.signal).catch(() => null)
        if (abort.signal.aborted) return
        if (review && (review.library_page_id !== found.library_page_id || review.pinned_version !== found.library_version)) continue
        setPin(found); setUpdate(review); setReviewStale(false)
        return
      }
      if (!abort.signal.aborted) { setPin(null); setUpdate(null); setReviewStale(true) }
    }
    void load().catch(() => { if (!abort.signal.aborted) { setPin(null); setUpdate(null) } })
    return () => abort.abort()
  }, [workspaceId, slug, round])
  if (reviewStale) return <p role="status" className="text-xs text-muted-foreground">The pinned Library changed while loading its update. <button type="button" className="underline" onClick={() => setRound(value => value + 1)}>Check again</button></p>
  if (!pin) return null
  const apply = () => {
    if (!canWrite || !update || update.library_page_id !== pin.library_page_id || update.pinned_version !== pin.library_version) return
    setBusy(true); setError(null)
    libraryClient.apply(workspaceId, slug, update.library_page_id, update.published_version, update.pinned_version)
      .then(() => { setReviewing(false); setUpdate(null); setRound(value => value + 1) }, failure => setError(message(failure)))
      .finally(() => setBusy(false))
  }
  const name = pin.library_name || "Library"
  return <section aria-label="Library" className="grid gap-2 rounded-card border px-3 py-2 text-sm" data-library-pin>
    <div className="flex flex-wrap items-center gap-2">
      <span>Uses <strong>{name}</strong> v{pin.library_version}</span>
      {update?.update_available && <>
        <span className="rounded-[var(--radius-row)] bg-primary/12 px-1.5 text-xs text-primary-hover">Update available: v{update.published_version}</span>
        <Button type="button" size="sm" variant="secondary" className="h-7" aria-expanded={reviewing} onClick={() => setReviewing(value => !value)}>{reviewing ? "Close review" : "Review update"}</Button>
      </>}
      {update && !update.library_available && <span className="text-xs text-muted-foreground">You can no longer open this library. This project keeps its pinned copy.</span>}
    </div>
    {reviewing && update?.update_available && <div className="grid gap-2">
      <ChangeList title="Added" items={update.changes.added} />
      <ChangeList title="Changed" items={update.changes.changed} />
      <ChangeList title="Removed" items={update.changes.removed} />
      {update.changes.truncated && <p className="text-xs text-muted-foreground">Only the first 200 changes of each kind are listed.</p>}
      <p className="text-xs text-muted-foreground">Nothing changes in this project until you apply the update.</p>
      {canWrite && <div><Button type="button" size="sm" disabled={busy} onClick={apply}>Apply v{update.published_version}</Button></div>}
    </div>}
    {error && <p role="alert" className="text-xs text-destructive">{error}</p>}
  </section>
}

function ChangeList({ title, items }: { title: string; items: string[] }) {
  if (items.length === 0) return null
  return <div>
    <h3 className="text-xs font-medium">{title} <span className="text-muted-foreground">{items.length}</span></h3>
    <ul className="font-mono text-xs text-muted-foreground">{items.map(item => <li key={item}>{item}</li>)}</ul>
  </div>
}
