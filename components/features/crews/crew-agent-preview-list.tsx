"use client"

import { useState } from "react"
import { Bot, ChevronDown } from "lucide-react"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { agentRoleLabel, isLeadRole } from "@/lib/agent-role"

interface PreviewAgent {
  name: string
  slug: string
  role_title: string
  /** Unvalidated: a template row or an LLM suggestion, normalised at render. */
  agent_role: string
  system_prompt: string
}

function AgentRow({ agent }: { agent: PreviewAgent }) {
  const [open, setOpen] = useState(false)
  const panelId = `agent-preview-${agent.slug}`
  return (
    <div className="rounded-lg border border-border overflow-hidden">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        aria-controls={panelId}
        className="w-full flex items-center gap-3 p-3 text-left hover:bg-accent transition-colors"
      >
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <span className="font-medium text-sm">{agent.name}</span>
            {/* Never the raw token: this list previews a lineup the wizard is
              * about to create, and the LLM behind /crew-ai-suggest can name a
              * role the create endpoint refuses. An unrecognised role presents
              * as an ordinary agent — the one thing the form can deliver for
              * that row (#2197). */}
            <Badge variant={isLeadRole(agent.agent_role) ? "default" : "secondary"} className="text-xs">
              {agentRoleLabel(agent.agent_role)}
            </Badge>
            <span className="text-xs text-muted-foreground">{agent.role_title}</span>
          </div>
        </div>
        <ChevronDown className={`h-3.5 w-3.5 text-muted-foreground transition-transform ${open ? "rotate-180" : ""}`} />
      </button>
      {open && (
        <div id={panelId} className="px-3 pb-3 border-t border-border bg-muted/30">
          <p className="text-xs text-muted-foreground mt-2 whitespace-pre-wrap leading-relaxed">{agent.system_prompt}</p>
        </div>
      )}
    </div>
  )
}

interface CrewAgentPreviewListProps {
  agents: PreviewAgent[]
}

export function CrewAgentPreviewList({ agents }: CrewAgentPreviewListProps) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base flex items-center gap-2">
          <Bot className="h-4 w-4" />
          Agents ({agents.length})
          <span className="text-xs font-normal text-muted-foreground ml-1">— click to preview system prompt</span>
        </CardTitle>
      </CardHeader>
      <CardContent>
        <div className="space-y-2">
          {agents.map((a) => (
            <AgentRow key={a.slug} agent={a} />
          ))}
        </div>
      </CardContent>
    </Card>
  )
}
