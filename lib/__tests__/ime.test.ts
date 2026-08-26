// The one place the "is a character still being composed?" question is
// answered, so four call sites cannot answer it four ways.
//
// Every branch here exists because some browser reports composition
// differently, and a guard that only reads `nativeEvent.isComposing` is
// correct on Chrome and wrong on Safari — which is the half-fix that looks
// like a fix.

import { describe, it, expect } from "vitest"

import { isImeComposing } from "@/lib/ime"

describe("isImeComposing", () => {
  it("is false for an ordinary Enter", () => {
    expect(isImeComposing({ key: "Enter", nativeEvent: { isComposing: false } })).toBe(false)
  })

  it("is true when the native event says composition is in flight", () => {
    expect(isImeComposing({ key: "Enter", nativeEvent: { isComposing: true } })).toBe(true)
  })

  it("reads a raw DOM event that has no nativeEvent wrapper", () => {
    // useCapture handlers and non-React listeners hand over the DOM event
    // itself; the same helper has to answer for both shapes.
    expect(isImeComposing({ key: "Enter", isComposing: true })).toBe(true)
  })

  it("treats keyCode 229 as composition", () => {
    // Safari and older WebKit never set isComposing on keydown. 229 is the
    // legacy signal, and without it the guard is a no-op on those browsers.
    expect(isImeComposing({ key: "Enter", keyCode: 229 })).toBe(true)
  })

  it("treats key 'Process' as composition", () => {
    expect(isImeComposing({ key: "Process" })).toBe(true)
  })

  it("is false for an event carrying none of the signals", () => {
    expect(isImeComposing({ key: "Enter" })).toBe(false)
  })

  it("does not throw on an event with no nativeEvent at all", () => {
    expect(isImeComposing({})).toBe(false)
  })
})
