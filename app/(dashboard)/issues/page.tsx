"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { useWorkspace } from "@/hooks/use-workspace"
import { useSession } from "@/hooks/use-auth"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { Spinner } from "@/components/ui/spinner"
import {
  IssuesToolbar,
  type IssuesFilters,
} from "@/components/features/issues/issues-toolbar"
import { IssuesBoardView } from "@/components/features/issues/issues-board-view"
import { IssuesListView } from "@/components/features/issues/issues-list-view"
import { CreateIssueDialog } from "@/components/features/issues/create-issue-dialog"
import { IssueDetailSheet } from "@/components/features/issues/issue-detail-sheet"
import type { IssueLabel, Mission } from "@/lib/types/mission"

interface CrewOption {
  id: string
  name: string
  slug: string
}

export default function IssuesPage() {
  const { status } = useSession()
  const { workspaceId, loading: wsLoading } = useWorkspace()

  const [issues, setIssues] = useState<Mission[]>([])
  const [labels, setLabels] = useState<IssueLabel[]>([])
  const [crews, setCrews] = useState<CrewOption[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const [viewMode, setViewMode] = useState<"board" | "list">("board")
  const [filters, setFilters] = useState<IssuesFilters>({
    status: [],
    priority: [],
    crew_id: "",
    search: "",
  })

  const [createOpen, setCreateOpen] = useState(false)
  const [selectedIssue, setSelectedIssue] = useState<Mission | null>(null)
  const [detailOpen, setDetailOpen] = useState(false)

  const fetchIssues = useCallback(async () => {
    if (!workspaceId) return
    try {
      const params = new URLSearchParams({ workspace_id: workspaceId })
      if (filters.status.length > 0) {
        params.set("status", filters.status.join(","))
      }
      if (filters.priority.length > 0) {
        params.set("priority", filters.priority.join(","))
      }
      if (filters.crew_id) {
        params.set("crew_id", filters.crew_id)
      }
      if (filters.search) {
        params.set("search", filters.search)
      }
      const res = await fetch(`/api/v1/issues?${params.toString()}`)
      if (!res.ok) throw new Error("Failed to fetch issues")
      const data = await res.json()
      setIssues(Array.isArray(data) ? data : [])
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to fetch issues")
    }
  }, [workspaceId, filters])

  const fetchLabels = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await fetch(
        `/api/v1/labels?workspace_id=${encodeURIComponent(workspaceId)}`,
      )
      if (res.ok) {
        const data = await res.json()
        setLabels(Array.isArray(data) ? data : [])
      }
    } catch {
      // ignore
    }
  }, [workspaceId])

  const fetchCrews = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await fetch(
        `/api/v1/crews?workspace_id=${encodeURIComponent(workspaceId)}`,
      )
      if (res.ok) {
        const data = await res.json()
        setCrews(
          Array.isArray(data)
            ? data.map((c: { id: string; name: string; slug: string }) => ({
                id: c.id,
                name: c.name,
                slug: c.slug,
              }))
            : [],
        )
      }
    } catch {
      // ignore
    }
  }, [workspaceId])

  // Initial load
  useEffect(() => {
    if (!workspaceId) return
    setLoading(true)
    Promise.all([fetchIssues(), fetchLabels(), fetchCrews()]).finally(() =>
      setLoading(false),
    )
  }, [workspaceId, fetchIssues, fetchLabels, fetchCrews])

  // Refetch issues when filters change (after initial load)
  useEffect(() => {
    if (!workspaceId || loading) return
    fetchIssues()
  }, [filters]) // eslint-disable-line react-hooks/exhaustive-deps

  // Real-time updates
  const handleRealtimeUpdate = useCallback(() => {
    fetchIssues()
  }, [fetchIssues])

  useRealtimeEvent("mission.updated", handleRealtimeUpdate)

  // Client-side search filter (for instant feedback)
  const filteredIssues = useMemo(() => {
    if (!filters.search) return issues
    const q = filters.search.toLowerCase()
    return issues.filter(
      (issue) =>
        issue.title.toLowerCase().includes(q) ||
        (issue.identifier && issue.identifier.toLowerCase().includes(q)) ||
        (issue.assignee_name && issue.assignee_name.toLowerCase().includes(q)),
    )
  }, [issues, filters.search])

  function handleIssueClick(issue: Mission) {
    setSelectedIssue(issue)
    setDetailOpen(true)
  }

  function handleIssueUpdated() {
    fetchIssues()
    // Refresh the selected issue data
    if (selectedIssue) {
      fetchIssues().then(() => {
        setIssues((prev) => {
          const updated = prev.find((i) => i.id === selectedIssue.id)
          if (updated) setSelectedIssue(updated)
          return prev
        })
      })
    }
  }

  if (status === "loading" || wsLoading) {
    return (
      <div className="flex items-center justify-center h-full">
        <Spinner className="h-6 w-6" />
      </div>
    )
  }

  if (loading) {
    return (
      <div className="flex items-center justify-center h-full">
        <Spinner className="h-6 w-6" />
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-4 p-6 h-full">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-semibold text-foreground">Issues</h1>
      </div>

      <IssuesToolbar
        viewMode={viewMode}
        onViewModeChange={setViewMode}
        filters={filters}
        onFiltersChange={setFilters}
        onCreateClick={() => setCreateOpen(true)}
        labels={labels}
        workspaceId={workspaceId || ""}
        onLabelsChanged={fetchLabels}
      />

      {error && (
        <div className="rounded-lg border border-red-500/20 bg-red-500/5 px-4 py-3 text-sm text-red-500">
          {error}
        </div>
      )}

      {!error && filteredIssues.length === 0 && !loading && (
        <div className="flex flex-col items-center gap-2 py-16 text-center">
          <p className="text-sm text-muted-foreground">No issues found</p>
          <p className="text-xs text-muted-foreground/60">
            {filters.status.length > 0 || filters.priority.length > 0 || filters.search
              ? "Try adjusting your filters."
              : "Create your first issue to get started."}
          </p>
        </div>
      )}

      {!error && filteredIssues.length > 0 && (
        <>
          {viewMode === "board" ? (
            <IssuesBoardView
              issues={filteredIssues}
              onIssueClick={handleIssueClick}
              onCreateClick={() => setCreateOpen(true)}
            />
          ) : (
            <IssuesListView
              issues={filteredIssues}
              onIssueClick={handleIssueClick}
            />
          )}
        </>
      )}

      <CreateIssueDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        crews={crews}
        labels={labels}
        onCreated={() => {
          fetchIssues()
          fetchLabels()
        }}
        workspaceId={workspaceId || ""}
      />

      <IssueDetailSheet
        issue={selectedIssue}
        open={detailOpen}
        onOpenChange={setDetailOpen}
        labels={labels}
        onUpdated={handleIssueUpdated}
        workspaceId={workspaceId || ""}
      />
    </div>
  )
}
