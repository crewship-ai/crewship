"use client"

import { ShieldCheck, Sparkles } from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CrewIcon } from "@/components/ui/crew-icon"
import { StatusPill } from "@/components/ui/status-pill"
import { getModelLabel } from "@/lib/cli-adapters"
import type { OnboardingProposal } from "./setup-agent-api"

interface OnboardingProposalSummaryProps {
  proposal: OnboardingProposal
  created: boolean
}

/**
 * Compact read-only twin of the proposal card in the chat pane, shown under
 * "Built so far" (Harbor 2, #3409): crew, one row per agent with the lead
 * marked, model, network. The chat remains the approval surface — Create
 * stays on the proposal card — and this keeps the actual crew visible beside
 * the conversation instead of one line of prose.
 */
export function OnboardingProposalSummary({ proposal, created }: OnboardingProposalSummaryProps) {
  return (
    <section
      aria-label={`Crew proposal: ${proposal.crewName}`}
      data-testid="onboarding-proposal-summary"
      className="overflow-hidden rounded-lg border border-hairline bg-card"
    >
      <div className="flex items-center gap-2.5 border-b border-hairline px-3 py-2.5">
        {proposal.crewIcon ? (
          <CrewIcon icon={proposal.crewIcon} color={proposal.crewColor} size="sm" />
        ) : (
          <span className="flex size-7 shrink-0 items-center justify-center rounded-md bg-primary/15 text-primary-hover" aria-hidden>
            <Sparkles className="size-3.5" />
          </span>
        )}
        <div className="min-w-0 flex-1">
          <div className="text-micro font-semibold uppercase tracking-wider text-muted-foreground-soft">
            {created ? "Created crew" : "Proposed crew"}
          </div>
          <div className="truncate text-body font-semibold tracking-tight">{proposal.crewName}</div>
        </div>
        <StatusPill tone={created ? "success" : "blue"} label={created ? "Created" : "Ready to review"} />
      </div>

      <ul className="divide-y divide-hairline">
        {proposal.agents.map((agent, index) => {
          const seed = `${proposal.crewSlug || proposal.crewName}-${agent.name}-${index}`
          return (
            <li key={seed} data-testid="onboarding-proposal-summary-agent" className="flex items-center gap-2.5 px-3 py-2">
              <AgentAvatar seed={seed} alt={agent.name} className="size-7 shrink-0 rounded-md bg-muted" />
              <span className="min-w-0 flex-1">
                <span className="flex items-center gap-1.5">
                  <span className="truncate text-label font-medium text-foreground">{agent.name}</span>
                  {index === 0 && (
                    <span title="Lead agent" className="text-micro font-semibold uppercase text-muted-foreground-soft">
                      Lead
                    </span>
                  )}
                </span>
                <span className="block truncate text-micro text-muted-foreground">{agent.role}</span>
              </span>
              <span className="max-w-[42%] truncate font-mono text-micro text-muted-foreground">
                {getModelLabel(agent.model)}
              </span>
            </li>
          )
        })}
      </ul>

      <div className="flex items-center gap-2 border-t border-hairline bg-muted px-3 py-2 text-micro text-muted-foreground">
        <ShieldCheck className="size-3.5 shrink-0 text-success" aria-hidden />
        {proposal.egressDomains.length > 0
          ? `Network: ${proposal.egressDomains.join(", ")}`
          : "Restricted network · model provider only"}
      </div>
    </section>
  )
}
