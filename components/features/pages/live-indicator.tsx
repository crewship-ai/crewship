"use client"

/**
 * "Is this page still receiving?" — one indicator, in the page header.
 *
 * It is bound to the SUBSCRIPTION, never to a timer. A dot driven by a
 * `setInterval` is lit on a page whose socket died ten minutes ago, which is
 * the exact claim §4 exists to stop us making; this one reads the socket's own
 * status plus whether `page:{pageId}` has been registered as a channel
 * (`hooks/use-pages.ts` — `usePagesRealtime`), and says nothing else.
 *
 * The honesty clause is the reason the not-lit copy is long. `use-pages.ts`
 * records in as many words that this surface has NO poll backstop: when the
 * socket is down nothing refetches, the panels keep drawing the last payload
 * they were given, and their own freshness verdict was computed by the server
 * at the moment of that read — so it ages silently. "Not live" therefore has
 * to say that the numbers may be older than they look, because no other
 * element on the page will.
 *
 * No animation lives here on purpose. A permanently pulsing dot looks the same
 * whether data arrived a second ago or never, and is invisible within a day;
 * "data just arrived" is said by the panel that received it flashing once
 * (`page-view.tsx`), and this element only answers "is the pipe open".
 */

import type { WSStatus } from "@/hooks/use-websocket"
import { cn } from "@/lib/utils"

/**
 * What the header can honestly claim about this page's liveness.
 *
 *  · `live`       — socket connected AND this page's channel registered.
 *  · `connecting` — the pipe is not open *yet*: the socket is still dialling,
 *                   or it is up but the page id (and therefore the channel) is
 *                   not known yet because the first read is still in flight.
 *  · `offline`    — the socket is down, errored, or there is no realtime
 *                   provider at all. Nothing will arrive.
 */
export type PageLiveness = "live" | "connecting" | "offline"

/**
 * Pure, so the state machine is table-testable without a socket.
 *
 * `channel` is what `usePagesRealtime` registered — `page:{pageId}` or null.
 * A registered channel plus a connected socket is as close to "subscribed" as
 * this client can get: the provider sends `subscribe` on registration and
 * re-sends it for every active channel after a reconnect
 * (`hooks/use-realtime.tsx`), and the server does not acknowledge. Anything
 * short of both is reported as not-live rather than assumed to be fine.
 */
export function pageLiveness(
  status: WSStatus | null | undefined,
  channel: string | null | undefined,
): PageLiveness {
  if (status === "connected") return channel ? "live" : "connecting"
  if (status === "connecting") return "connecting"
  return "offline"
}

interface LivenessCopy {
  label: string
  /** The whole truth, for the title attribute and for a screen reader. */
  hint: string
  dot: string
  text: string
}

const COPY: Record<PageLiveness, LivenessCopy> = {
  live: {
    label: "Live",
    hint: "Subscribed to this page. Panels update as producers push, and one flashes when its data arrives.",
    dot: "bg-success",
    text: "text-muted-foreground",
  },
  connecting: {
    label: "Connecting",
    hint: "Not subscribed yet. Nothing is arriving on this page until the connection is up, so what you see may be older than it looks.",
    dot: "bg-warn",
    text: "text-muted-foreground",
  },
  offline: {
    label: "Not live",
    hint: "The live connection is down. This page has no polling fallback, so nothing will update until it returns — what you see may be older than it looks. Reload to re-read it.",
    dot: "bg-muted-foreground/40",
    text: "text-warn",
  },
}

/**
 * The dot has a border in the not-lit states as well as a fill, so "out" is
 * legible where a fill alone would not be: the difference between lit and out
 * is never carried by colour alone (the label says it too).
 */
export function LiveIndicator({
  liveness,
  className,
}: {
  liveness: PageLiveness
  className?: string
}) {
  const copy = COPY[liveness]
  return (
    <span
      role="status"
      data-slot="page-liveness"
      data-liveness={liveness}
      title={copy.hint}
      className={cn(
        "inline-flex shrink-0 items-center gap-1.5 text-[11px] tracking-wide",
        copy.text,
        className,
      )}
    >
      <span
        aria-hidden="true"
        data-slot="page-liveness-dot"
        className={cn(
          "h-1.5 w-1.5 rounded-full",
          copy.dot,
          liveness === "live" ? "ring-1 ring-success/40" : "ring-1 ring-border",
        )}
      />
      <span>{copy.label}</span>
      {/* The caveat reaches a screen reader, not only a mouse hovering the
          title attribute. */}
      <span className="sr-only">{copy.hint}</span>
    </span>
  )
}
