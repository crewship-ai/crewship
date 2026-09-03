"use client"

import { useEffect, useState } from "react"
import { Check, Globe, ShieldOff, Sparkles, Star, Wrench } from "lucide-react"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CrewIcon } from "@/components/ui/crew-icon"
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
  /** True while the Create click's request is in flight, which disables the
   *  button so a double-click cannot fire a second apply for the same
   *  proposal. Create is the only control on this card: an "Edit" and an
   *  "Ask for something else" button used to sit beside it, but neither
   *  wrote anything — one prefilled the composer with "Let's change: " and
   *  the other sent the fixed sentence "Let's try a different crew." Both
   *  were slower than simply typing the next message, which is what people
   *  did instead, so the card now carries exactly the one control that has
   *  an effect. */
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
      className="overflow-hidden rounded-2xl border border-primary/30 bg-gradient-to-b from-primary/10 to-card shadow-lg"
    >
      {/* Header: the crew's real look (the Guide picks icon + colour, the
          server validates them), its name and size. */}
      <div className="flex items-center gap-3 border-b border-border/80 p-4">
        {proposal.crewIcon ? (
          <CrewIcon icon={proposal.crewIcon} color={proposal.crewColor} size="lg" className="shrink-0 border border-border/60" />
        ) : (
          <span className="flex h-12 w-12 shrink-0 items-center justify-center rounded-xl border border-primary/30 bg-primary/15">
            <Sparkles className="h-5 w-5 text-primary" />
          </span>
        )}
        <div className="min-w-0 flex-1">
          <div className="text-[10px] font-medium uppercase tracking-[0.16em] text-muted-foreground">Proposal</div>
          <div className="truncate text-base font-semibold tracking-tight">{proposal.crewName}</div>
          {/* No agent count here: the rows below ARE the roster, and a count
              is exactly the summary the card must never substitute for them
              (proposal-card.test.tsx pins this). */}
        </div>
      </div>

      {/* Agent rows — name, role, model. Concrete rows, never a paragraph.
          The role gets its own line and may wrap: the previous single-line
          layout truncated "Recenzent PR" to "R" so that the role could take
          the width, and the model chip fell off the end. */}
      <div className="space-y-2 p-3">
        {proposal.agents.map((agent, i) => {
          const seed = `${proposal.crewSlug || proposal.crewName}-${agent.name}-${i}`
          return (
            <div
              key={`${agent.name}-${i}`}
              data-testid="onboarding-proposal-agent-row"
              className="flex items-start gap-3 rounded-xl border border-border/80 bg-background/70 p-3"
            >
              <span className="relative shrink-0">
                <AgentAvatar seed={seed} alt="" className="h-9 w-9 rounded-lg bg-muted ring-1 ring-border" />
                {i === 0 && (
                  <span
                    title="Lead agent"
                    className="absolute -bottom-1 -right-1 flex h-4 w-4 items-center justify-center rounded-full bg-warn text-black shadow-sm"
                  >
                    <Star className="h-2.5 w-2.5 fill-current" />
                  </span>
                )}
              </span>
              <span className="min-w-0 flex-1">
                <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
                  <span className="text-sm font-medium tracking-tight">{agent.name}</span>
                  <span className="rounded-md bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
                    {getModelLabel(agent.model)}
                  </span>
                </span>
                <span className="mt-0.5 block text-xs leading-relaxed text-muted-foreground">{agent.role}</span>
              </span>
            </div>
          )
        })}
        {proposal.agents.length === 0 && (
          <div className="text-xs text-muted-foreground italic">No agents in this proposal yet.</div>
        )}
      </div>

      <div className="space-y-2 px-3 pb-3">
        {/* Runtime tools, for the same reason egress is on the card: they change
            what the crew's container IS, and a build the person waits through is
            not something to discover afterwards. Rendered from the SERVER's
            resolved list, never the Guide's request, so the card cannot promise
            a tool that will not be installed. Absent entirely when the default
            container is enough, which is the common case. */}
        {(proposal.tools ?? []).length > 0 && (
          <div className="flex items-start gap-2 rounded-lg border border-border/80 bg-background/70 px-3 py-2 text-xs">
            <Wrench className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
            <div className="min-w-0">
              <div className="mb-1 text-muted-foreground">Extra tools in the container:</div>
              <div className="flex flex-wrap gap-1">
                {(proposal.tools ?? []).map((tool) => (
                  <span
                    key={tool}
                    data-testid="onboarding-proposal-tool"
                    className="rounded-full bg-muted px-2 py-0.5 font-mono text-[11px]"
                  >
                    {tool}
                  </span>
                ))}
              </div>
            </div>
          </div>
        )}

        {/* Egress — PRD §4.2: "Network is on the card." Never implied, never
            folded into a sentence about what the crew "does". */}
        <div className="flex items-start gap-2 rounded-lg border border-border/80 bg-background/70 px-3 py-2 text-xs">
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
      </div>

      {error && (
        <div role="alert" className="mx-3 mb-3 rounded-lg border border-destructive/40 bg-destructive/10 p-2.5 text-xs text-destructive">
          {error}
        </div>
      )}

      {created ? (
        <div className="flex items-center gap-2 border-t border-border/80 px-4 py-3 text-sm text-success">
          <Check className="h-4 w-4" />
          <span className="font-medium">Crew created.</span>
        </div>
      ) : (
        <div className="flex flex-wrap items-center justify-between gap-2 border-t border-border/80 px-4 py-3">
          <span className="text-[11px] text-muted-foreground">Nothing is created until you click Create.</span>
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
        </div>
      )}
    </div>
  )
}
