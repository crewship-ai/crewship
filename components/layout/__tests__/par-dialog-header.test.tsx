import { describe, expect, it } from "vitest"
import { render, screen } from "@testing-library/react"
import { readFileSync } from "node:fs"
import path from "node:path"

import { CreateSurface, CreateSurfaceBody, CreateSurfaceFooter, CreateSurfaceHeader } from "../create-surface"

// PAR-151: every dialog header is v67's — a 32 px icon tile, a 15 px bold
// title and a one-line subtitle, over a blurred scrim, with key chips in the
// footer. No breadcrumb.

const css = readFileSync(path.resolve(__dirname, "../../../app/globals.css"), "utf8")

function dialog(extra?: Partial<React.ComponentProps<typeof CreateSurfaceHeader>>) {
  return render(
    <CreateSurface open onOpenChange={() => {}}>
      <CreateSurfaceHeader
        concept="routines"
        context="Routines"
        title="New routine"
        description="Runs an agent on a schedule or a trigger"
        onClose={() => {}}
        {...extra}
      />
      <CreateSurfaceBody>body</CreateSurfaceBody>
      <CreateSurfaceFooter onCancel={() => {}} primaryLabel="Create" onPrimary={() => {}} />
    </CreateSurface>,
  )
}

describe("dialog header (PAR-151)", () => {
  it("has a 32 px icon tile", () => {
    dialog()
    const tile = document.querySelector(".icon-tile") as HTMLElement
    expect(tile).toHaveClass("h-8", "w-8")
  })

  it("sets the title at 15 px bold", () => {
    dialog()
    expect(screen.getByText("New routine")).toHaveClass("type-dialog-title")
    const i = css.indexOf(".type-dialog-title {")
    const rule = css.slice(i, css.indexOf("}", i))
    expect(rule).toMatch(/font-size:\s*(15px|0\.9375rem)/)
    expect(rule).toMatch(/font-weight:\s*(700|bold)/)
  })

  it("keeps the subtitle to one line", () => {
    dialog()
    expect(screen.getByText("Runs an agent on a schedule or a trigger")).toHaveClass("truncate")
  })

  it("draws no breadcrumb: a plain context is read by screen readers, not shown", () => {
    dialog()
    const ctx = screen.getByText("Routines")
    expect(ctx).toHaveClass("sr-only")
  })

  it("still shows an interactive context (New issue's crew picker)", () => {
    dialog({ keepContext: true, context: <button type="button">Pick crew</button> })
    expect(screen.getByRole("button", { name: "Pick crew" }).parentElement).not.toHaveClass("sr-only")
  })

  it("blurs the page behind the scrim", () => {
    dialog()
    expect(document.querySelector('[data-slot="dialog-overlay"]')?.className).toContain("backdrop-blur")
  })

  it("prints the footer keys as key chips", () => {
    dialog()
    const kbd = document.querySelector("kbd") as HTMLElement
    expect(kbd).toHaveClass("rounded", "border")
  })
})
