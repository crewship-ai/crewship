"use client"

import { AlertTriangle } from "lucide-react"

/**
 * Why a second approver is required for THIS credential escalation.
 *
 * Lives on its own (#1574) because two surfaces render it: the crew escalations
 * panel, where the rule was first made visible (#1559), and the inbox, which
 * offers the same one-click Approve on the same escalation and was left saying
 * nothing — so an operator approving from there still met the 403 cold. One
 * component means the two cannot end up describing one rule differently.
 *
 * The rule has two independent sources and they are worth naming separately:
 * the workspace toggle is something an OWNER/ADMIN can turn off, and the
 * credential's tier is not — it can only tighten the toggle, never loosen it,
 * so "turn the setting off" is not a fix for the tier case.
 *
 * Every value here is the SERVER's read-time answer, computed against the live
 * governance row and the credential's current tier. This component never
 * re-derives it, and callers must not pass anything stored at raise time: both
 * inputs change afterwards, and a stale "no second approver needed" is exactly
 * the promise that turns into a refusal. It also decides the case where the
 * rule cannot be enforced at all — an agent with no recorded owner leaves
 * nothing to compare the approver against, and the server reports
 * required=false so the row claims nothing rather than threatening a 403 that
 * will not happen.
 */
export interface FourEyesFacts {
  /** The answer: does resolving this need someone other than the agent's owner. */
  required: boolean
  /** Because the workspace requires a second approver on every credential escalation. */
  byWorkspace: boolean
  /** Because the credential's tier forces it, whatever the workspace setting says. */
  byTier: boolean
  /** The credential's tier, e.g. "L4 · critical". Null when there is no credential. */
  securityLevelLabel: string | null
  /** Slug of the agent that raised it — the owner of this agent is who gets refused. */
  agentSlug?: string | null
}

export function FourEyesNotice({
  required,
  byWorkspace,
  byTier,
  securityLevelLabel,
  agentSlug,
}: FourEyesFacts) {
  if (!required) return null

  const tier = securityLevelLabel
  let why: string
  if (byTier && byWorkspace) {
    why = `This workspace requires a second approver, and ${tier ?? "this credential's"} credentials require one regardless of that setting.`
  } else if (byTier) {
    why = `This workspace's second-approver setting is off, but ${tier ?? "this credential's tier"} credentials require one anyway — a credential's tier can only tighten this rule, never loosen it.`
  } else {
    why = "This workspace requires a second approver on every credential escalation."
  }

  return (
    <div
      className="rounded-md border border-warn/30 bg-warn/5 dark:bg-warn/20 p-2.5 space-y-1"
      data-testid="escalation-four-eyes"
    >
      <div className="flex items-start gap-2">
        <AlertTriangle className="h-3.5 w-3.5 text-warn mt-0.5 shrink-0" />
        <span className="text-body font-medium">Needs a second approver</span>
      </div>
      <p className="text-body text-muted-foreground">
        Whoever owns {agentSlug ? `@${agentSlug}` : "the agent that raised this"} can&rsquo;t
        approve, reject or redirect this request — it is refused for them, whichever button
        they press. Someone else has to resolve it.
      </p>
      <p className="text-label text-muted-foreground">{why}</p>
    </div>
  )
}
