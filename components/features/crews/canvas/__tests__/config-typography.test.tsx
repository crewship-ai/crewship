import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"

import {
  ConfigCards, ConfigPresets, ConfigReadOnly, ConfigSelect, ConfigSwitch, ConfigText,
} from "@/components/features/crews/canvas/config-field"

// =============================================================================
// One size for a label, one size for a value, one height for a control.
//
// The screen that prompted this had 12px labels, 11px hints and 16px values,
// because the size class was being dropped before it reached the DOM (see
// lib/utils/__tests__/cn.test.ts) and because a <select> and an <input> were
// left to size themselves. The result read as three unrelated typefaces in one
// card.
//
// These assert roles, not pixels. If the product's density changes it changes
// in globals.css, and these still pass.
// =============================================================================

const noop = () => {}

function Section() {
  return (
    <div>
      <ConfigText label="Name" value="Morgan" onSave={noop} />
      <ConfigText label="Slug" mono hint="Used in the CLI." value="morgan" onSave={noop} />
      <ConfigText label="Description" multiline value="" onSave={noop} />
      <ConfigSelect
        label="Role in crew" hint="A lead may assign work."
        value="LEAD" options={[{ value: "LEAD", label: "Lead" }]} onSave={noop}
      />
      <ConfigSwitch label="Memory between sessions" checked onSave={noop} />
      <ConfigPresets label="Longest run" value={900} presets={[{ value: 900, label: "15 m" }]} onSave={noop} />
      <ConfigReadOnly label="Next run" value="—" note="scheduled" />
      <ConfigCards
        value="CODING"
        options={[{ value: "CODING", title: "CODING", description: "Everyday work." }]}
        onSave={noop}
      />
    </div>
  )
}

describe("config field typography", () => {
  it("labels and values share one role", () => {
    const { container } = render(<Section />)

    const labels = [...container.querySelectorAll("label")]
    expect(labels.length).toBeGreaterThan(4)
    for (const el of labels) expect(el.className).toContain("type-row")

    const controls = [...container.querySelectorAll("input, select, textarea")]
    expect(controls.length).toBeGreaterThan(3)
    for (const el of controls) expect(el.className).toContain("type-row")
  })

  it("hints and secondary notes share the smaller role", () => {
    render(<Section />)
    expect(screen.getByText("Used in the CLI.").className).toContain("type-meta")
    expect(screen.getByText("scheduled").className).toContain("type-meta")
    expect(screen.getByText("Everyday work.").className).toContain("type-meta")
  })

  it("a dropdown is the same height as a text field", () => {
    const { container } = render(<Section />)
    const input = container.querySelector('input[type="text"]')!
    const select = container.querySelector("select")!
    const height = (el: Element) => el.className.match(/\bh-\d+(\.\d+)?\b/)?.[0]

    expect(height(input)).toBeDefined()
    expect(height(select)).toBe(height(input))
  })

  it("no control hardcodes a pixel size", () => {
    const { container } = render(<Section />)
    for (const el of container.querySelectorAll("*")) {
      expect(el.className.toString()).not.toMatch(/text-\[\d+px\]/)
    }
  })
})
