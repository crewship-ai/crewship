"use client"

import { useState } from "react"
import {
  AlertTriangle,
  CheckCircle2,
  XCircle,
  ArrowRightLeft,
  FileText,
  Send,
  ExternalLink,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Badge } from "@/components/ui/badge"
import type { Escalation } from "@/lib/types/escalation"
import { parseEvidencePack } from "@/lib/types/escalation"

interface EscalationResponseCardProps {
  escalation: Escalation
  workspaceId: string
  crewId: string
  onResolved: () => void
}

function parseMetadataUrl(metadata: string | null): string | null {
  if (!metadata) return null
  try {
    const parsed = JSON.parse(metadata)
    return parsed.url || null
  } catch {
    if (metadata.startsWith("https://")) return metadata
    return null
  }
}

// Map confidence 0-1 to nearest Tailwind width class (w-0 through w-full).
function confidenceWidthClass(confidence: number): string {
  const pct = Math.round(confidence * 100)
  if (pct <= 0) return "w-0"
  if (pct <= 15) return "w-1/6"
  if (pct <= 25) return "w-1/4"
  if (pct <= 35) return "w-1/3"
  if (pct <= 50) return "w-1/2"
  if (pct <= 65) return "w-2/3"
  if (pct <= 75) return "w-3/4"
  if (pct <= 85) return "w-5/6"
  return "w-full"
}

function ConfidenceIndicator({ confidence }: { confidence: number }) {
  const level = confidence <= 0.3 ? "low" : confidence <= 0.6 ? "medium" : "high"
  const colors = {
    low: "bg-red-500",
    medium: "bg-amber-500",
    high: "bg-emerald-500",
  }
  const labels = { low: "Low", medium: "Medium", high: "High" }

  return (
    <div className="flex items-center gap-2">
      <span className="text-label text-muted-foreground">Confidence:</span>
      <div className="flex items-center gap-1.5">
        <div className="h-1.5 w-16 rounded-full bg-muted overflow-hidden">
          <div className={`h-full rounded-full ${colors[level]} ${confidenceWidthClass(confidence)}`} />
        </div>
        <span className={`text-label font-medium ${
          level === "low" ? "text-red-600 dark:text-red-400" :
          level === "medium" ? "text-amber-600 dark:text-amber-400" :
          "text-emerald-600 dark:text-emerald-400"
        }`}>
          {labels[level]} ({Math.round(confidence * 100)}%)
        </span>
      </div>
    </div>
  )
}

export function EscalationResponseCard({
  escalation,
  workspaceId,
  crewId,
  onResolved,
}: EscalationResponseCardProps) {
  const [resolution, setResolution] = useState("")
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [showRedirect, setShowRedirect] = useState(false)
  const [redirectTo, setRedirectTo] = useState("")
  const [agents, setAgents] = useState<{ slug: string; name: string }[]>([])
  const [agentsLoaded, setAgentsLoaded] = useState(false)

  const evidencePack = parseEvidencePack(escalation.metadata)
  const metadataUrl = parseMetadataUrl(escalation.metadata)

  const handleResolve = async (action: "approve" | "reject" | "redirect") => {
    if (!resolution.trim()) return
    if (action === "redirect" && !redirectTo) return

    setSubmitting(true)
    setError(null)
    try {
      const res = await fetch(`/api/v1/escalations/${escalation.id}/resolve`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          resolution: resolution.trim(),
          action,
          redirect_to: action === "redirect" ? redirectTo : undefined,
          workspace_id: workspaceId,
        }),
      })
      if (!res.ok) {
        const err = await res.json().catch(() => ({ error: "Failed to resolve" }))
        setError(err.error || "Failed to resolve")
        return
      }
      setResolution("")
      onResolved()
    } catch {
      setError("Network error")
    } finally {
      setSubmitting(false)
    }
  }

  const loadAgents = async () => {
    if (agentsLoaded) return
    try {
      const res = await fetch(`/api/v1/agents?crew_id=${crewId}&workspace_id=${workspaceId}`)
      if (!res.ok) return // Don't mark as loaded so user can retry
      const data = await res.json()
      const crewAgents = (Array.isArray(data) ? data : [])
        .filter((a: { slug: string }) => a.slug !== escalation.from_slug)
        .map((a: { slug: string; name: string }) => ({ slug: a.slug, name: a.name }))
      setAgents(crewAgents)
      setAgentsLoaded(true)
    } catch {
      // Don't mark as loaded on network error — user can retry by toggling redirect
    }
  }

  const handleRedirectClick = () => {
    setShowRedirect(!showRedirect)
    if (!agentsLoaded) loadAgents()
  }

  return (
    <div className="space-y-4 p-4">
      {/* Evidence Pack */}
      {evidencePack && (
        <div className="space-y-3 rounded-lg border border-border/50 bg-muted/20 p-3">
          <div className="flex items-center gap-2 text-body font-medium">
            <FileText className="h-3.5 w-3.5 text-muted-foreground" />
            Evidence Pack
          </div>

          {(evidencePack.task_title || evidencePack.agent_slug) && (
            <div className="text-body">
              {evidencePack.task_title && (
                <span className="font-medium">{evidencePack.task_title}</span>
              )}
              {evidencePack.agent_slug && (
                <span className="text-muted-foreground"> by @{evidencePack.agent_slug}</span>
              )}
            </div>
          )}

          {evidencePack.agent_actions && evidencePack.agent_actions.length > 0 && (
            <div>
              <span className="text-label font-medium text-muted-foreground">What was tried:</span>
              <ol className="mt-1 list-decimal list-inside space-y-0.5">
                {evidencePack.agent_actions.map((action, i) => (
                  <li key={i} className="text-body text-muted-foreground">{action}</li>
                ))}
              </ol>
            </div>
          )}

          {evidencePack.error && (
            <div className="rounded-md bg-red-50 dark:bg-red-950/30 border border-red-200 dark:border-red-900/50 p-2.5">
              <div className="flex items-start gap-2">
                <AlertTriangle className="h-3.5 w-3.5 text-red-600 dark:text-red-400 mt-0.5 shrink-0" />
                <span className="text-body text-red-700 dark:text-red-300 font-mono text-xs break-all">
                  {evidencePack.error}
                </span>
              </div>
            </div>
          )}

          {evidencePack.relevant_files && evidencePack.relevant_files.length > 0 && (
            <div>
              <span className="text-label font-medium text-muted-foreground">Relevant files:</span>
              <div className="mt-1 space-y-0.5">
                {evidencePack.relevant_files.map((file, i) => (
                  <div key={i} className="text-xs font-mono text-muted-foreground">{file}</div>
                ))}
              </div>
            </div>
          )}

          {evidencePack.confidence !== undefined && (
            <ConfidenceIndicator confidence={evidencePack.confidence} />
          )}

          {evidencePack.suggested_action && (
            <div className="rounded-md bg-blue-50 dark:bg-blue-950/30 border border-blue-200 dark:border-blue-900/50 p-2.5">
              <span className="text-label font-medium text-blue-700 dark:text-blue-300">
                Suggested: </span>
              <span className="text-body text-blue-600 dark:text-blue-400">
                {evidencePack.suggested_action}
              </span>
            </div>
          )}
        </div>
      )}

      {/* Context (non-evidence-pack) */}
      {!evidencePack && escalation.context && (
        <div className="text-body">
          <span className="font-medium text-muted-foreground">Context: </span>
          <span className="whitespace-pre-wrap">{escalation.context}</span>
        </div>
      )}

      {/* Link for LINK type */}
      {escalation.type === "LINK" && metadataUrl && (
        <div>
          <a
            href={metadataUrl}
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1.5 text-sm text-blue-600 hover:text-blue-700 dark:text-blue-400 dark:hover:text-blue-300 underline"
          >
            <ExternalLink className="h-3.5 w-3.5" />
            Open link
          </a>
        </div>
      )}

      {/* Response input */}
      <div className="space-y-2">
        {escalation.type === "CREDENTIAL" ? (
          <Input
            type="password"
            placeholder="Paste credential value..."
            aria-label="Credential value"
            value={resolution}
            onChange={(e) => setResolution(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault()
                handleResolve("approve")
              }
            }}
            disabled={submitting}
            className="font-mono text-sm"
          />
        ) : (
          <Textarea
            placeholder={escalation.type === "LINK" ? "Confirm completion..." : "Type your response..."}
            aria-label="Resolution response"
            value={resolution}
            onChange={(e) => setResolution(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault()
                handleResolve("approve")
              }
            }}
            disabled={submitting}
            rows={2}
            className="text-sm resize-none"
          />
        )}

        {/* Redirect agent selector */}
        {showRedirect && (
          <div className="flex items-center gap-2">
            <span className="text-label text-muted-foreground shrink-0">Redirect to:</span>
            <select
              value={redirectTo}
              onChange={(e) => setRedirectTo(e.target.value)}
              className="flex h-8 w-full rounded-md border border-input bg-background px-2 py-1 text-sm"
              disabled={submitting}
            >
              <option value="">Select agent...</option>
              {agents.map((agent) => (
                <option key={agent.slug} value={agent.slug}>
                  @{agent.slug} — {agent.name}
                </option>
              ))}
            </select>
          </div>
        )}

        {/* Action buttons */}
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            onClick={() => handleResolve("approve")}
            disabled={submitting || !resolution.trim()}
            className="bg-emerald-600 hover:bg-emerald-700 text-white"
          >
            <CheckCircle2 className="h-3.5 w-3.5 mr-1" />
            {submitting ? "Sending..." : "Approve"}
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={() => handleResolve("reject")}
            disabled={submitting || !resolution.trim()}
            className="border-red-300 text-red-700 hover:bg-red-50 dark:border-red-800 dark:text-red-400 dark:hover:bg-red-950/30"
          >
            <XCircle className="h-3.5 w-3.5 mr-1" />
            Reject
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={handleRedirectClick}
            disabled={submitting}
            className="border-blue-300 text-blue-700 hover:bg-blue-50 dark:border-blue-800 dark:text-blue-400 dark:hover:bg-blue-950/30"
          >
            <ArrowRightLeft className="h-3.5 w-3.5 mr-1" />
            Redirect
          </Button>
          {showRedirect && redirectTo && (
            <Button
              size="sm"
              onClick={() => handleResolve("redirect")}
              disabled={submitting || !resolution.trim() || !redirectTo}
              className="bg-blue-600 hover:bg-blue-700 text-white"
            >
              <Send className="h-3.5 w-3.5 mr-1" />
              Send redirect
            </Button>
          )}
        </div>
      </div>

      {error && (
        <p className="text-sm text-destructive">{error}</p>
      )}
    </div>
  )
}

const ACTION_BADGES: Record<string, { label: string; className: string; icon: React.ComponentType<{ className?: string }> }> = {
  approve: {
    label: "Approved",
    className: "bg-emerald-100 text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-300",
    icon: CheckCircle2,
  },
  reject: {
    label: "Rejected",
    className: "bg-red-100 text-red-800 dark:bg-red-900/40 dark:text-red-300",
    icon: XCircle,
  },
  redirect: {
    label: "Redirected",
    className: "bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-300",
    icon: ArrowRightLeft,
  },
}

export function ActionBadge({ action, redirectTo }: { action: string | null; redirectTo?: string | null }) {
  if (!action) return null
  const config = ACTION_BADGES[action]
  if (!config) return null
  const Icon = config.icon

  return (
    <Badge variant="outline" className={`gap-1 border-0 ${config.className}`}>
      <Icon className="h-3 w-3" />
      {config.label}
      {action === "redirect" && redirectTo && (
        <span className="ml-0.5">@{redirectTo}</span>
      )}
    </Badge>
  )
}
