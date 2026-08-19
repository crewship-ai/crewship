/**
 * `embed.v1` — the escape hatch (PRD §3.1) and the only panel that renders
 * somebody else's HTML.
 *
 * Everything asserted here is a refusal or the shape of the one frame we do
 * draw, because for this panel those are the same subject. The rules it must
 * hold, in the order the component evaluates them:
 *
 *  1. Never a frame on a public page (§7.3) — an anonymous reader's browser
 *     would report itself to the embedded site.
 *  2. Never a frame without a server-resolved URL. The payload has no URL
 *     field at all (§8 rules 2, 3), so a client that invented one from the
 *     `source` name would be reopening the channel the schema closed.
 *  3. Never a SAME-ORIGIN frame. That is the silent degradation §3.1 exists to
 *     prevent: a frame on our own origin shares cookies and storage with the
 *     page that framed it, so "sandboxed" would be a word rather than a
 *     boundary.
 *  4. `allow-scripts` and nothing else. `allow-same-origin` beside it is the
 *     documented way for a framed document to reach its own frame element and
 *     delete the sandbox attribute. Mirrored from `EmbedSandbox` in
 *     `internal/pages/embed.go`.
 */
import { describe, it, expect, beforeAll } from "vitest"
import { render, screen } from "@testing-library/react"

import { EmbedPanel, embedGate, EMBED_SANDBOX } from "../embed-panel"
import { PanelRenderer, PANEL_REGISTRY } from "../registry"
import type { PanelSnapshot, PanelSpec } from "../types"

// happy-dom will genuinely try to FETCH an iframe's src when the element is
// connected. That is the one thing this panel's tests must not do: a unit test
// that reaches the network is slow when it works and flaky when it does not,
// and here it would be reaching a domain we invented. The attributes are the
// subject; the document behind them is not.
beforeAll(() => {
  const w = window as unknown as {
    happyDOM?: { settings: { disableIframePageLoading: boolean } }
  }
  if (w.happyDOM) w.happyDOM.settings.disableIframePageLoading = true
})

const NOW = new Date("2026-08-13T12:00:00Z")
const SELF = "https://crewship.example.com"

const panel: PanelSpec = { id: "grafana", schema: "embed.v1", title: "Fleet board", span: 6 }

function snapshot(over: Partial<PanelSnapshot> = {}): PanelSnapshot {
  return {
    state: "fresh",
    payload: { source: "grafana-fleet", caption: "eu-west" },
    provenance: { producer: "script/push-board.sh", produced_at: "2026-08-13T11:59:00Z" },
    embed: { url: "https://grafana.example.com/d/abc/fleet" },
    ...over,
  }
}

describe("embedGate — the three conditions before a frame exists (§3.1)", () => {
  it("draws the frame when the URL is server-resolved and cross-origin", () => {
    const gate = embedGate(snapshot(), SELF, false)
    expect(gate).toEqual({
      kind: "frame",
      url: "https://grafana.example.com/d/abc/fleet",
      caption: "eu-west",
    })
  })

  it("refuses on a public page, whatever the payload says (§7.3)", () => {
    const gate = embedGate(snapshot(), SELF, true)
    expect(gate.kind).toBe("refused")
    expect(gate.kind === "refused" && gate.reason).toMatch(/public link/i)
  })

  it("refuses when the server resolved nothing — the client never invents a URL", () => {
    for (const embed of [undefined, null, {}, { url: null }, { url: "  " }] as const) {
      const gate = embedGate(snapshot({ embed: embed as PanelSnapshot["embed"] }), SELF, false)
      expect(gate.kind).toBe("refused")
      expect(gate.kind === "refused" && gate.reason).toMatch(/not enabled on this instance/i)
    }
  })

  it("never resolves a URL out of the payload, however inviting the key", () => {
    // The payload type has no url/src/html field and the Go schema refuses
    // them, but a client that read one anyway would be the whole hole.
    const gate = embedGate(
      snapshot({
        embed: null,
        payload: {
          source: "grafana-fleet",
          url: "https://evil.example/steal",
          src: "https://evil.example/steal",
          html: "<script>x</script>",
        } as Record<string, unknown>,
      }),
      SELF,
      false,
    )
    expect(gate.kind).toBe("refused")
  })

  it("refuses a same-origin frame rather than degrading to one", () => {
    const gate = embedGate(
      snapshot({ embed: { url: "https://crewship.example.com/exposed/tok" } }),
      SELF,
      false,
    )
    expect(gate.kind).toBe("refused")
    expect(gate.kind === "refused" && gate.reason).toMatch(/own origin/i)
  })

  it("compares origins, not prefixes — a lookalike host is somebody else", () => {
    const gate = embedGate(
      snapshot({ embed: { url: "https://crewship.example.com.evil.test/x" } }),
      SELF,
      false,
    )
    expect(gate.kind).toBe("frame")
  })

  it("refuses http and anything that is not a URL", () => {
    expect(embedGate(snapshot({ embed: { url: "http://a.example/" } }), SELF, false).kind).toBe(
      "refused",
    )
    expect(embedGate(snapshot({ embed: { url: "javascript:alert(1)" } }), SELF, false).kind).toBe(
      "refused",
    )
    expect(embedGate(snapshot({ embed: { url: "/dashboards/1" } }), SELF, false).kind).toBe(
      "refused",
    )
  })

  it("refuses when the browser origin is unknown, so a prerender ships no frame", () => {
    const gate = embedGate(snapshot(), "", false)
    expect(gate.kind).toBe("refused")
  })
})

describe("the frame the panel actually renders", () => {
  function frameOf() {
    const { container } = render(<EmbedPanel panel={panel} data={snapshot()} now={NOW} />)
    const frame = container.querySelector('[data-slot="panel-embed-frame"]')
    if (!frame) throw new Error("no frame rendered")
    return frame as HTMLIFrameElement
  }

  it("grants allow-scripts and nothing else", () => {
    expect(EMBED_SANDBOX).toBe("allow-scripts")
    expect(frameOf().getAttribute("sandbox")).toBe("allow-scripts")
  })

  it("never grants allow-same-origin, top-navigation, forms, popups or downloads", () => {
    const sandbox = frameOf().getAttribute("sandbox") ?? ""
    for (const forbidden of [
      "allow-same-origin",
      "allow-top-navigation",
      "allow-forms",
      "allow-popups",
      "allow-modals",
      "allow-downloads",
      "allow-pointer-lock",
      "allow-presentation",
      "allow-storage-access-by-user-activation",
    ]) {
      expect(sandbox).not.toContain(forbidden)
    }
  })

  it("denies every delegated permission and leaks no referrer", () => {
    const frame = frameOf()
    expect(frame.getAttribute("allow")).toBe("")
    expect(frame.getAttribute("referrerpolicy")).toBe("no-referrer")
    expect(frame.hasAttribute("credentialless")).toBe(true)
    expect(frame.hasAttribute("allowfullscreen")).toBe(false)
  })

  it("takes no geometry from the payload", () => {
    const { container } = render(
      <EmbedPanel
        panel={panel}
        data={snapshot({
          payload: { source: "grafana-fleet", height: 9000, width: 9000 } as Record<
            string,
            unknown
          >,
        })}
        now={NOW}
      />,
    )
    const frame = container.querySelector('[data-slot="panel-embed-frame"]')
    expect(frame?.getAttribute("height")).toBeNull()
    expect(frame?.getAttribute("width")).toBeNull()
  })

  it("draws the caption as text beside the frame", () => {
    render(<EmbedPanel panel={panel} data={snapshot()} now={NOW} />)
    expect(screen.getByText("eu-west")).toBeInTheDocument()
  })
})

describe("the panel refuses in words, and the registry routes to it", () => {
  it("is the component embed.v1 resolves to", () => {
    expect(PANEL_REGISTRY["embed.v1"]).toBe(EmbedPanel)
  })

  it("says why, rather than showing an empty rectangle", () => {
    const { container } = render(
      <PanelRenderer panel={panel} data={snapshot({ embed: null })} now={NOW} />,
    )
    const refusal = container.querySelector('[data-slot="panel-embed-refused"]')
    expect(refusal?.textContent).toMatch(/CREWSHIP_PAGES_EMBED_SOURCES/)
    expect(container.querySelector("iframe")).toBeNull()
  })

  it("draws no frame on a public page", () => {
    const { container } = render(
      <PanelRenderer panel={panel} data={snapshot()} now={NOW} publicView />,
    )
    expect(container.querySelector("iframe")).toBeNull()
  })

  it("keeps the em dash for a panel nothing was ever pushed to (§9b.4)", () => {
    const { container } = render(
      <PanelRenderer
        panel={panel}
        data={{ state: "never_produced" }}
        now={NOW}
      />,
    )
    expect(container.querySelector("iframe")).toBeNull()
    expect(container.textContent).toContain("—")
  })
})
