"use client"

import { useState } from "react"
import {
  Loader2,
  Clock,
  Globe,
  Copy,
  CheckCircle2,
  XCircle,
} from "lucide-react"
import { toast } from "sonner"
import { useAgentFetch } from "@/hooks/use-agent-fetch"

interface AgentScheduleInfo {
  schedule_cron: string | null
  schedule_prompt: string | null
  schedule_enabled: boolean
  schedule_last_run: string | null
  schedule_next_run: string | null
  webhook_secret: string | null
  crew_id: string | null
  slug: string
}

export interface TriggersTabProps {
  agentId: string
  workspaceId: string | null
}

export function TriggersTab({ agentId, workspaceId }: TriggersTabProps) {
  const [copied, setCopied] = useState(false)

  const { data: agent, loading } = useAgentFetch<AgentScheduleInfo>(
    async (signal) => {
      const r = await fetch(`/api/v1/agents/${agentId}?workspace_id=${workspaceId}`, { signal })
      if (!r.ok) throw new Error(`HTTP ${r.status}`)
      return r.json()
    },
    [agentId, workspaceId],
    { enabled: workspaceId !== null, logLabel: "TriggersTab" },
  )

  if (loading) return <div className="flex items-center justify-center h-full"><Loader2 className="h-5 w-5 animate-spin text-muted-foreground" /></div>
  if (!workspaceId) return <div className="p-4 text-label text-muted-foreground">Select a workspace to view triggers.</div>
  if (!agent) return <div className="p-4 text-label text-muted-foreground">Unable to load agent</div>

  const webhookUrl = agent.crew_id && agent.slug
    ? `/api/v1/webhooks/${agent.crew_id}/${agentId}/trigger`
    : null

  return (
    <div className="p-3 space-y-4 text-sm">
      {/* Cron Schedule */}
      <div className="space-y-2">
        <div className="flex items-center gap-1.5 text-label font-medium text-muted-foreground uppercase tracking-wider">
          <Clock className="h-3 w-3" />
          Schedule
        </div>
        {agent.schedule_cron ? (
          <div className="space-y-1.5">
            <div className="flex items-center gap-2">
              <code className="text-label bg-accent px-1.5 py-0.5 rounded font-mono">{agent.schedule_cron}</code>
              {agent.schedule_enabled ? (
                <span className="flex items-center gap-1 text-micro text-emerald-500"><CheckCircle2 className="h-3 w-3" /> Active</span>
              ) : (
                <span className="flex items-center gap-1 text-micro text-muted-foreground"><XCircle className="h-3 w-3" /> Disabled</span>
              )}
            </div>
            {agent.schedule_prompt && (
              <p className="text-label text-muted-foreground line-clamp-2">{agent.schedule_prompt}</p>
            )}
            {agent.schedule_next_run && (
              <p className="text-micro text-muted-foreground">
                Next run: {new Date(agent.schedule_next_run).toLocaleString()}
              </p>
            )}
            {agent.schedule_last_run && (
              <p className="text-micro text-muted-foreground">
                Last run: {new Date(agent.schedule_last_run).toLocaleString()}
              </p>
            )}
          </div>
        ) : (
          <p className="text-label text-muted-foreground">No schedule configured. Set one in Agent Settings &rarr; Schedule.</p>
        )}
      </div>

      {/* Webhook */}
      <div className="space-y-2">
        <div className="flex items-center gap-1.5 text-label font-medium text-muted-foreground uppercase tracking-wider">
          <Globe className="h-3 w-3" />
          Webhook
        </div>
        {webhookUrl ? (
          <div className="space-y-1.5">
            <div className="flex items-center gap-1">
              <code className="text-micro bg-accent px-1.5 py-0.5 rounded font-mono truncate flex-1">{webhookUrl}</code>
              <button
                type="button"
                aria-label={copied ? "Webhook URL copied" : "Copy webhook URL"}
                onClick={async () => {
                  // Clipboard write can reject if the page loses focus or
                  // the permission is denied — surface a toast instead of
                  // leaving an unhandled promise rejection.
                  try {
                    await navigator.clipboard.writeText(window.location.origin + webhookUrl)
                    setCopied(true)
                    setTimeout(() => setCopied(false), 2000)
                  } catch {
                    toast.error("Failed to copy webhook URL")
                  }
                }}
                className="p-1 rounded hover:bg-accent text-muted-foreground"
              >
                {copied ? <CheckCircle2 className="h-3 w-3 text-emerald-500" /> : <Copy className="h-3 w-3" />}
              </button>
            </div>
            <p className="text-micro text-muted-foreground">
              POST with JSON body. {agent.webhook_secret ? "Secret header required." : "No secret configured."}
            </p>
          </div>
        ) : (
          <p className="text-label text-muted-foreground">Assign agent to a crew to enable webhooks.</p>
        )}
      </div>
    </div>
  )
}
