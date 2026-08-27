// The one place the "is a character still being composed?" question is
// answered, so four call sites cannot answer it four ways.
//
// Every branch here exists because some engine reports composition
// differently, and a guard that only reads `nativeEvent.isComposing` is
// wrong on the keystroke that ends a composition — always so on Safari
// 10.1-26, at the boundary everywhere else. That is the half-fix that looks
// like a fix, so each signal gets its own case.

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
    // The case that carries the whole helper. `isComposing` is false on the
    // keystroke that ENDS a composition — always on Safari 10.1-26, where
    // the keydown is dispatched after compositionend (webkit.org/b/165004,
    // fixed in 27), and at the composition boundary on every other engine
    // per MDN. 229 is the signal that survives, and without this arm the
    // guard is a no-op on exactly the keystroke a submit handler sees.
    expect(isImeComposing({ key: "Enter", keyCode: 229, isComposing: false })).toBe(true)
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
