"use client"

import * as React from "react"
import { useSessionSafe } from "@/hooks/use-auth"
import { Button } from "@/components/ui/button"
import { artifactRequest, type ArtifactRequestError } from "@/components/features/chat/artifact-context/projects/client"
import { useArtifactViewer } from "@/components/features/chat/artifact-context/projects/artifact-viewer-context"
import { parsePinnedFiles, pinnedThemeCss, type PinnedFile } from "@/components/features/artifacts/design-system/pinned-theme"

/*
 * Library publication and consumer pins (#5189). Publishing is the Library
 * owner's right, separate from editing; a consumer project keeps its own copy
 * of the published design-system.json and updates only when someone reviews
 * and applies a newer publication.
 */

export interface LibraryPublication { published_version: number; can_publish: boolean }
/** not_copied names the fonts this pin left behind, as "path:status" (#5248). */
export interface LibraryPin { library_page_id: string; library_version: number; library_name: string; copied_at: string; not_copied: string[] }
export interface LibraryChanges { added: string[]; removed: string[]; changed: string[]; truncated?: boolean }
export interface LibraryUpdate { library_page_id: string; pinned_version: number; published_version: number; library_available: boolean; update_available: boolean; changes: LibraryChanges; file_changes: LibraryChanges; not_copied: string[]; dependencies_missing: boolean }

const path = (workspaceId: string, slug: string, rest: string) =>
  `/api/v1/pages/${encodeURIComponent(slug)}/artifact${rest}?${new URLSearchParams({ workspace_id: workspaceId })}`
const isObject = (raw: unknown): raw is Record<string, unknown> => typeof raw === "object" && raw !== null
const isInt = (raw: unknown): raw is number => Number.isSafeInteger(raw) && (raw as number) >= 0
const strings = (raw: unknown): string[] | null => Array.isArray(raw) && raw.every(item => typeof item === "string") ? raw : null

export function parsePublication(raw: unknown): LibraryPublication | null {
  return isObject(raw) && isInt(raw.published_version) && typeof raw.can_publish === "boolean" ? { published_version: raw.published_version, can_publish: raw.can_publish } : null
}
export function parsePin(raw: unknown): LibraryPin | null {
  if (!isObject(raw) || typeof raw.library_page_id !== "string" || !isInt(raw.library_version) || typeof raw.library_name !== "string" || typeof raw.copied_at !== "string") return null
  // Older servers list no files; a file entry that is not well formed rejects the pin.
  const files = raw.files ?? []
  if (!Array.isArray(files)) return null
  const notCopied: string[] = []
  for (const file of files) {
    if (!isObject(file) || typeof file.path !== "string" || typeof file.status !== "string") return null
    if (file.status !== "copied") notCopied.push(`${file.path}:${file.status}`)
  }
  return { library_page_id: raw.library_page_id, library_version: raw.library_version, library_name: raw.library_name, copied_at: raw.copied_at, not_copied: notCopied }
}
export function parseUpdate(raw: unknown): LibraryUpdate | null {
  if (!isObject(raw) || typeof raw.library_page_id !== "string" || !raw.library_page_id || !isInt(raw.pinned_version) || !isInt(raw.published_version) || typeof raw.library_available !== "boolean" || typeof raw.update_available !== "boolean" || !isObject(raw.changes)) return null
  const added = strings(raw.changes.added), removed = strings(raw.changes.removed), changed = strings(raw.changes.changed)
  if (!added || !removed || !changed) return null
  // Older servers send no file review; an empty one says nothing changed.
  const files = isObject(raw.file_changes) ? raw.file_changes : {}
  const fileAdded = strings(files.added ?? []), fileRemoved = strings(files.removed ?? []), fileChanged = strings(files.changed ?? []), notCopied = strings(raw.not_copied ?? [])
  if (!fileAdded || !fileRemoved || !fileChanged || !notCopied) return null
  return { library_page_id: raw.library_page_id, pinned_version: raw.pinned_version, published_version: raw.published_version, library_available: raw.library_available, update_available: raw.update_available, changes: { added, removed, changed, truncated: raw.changes.truncated === true }, file_changes: { added: fileAdded, removed: fileRemoved, changed: fileChanged }, not_copied: notCopied, dependencies_missing: raw.dependencies_missing === true }
}

export const libraryClient = {
  publication: (ws: string, slug: string, signal?: AbortSignal) => artifactRequest(path(ws, slug, "/library"), parsePublication, { signal }),
  publish: (ws: string, slug: string, version: number) => artifactRequest(path(ws, slug, "/library/publish"), parsePublication, { method: "POST", body: { version } }),
  pin: (ws: string, slug: string, signal?: AbortSignal) => artifactRequest(path(ws, slug, "/library-pin"), parsePin, { signal }),
  update: (ws: string, slug: string, signal?: AbortSignal) => artifactRequest(path(ws, slug, "/library-pin/update"), parseUpdate, { signal }),
  snapshot: (ws: string, slug: string, signal?: AbortSignal) => artifactRequest(path(ws, slug, "/library-pin/files"), (raw: unknown) => {
    if (!isObject(raw) || typeof raw.library_page_id !== "string" || !raw.library_page_id || !isInt(raw.library_version) || raw.library_version < 1 ||
      typeof raw.sha256 !== "string" || !/^[0-9a-f]{64}$/.test(raw.sha256) || !isObject(raw.tokens) || Array.isArray(raw.tokens)) return null
    const files = parsePinnedFiles(raw)
    return files ? { tokens: JSON.stringify(raw.tokens), files } : null
  }, { signal }),
  /** Applies exactly the reviewed state: the published version shown, over the pin shown. */
  apply: (ws: string, slug: string, library: string, version: number, ifPinned: number) => artifactRequest(path(ws, slug, "/library-pin"), parsePin, { method: "PUT", body: { library: "@" + library, version, if_pinned_library: "@" + library, if_pinned_version: ifPinned } }),
}

const PIN_CHANGED = "crewship:library-pin-changed"

export interface PinnedLibrary { css: string | null; tokens: string; files: PinnedFile[] }

/**
 * The consumer's pinned theme, derived from its own copy (#5248). Null while
 * loading, without a pin or when the project is itself a Library. A change to
 * the live Library never reaches this until someone applies an update.
 */
export function usePinnedLibrary(workspaceId: string, slug: string, enabled: boolean): PinnedLibrary | null {
  const principal = useSessionSafe()?.data?.user?.id
  const [state, setState] = React.useState<{ scope: string; value: PinnedLibrary } | null>(null)
  const [round, setRound] = React.useState(0)
  const target = JSON.stringify([workspaceId, slug])
  const scope = JSON.stringify([principal, target, enabled, round])
  React.useEffect(() => {
    const changed = (event: Event) => { if ((event as CustomEvent<string>).detail === target) setRound(value => value + 1) }
    window.addEventListener(PIN_CHANGED, changed)
    return () => window.removeEventListener(PIN_CHANGED, changed)
  }, [target])
  React.useEffect(() => {
    if (!enabled) return
    const abort = new AbortController()
    libraryClient.snapshot(workspaceId, slug, abort.signal)
      .then(({ tokens, files }) => {
        if (abort.signal.aborted) return
        const css = pinnedThemeCss(tokens, files)
        setState(css === null ? null : { scope, value: { css, tokens, files } })
      }, () => { if (!abort.signal.aborted) setState(null) })
    return () => abort.abort()
  }, [workspaceId, slug, enabled, scope])
  return enabled && state?.scope === scope ? state.value : null
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
      .then(() => { setReviewing(false); setUpdate(null); setRound(value => value + 1); window.dispatchEvent(new CustomEvent(PIN_CHANGED, { detail: JSON.stringify([workspaceId, slug]) })) }, failure => setError(message(failure)))
      .finally(() => setBusy(false))
  }
  const name = pin.library_name || "Library"
  return <section aria-label="Library" className="grid gap-2 rounded-card border px-3 py-2 text-sm" data-library-pin>
    <div className="flex flex-wrap items-center gap-2">
      <span>Uses <strong>{name}</strong> v{pin.library_version}</span>
      {update?.update_available && <>
        <span className="rounded-[var(--radius-row)] bg-primary/12 px-1.5 text-xs text-primary-hover">{update.dependencies_missing ? "Fonts not copied yet" : `Update available: v${update.published_version}`}</span>
        <Button type="button" size="sm" variant="secondary" className="h-7" aria-expanded={reviewing} onClick={() => setReviewing(value => !value)}>{reviewing ? "Close review" : "Review update"}</Button>
      </>}
      {update && !update.library_available && <span className="text-xs text-muted-foreground">You can no longer open this library. This project keeps its pinned copy.</span>}
    </div>
    {pin.not_copied.length > 0 && <ChangeList title="Not carried by this pin" items={pin.not_copied.map(notCopiedLabel)} />}
    {reviewing && update?.update_available && <div className="grid gap-2">
      {update.dependencies_missing && <p className="text-xs text-muted-foreground">This pin was made before fonts travelled with a Library. Copying them keeps v{update.published_version} and its tokens.</p>}
      <ChangeList title="Added" items={update.changes.added} />
      <ChangeList title="Changed" items={update.changes.changed} />
      <ChangeList title="Removed" items={update.changes.removed} />
      <ChangeList title="Files added" items={update.file_changes.added} />
      <ChangeList title="Files changed" items={update.file_changes.changed} />
      <ChangeList title="Files removed" items={update.file_changes.removed} />
      <ChangeList title="Not copied" items={update.not_copied.map(notCopiedLabel)} />
      {update.changes.truncated && <p className="text-xs text-muted-foreground">Only the first 200 changes of each kind are listed.</p>}
      <p className="text-xs text-muted-foreground">Nothing changes in this project until you apply the update.</p>
      {canWrite && <div><Button type="button" size="sm" disabled={busy} onClick={apply}>{update.dependencies_missing ? `Copy the fonts of v${update.published_version}` : `Apply v${update.published_version}`}</Button></div>}
    </div>}
    {error && <p role="alert" className="text-xs text-destructive">{error}</p>}
  </section>
}

/** "fonts/X.woff2:licence" as "fonts/X.woff2 (no redistributable licence)". */
function notCopiedLabel(item: string): string {
  const at = item.lastIndexOf(":")
  const reason = item.slice(at + 1)
  return `${item.slice(0, at)} (${reason === "licence" ? "no redistributable licence" : reason === "too_large" ? "too large" : reason})`
}

function ChangeList({ title, items }: { title: string; items: string[] }) {
  if (items.length === 0) return null
  return <div>
    <h3 className="text-xs font-medium">{title} <span className="text-muted-foreground">{items.length}</span></h3>
    <ul className="font-mono text-xs text-muted-foreground">{items.map(item => <li key={item}>{item}</li>)}</ul>
  </div>
}
