"use client"

import { useEffect, useMemo, useState } from "react"
import { Code2, Eye, History, MessageSquare, Upload, Columns2, Copy } from "lucide-react"
import { Button } from "@/components/ui/button"
import { ArtifactAgentWorkProvider, ArtifactAgentRequests, ArtifactSendButton } from "./artifact-agent-work"
import { ArtifactComments } from "./artifact-comments"
import { ArtifactSource } from "./artifact-source"
import { ContentPresence } from "./content-presence"
import { ArtifactStaticPreview } from "./artifact-static-preview"
import type { ArtifactAnchor } from "@/hooks/use-artifact-threads"
import { ArtifactCopyDialog } from "./artifact-copy-dialog"
import { ArtifactCopyError, useArtifactCopy } from "@/hooks/use-artifact-copy"
import { ArtifactCompare } from "./artifact-compare"
import { ArtifactHistory } from "./artifact-history"
import { PageArtifactError, usePageArtifact } from "@/hooks/use-page-artifact"
import { useArtifactRevision } from "@/hooks/use-artifact-revision"
import { staticArtifactDocument } from "@/lib/pages/artifact-static"

const MAX_BYTES = 10 * 1024 * 1024
export function PageArtifactView({ workspaceId, slug, pageId, onDirtyChange }: { workspaceId: string; slug: string; pageId?: string; onDirtyChange?: (dirty: boolean) => void }) {
  const [revision, setRevision] = useArtifactRevision()
  const { query, save } = usePageArtifact(workspaceId, slug, revision)
  const copy = useArtifactCopy(workspaceId, slug)
  const cancelCopy = copy.cancel
  const [copyRevision, setCopyRevision] = useState<number | null>(null)
  const [copyResult, setCopyResult] = useState<string | null>(null)
  const [view, setView] = useState<"preview" | "source" | "history" | "compare">("preview")
  const [comparison, setComparison] = useState<{ before: number; after: number } | null>(null)
  const [pickingPoint, setPickingPoint] = useState(false)
  const [point, setPoint] = useState<{ revision: number; anchor: Extract<ArtifactAnchor, { type: "point" }> } | null>(null)
  const [selectedAnchor, setSelectedAnchor] = useState<{ revision: number; anchor: ArtifactAnchor } | null>(null)
  const [commentsOpen, setCommentsOpen] = useState(false)
  const [commentsVisited, setCommentsVisited] = useState(false)
  const [commentsDirty, setCommentsDirty] = useState(false)
  const [agentDraftDirty, setAgentDraftDirty] = useState(false)
  const [draftBaseRev, setDraftBaseRev] = useState(0)
  const [draft, setDraft] = useState<string | null>(null)
  const [fileError, setFileError] = useState<string | null>(null)
  useEffect(() => {
    onDirtyChange?.(draft !== null || commentsDirty || agentDraftDirty || copyRevision !== null)
    return () => onDirtyChange?.(false)
  }, [draft, commentsDirty, agentDraftDirty, copyRevision, onDirtyChange])
  const artifact = query.data
  const sourceAccessDenied = query.error instanceof PageArtifactError && [401, 403, 404].includes(query.error.status)
  useEffect(() => {
    if (view !== "preview" || !artifact?.can_comment || !artifact.head_rev || sourceAccessDenied) return
    const keydown = (event: KeyboardEvent) => {
      const target = event.target instanceof Element ? event.target : null
      if (event.defaultPrevented || event.isComposing || event.repeat || event.metaKey || event.ctrlKey || event.altKey || target?.closest("input, textarea, select, [contenteditable], [role='textbox'], [role='dialog'], [role='alertdialog']")) return
      if (event.key === "Escape") {
        setPickingPoint(false)
        setPoint(null)
      } else if (event.key.toLowerCase() === "c" && !commentsDirty) {
        event.preventDefault()
        setPickingPoint(value => !value)
        setPoint(null)
      }
    }
    // Events inside the opaque document are intentionally not intercepted.
    window.addEventListener("keydown", keydown)
    return () => window.removeEventListener("keydown", keydown)
  }, [view, artifact?.can_comment, artifact?.head_rev, sourceAccessDenied, commentsDirty])
  useEffect(() => {
    if (sourceAccessDenied) { setCopyRevision(null); setCopyResult(null); cancelCopy(); setDraft(null); setCommentsDirty(false); setPoint(null); setSelectedAnchor(null); setPickingPoint(false) }
  }, [sourceAccessDenied, cancelCopy])
  useEffect(() => {
    if (artifact?.can_copy === false) { setCopyRevision(null); cancelCopy() }
  }, [artifact?.can_copy, cancelCopy])
  const document = useMemo(() => artifact ? staticArtifactDocument(artifact.html) : "", [artifact])
  const error = fileError ?? save.error?.message
  async function upload(file?: File) {
    if (!file) return
    setFileError(null)
    if (file.size > MAX_BYTES) { setFileError("Choose an HTML file smaller than 10 MiB."); return }
    const base = artifact?.head_rev ?? 0
    try { setDraftBaseRev(base); setDraft(await file.text()); setView("source"); save.reset() }
    catch { setFileError("Could not read this file. Choose it again.") }
  }
  function downloadSource() {
    const url = URL.createObjectURL(new Blob([artifact?.html ?? ""], { type: "text/plain;charset=utf-8" }))
    const link = window.document.createElement("a")
    link.href = url; link.download = `${slug}-revision-${artifact?.revision ?? 0}.html.txt`; link.click()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  }
  async function publish() {
    if (!artifact || draft === null) return
    setFileError(null)
    if (new TextEncoder().encode(draft).length > MAX_BYTES) { setFileError("HTML source must be smaller than 10 MiB."); return }
    try { await save.mutateAsync({ html: draft, base_rev: draftBaseRev }); setDraft(null); setRevision(undefined); setView("preview") } catch { /* Mutation error stays visible; preserve the draft. */ }
  }
  if (query.error && (sourceAccessDenied || !artifact)) return <div role="alert" className="rounded-lg border border-destructive/30 p-4 text-sm text-destructive">{query.error.message}<Button variant="outline" className="ml-3" onClick={() => void query.refetch()}>Retry</Button></div>
  if (!artifact) return <p role="status" className="p-6 text-sm text-muted-foreground">Loading artifact…</p>
  const content = <section className="overflow-hidden rounded-xl border bg-card" aria-label="HTML artifact">
    <div className="flex flex-wrap items-center gap-2 border-b px-4 py-3">
      <span className="mr-auto text-sm font-medium">HTML artifact {artifact.head_rev > 0 && <span className="ml-2 text-xs font-normal text-muted-foreground">Revision {artifact.revision}{artifact.revision !== artifact.head_rev ? ` · latest ${artifact.head_rev}` : " · latest"}</span>}</span>
      {([["preview", Eye, "Static preview"], ["source", Code2, "Source"], ["history", History, "History"]] as const).map(([id, Icon, label]) => <Button key={id} variant={view === id ? "secondary" : "ghost"} size="sm" className="coarse:h-12" onClick={() => setView(id)} aria-pressed={view === id}><Icon className="mr-1.5 h-4 w-4" />{label}</Button>)}
      {pageId && artifact.head_rev > 0 && <ContentPresence context={{ kind: "artifact", pageId, revision: artifact.revision }} />}
      {artifact.revisions.length > 1 && <Button variant={view === "compare" ? "secondary" : "ghost"} size="sm" className="coarse:h-12" aria-pressed={view === "compare"} onClick={() => {
        const newest = Math.max(...artifact.revisions.map(item => item.revision))
        setComparison({ before: artifact.revision !== newest ? artifact.revision : [...artifact.revisions].sort((a, b) => b.revision - a.revision)[1]?.revision ?? newest, after: newest })
        setView("compare")
      }}><Columns2 className="mr-1.5 h-4 w-4" />Compare</Button>}
      {artifact.head_rev > 0 && <ArtifactSendButton />}
      {artifact.can_copy && artifact.head_rev > 0 && <Button variant="ghost" size="sm" className="coarse:h-12" onClick={() => { copy.reset(); setCopyRevision(artifact.revision) }}><Copy className="mr-1.5 h-4 w-4" />Copy</Button>}
      {artifact.head_rev > 0 && <Button variant={commentsOpen ? "secondary" : "ghost"} size="sm" className="coarse:h-12" aria-pressed={commentsOpen} onClick={() => { setCommentsVisited(true); setCommentsOpen(open => !open) }}><MessageSquare className="mr-1.5 h-4 w-4" />Comments</Button>}
      {artifact.can_write && <label className="inline-flex cursor-pointer items-center gap-1.5 rounded-md border px-3 py-2 text-xs font-medium hover:bg-muted coarse:min-h-12"><Upload className="h-4 w-4" />Upload HTML<input aria-label="Upload HTML" type="file" accept=".html,.htm,text/html" className="sr-only" disabled={save.isPending} onChange={event => { void upload(event.target.files?.[0]); event.target.value = "" }} /></label>}
    </div>
    <p className="border-b bg-muted/30 px-4 py-2 text-xs text-muted-foreground">Static preview · Scripts, navigation and external resources are disabled. Interactive artifacts are unavailable.</p>
    {query.error && <p role="alert" className="m-4 rounded-md border border-warn/30 p-3 text-sm text-warn">The artifact could not refresh. Showing the last authorized revision; drafts are preserved. <Button variant="outline" size="sm" onClick={() => void query.refetch()}>Retry artifact</Button></p>}
    {copyResult && <p role="status" className="m-4 text-sm">Private copy created. <a className="underline" href={`/pages/${encodeURIComponent(copyResult)}`}>Open copy</a></p>}
    {artifact.copy_provenance && <p className="border-b px-4 py-2 text-xs text-muted-foreground">Originally copied from revision {artifact.copy_provenance.source_revision}{artifact.copy_provenance.source ? <> · <a className="underline" href={`/pages/${encodeURIComponent(artifact.copy_provenance.source.page_slug)}?revision=${artifact.copy_provenance.source_revision}`}>Open original</a></> : ' · Original unavailable'}</p>}
    {copyRevision !== null && <ArtifactCopyDialog revision={copyRevision} pending={copy.isPending} uncertain={copy.error instanceof ArtifactCopyError && copy.error.uncertain} error={copy.error?.message} onClose={() => { setCopyRevision(null); copy.reset() }} onCopy={input => { copy.mutate(input, { onSuccess: result => { setCopyResult(result.page_slug); setCopyRevision(null) } }) }} />}
    {error && <p role="alert" className="m-4 rounded-md border border-destructive/30 p-3 text-sm text-destructive">{error}<Button variant="outline" size="sm" className="ml-3" onClick={() => { void query.refetch(); save.reset(); setFileError(null) }}>Review latest revision</Button></p>}
    {artifact.head_rev === 0 && draft === null && <div className="space-y-3 p-6"><h2 className="text-base font-medium">Attach an HTML artifact</h2><p className="max-w-xl text-sm text-muted-foreground">Upload a report or paste HTML source. Each save creates an immutable revision on this page.</p>{artifact.can_write && <Button variant="outline" onClick={() => { setDraftBaseRev(artifact.head_rev); setDraft(""); setView("source") }}>Paste HTML</Button>}</div>}
    {view === "preview" && artifact.head_rev > 0 && <>
      <div className="flex flex-wrap gap-2 px-4 py-2">
        {artifact.can_comment && <Button size="sm" variant="outline" aria-keyshortcuts="C" title="Comment on a point (C); Escape closes the overlay" disabled={commentsDirty} onClick={() => { setPickingPoint(value => !value); setPoint(null) }}>{pickingPoint ? "Cancel point selection" : "Comment on a point"}</Button>}
        {point && <Button size="sm" variant="ghost" onClick={() => setPoint(null)}>Close point overlay</Button>}
      </div>
      <ArtifactStaticPreview document={document} revision={artifact.revision} picking={pickingPoint && artifact.can_comment === true} shown={point} onPick={anchor => {
        setPoint({ revision: artifact.revision, anchor }); setSelectedAnchor({ revision: artifact.revision, anchor }); setPickingPoint(false); setCommentsVisited(true); setCommentsOpen(true)
      }} />
    </>}
    {view === "source" && <ArtifactSource
      html={artifact.html} draft={draft} base={draftBaseRev} head={artifact.head_rev}
      canWrite={artifact.can_write} pending={save.isPending}
      onChange={setDraft} onUseLatest={() => setDraftBaseRev(artifact.head_rev)}
      onSave={() => void publish()} onCancel={() => { setDraft(null); save.reset() }}
      onEdit={() => { setDraftBaseRev(artifact.head_rev); setDraft(artifact.html) }}
      onDownload={downloadSource}
    />}
    {view === "compare" && comparison && <ArtifactCompare workspaceId={workspaceId} slug={slug} revisions={artifact.revisions.map(item => item.revision)} comparison={comparison} onComparisonChange={setComparison} />}
    {view === "history" && <ArtifactHistory
      artifact={artifact} pending={save.isPending}
      onView={selected => { setRevision(selected); setView("preview") }}
      onRestore={async selected => {
        try {
          await save.mutateAsync({ revision: selected, base_rev: artifact.head_rev })
          setRevision(undefined); setView("preview")
        } catch { /* Keep the mutation error visible. */ }
      }}
    />}
    {artifact.head_rev > 0 && <ArtifactAgentRequests />}
    {commentsVisited && artifact.head_rev > 0 && <div hidden={!commentsOpen}><ArtifactComments workspaceId={workspaceId} slug={slug} revision={artifact.revision} onViewRevision={selected => { setRevision(selected); setView("preview") }} onDirtyChange={setCommentsDirty} selectedAnchor={selectedAnchor} onAnchorConsumed={() => setSelectedAnchor(null)} onViewAnchor={(selected, anchor) => { setRevision(selected); setView("preview"); setPickingPoint(false); setPoint(anchor.type === "point" ? { revision: selected, anchor } : null) }} /></div>}
  </section>
  return artifact.head_rev > 0 ? <ArtifactAgentWorkProvider workspaceId={workspaceId} slug={slug} revision={artifact.revision} head={artifact.head_rev} onViewRevision={selected => { setRevision(selected); setView("preview") }} onCompareRevisions={(before, after) => { setComparison({ before, after }); setView("compare") }} onDirtyChange={setAgentDraftDirty}>{content}</ArtifactAgentWorkProvider> : content
}
