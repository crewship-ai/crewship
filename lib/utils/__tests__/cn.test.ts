import { describe, it, expect } from "vitest"

import { cn } from "../cn"

// =============================================================================
// tailwind-merge has to be told about our typography scale.
//
// globals.css defines text-micro/label/body/default/heading/title/display as
// font sizes. tailwind-merge only knows Tailwind's stock scale, so it filed
// every one of them under "text colour" — and then dropped them whenever an
// actual colour followed in the same cn() call:
//
//   cn("... text-label text-foreground ...")  ->  "... text-foreground ..."
//
// The class vanished from the DOM, the element fell back to the inherited
// 16px, and the surface silently rendered at the wrong size. That is what put
// a 16px value next to a 12px label on the agent config screen. 504 uses
// across 82 files were exposed to it.
// =============================================================================

const SCALE = ["micro", "label", "body", "default", "heading", "title", "display"]

describe("cn", () => {
  it.each(SCALE)("keeps text-%s when a colour follows it", (size) => {
    expect(cn(`text-${size} text-foreground`)).toBe(`text-${size} text-foreground`)
  })

  it.each(SCALE)("keeps text-%s when a semantic colour follows it", (size) => {
    expect(cn(`text-${size}`, "text-muted-foreground")).toBe(`text-${size} text-muted-foreground`)
  })

  it("still lets one scale size override another", () => {
    expect(cn("text-body", "text-micro")).toBe("text-micro")
    expect(cn("text-title text-label")).toBe("text-label")
  })

  it("still lets one colour override another", () => {
    expect(cn("text-foreground", "text-destructive")).toBe("text-destructive")
  })

  it("does not confuse the scale with Tailwind's own sizes", () => {
    expect(cn("text-sm", "text-body")).toBe("text-body")
    expect(cn("text-body", "text-sm")).toBe("text-sm")
  })

  it("leaves unrelated utilities alone", () => {
    expect(cn("px-2 text-label font-mono text-success")).toBe("px-2 text-label font-mono text-success")
  })
})
