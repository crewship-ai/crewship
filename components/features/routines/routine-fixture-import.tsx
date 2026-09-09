"use client"

import { useEffect, useRef, useState } from "react"
import { Button } from "@/components/ui/button"
import { apiFetch } from "@/lib/api-fetch"
import { capturedRoutineFixture, type CapturedRoutineFixture } from "@/lib/routine-fixtures"

export function RoutineFixtureImport({
  workspaceId,
  disabled,
  onImport,
}: {
  workspaceId: string
  disabled?: boolean
  onImport: (fixture: CapturedRoutineFixture) => void
}) {
  const [runId, setRunId] = useState("")
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const sequence = useRef(0)
  const pending = useRef(false)
  useEffect(() => {
    const epochRef = sequence
    epochRef.current++
    pending.current = false
    setLoading(false)
    setError(null)
    setRunId("")
    return () => {
      epochRef.current++
    }
  }, [workspaceId])
  const load = async () => {
    if (pending.current || disabled || !runId.trim()) return
    pending.current = true
    setLoading(true)
    setError(null)
    const seq = ++sequence.current
    const id = runId.trim()
    try {
      const response = await apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipeline-runs/${encodeURIComponent(id)}`,
      )
      if (!response.ok) throw new Error(`Could not read captured run (${response.status}).`)
      const raw: unknown = await response.json()
      if (sequence.current !== seq) return
      onImport(capturedRoutineFixture(raw, workspaceId, id))
    } catch (e) {
      if (sequence.current === seq) setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (sequence.current === seq) {
        pending.current = false
        setLoading(false)
      }
    }
  }
  return (
    <div className="space-y-2 rounded-md border p-3">
      <label className="block text-sm font-medium" htmlFor="fixture-source-run">
        Use captured run data
      </label>
      <div className="flex gap-2">
        <input
          id="fixture-source-run"
          value={runId}
          disabled={loading || disabled}
          onChange={(e) => setRunId(e.target.value)}
          placeholder="Run ID"
          className="min-w-0 flex-1 rounded-md border bg-card p-2 text-sm"
        />
        <Button
          type="button"
          variant="outline"
          disabled={loading || disabled || !runId.trim()}
          onClick={() => void load()}
        >
          {loading ? "Loading…" : "Load captured data"}
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">
        Copies recorded inputs and available outputs into this test. This does not resume or rerun
        the source.
      </p>
      {error && (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      )}
    </div>
  )
}
