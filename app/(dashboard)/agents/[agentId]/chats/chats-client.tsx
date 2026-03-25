"use client"

import { useParams } from "next/navigation"

import { useState, useEffect, useCallback } from "react"
import Link from "next/link"
import { Plus, MessageSquare, AlertCircle, Inbox } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { useRealtimeEvent } from "@/hooks/use-realtime"

interface Session {
  id: string
  title: string | null
  mode: string
  status: string
  message_count: number
  started_at: string
  ended_at: string | null
}

const STATUS_STYLES: Record<string, string> = {
  ACTIVE: "bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400",
  COMPLETED: "bg-muted text-muted-foreground",
  ERROR: "bg-red-50 text-red-700 dark:bg-red-950 dark:text-red-400",
}

function formatDuration(start: string, end: string | null): string {
  const startDate = new Date(start)
  const endDate = end ? new Date(end) : new Date()
  const diffMs = endDate.getTime() - startDate.getTime()
  const minutes = Math.floor(diffMs / 60000)
  if (minutes < 1) return "<1m"
  if (minutes >= 60) {
    const hours = Math.floor(minutes / 60)
    const remaining = minutes % 60
    return remaining > 0 ? `${hours}h ${remaining}m` : `${hours}h`
  }
  return `${minutes}m`
}

function formatRelativeTime(dateStr: string): string {
  const diff = Date.now() - new Date(dateStr).getTime()
  const minutes = Math.floor(diff / 60000)
  if (minutes < 1) return "Just now"
  if (minutes < 60) return `${minutes} min ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  return days === 1 ? "Yesterday" : `${days}d ago`
}

export function SessionsPageClient() {
  const { agentId } = useParams<{ agentId: string }>()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [sessions, setSessions] = useState<Session[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const fetchSessions = useCallback(async (silent = false) => {
    if (!workspaceId) return
    if (!silent) { setLoading(true); setError(null) }
    try {
      const res = await fetch(`/api/v1/agents/${agentId}/chats?workspace_id=${workspaceId}`)
      if (!res.ok) {
        if (!silent) setError("Failed to load chats")
        return
      }
      const data = await res.json()
      setSessions(Array.isArray(data) ? data : [])
    } catch {
      if (!silent) setError("Network error. Please try again.")
    } finally {
      if (!silent) setLoading(false)
    }
  }, [agentId, workspaceId])

  useEffect(() => {
    if (!workspaceId) return
    fetchSessions()
  }, [workspaceId, fetchSessions])

  // Real-time: refetch sessions when agent runs start/complete
  useRealtimeEvent("run.started", useCallback(() => { fetchSessions(true) }, [fetchSessions]))
  useRealtimeEvent("run.completed", useCallback(() => { fetchSessions(true) }, [fetchSessions]))

  if (wsLoading || loading) {
    return <SessionsSkeleton />
  }

  if (error) {
    return (
      <div className="p-4 sm:p-6">
        <div className="flex items-center gap-2 text-destructive">
          <AlertCircle className="h-5 w-5" />
          <p className="text-body">{error}</p>
        </div>
      </div>
    )
  }

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      {/* Header */}
      <div className="flex flex-wrap items-center gap-2">
        <p className="text-body text-muted-foreground">{sessions.length} session{sessions.length !== 1 ? "s" : ""} total</p>
        <div className="ml-auto">
          <Button size="sm" className="gap-1.5" asChild>
            <Link href={`/agents/${agentId}/chat`}>
              <Plus className="h-3.5 w-3.5" /> New Session
            </Link>
          </Button>
        </div>
      </div>

      {sessions.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-16 text-center">
          <Inbox className="h-10 w-10 text-muted-foreground/50 mb-3" />
          <p className="text-body font-medium text-muted-foreground">No chats yet</p>
          <p className="text-label text-muted-foreground mt-1">Start a chat to create the first session.</p>
        </div>
      ) : (
        <div className="border rounded-lg overflow-x-auto">
          <table className="w-full text-body">
            <thead>
              <tr className="border-b bg-muted/50 text-label text-muted-foreground uppercase tracking-wide">
                <th className="text-left px-4 sm:px-6 py-3 font-medium">Title</th>
                <th className="text-left px-4 sm:px-6 py-3 font-medium">Mode</th>
                <th className="text-left px-4 sm:px-6 py-3 font-medium">Status</th>
                <th className="text-left px-4 sm:px-6 py-3 font-medium">Messages</th>
                <th className="text-left px-4 sm:px-6 py-3 font-medium hidden sm:table-cell">Duration</th>
                <th className="text-left px-4 sm:px-6 py-3 font-medium hidden md:table-cell">Started</th>
              </tr>
            </thead>
            <tbody className="divide-y">
              {sessions.map((s) => (
                <tr key={s.id} className="hover:bg-muted/50">
                  <td className="px-4 sm:px-6 py-3">
                    <Link href={`/agents/${agentId}/chat?session=${s.id}&workspace_id=${workspaceId ?? ""}`} className="hover:underline flex items-center gap-1.5">
                      <MessageSquare className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
                      <span className="truncate max-w-[200px] sm:max-w-none">{s.title ?? "Untitled session"}</span>
                    </Link>
                  </td>
                  <td className="px-4 sm:px-6 py-3">
                    <Badge variant={s.mode === "CHAT" ? "secondary" : "outline"} className="text-label">{s.mode}</Badge>
                  </td>
                  <td className="px-4 sm:px-6 py-3">
                    <Badge variant="secondary" className={`${STATUS_STYLES[s.status] ?? ""} text-label`}>
                      {s.status === "ACTIVE" && <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse mr-1" />}
                      {s.status}
                    </Badge>
                  </td>
                  <td className="px-4 sm:px-6 py-3 font-mono text-xs">{s.message_count}</td>
                  <td className="px-4 sm:px-6 py-3 font-mono text-xs hidden sm:table-cell">
                    {formatDuration(s.started_at, s.ended_at)}
                  </td>
                  <td className="px-4 sm:px-6 py-3 text-label text-muted-foreground hidden md:table-cell">
                    {formatRelativeTime(s.started_at)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function SessionsSkeleton() {
  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <div className="flex items-center justify-between">
        <Skeleton className="h-5 w-32" />
        <Skeleton className="h-8 w-28" />
      </div>
      <div className="border rounded-lg">
        <div className="border-b bg-muted/50 px-4 sm:px-6 py-3">
          <Skeleton className="h-4 w-full max-w-md" />
        </div>
        {Array.from({ length: 4 }).map((_, i) => (
          <div key={i} className="px-4 sm:px-6 py-3 border-b last:border-b-0">
            <Skeleton className="h-5 w-full" />
          </div>
        ))}
      </div>
    </div>
  )
}
