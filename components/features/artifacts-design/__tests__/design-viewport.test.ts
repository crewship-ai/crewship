import { describe, expect, it } from "vitest"
import { clampScale, fitView, focusView, MAX_SCALE, MIN_SCALE, panBy, pinchOf, pinchView, pressIntent, toCanvas, toScreen, typingTarget, wheelGesture, zoomAround, type DesignView } from "../design-viewport"

const view: DesignView = { scale: 0.5, x: 100, y: 40 }

describe("Design viewport model", () => {
 it("maps canvas and screen points both ways", () => {
  expect(toScreen(view, { x: 200, y: 100 })).toEqual({ x: 200, y: 90 })
  expect(toCanvas(view, { x: 200, y: 90 })).toEqual({ x: 200, y: 100 })
 })

 it.each([[2], [0.1], [1.37]])("zooms to %s around the pointer: the canvas point under it stays put", scale => {
  const anchor = { x: 333, y: 217 }
  const before = toCanvas(view, anchor)
  const zoomed = zoomAround(view, anchor, scale)
  expect(zoomed.scale).toBe(scale)
  const after = toCanvas(zoomed, anchor)
  expect(after.x).toBeCloseTo(before.x, 9)
  expect(after.y).toBeCloseTo(before.y, 9)
 })

 it("clamps the scale", () => {
  expect(clampScale(100)).toBe(MAX_SCALE)
  expect(clampScale(0)).toBe(MIN_SCALE)
  expect(clampScale(Number.NaN)).toBe(1)
  expect(zoomAround(view, { x: 0, y: 0 }, 1000).scale).toBe(MAX_SCALE)
 })

 it("pans by screen pixels without touching the scale", () => {
  expect(panBy(view, -30, 12)).toEqual({ scale: 0.5, x: 70, y: 52 })
 })

 it("fits a target inside the room left by the toolbars, centred", () => {
  const fitted = fitView({ x: 64, y: 64, width: 1000, height: 500 }, { width: 1100, height: 700 }, { top: 80, bottom: 120, side: 48 })
  expect(fitted.scale).toBeCloseTo(1, 5)
  const topLeft = toScreen(fitted, { x: 64, y: 64 })
  expect(topLeft.y).toBeCloseTo(80)
  expect(topLeft.x).toBeCloseTo(50)
 })

 it("fits only the width on a phone and pins the target to the side padding", () => {
  const fitted = fitView({ x: 400, y: 64, width: 390, height: 844 }, { width: 402, height: 700 }, { top: 88, bottom: 0, side: 12, widthOnly: true })
  expect(fitted.scale).toBeCloseTo(378 / 390)
  expect(toScreen(fitted, { x: 400, y: 64 })).toEqual({ x: 12, y: 88 })
 })

 it("brings an artboard to the top-left at the current scale", () => {
  const focused = focusView(view, { x: 1600, y: 64, width: 390, height: 844 }, { top: 80, side: 48 })
  expect(focused.scale).toBe(view.scale)
  expect(toScreen(focused, { x: 1600, y: 64 })).toEqual({ x: 48, y: 80 })
 })

 it.each([
  [{ deltaX: 0, deltaY: 100, deltaMode: 0, ctrlKey: false, metaKey: false, shiftKey: false }, { kind: "pan", dx: -0, dy: -100 }],
  [{ deltaX: 12, deltaY: -4, deltaMode: 0, ctrlKey: false, metaKey: false, shiftKey: false }, { kind: "pan", dx: -12, dy: 4 }],
  [{ deltaX: 0, deltaY: 3, deltaMode: 1, ctrlKey: false, metaKey: false, shiftKey: false }, { kind: "pan", dx: -0, dy: -48 }],
  [{ deltaX: 0, deltaY: 100, deltaMode: 0, ctrlKey: false, metaKey: false, shiftKey: true }, { kind: "pan", dx: -100, dy: -0 }],
 ])("a plain wheel or a two-finger trackpad scroll pans (%o)", (input, gesture) => {
  expect(wheelGesture(input)).toEqual(gesture)
 })

 it("Ctrl or Cmd with the wheel, and a trackpad pinch, zoom: down zooms out, up zooms in, one notch is bounded", () => {
  const notchOut = wheelGesture({ deltaX: 0, deltaY: 100, deltaMode: 0, ctrlKey: true, metaKey: false, shiftKey: false })
  const notchIn = wheelGesture({ deltaX: 0, deltaY: -100, deltaMode: 0, ctrlKey: false, metaKey: true, shiftKey: false })
  const pinch = wheelGesture({ deltaX: 0, deltaY: -3, deltaMode: 0, ctrlKey: true, metaKey: false, shiftKey: false })
  expect(notchOut.kind).toBe("zoom")
  if (notchOut.kind !== "zoom" || notchIn.kind !== "zoom" || pinch.kind !== "zoom") throw new Error("expected zoom")
  expect(notchOut.factor).toBeCloseTo(Math.exp(-0.2))
  expect(notchIn.factor).toBeCloseTo(Math.exp(0.2))
  expect(pinch.factor).toBeGreaterThan(1)
  expect(pinch.factor).toBeLessThan(notchIn.factor)
 })

 it.each([
  // button, pointer, space, onNode, canMove → intent
  [2, "mouse", false, true, true, "pan"],
  [2, "mouse", false, false, true, "pan"],
  [1, "mouse", false, true, true, "pan"],
  [1, "mouse", false, false, false, "pan"],
  [0, "mouse", true, true, true, "pan"],
  [0, "mouse", true, false, true, "pan"],
  [0, "mouse", false, true, true, "move"],
  [0, "mouse", false, true, false, "select"],
  [0, "mouse", false, false, true, "deselect"],
  [0, "touch", false, false, true, "pan"],
  [0, "touch", false, true, true, "move"],
  [0, "touch", false, true, false, "pan"],
  [0, "pen", false, true, true, "move"],
  [5, "pen", false, true, true, "none"],
  [3, "mouse", false, false, true, "none"],
 ] as const)("button %s %s space=%s onNode=%s canMove=%s → %s", (button, pointerType, space, onNode, canMove, intent) => {
  expect(pressIntent({ button, pointerType, space, onNode, canMove })).toBe(intent)
 })

 it("pinches around the midpoint and follows it", () => {
  const from = pinchOf({ x: 100, y: 100 }, { x: 200, y: 100 })
  expect(from).toEqual({ center: { x: 150, y: 100 }, distance: 100 })
  const under = toCanvas(view, from.center)
  // Fingers spread to twice the distance, midpoint unchanged: zoom ×2 around it.
  const spread = pinchView(view, from, pinchOf({ x: 50, y: 100 }, { x: 250, y: 100 }))
  expect(spread.scale).toBeCloseTo(1)
  expect(toCanvas(spread, from.center).x).toBeCloseTo(under.x)
  // Same distance, midpoint moved: a pan.
  const moved = pinchView(view, from, pinchOf({ x: 130, y: 120 }, { x: 230, y: 120 }))
  expect(moved).toEqual({ scale: 0.5, x: 130, y: 60 })
 })

 it("leaves Space and shortcuts to text fields", () => {
  const input = document.createElement("input")
  const editable = document.createElement("div"); editable.setAttribute("contenteditable", "true")
  const inner = document.createElement("span"); editable.append(inner)
  expect(typingTarget(input)).toBe(true)
  expect(typingTarget(inner)).toBe(true)
  expect(typingTarget(document.createElement("div"))).toBe(false)
  expect(typingTarget(null)).toBe(false)
 })
})
