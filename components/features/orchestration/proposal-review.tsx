"use client"

import { useCallback, useEffect, useState } from "react"
import {
  FileText, Check, X, Clock, ChevronDown, ChevronRight,
  Loader2, Users, ArrowRight, Trash2,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Textarea } from "@/components/ui/textarea"
import { EmptyState } from "@/components/layout/empty-state"
import { cn } from "@/lib/utils"
import { toast } from "sonner"

interface ProposalTask {
  title: string
  description?: string
  assigned_agent_id?: string
  task_order: number
  depends_on?: string[]
}

interface ProposalMission {
  crew_id: string
  title: string
  description?: string
  tasks: ProposalTask[]
}

interface Proposal {
  id: string
  workspace_id: string
  proposed_by_id?: string
  proposer_name?: string
  proposer_slug?: string
  title: string
  description?: string
  plan?: string
  status: string
  missions?: ProposalMission[]
  mission_ids?: string[]
  reviewed_by?: string
  reviewed_at?: string
  review_notes?: string
  created_at: string
  updated_at: string
}

interface ProposalReviewProps {
  workspaceId: string
}

const statusConfig: Record<string, { label: string; color: string; icon: typeof Clock }> = {
  PENDING:  { label: "Pending Review", color: "amber",  icon: Clock },
  APPROVED: { label: "Approved",       color: "green",  icon: Check },
  REJECTED: { label: "Rejected",       color: "red",    icon: X },
  EXPIRED:  { label: "Expired",        color: "zinc",   icon: Clock },
}

export function ProposalReview({ workspaceId }: ProposalReviewProps) {
  const [proposals, setProposals] = useState<Proposal[]>([])
  const [loading, setLoading] = useState(true)
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [reviewNotes, setReviewNotes] = useState<Record<string, string>>({})
  const [processing, setProcessing] = useState<string | null>(null)

  const fetchProposals = useCallback(async () => {
    try {
      const res = await fetch(`/api/v1/mission-proposals?workspace_id=${workspaceId}`)
      if (res.ok) {
        setProposals(await res.json())
      }
    } catch {
      // ignore
    } finally {
      setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => { fetchProposals() }, [fetchProposals])

  // Poll for updates every 5s when there are pending proposals
  useEffect(() => {
    const hasPending = proposals.some((p) => p.status === "PENDING")
    if (!hasPending) return
    const interval = setInterval(fetchProposals, 5000)
    return () => clearInterval(interval)
  }, [proposals, fetchProposals])

  const handleApprove = async (proposalId: string) => {
    setProcessing(proposalId)
    try {
      const res = await fetch(`/api/v1/mission-proposals/${proposalId}/approve?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          reviewed_by: "human",
          review_notes: reviewNotes[proposalId] || "",
        }),
      })
      if (res.ok) {
        const data = await res.json()
        toast.success(`Proposal approved — ${data.mission_ids?.length || 0} missions created`)
        fetchProposals()
      } else {
        const err = await res.json().catch(() => ({ detail: "Unknown error" }))
        toast.error(err.detail || "Failed to approve")
      }
    } catch {
      toast.error("Network error")
    } finally {
      setProcessing(null)
    }
  }

  const handleReject = async (proposalId: string) => {
    setProcessing(proposalId)
    try {
      const res = await fetch(`/api/v1/mission-proposals/${proposalId}/reject?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          reviewed_by: "human",
          review_notes: reviewNotes[proposalId] || "",
        }),
      })
      if (res.ok) {
        toast.success("Proposal rejected")
        fetchProposals()
      }
    } catch {
      toast.error("Network error")
    } finally {
      setProcessing(null)
    }
  }

  const handleDelete = async (proposalId: string) => {
    try {
      const res = await fetch(`/api/v1/mission-proposals/${proposalId}?workspace_id=${workspaceId}`, {
        method: "DELETE",
      })
      if (res.ok) {
        toast.success("Proposal deleted")
        fetchProposals()
      }
    } catch {
      toast.error("Network error")
    }
  }

  if (loading) {
    return (
      <div className="flex items-center justify-center h-48 text-muted-foreground/70">
        <Loader2 className="h-5 w-5 animate-spin" />
      </div>
    )
  }

  if (proposals.length === 0) {
    return (
      <EmptyState
        icon={FileText}
        title="No proposals yet"
        description="Mission proposals appear here for your review. Retained as a primitive for future human-in-the-loop crew handoff flows."
      />
    )
  }

  const pending = proposals.filter((p) => p.status === "PENDING")
  const processed = proposals.filter((p) => p.status !== "PENDING")

  return (
    <div className="space-y-6">
      {pending.length > 0 && (
        <div className="space-y-3">
          <h2 className="text-sm font-medium text-amber-400/80 flex items-center gap-2">
            <Clock className="h-3.5 w-3.5" />
            Pending Review ({pending.length})
          </h2>
          {pending.map((p) => (
            <ProposalCard
              key={p.id}
              proposal={p}
              expanded={expandedId === p.id}
              onToggle={() => setExpandedId(expandedId === p.id ? null : p.id)}
              reviewNotes={reviewNotes[p.id] || ""}
              onNotesChange={(v) => setReviewNotes({ ...reviewNotes, [p.id]: v })}
              onApprove={() => handleApprove(p.id)}
              onReject={() => handleReject(p.id)}
              onDelete={() => handleDelete(p.id)}
              processing={processing === p.id}
            />
          ))}
        </div>
      )}

      {processed.length > 0 && (
        <div className="space-y-3">
          <h2 className="text-xs font-medium text-muted-foreground/70 uppercase tracking-wider">
            Previous ({processed.length})
          </h2>
          {processed.map((p) => (
            <ProposalCard
              key={p.id}
              proposal={p}
              expanded={expandedId === p.id}
              onToggle={() => setExpandedId(expandedId === p.id ? null : p.id)}
              compact
            />
          ))}
        </div>
      )}
    </div>
  )
}

function ProposalCard({
  proposal: p,
  expanded,
  onToggle,
  reviewNotes,
  onNotesChange,
  onApprove,
  onReject,
  onDelete,
  processing,
  compact,
}: {
  proposal: Proposal
  expanded: boolean
  onToggle: () => void
  reviewNotes?: string
  onNotesChange?: (v: string) => void
  onApprove?: () => void
  onReject?: () => void
  onDelete?: () => void
  processing?: boolean
  compact?: boolean
}) {
  const config = statusConfig[p.status] || statusConfig.PENDING
  const StatusIcon = config.icon

  return (
    <Card className={cn(
      "bg-accent/50 border-border transition-all",
      p.status === "PENDING" && "border-amber-500/20",
    )}>
      <CardHeader className="py-3 px-4 cursor-pointer" onClick={onToggle}>
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-3">
            {expanded ? <ChevronDown className="h-3.5 w-3.5 text-muted-foreground/70" /> : <ChevronRight className="h-3.5 w-3.5 text-muted-foreground/70" />}
            <CardTitle className="text-sm font-medium">{p.title}</CardTitle>
            <Badge variant="outline" className={cn(
              "text-[10px] h-5 gap-1",
              config.color === "amber" && "border-amber-500/30 text-amber-400",
              config.color === "green" && "border-green-500/30 text-green-400",
              config.color === "red" && "border-red-500/30 text-red-400",
              config.color === "zinc" && "border-zinc-500/30 text-zinc-400",
            )}>
              <StatusIcon className="h-2.5 w-2.5" />
              {config.label}
            </Badge>
          </div>
          <div className="flex items-center gap-2 text-[10px] text-muted-foreground/70">
            {p.proposer_name && (
              <span>by @{p.proposer_slug || p.proposer_name}</span>
            )}
            <span>{new Date(p.created_at).toLocaleDateString()}</span>
            {p.missions && (
              <Badge variant="outline" className="text-[10px] h-4 border-border">
                {p.missions.length} mission{p.missions.length !== 1 ? "s" : ""}
              </Badge>
            )}
          </div>
        </div>
      </CardHeader>

      {expanded && (
        <CardContent className="pt-0 px-4 pb-4 space-y-4">
          {p.description && (
            <p className="text-xs text-muted-foreground">{p.description}</p>
          )}

          {p.plan && (
            <div className="rounded-lg bg-accent/50 border border-border p-3">
              <h4 className="text-[10px] font-medium text-muted-foreground uppercase tracking-wider mb-2">Plan</h4>
              <p className="text-xs text-muted-foreground whitespace-pre-wrap">{p.plan}</p>
            </div>
          )}

          {p.missions && p.missions.length > 0 && (
            <div className="space-y-2">
              <h4 className="text-[10px] font-medium text-muted-foreground uppercase tracking-wider">
                Proposed Missions ({p.missions.length})
              </h4>
              {p.missions.map((m, i) => (
                <div key={i} className="rounded-lg bg-accent/50 border border-border p-3">
                  <div className="flex items-center gap-2 mb-2">
                    <Users className="h-3 w-3 text-muted-foreground/70" />
                    <span className="text-xs font-medium">{m.title}</span>
                    <Badge variant="outline" className="text-[10px] h-4 border-border">
                      crew: {m.crew_id.slice(0, 8)}...
                    </Badge>
                  </div>
                  {m.description && (
                    <p className="text-[11px] text-muted-foreground mb-2">{m.description}</p>
                  )}
                  {m.tasks.length > 0 && (
                    <div className="space-y-1">
                      {m.tasks.map((t, j) => (
                        <div key={j} className="flex items-center gap-2 text-[11px] text-muted-foreground">
                          <span className="text-muted-foreground/50">#{t.task_order}</span>
                          <ArrowRight className="h-2.5 w-2.5 text-muted-foreground/50" />
                          <span>{t.title}</span>
                          {t.depends_on && t.depends_on.length > 0 && (
                            <span className="text-muted-foreground/50">(depends on {t.depends_on.length})</span>
                          )}
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              ))}
            </div>
          )}

          {p.review_notes && (
            <div className="rounded-lg bg-accent/50 border border-border p-3">
              <h4 className="text-[10px] font-medium text-muted-foreground uppercase tracking-wider mb-1">Review Notes</h4>
              <p className="text-xs text-muted-foreground">{p.review_notes}</p>
            </div>
          )}

          {p.mission_ids && p.mission_ids.length > 0 && (
            <div className="text-[11px] text-green-400/60">
              Created missions: {p.mission_ids.join(", ")}
            </div>
          )}

          {!compact && p.status === "PENDING" && (
            <div className="space-y-3 pt-2 border-t border-border">
              <Textarea
                placeholder="Review notes (optional)..."
                value={reviewNotes || ""}
                onChange={(e) => onNotesChange?.(e.target.value)}
                className="text-xs h-16 bg-accent/50 border-border resize-none"
              />
              <div className="flex items-center justify-between">
                <Button
                  variant="ghost"
                  size="sm"
                  className="text-xs text-red-400/60 hover:text-red-400 h-7 px-2"
                  onClick={(e) => { e.stopPropagation(); onDelete?.() }}
                >
                  <Trash2 className="h-3 w-3 mr-1" /> Delete
                </Button>
                <div className="flex items-center gap-2">
                  <Button
                    variant="outline"
                    size="sm"
                    className="text-xs h-7 border-red-500/30 text-red-400 hover:bg-red-500/10"
                    onClick={(e) => { e.stopPropagation(); onReject?.() }}
                    disabled={processing}
                  >
                    {processing ? <Loader2 className="h-3 w-3 animate-spin mr-1" /> : <X className="h-3 w-3 mr-1" />}
                    Reject
                  </Button>
                  <Button
                    size="sm"
                    className="text-xs h-7 bg-green-600 hover:bg-green-700 text-white"
                    onClick={(e) => { e.stopPropagation(); onApprove?.() }}
                    disabled={processing}
                  >
                    {processing ? <Loader2 className="h-3 w-3 animate-spin mr-1" /> : <Check className="h-3 w-3 mr-1" />}
                    Approve & Create Missions
                  </Button>
                </div>
              </div>
            </div>
          )}
        </CardContent>
      )}
    </Card>
  )
}
