"use client"

import { useCallback, useEffect, useState } from "react"
import { Plus, Link2, X } from "lucide-react"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { StatusIcon } from "@/components/features/issues/status-icon"
import { SectionHeader } from "@/components/features/issues/property-row"
import { cn } from "@/lib/utils"
import { toast } from "sonner"
import { RELATION_TYPE_LABELS, RELATION_TYPE_OPTIONS } from "@/components/features/issues/issue-constants"
import type { Mission, IssueRelation, RelationType } from "@/lib/types/mission"

interface IssueRelationsPanelProps {
  issue: Mission
  workspaceId: string
}

export function IssueRelationsPanel({ issue, workspaceId }: IssueRelationsPanelProps) {
  const [relationsOpen, setRelationsOpen] = useState(true)
  const [subIssuesOpen, setSubIssuesOpen] = useState(true)

  // Sub-issues
  const [subIssues, setSubIssues] = useState<Mission[]>([])

  // Relations
  const [relations, setRelations] = useState<IssueRelation[]>([])
  const [relationsLoading, setRelationsLoading] = useState(false)
  const [addRelationOpen, setAddRelationOpen] = useState(false)
  const [newRelationTarget, setNewRelationTarget] = useState("")
  const [newRelationType, setNewRelationType] = useState<RelationType>("relates_to")
  const [addingRelation, setAddingRelation] = useState(false)

  // Fetch sub-issues — reset state on issue switch so the previous issue's
  // subtasks don't briefly render while the new fetch is in flight.
  useEffect(() => {
    setSubIssues([])
    if (!issue.crew_id || !issue.identifier || !issue.sub_issues_count) return
    fetch(`/api/v1/crews/${issue.crew_id}/issues/${issue.identifier}/subtasks?workspace_id=${workspaceId}`)
      .then((r) => r.ok ? r.json() : [])
      .then((data) => setSubIssues(Array.isArray(data) ? data : data.subtasks ?? []))
      .catch(() => setSubIssues([]))
  }, [issue.crew_id, issue.identifier, issue.sub_issues_count, workspaceId])

  // Fetch relations
  const fetchRelations = useCallback(async () => {
    if (!issue.crew_id || !issue.identifier) return
    setRelationsLoading(true)
    try {
      const res = await fetch(
        `/api/v1/crews/${issue.crew_id}/issues/${issue.identifier}/relations?workspace_id=${workspaceId}`,
      )
      if (res.ok) {
        const data = await res.json()
        setRelations(Array.isArray(data) ? data : data.relations ?? [])
      }
    } catch {
      // silent — relations are supplementary
    } finally {
      setRelationsLoading(false)
    }
  }, [issue.crew_id, issue.identifier, workspaceId])

  useEffect(() => {
    fetchRelations()
  }, [fetchRelations])

  const handleAddRelation = useCallback(async () => {
    if (!newRelationTarget.trim() || !issue.crew_id || !issue.identifier) return
    setAddingRelation(true)
    try {
      const res = await fetch(
        `/api/v1/crews/${issue.crew_id}/issues/${issue.identifier}/relations?workspace_id=${workspaceId}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            target_identifier: newRelationTarget.trim(),
            relation_type: newRelationType,
          }),
        },
      )
      if (!res.ok) {
        const b = await res.json().catch(() => null)
        toast.error(b?.detail ?? "Failed to add relation")
        return
      }
      toast.success("Relation added")
      setNewRelationTarget("")
      setAddRelationOpen(false)
      fetchRelations()
    } catch {
      toast.error("Failed to add relation")
    } finally {
      setAddingRelation(false)
    }
  }, [newRelationTarget, newRelationType, issue.crew_id, issue.identifier, workspaceId, fetchRelations])

  const handleDeleteRelation = useCallback(async (relationId: string) => {
    try {
      const res = await fetch(
        `/api/v1/relations/${relationId}?workspace_id=${workspaceId}`,
        { method: "DELETE" },
      )
      if (!res.ok) {
        toast.error("Failed to remove relation")
        return
      }
      fetchRelations()
    } catch {
      toast.error("Failed to remove relation")
    }
  }, [workspaceId, fetchRelations])

  // Group relations by type
  const relationsByType = relations.reduce<Record<string, IssueRelation[]>>((acc, rel) => {
    const key = rel.relation_type
    if (!acc[key]) acc[key] = []
    acc[key].push(rel)
    return acc
  }, {})

  return (
    <>
      {/* ── Relations section ────────────────────────────────────────── */}
      <div className="mt-1 mx-2 rounded-lg border border-white/[0.04] bg-[#18171D]">
        <SectionHeader
          title="Relations"
          open={relationsOpen}
          onToggle={() => setRelationsOpen((v) => !v)}
          action={
            <Popover open={addRelationOpen} onOpenChange={setAddRelationOpen}>
              <PopoverTrigger asChild>
                <button
                  type="button"
                  aria-label="Add relation"
                  className="p-0.5 rounded hover:bg-white/[0.06] text-muted-foreground/40 hover:text-muted-foreground/70 transition-colors"
                >
                  <Plus className="h-3 w-3" />
                </button>
              </PopoverTrigger>
              <PopoverContent className="w-[260px] p-3" align="end" sideOffset={4}>
                <div className="space-y-2">
                  <p className="text-[11px] font-medium text-foreground/80">Add relation</p>
                  <input
                    aria-label="Target issue identifier"
                    value={newRelationTarget}
                    onChange={(e) => setNewRelationTarget(e.target.value)}
                    placeholder="Target identifier (e.g. ENG-5)"
                    className="w-full h-7 px-2 bg-white/[0.04] border border-white/[0.08] rounded text-[11px] text-foreground placeholder:text-muted-foreground/30 outline-none focus:border-blue-400/40"
                    onKeyDown={(e) => {
                      if (e.key === "Enter") handleAddRelation()
                    }}
                  />
                  <div className="flex gap-1 flex-wrap">
                    {RELATION_TYPE_OPTIONS.map((opt) => (
                      <button
                        key={opt.value}
                        onClick={() => setNewRelationType(opt.value)}
                        className={cn(
                          "px-2 py-0.5 rounded text-[10px] border transition-colors",
                          newRelationType === opt.value
                            ? "border-blue-400/50 bg-blue-500/10 text-blue-400"
                            : "border-white/[0.08] text-muted-foreground/60 hover:border-white/[0.15]",
                        )}
                      >
                        {opt.label}
                      </button>
                    ))}
                  </div>
                  <button
                    onClick={handleAddRelation}
                    disabled={!newRelationTarget.trim() || addingRelation}
                    className={cn(
                      "w-full h-7 rounded text-[11px] font-medium transition-colors",
                      newRelationTarget.trim() && !addingRelation
                        ? "bg-blue-600 text-white hover:bg-blue-500"
                        : "bg-white/[0.04] text-muted-foreground/30 cursor-not-allowed",
                    )}
                  >
                    {addingRelation ? "Adding..." : "Add relation"}
                  </button>
                </div>
              </PopoverContent>
            </Popover>
          }
        />
        {relationsOpen && (
          <div className="px-3 pb-1.5">
            {relationsLoading ? (
              <span className="text-[11px] text-muted-foreground/40">Loading...</span>
            ) : relations.length === 0 ? (
              <span className="text-[11px] text-muted-foreground/40 pl-0.5">
                No relations
              </span>
            ) : (
              <div className="space-y-2">
                {Object.entries(relationsByType).map(([type, rels]) => (
                  <div key={type}>
                    <span className="text-[10px] uppercase tracking-wider text-foreground/50">
                      {RELATION_TYPE_LABELS[type as RelationType] || type}
                    </span>
                    <div className="mt-0.5 space-y-0.5">
                      {rels.map((rel) => (
                        <div
                          key={rel.id}
                          className="group flex items-center gap-1.5 py-1 px-1 rounded hover:bg-white/[0.04] transition-colors"
                        >
                          {rel.target_status ? (
                            <StatusIcon
                              status={rel.target_status}
                              className="h-3 w-3 shrink-0"
                            />
                          ) : (
                            <Link2 className="h-3 w-3 shrink-0 text-muted-foreground/40" />
                          )}
                          <span className="text-[10px] font-mono text-muted-foreground/60 shrink-0">
                            {rel.target_identifier || "--"}
                          </span>
                          <span className="text-[11px] text-foreground/70 truncate flex-1">
                            {rel.target_title || "Untitled"}
                          </span>
                          <button
                            type="button"
                            aria-label={`Remove ${rel.target_identifier || "relation"}`}
                            onClick={() => handleDeleteRelation(rel.id)}
                            className="opacity-0 group-hover:opacity-100 p-0.5 rounded hover:bg-white/[0.08] text-muted-foreground/40 hover:text-red-400 transition-all"
                          >
                            <X className="h-2.5 w-2.5" />
                          </button>
                        </div>
                      ))}
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        )}
      </div>

      {/* ── Sub-issues section ──────────────────────────────────────── */}
      {issue.sub_issues_count != null && issue.sub_issues_count > 0 && (
        <div className="mt-1 mx-2 rounded-lg border border-white/[0.04] bg-[#18171D]">
          <SectionHeader
            title={`Sub-issues (${issue.sub_issues_count})`}
            open={subIssuesOpen}
            onToggle={() => setSubIssuesOpen((v) => !v)}
          />
          {subIssuesOpen && (
            <div className="px-3 pb-2 space-y-1">
              {subIssues.length === 0 ? (
                <span className="text-[11px] text-foreground/40 pl-0.5">Loading...</span>
              ) : (
                subIssues.map((sub) => (
                  <a
                    key={sub.id}
                    href={`/orchestration/issues/${sub.identifier}`}
                    className="flex items-center gap-2 py-1 text-xs hover:bg-white/[0.04] rounded px-1"
                  >
                    <StatusIcon status={sub.status} className="h-3.5 w-3.5" />
                    <span className="font-mono text-muted-foreground/60">{sub.identifier}</span>
                    <span className="truncate text-foreground/70">{sub.title}</span>
                  </a>
                ))
              )}
            </div>
          )}
        </div>
      )}
    </>
  )
}
