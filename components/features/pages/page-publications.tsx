"use client"

import { publicationReceiptMessage } from "@/lib/pages/publication-receipt"

import { useState } from "react"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { usePageApplication } from "@/hooks/use-page-application"
import { usePagePublications, type PublicationReceipt } from "@/hooks/use-page-publications"

export function PagePublicationsDialog({ workspaceId, slug, onClose }: { workspaceId: string; slug: string; onClose: () => void }) {
  const [selected, setSelected] = useState<PublicationReceipt | null>(null)
  const [path, setPath] = useState("")
  const [reviewed, setReviewed] = useState(false)
  const [confirmedWithdrawal, setConfirmedWithdrawal] = useState<number | null>(null)
  const [reviewedHead, setReviewedHead] = useState<number | null>(null)
  const { query, source, withdraw } = usePagePublications(workspaceId, slug, selected?.source_revision)
  const { publish } = usePageApplication(workspaceId, slug)
  const state = query.data?.pages[0]
  const file = source.data?.project.files.find(file => file.path === path) ?? source.data?.project.files[0]
  const failed = query.isError || source.isError || withdraw.isError || publish.isError
  const busy = withdraw.isPending || publish.isPending
  return <Dialog open onOpenChange={open => { if (!open) onClose() }}>
    <DialogContent className="flex max-h-[90vh] flex-col sm:max-w-[960px]">
      <DialogHeader><DialogTitle>Application publications</DialogTitle><DialogDescription>Inspect an earlier version before restoring it. Restoring creates a new publication and preserves the current source draft. Completed routine actions are not undone.</DialogDescription></DialogHeader>
      {query.isPending && <p role="status">Loading publications…</p>}
      {failed && <p role="alert">{query.error?.message ?? source.error?.message ?? withdraw.error?.message ?? publish.error?.message}</p>}
      {publish.isSuccess && <p role="status">{publicationReceiptMessage(publish.data)}</p>}
      {withdraw.isSuccess && <p role="status">Application withdrawn. Panels and producers remain available.</p>}
      {state && <div className="flex flex-wrap items-center gap-3">
        <span>{state.published ? `Live version ${state.publication_version}` : "Application is not published"}</span>
        {state.can_publish && state.published && <>
          <label className="text-sm"><input type="checkbox" checked={confirmedWithdrawal === state.publication_version} onChange={event => setConfirmedWithdrawal(event.target.checked ? state.publication_version : null)} /> Stop this application for all viewers</label>
          <Button variant="destructive" disabled={confirmedWithdrawal !== state.publication_version || busy || query.isError} onClick={() => withdraw.mutate(state.publication_version)}>Withdraw application</Button>
        </>}
      </div>}
      <div className="min-h-0 overflow-auto">
        {query.data?.pages.flatMap(page => page.publications).map(receipt => <div key={receipt.version} className="flex items-center justify-between gap-3 border-b py-2">
          <div><p>Version {receipt.version} · source {receipt.source_revision}{receipt.rollback_of ? ` · restored from ${receipt.rollback_of}` : ""}</p><p className="text-xs text-muted-foreground">{receipt.created_at}{receipt.withdrawn_at ? " · withdrawn" : ""}</p></div>
          <Button variant="outline" onClick={() => { setSelected(receipt); setReviewed(false); setPath("") }}>Inspect version {receipt.version}</Button>
        </div>)}
        {state?.publications.length === 0 && <p>No application has been published yet.</p>}
        {query.hasNextPage && <Button disabled={query.isFetchingNextPage} onClick={() => void query.fetchNextPage()}>Older publications</Button>}
        {selected && <section className="mt-4 space-y-3" aria-label={`Inspect version ${selected.version}`}>
          <p className="break-all text-xs">Build: {selected.build_id}<br />Git: {selected.git_commit}<br />Artifact: {selected.artifact_digest}</p>
          {source.isPending && <p role="status">Loading archived source…</p>}
          {source.data && !source.isError && !query.isError && <>
            <label className="flex items-center gap-2 text-sm">Source file<select className="max-w-full rounded border bg-background p-1" value={file?.path ?? ""} onChange={event => setPath(event.target.value)}>{source.data.project.files.map(file => <option key={file.path} value={file.path}>{file.path}</option>)}</select></label>
            <pre className="max-h-64 overflow-auto whitespace-pre-wrap rounded border p-3 text-xs">{file?.encoding === "base64" ? "Binary asset (base64). Export the project to inspect this asset." : file?.content}</pre>
            {state?.can_publish && <>
              <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={reviewed && reviewedHead === state.publication_version} onChange={event => { setReviewed(event.target.checked); setReviewedHead(state.publication_version) }} />I reviewed version {selected.version} and trust its code.</label>
              <Button disabled={!reviewed || reviewedHead !== state.publication_version || busy || query.isError || source.data.git_commit !== selected.git_commit || (state.published && state.publication_version === selected.version)} onClick={() => publish.mutate({ rollback_version: selected.version, expected_publication: state.publication_version, reviewed_code: true })}>Restore version {selected.version}</Button>
            </>}
          </>}
        </section>}
      </div>
    </DialogContent>
  </Dialog>
}
