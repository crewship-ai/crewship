import * as React from "react"
import { readFileSync } from "node:fs"
import path from "node:path"
import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it } from "vitest"
import { AlertTriangle } from "lucide-react"

import * as Banners from "../app-banners"

// COM-03 (#3481, G09 / KIT-04): the banner had warn / ok / info only, so the
// Inbox's partial-source banner composed the chip-danger tokens inline. The
// kit banner gets a `bad` tone, and an in-page form without the app slot's
// inset, so a page uses the same look for its own state.
type Tone = "warn" | "ok" | "info" | "bad"
type BannerProps = { tone: Tone; icon: typeof AlertTriangle; children: React.ReactNode; action?: React.ReactNode }
const AppBanner = Banners.AppBanner as unknown as React.ComponentType<BannerProps>
function PageBanner(props: BannerProps) {
  const C = (Banners as unknown as { PageBanner?: React.ComponentType<BannerProps> }).PageBanner
  if (!C) throw new Error("PageBanner is not exported")
  return <C {...props} />
}

const css = readFileSync(path.resolve(__dirname, "../../../app/globals.css"), "utf8")

afterEach(cleanup)

describe("banner tones", () => {
  it("has a bad tone drawn from the danger chip tokens", () => {
    expect(css).toMatch(/\.page-banner-bad\s*\{\s*background:\s*var\(--chip-danger-bg\);?\s*\}/)
    expect(css).toMatch(/\.page-banner-bad\s*>\s*svg\s*\{\s*color:\s*var\(--chip-danger-fg\);?\s*\}/)
  })

  it("the app banner takes it", () => {
    render(<AppBanner tone="bad" icon={AlertTriangle}>Two sources failed to load.</AppBanner>)
    const banner = screen.getByRole("status")
    expect(banner).toHaveAttribute("data-tone", "bad")
    expect(banner).toHaveClass("page-banner", "page-banner-bad")
  })

  it("a page banner has the same look without the app slot, and is an alert when bad", () => {
    const { container } = render(
      <PageBanner tone="bad" icon={AlertTriangle} action={<button type="button">Retry</button>}>
        <b>Two sources failed.</b> The list may be incomplete.
      </PageBanner>,
    )
    const banner = screen.getByRole("alert")
    expect(banner).toHaveClass("page-banner", "page-banner-bad")
    expect(container.querySelector(".app-banner-slot")).toBeNull()
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument()
  })

  it("a page banner in a quiet tone is a status", () => {
    render(<PageBanner tone="info" icon={AlertTriangle}>Synced from the CLI.</PageBanner>)
    expect(screen.getByRole("status")).toHaveAttribute("data-tone", "info")
  })
})
