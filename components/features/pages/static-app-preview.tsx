"use client"

import { useEffect, useRef, useState } from "react"
import { staticAppPreview } from "@/lib/feature-flags"
import { staticArtifactDocument } from "@/lib/pages/artifact-static"

/** Exact confirmed content supplied by the Page authority, never an ACL grant.
 * The caller must pass null immediately when access is lost or unverified. */
export interface StaticAppPublication {
  workspaceId: string
  pageId: string
  revision: number
  profile: string
  html: string
}

function samePage(left: StaticAppPublication, right: StaticAppPublication) {
  return left.workspaceId === right.workspaceId && left.pageId === right.pageId
}
function supported(publication: StaticAppPublication) {
  return publication.profile === "static-html-v1" && Number.isSafeInteger(publication.revision) && publication.revision > 0
}

/** Static HTML only. Existing interactive Pages Apps keep their own profile.
 * Mount a new component (new key) for a different Page and after every authority change,
 * including revoke then regrant; no identity follows a reused slug.
 * Not mounted. Any future mount needs a principal/authority epoch fence: when the
 * user, workspace or permission changes, a preview loaded under the old identity
 * must be discarded (unmounted, late preparation ignored) and remounted only under
 * the new epoch, e.g. by keying this component on it. */
export function StaticAppPreview({ publication }: { publication: StaticAppPublication | null }) {
  const original = useRef(publication)
  const [opened, setOpened] = useState(publication)
  const [rendered, setRendered] = useState<{ publication: StaticAppPublication; document: string }>()
  const [failed, setFailed] = useState<StaticAppPublication>()
  const enabled = staticAppPreview()
  const verified = enabled && publication && original.current && samePage(original.current, publication) && supported(publication)
  const active = verified && opened && samePage(opened, publication) && supported(opened) ? opened : null
  // Clear during render, not in an effect, so no frame is committed for lost authority.
  // A revoke and regrant batched into one render is invisible here: the caller
  // must remount (key) after any authority change.
  if (!verified && opened) setOpened(null)
  useEffect(() => {
    if (!active) return
    const controller = new AbortController()
    void staticArtifactDocument(active.html, controller.signal).then(document => {
      if (!controller.signal.aborted) setRendered({ publication: active, document })
    }).catch(() => {
      if (!controller.signal.aborted) setFailed(active)
    })
    return () => controller.abort()
  }, [active])

  if (!enabled) return null
  if (!publication) return <p role="status">Static App preview unavailable. Verify access before reopening.</p>
  if (!verified || !active) return <p role="alert">Static App preview unavailable for this Page or execution profile. Reopen the Page.</p>
  const ready = rendered?.publication === active
  return <section aria-label="Static App preview" className="flex min-h-0 flex-1 flex-col gap-2">
    <p className="text-sm text-muted-foreground">Static preview · version {active.revision}. Scripts, navigation and external resources are disabled.</p>
    {publication.revision !== active.revision && <div className="flex flex-wrap items-center gap-2">
      <p>Version {publication.revision} is available. Loading it resets temporary preview state.</p>
      <button type="button" className="rounded border px-3 py-2 coarse:min-h-12" onClick={() => setOpened(publication)}>Load version {publication.revision}</button>
    </div>}
    {failed === active ? <p role="alert">Static preview could not be prepared. Reopen the Page to retry.</p> : <>
      {!ready && <p role="status">Preparing static preview…</p>}
      <iframe key={`${active.workspaceId}:${active.pageId}:${active.revision}`} title="App static preview" sandbox="" allow="" referrerPolicy="no-referrer" srcDoc={ready ? rendered.document : ""} className="min-h-80 w-full flex-1 border bg-white" />
    </>}
  </section>
}
