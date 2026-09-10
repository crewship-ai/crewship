"use client"
import { useCallback, useEffect, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { DetailCard } from "@/components/ui/detail"
import { Files } from "lucide-react"
import { Button } from "@/components/ui/button"
interface Artifact {
  id: string
  kind: string
  label: string
  state: string
  content?: string
  source: string
  error: string
  execution_path: string
  attempt: number
  sha256: string
}
interface RoutineRunArtifactsProps {
  workspaceId: string
  runId: string
  active: boolean
  compact?: boolean
  noFinalResult?: boolean
}

export function RoutineRunArtifacts({
  workspaceId,
  runId,
  active,
  compact = false,
  noFinalResult = false,
}: RoutineRunArtifactsProps) {
  const [artifacts, setArtifacts] = useState<Artifact[]>([])
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [cursor, setCursor] = useState<string | null>(null)
  const [page, setPage] = useState("")
  const [pages, setPages] = useState<string[]>([])
  const [contents, setContents] = useState<Record<string, string>>({})
  const base = `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipeline-runs/${encodeURIComponent(runId)}/artifacts`
  const refresh = useCallback(
    async (signal: AbortSignal) => {
      try {
        const res = await apiFetch(`${base}?after=${encodeURIComponent(page)}`, { signal })
        if (!res.ok) throw new Error("Results could not be loaded.")
        const data = await res.json()
        if (!signal.aborted) {
          setArtifacts(data.artifacts)
          setCursor(data.next_cursor)
          setError(null)
        }
      } catch (e) {
        if (!signal.aborted) setError(e instanceof Error ? e.message : String(e))
      } finally {
        if (!signal.aborted) setLoading(false)
      }
    },
    [base, page],
  )
  useEffect(() => {
    const controller = new AbortController()
    void refresh(controller.signal)
    const timer = active ? setInterval(() => void refresh(controller.signal), 3000) : null
    return () => {
      controller.abort()
      if (timer) clearInterval(timer)
    }
  }, [refresh, active])
  const loadContent = async (artifact: Artifact) => {
    if (contents[artifact.id] !== undefined) return
    try {
      const res = await apiFetch(`${base}?artifact_id=${encodeURIComponent(artifact.id)}`)
      if (!res.ok) throw new Error("Output content could not be loaded.")
      const data = await res.json()
      setContents((current) => ({
        ...current,
        [artifact.id]: data.content || artifact.source || "No content recorded.",
      }))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }
  const download = async (artifact: Artifact) => {
    try {
      const res = await apiFetch(`${base}?download=${encodeURIComponent(artifact.id)}`)
      if (!res.ok) throw new Error("This file is unavailable or access was denied.")
      const url = URL.createObjectURL(await res.blob())
      const anchor = document.createElement("a")
      anchor.href = url
      anchor.download = artifact.label
      anchor.click()
      setTimeout(() => URL.revokeObjectURL(url), 1000)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }
  if (compact && !loading && !error && !artifacts.length)
    return (
      <div className="flex items-center gap-3 rounded-2xl border border-border/60 bg-card px-4 py-3 text-xs text-muted-foreground">
        <Files className="h-4 w-4 shrink-0" />
        {noFinalResult ? "No final result or saved files" : "No saved files"}
        {active ? " yet" : ""}.
      </div>
    )
  const content = (
    <section className="space-y-3">
      {!compact && <h2 className="text-sm font-medium">Results</h2>}
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}{" "}
          <button onClick={() => void refresh(new AbortController().signal)}>Retry</button>
        </p>
      )}
      {loading ? (
        <p className="text-sm text-muted-foreground">Loading outputs…</p>
      ) : (
        !error &&
        !artifacts.length && (
          <p className="text-sm text-muted-foreground">
            No declared outputs recorded. Step responses are shown below.
          </p>
        )
      )}
      {artifacts.map((artifact) => (
        <article key={artifact.id} className="space-y-2 rounded-xl border p-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h3 className="text-sm font-medium">{artifact.label}</h3>
            <span className="text-xs text-muted-foreground">
              {artifact.kind.replaceAll("_", " ")} · {artifact.state}
            </span>
          </div>
          <p className="text-xs text-muted-foreground">Attempt {artifact.attempt}</p>
          {artifact.error && <p className="text-sm text-destructive">{artifact.error}</p>}
          {artifact.kind === "file" ? (
            artifact.sha256 && (
              <Button size="sm" variant="outline" onClick={() => download(artifact)}>
                Download saved version
              </Button>
            )
          ) : (
            <details
              onToggle={(e) => {
                if (e.currentTarget.open) void loadContent(artifact)
              }}
            >
              <summary className="cursor-pointer text-sm">View content</summary>
              <pre className="mt-2 max-h-80 overflow-auto whitespace-pre-wrap break-words text-xs">
                {contents[artifact.id] ?? "Loading content…"}
              </pre>
            </details>
          )}
          <details className="text-xs text-muted-foreground">
            <summary className="cursor-pointer">Technical details</summary>
            <p className="break-all">{artifact.execution_path}</p>
            <p className="break-all">{artifact.source}</p>
            {artifact.sha256 && <p className="break-all">SHA-256: {artifact.sha256}</p>}
          </details>
        </article>
      ))}
      <div className="flex gap-2">
        {pages.length > 0 && (
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              setPage(pages[pages.length - 1])
              setPages(pages.slice(0, -1))
            }}
          >
            Previous outputs
          </Button>
        )}
        {cursor && (
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              setPages([...pages, page])
              setPage(cursor)
            }}
          >
            More outputs
          </Button>
        )}
      </div>
    </section>
  )
  return compact ? (
    <DetailCard title="Results and files" icon={Files}>
      {content}
    </DetailCard>
  ) : (
    content
  )
}
