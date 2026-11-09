"use client"

import * as React from "react"
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
export interface LibraryUpdate { pinned_version: number; published_version: number; library_available: boolean; update_available: boolean; changes: LibraryChanges; file_changes: LibraryChanges; not_copied: string[]; dependencies_missing: boolean }

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
  if (!isObject(raw) || !isInt(raw.pinned_version) || !isInt(raw.published_version) || typeof raw.library_available !== "boolean" || typeof raw.update_available !== "boolean" || !isObject(raw.changes)) return null
  const added = strings(raw.changes.added), removed = strings(raw.changes.removed), changed = strings(raw.changes.changed)
  if (!added || !removed || !changed) return null
  // Older servers send no file review; an empty one says nothing changed.
  const files = isObject(raw.file_changes) ? raw.file_changes : {}
  const fileAdded = strings(files.added ?? []), fileRemoved = strings(files.removed ?? []), fileChanged = strings(files.changed ?? []), notCopied = strings(raw.not_copied ?? [])
  if (!fileAdded || !fileRemoved || !fileChanged || !notCopied) return null
  return { pinned_version: raw.pinned_version, published_version: raw.published_version, library_available: raw.library_available, update_available: raw.update_available, changes: { added, removed, changed, truncated: raw.changes.truncated === true }, file_changes: { added: fileAdded, removed: fileRemoved, changed: fileChanged }, not_copied: notCopied, dependencies_missing: raw.dependencies_missing === true }
}

export const libraryClient = {
  publication: (ws: string, slug: string, signal?: AbortSignal) => artifactRequest(path(ws, slug, "/library"), parsePublication, { signal }),
  publish: (ws: string, slug: string, version: number) => artifactRequest(path(ws, slug, "/library/publish"), parsePublication, { method: "POST", body: { version } }),
  pin: (ws: string, slug: string, signal?: AbortSignal) => artifactRequest(path(ws, slug, "/library-pin"), parsePin, { signal }),
  update: (ws: string, slug: string, signal?: AbortSignal) => artifactRequest(path(ws, slug, "/library-pin/update"), parseUpdate, { signal }),
  /** The pinned design-system.json, as the server stored it. */
  tokens: (ws: string, slug: string, signal?: AbortSignal) => artifactRequest(path(ws, slug, "/library-pin"), (raw: unknown) => isObject(raw) && isObject(raw.tokens) ? JSON.stringify(raw.tokens) : null, { signal }),
  files: (ws: string, slug: string, signal?: AbortSignal) => artifactRequest(path(ws, slug, "/library-pin/files"), parsePinnedFiles, { signal }),
  /** Applies exactly the reviewed state: the published version shown, over the pin shown. */
  apply: (ws: string, slug: string, library: string, version: number, ifPinned: number) => artifactRequest(path(ws, slug, "/library-pin"), parsePin, { method: "PUT", body: { library: "@" + library, version, if_pinned_library: "@" + library, if_pinned_version: ifPinned } }),
}

const PIN_CHANGED = "crewship:library-pin-changed"

export interface PinnedLibrary { css: string | null; files: PinnedFile[] }

/**
 * The consumer's pinned theme, derived from its own copy (#5248). Null while
 * loading, without a pin or when the project is itself a Library. A change to
 * the live Library never reaches this until someone applies an update.
 */
export function usePinnedLibrary(workspaceId: string, slug: string, enabled: boolean): PinnedLibrary | null {
  const [state, setState] = React.useState<PinnedLibrary | null>(null)
  const [round, setRound] = React.useState(0)
  React.useEffect(() => {
    // An applied update re-renders from the new copy.
    const changed = (event: Event) => { if ((event as CustomEvent<string>).detail === slug) setRound(value => value + 1) }
    window.addEventListener(PIN_CHANGED, changed)
    return () => window.removeEventListener(PIN_CHANGED, changed)
  }, [slug])
  React.useEffect(() => {
    if (!enabled) { setState(null); return }
    const abort = new AbortController()
    // Files are asked for only once a pin exists.
    libraryClient.tokens(workspaceId, slug, abort.signal)
      .then(tokens => libraryClient.files(workspaceId, slug, abort.signal).then(files => setState({ css: pinnedThemeCss(tokens, files), files })))
      .catch(() => setState(null))
    return () => abort.abort()
  }, [workspaceId, slug, enabled, round])
  return state
}

const message = (error: unknown) => (error as ArtifactRequestError)?.message || "Something went wrong. Try again."

/** "Published vN" for every reader, and Publish for the Library's owner only. */
export function LibraryPublishControl({ shown }: { shown: number }) {
  const viewer = useArtifactViewer()
  if (!viewer?.target) return null
  return <PublishControl workspaceId={viewer.workspaceId} slug={viewer.target.slug} shown={shown} />
}

function PublishControl({ workspaceId, slug, shown }: { workspaceId: string; slug: string; shown: number }) {
  const [state, setState] = React.useState<LibraryPublication | null>(null)
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)
  React.useEffect(() => {
    const abort = new AbortController()
    libraryClient.publication(workspaceId, slug, abort.signal).then(setState, () => setState(null))
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
export function LibraryPinNotice() {
  const viewer = useArtifactViewer()
  if (!viewer?.target) return null
  return <PinNotice workspaceId={viewer.workspaceId} slug={viewer.target.slug} />
}

function PinNotice({ workspaceId, slug }: { workspaceId: string; slug: string }) {
  const [pin, setPin] = React.useState<LibraryPin | null>(null)
  const [update, setUpdate] = React.useState<LibraryUpdate | null>(null)
  const [reviewing, setReviewing] = React.useState(false)
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)
  const [round, setRound] = React.useState(0)
  React.useEffect(() => {
    const abort = new AbortController()
    libraryClient.pin(workspaceId, slug, abort.signal).then(found => {
      setPin(found)
      return libraryClient.update(workspaceId, slug, abort.signal).then(setUpdate, () => setUpdate(null))
    }, () => { setPin(null); setUpdate(null) })
    return () => abort.abort()
  }, [workspaceId, slug, round])
  if (!pin) return null
  const apply = () => {
    if (!update) return
    setBusy(true); setError(null)
    libraryClient.apply(workspaceId, slug, pin.library_page_id, update.published_version, update.pinned_version)
      .then(() => { setReviewing(false); setRound(value => value + 1); window.dispatchEvent(new CustomEvent(PIN_CHANGED, { detail: slug })) }, failure => setError(message(failure)))
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
      <div><Button type="button" size="sm" disabled={busy} onClick={apply}>{update.dependencies_missing ? `Copy the fonts of v${update.published_version}` : `Apply v${update.published_version}`}</Button></div>
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
