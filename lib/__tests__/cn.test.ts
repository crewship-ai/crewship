import { describe, it, expect } from "vitest"
import { cn } from "@/lib/utils/cn"

describe("cn", () => {
  it("merges basic classes", () => {
    const result = cn("foo", "bar")
    expect(result).toBe("foo bar")
  })

  it("deduplicates Tailwind classes (last wins)", () => {
    const result = cn("p-4", "p-2")
    expect(result).toBe("p-2")
  })

  it("merges conflicting Tailwind utilities", () => {
    const result = cn("text-red-500", "text-blue-500")
    expect(result).toBe("text-blue-500")
  })

  it("removes falsy values", () => {
    const result = cn("foo", false && "bar", null, undefined, "baz")
    expect(result).toBe("foo baz")
  })

  it("handles conditional classes", () => {
    const isActive = true
    const isDisabled = false
    const result = cn(
      "base",
      isActive && "active",
      isDisabled && "disabled"
    )
    expect(result).toBe("base active")
  })

  it("returns empty string for no input", () => {
    const result = cn()
    expect(result).toBe("")
  })

  it("returns empty string for only falsy input", () => {
    const result = cn(false, null, undefined)
    expect(result).toBe("")
  })

  it("handles array input", () => {
    const result = cn(["foo", "bar"])
    expect(result).toBe("foo bar")
  })

  it("merges complex Tailwind patterns", () => {
    const result = cn(
      "px-2 py-1 bg-red-500 hover:bg-red-600",
      "bg-blue-500"
    )
    expect(result).toContain("px-2")
    expect(result).toContain("py-1")
    expect(result).toContain("bg-blue-500")
    expect(result).not.toContain("bg-red-500")
    expect(result).toContain("hover:bg-red-600")
  })
})
