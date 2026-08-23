"use client"

import { CheckCircle2, ShieldCheck, Sparkles, Star } from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { getModelLabel } from "@/lib/cli-adapters"
import type { OnboardingProposal } from "./setup-agent-api"

interface OnboardingProposalSummaryProps {
  proposal: OnboardingProposal
  created: boolean
}

/**
 * Compact read-only twin of the proposal card in the chat pane. The chat
 * remains the approval surface; this summary keeps the user's actual crew
 * visible next to the step controls instead of replacing it with one line of
 * prose as soon as the proposal is ready.
 */
export function OnboardingProposalSummary({ proposal, created }: OnboardingProposalSummaryProps) {
  return (
    <section
      aria-label={`Crew proposal: ${proposal.crewName}`}
      data-testid="onboarding-proposal-summary"
      className="overflow-hidden rounded-2xl border border-primary/25 bg-gradient-to-b from-primary/10 to-card shadow-lg"
    >
      <div className="flex items-center gap-3 border-b border-border/80 p-4">
        <span className="flex h-11 w-11 shrink-0 items-center justify-center rounded-xl border border-primary/30 bg-primary/15 shadow-sm shadow-primary/10">
          <Sparkles className="h-5 w-5 text-primary" />
        </span>
        <div className="min-w-0 flex-1">
          <div className="text-[10px] font-medium uppercase tracking-[0.16em] text-muted-foreground">
            {created ? "Your first crew" : "Proposed crew"}
          </div>
          <div className="truncate text-base font-semibold tracking-tight">{proposal.crewName}</div>
          <div className="text-xs text-muted-foreground">
            {proposal.agents.length} {proposal.agents.length === 1 ? "agent" : "agents"}
          </div>
        </div>
        <span
          className={`inline-flex shrink-0 items-center gap-1 rounded-full border px-2 py-1 text-[10px] font-medium ${
            created
              ? "border-success/30 bg-success/10 text-success"
              : "border-primary/25 bg-primary/10 text-primary"
          }`}
        >
          {created ? <CheckCircle2 className="h-3 w-3" /> : <Sparkles className="h-3 w-3" />}
          {created ? "Created" : "Ready to review"}
        </span>
      </div>

      <div className="space-y-2 p-3">
        {proposal.agents.map((agent, index) => {
          const seed = `${proposal.crewSlug || proposal.crewName}-${agent.name}-${index}`
          return (
            <div
              key={seed}
              data-testid="onboarding-proposal-summary-agent"
              className="flex items-center gap-3 rounded-xl border border-border/80 bg-background/70 p-2.5"
            >
              <span className="relative shrink-0">
                <AgentAvatar
                  seed={seed}
                  alt={agent.name}
                  className="h-9 w-9 rounded-lg bg-muted ring-1 ring-border"
                />
                {index === 0 && (
                  <span
                    title="Lead agent"
                    className="absolute -bottom-1 -right-1 flex h-4 w-4 items-center justify-center rounded-full bg-warn text-black shadow-sm"
                  >
                    <Star className="h-2.5 w-2.5 fill-current" />
                  </span>
                )}
              </span>
              <span className="min-w-0 flex-1">
                <span className="block truncate text-sm font-medium">{agent.name}</span>
                <span className="block truncate text-[11px] text-muted-foreground">{agent.role}</span>
              </span>
              <span className="max-w-[42%] truncate rounded-md bg-muted px-2 py-1 font-mono text-[10px] text-muted-foreground">
                {getModelLabel(agent.model)}
              </span>
            </div>
          )
        })}
      </div>

      <div className="flex items-center gap-2 border-t border-border/80 px-4 py-3 text-[11px] text-muted-foreground">
        <ShieldCheck className="h-3.5 w-3.5 shrink-0 text-success" />
        {proposal.egressDomains.length > 0
          ? `Network: ${proposal.egressDomains.join(", ")}`
          : "Restricted network · model provider only"}
      </div>
    </section>
  )
}
