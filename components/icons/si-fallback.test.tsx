import { describe, it, expect } from "vitest"
import { render } from "@testing-library/react"
import { createElement } from "react"

import * as Fallback from "@/components/icons/si-fallback"
import { BRAND_REGISTRY } from "@/lib/credential-providers/registry"

// Guards the react-icons 5.7.0 migration: Simple Icons dropped Slack,
// OpenAI, Twilio, Heroku, SendGrid and Salesforce, so we vendor them in
// si-fallback. If a future bump (or an accidental revert to
// `react-icons/si`) drops one again, these assertions fail loudly
// instead of surfacing only as a `tsc` break in an unrelated PR.

const REMOVED_ICON_EXPORTS = [
  "SiSlack",
  "SiOpenai",
  "SiTwilio",
  "SiHeroku",
  "SiSendgrid",
  "SiSalesforce",
] as const

describe("si-fallback vendored icons", () => {
  it.each(REMOVED_ICON_EXPORTS)("%s renders an svg with non-empty path data", (name) => {
    const Icon = (Fallback as Record<string, React.ComponentType>)[name]
    expect(Icon, `${name} should be exported`).toBeTypeOf("function")

    const { container } = render(createElement(Icon, { className: "size-4" }))
    const svg = container.querySelector("svg")
    expect(svg).not.toBeNull()
    // className is forwarded so callers keep sizing/tinting via classes.
    expect(svg?.getAttribute("class")).toContain("size-4")
    const d = container.querySelector("path")?.getAttribute("d") ?? ""
    expect(d.length).toBeGreaterThan(0)
  })

  it("mirrors react-icons GenIcon props (size + title)", () => {
    const { container } = render(
      createElement(Fallback.SiSlack, { size: 32, title: "Slack" }),
    )
    const svg = container.querySelector("svg")
    expect(svg?.getAttribute("height")).toBe("32")
    expect(svg?.getAttribute("width")).toBe("32")
    expect(container.querySelector("title")?.textContent).toBe("Slack")
  })
})

describe("credential brand registry retains the removed brands", () => {
  it.each(["SLACK", "OPENAI", "TWILIO", "HEROKU", "SENDGRID", "SALESFORCE"])(
    "%s resolves to a renderable icon",
    (key) => {
      const entry = BRAND_REGISTRY.find((b) => b.key === key)
      expect(entry, `brand ${key} missing from registry`).toBeDefined()
      const { container } = render(
        createElement(entry!.Icon as React.ComponentType, {}),
      )
      expect(container.querySelector("svg")).not.toBeNull()
    },
  )
})
