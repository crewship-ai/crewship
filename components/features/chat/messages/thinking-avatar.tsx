"use client"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { cn } from "@/lib/utils"

import type { ChatAgent } from "../chat-agent-context"

/**
 * The agent's face, doing double duty as the transcript's only spinner.
 *
 * While the agent is working the face breathes and a ring turns around it;
 * when the stream ends both stop. That is the whole loading affordance for a
 * reply — there is no second spinner, no shimmer bar, no "Morgan is typing"
 * row. The reason to spend the animation on the face rather than on a neutral
 * glyph is that the reader is never waiting on "the system", they are waiting
 * on a named agent, and in a crew of seven that distinction is the entire
 * point of the product.
 *
 * Motion lives in CSS (see `.thinking-ring` in globals.css) rather than in a
 * rAF loop: this renders once per streaming turn, and a 24px ring does not
 * justify what components/branding/animated-mark.tsx spends on a full-panel
 * canvas. `prefers-reduced-motion` holds the ring still and visible instead of
 * removing it — a reader who suppresses motion still needs to know the agent
 * has not finished.
 */
export function ThinkingAvatar({
  agent,
  active,
  className,
}: {
  agent: ChatAgent | null
  /** True while the reply (or its reasoning) is still streaming. */
  active: boolean
  className?: string
}) {
  if (!agent) {
    // No agent in context — the classic route, or a panel mounted without
    // one. Render the same box so the gutter keeps its width and the
    // transcript does not reflow when the skin is absent.
    return <div className={cn("h-8 w-8 shrink-0", className)} aria-hidden="true" />
  }

  return (
    <div
      className={cn("relative h-8 w-8 shrink-0", className)}
      data-testid="thinking-avatar"
      data-active={active ? "true" : "false"}
    >
      {active && <span aria-hidden="true" className="thinking-ring" />}
      <AgentAvatar
        seed={agent.avatarSeed || agent.id}
        style={agent.avatarStyle}
        agentId={agent.id}
        avatarUrl={agent.avatarUrl}
        alt=""
        className={cn(
          // h-full so the wrapper's size governs: the gutter mounts this at
          // 32px, the empty state at 48px, and neither should have to know
          // about the other.
          "relative h-full w-full rounded-[10px]",
          active && "thinking-face",
        )}
      />
    </div>
  )
}
