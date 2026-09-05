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
import { apiFetch } from "@/lib/api-fetch"
import { FourEyesNotice } from "./four-eyes-notice"
import { escalationSupplyCredential } from "@/lib/api/escalations"

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
    low: "bg-destructive",
    medium: "bg-warn",
    high: "bg-success",
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
          level === "low" ? "text-destructive dark:text-destructive" :
          level === "medium" ? "text-warn dark:text-warn" :
          "text-success dark:text-success"
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
  // The name for a free-text credential ask (#2376): the agent described
  // what it needs in prose, so the human names the credential they store.
  const [credentialName, setCredentialName] = useState("")
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [showRedirect, setShowRedirect] = useState(false)
  const [redirectTo, setRedirectTo] = useState("")
  const [agents, setAgents] = useState<{ slug: string; name: string }[]>([])
  const [agentsLoaded, setAgentsLoaded] = useState(false)

  const evidencePack = parseEvidencePack(escalation.metadata)
  const metadataUrl = parseMetadataUrl(escalation.metadata)
  // Three shapes of a CREDENTIAL escalation (#2376):
  //   - a PROPOSAL: the agent supplied a value, it sits PENDING_APPROVAL in
  //     the vault; Approve activates it, nothing to type.
  //   - an ASK: the agent staged a REQUESTED credential and needs a human to
  //     fill it; the masked input posts to /supply.
  //   - a legacy free-text ask: no staged row; the human names it and
  //     supplies it in one step.
  // /resolve never carries the value — the server refuses text on a
  // CREDENTIAL escalation — so approve/reject send none.
  const isCredential = escalation.type === "CREDENTIAL"
  const credentialStatus = escalation.credential_status ?? null
  const hasPendingCredential = isCredential && Boolean(escalation.credential_id) && credentialStatus !== "REQUESTED"
  const needsCredentialValue = isCredential && !hasPendingCredential
  const needsCredentialName = needsCredentialValue && !escalation.credential_id

  const handleSupply = async () => {
    if (!resolution) return
    if (needsCredentialName && !credentialName.trim()) return
    setSubmitting(true)
    setError(null)
    try {
      const res = await escalationSupplyCredential(escalation.id, resolution, workspaceId, {
        name: needsCredentialName ? credentialName.trim() : undefined,
      })
      if (!res.ok) {
        setError(res.error)
        return
      }
      setResolution("")
      setCredentialName("")
      onResolved()
    } finally {
      setSubmitting(false)
    }
  }

  const handleResolve = async (action: "approve" | "reject" | "redirect") => {
    if (isCredential && action === "approve" && needsCredentialValue) {
      await handleSupply()
      return
    }
    const needsResolution = !isCredential
    if (needsResolution && !resolution.trim()) return
    if (action === "redirect" && !redirectTo) return

    setSubmitting(true)
    setError(null)
    try {
      // workspace_id MUST be on the query string — RequireWorkspace reads it
      // from the URL, not the body (a body-only workspace_id is silently ignored
      // and the request 400s with "workspace_id is required").
      const res = await apiFetch(`/api/v1/escalations/${escalation.id}/resolve?workspace_id=${encodeURIComponent(workspaceId)}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          resolution: isCredential
            ? ""
            : resolution.trim() ||
              (action === "approve" ? "Approved" : action === "reject" ? "Rejected" : ""),
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
      const res = await apiFetch(`/api/v1/agents?crew_id=${crewId}&workspace_id=${workspaceId}`)
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
      {/* Ahead of the evidence: whether this person can resolve it at all
          decides whether the rest is worth reading. */}
      <FourEyesNotice
        required={escalation.second_approver_required}
        byWorkspace={escalation.second_approver_by_workspace}
        byTier={escalation.second_approver_by_tier}
        securityLevelLabel={escalation.security_level_label}
        agentSlug={escalation.from_slug}
      />

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
            <div className="rounded-md bg-destructive/5 dark:bg-destructive/30 border border-destructive/30 dark:border-destructive/50 p-2.5">
              <div className="flex items-start gap-2">
                <AlertTriangle className="h-3.5 w-3.5 text-destructive dark:text-destructive mt-0.5 shrink-0" />
                <span className="text-body text-destructive dark:text-destructive font-mono text-xs break-all">
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
            <div className="rounded-md bg-info/10 dark:bg-info/30 border border-info/20 dark:border-info/50 p-2.5">
              <span className="text-label font-medium text-info dark:text-info">
                Suggested: </span>
              <span className="text-body text-info dark:text-info">
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
            className="inline-flex items-center gap-1.5 text-sm text-primary hover:text-primary dark:text-primary dark:hover:text-primary underline"
          >
            <ExternalLink className="h-3.5 w-3.5" />
            Open link
          </a>
        </div>
      )}

      {/* Response input */}
      <div className="space-y-2">
        {isCredential && hasPendingCredential ? (
          <p className="text-xs text-muted-foreground" data-testid="escalation-credential-proposal">
            The agent proposed a value; it is in the vault awaiting your approval. Nothing to type.
          </p>
        ) : isCredential ? (
          <div className="space-y-2" data-testid="escalation-credential-ask">
            <p className="text-xs text-muted-foreground">
              The agent is asking for a credential it does not have. What you enter goes straight
              into the vault; the agent is granted it by name and never sees the value.
            </p>
            {needsCredentialName && (
              <Input
                placeholder="Credential name, e.g. PG_PASSWORD"
                aria-label="Credential name"
                value={credentialName}
                onChange={(e) => setCredentialName(e.target.value.toUpperCase())}
                disabled={submitting}
                className="font-mono text-sm"
              />
            )}
            <Input
              type="password"
              autoComplete="off"
              placeholder="Credential value (never shown to the agent)"
              aria-label="Credential value"
              value={resolution}
              onChange={(e) => setResolution(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.shiftKey) {
                  e.preventDefault()
                  handleSupply()
                }
              }}
              disabled={submitting}
              className="font-mono text-sm"
            />
          </div>
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
              aria-label="Redirect to agent"
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
            disabled={
              submitting ||
              (needsCredentialValue
                ? !resolution || (needsCredentialName && !credentialName.trim())
                : !hasPendingCredential && !resolution.trim())
            }
            className="bg-success hover:bg-success text-white"
          >
            <CheckCircle2 className="h-3.5 w-3.5 mr-1" />
            {submitting ? "Sending..." : needsCredentialValue ? "Supply" : "Approve"}
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={() => handleResolve("reject")}
            disabled={submitting || (!isCredential && !resolution.trim())}
            className="border-destructive/30 text-destructive hover:bg-destructive/5 dark:border-destructive/50 dark:text-destructive dark:hover:bg-destructive/30"
          >
            <XCircle className="h-3.5 w-3.5 mr-1" />
            Reject
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={handleRedirectClick}
            disabled={submitting}
            className="border-primary text-primary hover:bg-primary/10 dark:border-primary dark:text-primary dark:hover:bg-primary/30"
          >
            <ArrowRightLeft className="h-3.5 w-3.5 mr-1" />
            Redirect
          </Button>
          {showRedirect && redirectTo && (
            <Button
              size="sm"
              onClick={() => handleResolve("redirect")}
              disabled={submitting || !resolution.trim() || !redirectTo}
              className="bg-primary hover:bg-primary text-white"
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
    className: "bg-success/15 text-success dark:bg-success/40 dark:text-success",
    icon: CheckCircle2,
  },
  reject: {
    label: "Rejected",
    className: "bg-destructive/15 text-destructive dark:bg-destructive/40 dark:text-destructive",
    icon: XCircle,
  },
  redirect: {
    label: "Redirected",
    className: "bg-info/15 text-info dark:bg-info/40 dark:text-info",
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
