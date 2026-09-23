"use client"

import { useMemo } from "react"
import Link from "next/link"
import { Copy, FileSearch } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { DetailCard } from "@/components/ui/detail"
import { useJournalList } from "@/hooks/use-journal-list"
import { buildRunEvidence, formatRunEvidence, type RunEvidenceInput } from "@/lib/run-evidence"
import { relTime } from "@/lib/time"

interface Props {
  workspaceId: string
  run: Omit<RunEvidenceInput, "entries" | "capturedAt" | "journalUnavailable" | "journalIncomplete">
}

/** The preview and the clipboard consume the same bounded projection. */
export function RunEvidencePanel({ workspaceId, run }: Props) {
  const params = useMemo(() => ({ run_id: run.runId }), [run.runId])
  const { entries, loading, error, nextCursor, refresh } = useJournalList({
    workspaceId, params, limit: 100, maxEntries: 100,
  })
  const capturedAt = new Date().toISOString()
  const view = useMemo(() => buildRunEvidence({
    ...run,
    capturedAt,
    entries: error ? [] : entries,
    journalUnavailable: !!error,
    journalIncomplete: !!nextCursor,
  }), [run, capturedAt, entries, error, nextCursor])
  const text = useMemo(() => formatRunEvidence(view), [view])

  async function copy(value: string, label: string) {
    try {
      await navigator.clipboard.writeText(value)
      toast.success(`${label} copied`)
    } catch {
      toast.error(`Could not copy ${label.toLowerCase()}`)
    }
  }

  return <DetailCard title="Run evidence" icon={FileSearch} subtitle="Preview before sharing">
    <div className="flex flex-wrap items-center gap-2">
      <span className="font-mono text-xs">{run.runId}</span>
      <Button type="button" variant="outline" size="sm" onClick={() => void copy(`${window.location.origin}/activity?run=${encodeURIComponent(run.runId)}`, "Link")}>Copy link</Button>
      <Button type="button" variant="outline" size="sm" disabled={loading} onClick={() => void copy(text, "Evidence") }><Copy className="mr-1.5 h-3.5 w-3.5" />Copy evidence</Button>
      <Button type="button" variant="ghost" size="sm" disabled={loading} onClick={() => void refresh()}>Refresh evidence</Button>
      <Link className="text-xs text-primary hover:underline" href={view.sourceLinks[0]}>Open source activity ↗</Link>
    </div>
    <p className="mt-2 text-xs text-muted-foreground">{view.lastRecordedAt ? `Latest exported event ${relTime(view.lastRecordedAt)}.` : "No correlated event available for export."} This is a snapshot, not a live process check.</p>
    {loading && <p role="status" className="mt-2 text-xs text-muted-foreground">Reading run events…</p>}
    {error && <p role="alert" className="mt-2 text-xs text-destructive">Journal unavailable. The preview contains run metadata only. <button type="button" className="underline" onClick={() => void refresh()}>Retry</button></p>}
    <details className="mt-3 text-xs">
      <summary className="cursor-pointer">Preview evidence</summary>
      <pre data-testid="run-evidence-preview" className="mt-2 max-h-72 overflow-auto whitespace-pre-wrap break-all rounded-lg border border-border/60 bg-muted/30 p-3">{text}</pre>
    </details>
    <p className="mt-2 text-[11px] text-muted-foreground">Only recorded, correlated facts appear here. Missing events do not prove that work never ran.</p>
  </DetailCard>
}
