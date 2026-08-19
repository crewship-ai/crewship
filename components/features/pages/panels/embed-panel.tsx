"use client"

import * as React from "react"
import { Frame } from "lucide-react"

import { cn } from "@/lib/utils"
import { defaultEmptyHint, panelGate, provenanceProducedAt } from "./freshness"
import {
  FailedValue,
  NeverProducedValue,
  PanelFrame,
  PanelValue,
  resolveNow,
} from "./panel-frame"
import type { EmbedPayload, PanelProps, PanelSnapshot } from "./types"

/**
 * `embed.v1` — the one escape hatch (PRD §3.1), and the only panel whose
 * contents this product does not draw.
 *
 * ## The sandbox argument, in full
 *
 * Three separate things have to be true before a frame appears, and this
 * component refuses rather than degrading when any of them is not.
 *
 * 1. **The URL was authored by a human, not by a producer.** The payload
 *    carries a `source` NAME; the server looks it up in an operator-vetted
 *    allow-list and puts the resolved URL on `data.embed.url`. This component
 *    never assembles a URL and never accepts one from `data.payload` — an
 *    iframe src is fetched by the reader's browser, from the reader's network,
 *    so a producer-settable URL is an exfiltration channel that no sandbox and
 *    no CSP closes (§8 rules 2 and 3, and `internal/pages/embed.go` for the
 *    argument at length).
 * 2. **The frame is cross-origin.** `sandbox` without `allow-same-origin`
 *    hands the framed document an opaque origin, so it has no cookies, no
 *    storage and no same-origin handle on us. That is the property, and it
 *    survives only while the URL is genuinely somebody else's. The server
 *    refuses an allow-list entry on our own origin; this component checks it
 *    AGAIN against `window.location.origin` before rendering, because a
 *    same-origin iframe is not a sandbox and the failure is silent.
 * 3. **Nobody anonymous is looking.** A public page (§7.3) framing a third
 *    party turns every anonymous reader's browser into a beacon for that third
 *    party — their IP, their timing, and the fact that this link exists. §7.3.2
 *    rule 5 strips provenance from a public page for a smaller version of the
 *    same reason. Embeds are refused there outright.
 *
 * The frame itself grants the minimum that renders anything: `allow-scripts`
 * and nothing else, mirrored from `EmbedSandbox` in `internal/pages/embed.go`.
 * `allow-same-origin` alongside it is the one combination that lets a framed
 * document reach its own frame element and remove the sandbox attribute, and
 * it is asserted absent by tests on both sides of the wire. `allow=""` denies
 * every delegated Permissions-Policy feature (camera, microphone, geolocation,
 * payment, …), `referrerpolicy="no-referrer"` keeps the page's own URL — which
 * carries an internal slug — out of the embedded host's logs, `credentialless`
 * keeps the reader's cookies for that host out of the frame, and there is no
 * `allowFullScreen`.
 *
 * Sizing is ours, not the payload's. §3.1 pins sizing as negotiated rather
 * than free, and a producer-supplied height is a panel that can cover the page
 * it sits in.
 */

/** What the panel decided to draw. Pure, so the rules are testable directly. */
export type EmbedGate =
  | { kind: "frame"; url: string; caption: string }
  | { kind: "refused"; reason: string }

/**
 * The one place the three conditions above are evaluated.
 *
 * `selfOrigin` is `window.location.origin`, passed in rather than read, so the
 * same-origin refusal has a test that does not have to move the browser.
 * An EMPTY `selfOrigin` is "we do not know where we are", and it refuses:
 * during static prerender there is no window, and shipping a frame into the
 * exported HTML without ever having checked its origin is the degradation this
 * whole component exists to avoid.
 */
export function embedGate(
  data: Pick<PanelSnapshot, "payload" | "embed">,
  selfOrigin: string,
  publicView: boolean,
): EmbedGate {
  if (publicView) {
    return {
      kind: "refused",
      reason:
        "Embedded panels are not shown on a public link — a frame would report every anonymous reader to the embedded site.",
    }
  }

  const payload = (data.payload ?? {}) as EmbedPayload
  const caption = typeof payload.caption === "string" ? payload.caption.trim() : ""
  const url = typeof data.embed?.url === "string" ? data.embed.url.trim() : ""

  if (!url) {
    return {
      kind: "refused",
      reason:
        "Embedded panels are not enabled on this instance. An operator declares the vetted destinations in CREWSHIP_PAGES_EMBED_SOURCES; until one is declared there is nothing this panel may point at.",
    }
  }

  let parsed: URL
  try {
    parsed = new URL(url)
  } catch {
    return { kind: "refused", reason: "The embedded destination is not a usable address." }
  }
  if (parsed.protocol !== "https:") {
    return { kind: "refused", reason: "An embedded destination must be served over HTTPS." }
  }
  if (!selfOrigin) {
    return { kind: "refused", reason: "Embedded panels render in the browser only." }
  }
  if (parsed.origin === selfOrigin) {
    // Never silently degrade to a same-origin iframe: a frame on our own
    // origin shares cookies and storage with the page that framed it, and
    // "sandboxed" would then be a word rather than a boundary.
    return {
      kind: "refused",
      reason:
        "The embedded destination is on this Crewship's own origin. An embed must be cross-origin, so this panel will not render it.",
    }
  }
  return { kind: "frame", url: parsed.toString(), caption }
}

/**
 * The sandbox token list, mirrored from `EmbedSandbox` in
 * `internal/pages/embed.go`. Exported so a test can assert what it does NOT
 * contain, which is the half that matters.
 */
export const EMBED_SANDBOX = "allow-scripts"

export function EmbedPanel({ panel, data, now, publicView = false, className }: PanelProps) {
  const clock = resolveNow(now)
  const gate = panelGate(data)
  const selfOrigin = typeof window === "undefined" ? "" : window.location.origin

  let body: React.ReactNode
  if (gate.kind === "failed") {
    body = (
      <FailedValue
        failure={data.failure}
        publicView={publicView}
        producedAt={provenanceProducedAt(data.provenance)}
        now={clock}
      />
    )
  } else if (gate.kind === "never") {
    body = <NeverProducedValue hint={data.emptyHint ?? defaultEmptyHint(panel)} />
  } else {
    const decision = embedGate(data, selfOrigin, publicView)
    if (decision.kind === "refused") {
      body = (
        <PanelValue basis="none" tone="muted" dimmed={gate.dimmed}>
          <span data-slot="panel-embed-refused" className="type-page-value">
            {decision.reason}
          </span>
        </PanelValue>
      )
    } else {
      body = (
        <div className={cn("flex flex-col gap-2", gate.dimmed && "opacity-60")}>
          {decision.caption ? (
            <p data-slot="panel-embed-caption" className="type-page-meta text-muted-foreground">
              {decision.caption}
            </p>
          ) : null}
          <div className="overflow-hidden rounded-md border bg-muted/30">
            <iframe
              data-slot="panel-embed-frame"
              src={decision.url}
              title={panel.title || `Embedded view · ${panel.id}`}
              // The security contract. Every one of these is a refusal, and
              // the doc comment above says which.
              sandbox={EMBED_SANDBOX}
              allow=""
              referrerPolicy="no-referrer"
              loading="lazy"
              {...({ credentialless: "" } as Record<string, string>)}
              className="aspect-video w-full border-0"
            />
          </div>
        </div>
      )
    }
  }

  return (
    <PanelFrame
      panel={panel}
      data={data}
      now={now}
      publicView={publicView}
      className={className}
      icon={Frame}
    >
      {body}
    </PanelFrame>
  )
}
