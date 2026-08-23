"use client"

import { useEffect, useState } from "react"
import { Check, Globe, Pencil, RefreshCcw, ShieldOff, Sparkles } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { getModelLabel } from "@/lib/cli-adapters"
import type { OnboardingProposal } from "./setup-agent-api"

interface ProposalCardProps {
  proposal: OnboardingProposal
  /** Fired ONLY by the Create button's own onClick. Nothing in this
   *  component may call it from a mount effect, a prop-change effect, or
   *  anywhere else — PRD §5.6/§4.2 ("Nothing is written before Create") and
   *  the Vitest test that pins it, __tests__/proposal-card.test.tsx. */
  onCreate: () => void
  onEdit: () => void
  onAskDifferent: () => void
  /** True while the Create click's request is in flight. Disables all three
   *  buttons — Edit and Ask-for-something-else would otherwise let a user
   *  fire a second, contradictory request into the same conversation while
   *  the first proposal is still being applied. */
  creating?: boolean
  /** Set once the proposal has been applied — swaps the buttons for a
   *  confirmation and makes the card inert. */
  created?: boolean
  /** Surfaced when applyOnboardingProposal rejects. Rendered on the card
   *  itself so the failure sits next to the thing that failed, not as a
   *  toast the user has already looked away from. */
  error?: string | null
}

/**
 * The proposal card — the one place a setup-agent conversation can turn into
 * real objects.
 *
 * Every row here is concrete: a crew name, each agent's name/role/model, and
 * the egress domains the crew would get. Nothing is summarised into a count
 * or a sentence (PRD §4.2), because a summary is exactly the thing a
 * prompt-injected agent could lie in ("3 agents" while creating 30) — the
 * card has to show enough that a human reading it actually knows what they
 * are approving.
 */
export function ProposalCard({
  proposal,
  onCreate,
  onEdit,
  onAskDifferent,
  creating = false,
  created = false,
  error = null,
}: ProposalCardProps) {
  // Local double-click guard. `creating` is the source of truth once the
  // parent has re-rendered with it, but that round trip is not instant, and
  // a second physical click before it lands must not fire a second request.
  const [clicked, setClicked] = useState(false)
  const busy = creating || clicked

  // Release the local guard once the parent's own request has settled. Only
  // matters on failure — on success `created` replaces this whole button row,
  // so nothing would re-enable a stuck "busy" state otherwise (error set,
  // `creating` back to false, but a stale `clicked` would leave Create
  // permanently disabled).
  useEffect(() => {
    if (!creating) setClicked(false)
  }, [creating])

  return (
    <div
      data-testid="onboarding-proposal-card"
      className="rounded-2xl border border-primary/30 bg-primary/5 p-4 space-y-4"
    >
      <div className="flex items-center gap-2">
        <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary/15 border border-primary/30">
          <Sparkles className="h-4 w-4 text-primary" />
        </span>
        <div className="min-w-0">
          <div className="text-[10px] uppercase tracking-[0.14em] text-muted-foreground">Proposal</div>
          <div className="font-semibold tracking-tight truncate">{proposal.crewName}</div>
        </div>
      </div>

      {/* Agent rows — name, role, model. Concrete rows, never a paragraph. */}
      <div className="space-y-1.5">
        {proposal.agents.map((agent, i) => (
          <div
            key={`${agent.name}-${i}`}
            data-testid="onboarding-proposal-agent-row"
            className="flex items-center gap-2 rounded-lg border border-border bg-card px-3 py-2 text-sm"
          >
            <span className="font-medium flex-1 min-w-0 truncate">{agent.name}</span>
            <span className="text-xs text-muted-foreground shrink-0">{agent.role}</span>
            <span className="text-[11px] font-mono text-muted-foreground shrink-0 rounded bg-muted px-1.5 py-0.5">
              {getModelLabel(agent.model)}
            </span>
          </div>
        ))}
        {proposal.agents.length === 0 && (
          <div className="text-xs text-muted-foreground italic">No agents in this proposal yet.</div>
        )}
      </div>

      {/* Egress — PRD §4.2: "Network is on the card." Never implied, never
          folded into a sentence about what the crew "does". */}
      <div className="flex items-start gap-2 rounded-lg border border-border bg-card px-3 py-2 text-xs">
        {proposal.egressDomains.length > 0 ? (
          <>
            <Globe className="h-3.5 w-3.5 text-muted-foreground shrink-0 mt-0.5" />
            <div className="min-w-0">
              <div className="text-muted-foreground mb-1">Network access requested:</div>
              <div className="flex flex-wrap gap-1">
                {proposal.egressDomains.map((domain) => (
                  <span
                    key={domain}
                    data-testid="onboarding-proposal-domain"
                    className="rounded-full bg-muted px-2 py-0.5 font-mono text-[11px]"
                  >
                    {domain}
                  </span>
                ))}
              </div>
            </div>
          </>
        ) : (
          <>
            <ShieldOff className="h-3.5 w-3.5 text-muted-foreground shrink-0 mt-0.5" />
            <span className="text-muted-foreground">No external network access beyond the model provider.</span>
          </>
        )}
      </div>

      {error && (
        <div role="alert" className="rounded-lg border border-destructive/40 bg-destructive/10 p-2.5 text-xs text-destructive">
          {error}
        </div>
      )}

      {created ? (
        <div className="flex items-center gap-2 text-sm text-success">
          <Check className="h-4 w-4" />
          <span className="font-medium">Crew created.</span>
        </div>
      ) : (
        <div className="flex flex-wrap items-center gap-2 pt-1">
          <Button
            type="button"
            size="sm"
            disabled={busy}
            onClick={() => {
              // The ONLY call site of onCreate in this file. See the
              // component doc comment and the pinning test.
              setClicked(true)
              onCreate()
            }}
          >
            {busy ? <Spinner className="mr-1.5 h-3.5 w-3.5" /> : <Check className="mr-1.5 h-3.5 w-3.5" />}
            Create
          </Button>
          <Button type="button" variant="outline" size="sm" disabled={busy} onClick={onEdit}>
            <Pencil className="mr-1.5 h-3.5 w-3.5" />
            Edit
          </Button>
          <Button type="button" variant="ghost" size="sm" disabled={busy} onClick={onAskDifferent}>
            <RefreshCcw className="mr-1.5 h-3.5 w-3.5" />
            Ask for something else
          </Button>
        </div>
      )}
    </div>
  )
}
