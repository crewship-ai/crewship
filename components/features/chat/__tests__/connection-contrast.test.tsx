import { describe, it, expect } from "vitest"
import { readFileSync } from "node:fs"
import path from "node:path"
import { render, screen } from "@testing-library/react"

import { ReconnectBanner } from "../messages/reconnect-banner"
import { ConnectionBadge } from "../messages/connection-badge"

// a11y.spec.ts "Chat page" (nightly 39130662018): when the chat socket is not
// connected, the Disconnected banner was text-warn on bg-warn/20 — 4.44:1 in
// light — and the header badge text-destructive on bg-destructive/30 — 3.98:1
// in dark. Both are small text and need 4.5:1. They use the chip pairs, whose
// colours are chosen for text in both schemes.

const globals = readFileSync(path.resolve(__dirname, "../../../../app/globals.css"), "utf8")
const lightBlock = globals.slice(globals.indexOf(":root {\n  /* ── Light surfaces"), globals.indexOf("\n.dark {"))
const darkBlock = globals.slice(globals.indexOf("\n.dark {"), globals.indexOf("@theme inline"))
function token(block: string, name: string): string {
  const m = new RegExp(`--${name}:\\s*(#[0-9A-Fa-f]{6})`).exec(block)
  if (!m) throw new Error(`--${name} not a hex colour`)
  return m[1]
}
function lum(hex: string) {
  const c = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255).map((x) => (x <= 0.04045 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4))
  return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2]
}
function contrast(a: string, b: string) {
  const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}
/** The chip pair a class list paints with, or null when it is not one. */
function chipPair(className: string): string | null {
  const bg = /(?:^|\s)bg-chip-([a-z]+)-bg(?:\s|$)/.exec(className)?.[1]
  const fg = /(?:^|\s)text-chip-([a-z]+)-fg(?:\s|$)/.exec(className)?.[1]
  return bg && bg === fg && !/\sdark:(?:bg|text)-/.test(className) ? bg : null
}
function expectReadable(el: HTMLElement) {
  const pair = chipPair(el.className)
  expect(pair, el.className).not.toBeNull()
  for (const block of [lightBlock, darkBlock]) {
    expect(contrast(token(block, `chip-${pair}-fg`), token(block, `chip-${pair}-bg`))).toBeGreaterThanOrEqual(4.5)
  }
}

describe("chat connection states are readable (WCAG 1.4.3)", () => {
  for (const status of ["connecting", "disconnected"]) {
    it(`the ${status} banner`, () => {
      render(<ReconnectBanner status={status} />)
      expectReadable(screen.getByRole("status"))
    })
    it(`the ${status} header badge`, () => {
      const { container } = render(<ConnectionBadge status={status} />)
      expectReadable(container.firstElementChild as HTMLElement)
    })
  }
})
