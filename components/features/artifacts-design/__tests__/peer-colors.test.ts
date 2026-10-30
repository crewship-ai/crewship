import { describe, expect, it } from "vitest"
import { contrast, hexToRgb } from "@/lib/theme/derive-accent"
import { readablePeerFill } from "../peer-colors"

const readable = (color: string) => contrast(hexToRgb(color), hexToRgb("#ffffff"))

// Peer avatars and canvas cursor labels draw white text on the peer colour,
// which arrives from other clients through Yjs awareness.
describe("readablePeerFill", () => {
 it.each(["#2563eb", "#c026d3", "#059669", "#ea580c", "#9333ea"])("makes palette colour %s readable under white labels", color => {
  expect(readable(readablePeerFill(color))).toBeGreaterThanOrEqual(4.5)
 })
 it("keeps colours that already meet AA", () => {
  expect(readablePeerFill("#2563eb")).toBe("#2563eb")
  expect(readablePeerFill("#9333EA")).toBe("#9333ea")
 })
 it("darkens the earlier green and orange to the next step of the same hue", () => {
  expect(readablePeerFill("#059669")).toBe("#047857")
  expect(readablePeerFill("#ea580c")).toBe("#c2410c")
 })
 it.each(["#ffffff", "#ffff00", "#22d3ee", "#a3e635"])("darkens a light colour %s from another client until white text reads", color => {
  expect(readable(readablePeerFill(color))).toBeGreaterThanOrEqual(4.5)
 })
 it.each(["", "red", "#fff", "url(#x)", "#12345g"])("replaces a colour that is not #rrggbb (%s)", color => {
  expect(readablePeerFill(color)).toBe("#2563eb")
 })
})
