import { describe, it, expect, beforeEach, afterEach, vi } from "vitest"
import { fmtSize, fmtTime, getLang, isPreviewable } from "@/lib/file-format"

describe("fmtSize", () => {
  it("returns a placeholder for zero bytes", () => {
    expect(fmtSize(0)).toBe("—")
  })

  it("formats bytes without a decimal", () => {
    expect(fmtSize(512)).toBe("512 B")
  })

  it("formats values under 10 units with one decimal", () => {
    expect(fmtSize(1024)).toBe("1.0 KB")
    expect(fmtSize(1536)).toBe("1.5 KB")
    expect(fmtSize(5 * 1024 * 1024)).toBe("5.0 MB")
  })

  it("rounds values of 10+ units to whole numbers", () => {
    expect(fmtSize(10 * 1024)).toBe("10 KB")
    expect(fmtSize(123 * 1024 * 1024)).toBe("123 MB")
  })

  it("formats gigabytes", () => {
    expect(fmtSize(3 * 1024 ** 3)).toBe("3.0 GB")
  })
})

describe("fmtTime", () => {
  const NOW = new Date("2026-06-15T12:00:00Z")
  const MIN = 60_000
  const HOUR = 60 * MIN
  const DAY = 24 * HOUR
  const minus = (ms: number) => new Date(NOW.getTime() - ms).toISOString()

  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(NOW)
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it("returns 'Just now' under a minute", () => {
    expect(fmtTime(minus(30_000))).toBe("Just now")
  })

  it("formats minutes", () => {
    expect(fmtTime(minus(12 * MIN))).toBe("12m ago")
  })

  it("formats hours", () => {
    expect(fmtTime(minus(5 * HOUR))).toBe("5h ago")
  })

  it("returns 'Yesterday' for exactly one day", () => {
    expect(fmtTime(minus(26 * HOUR))).toBe("Yesterday")
  })

  it("formats days under a week", () => {
    expect(fmtTime(minus(4 * DAY))).toBe("4d ago")
  })

  it("falls back to a locale date at 7+ days", () => {
    const sevenDaysAgo = minus(8 * DAY)
    const out = fmtTime(sevenDaysAgo)
    expect(out).not.toContain("ago")
    expect(out).toBe(new Date(sevenDaysAgo).toLocaleDateString())
  })
})

describe("getLang", () => {
  it("maps known extensions to languages", () => {
    expect(getLang("main.ts")).toBe("typescript")
    expect(getLang("page.tsx")).toBe("tsx")
    expect(getLang("script.py")).toBe("python")
    expect(getLang("server.go")).toBe("go")
    expect(getLang("config.yml")).toBe("yaml")
    expect(getLang(".env")).toBe("bash")
  })

  it("is case-insensitive on the extension", () => {
    expect(getLang("FILE.TS")).toBe("typescript")
  })

  it("uses only the last extension segment", () => {
    expect(getLang("archive.spec.ts")).toBe("typescript")
  })

  it("falls back to text for unknown extensions and bare names", () => {
    expect(getLang("image.png")).toBe("text")
    expect(getLang("Makefile")).toBe("text")
  })
})

describe("isPreviewable", () => {
  it("accepts well-known filenames case-insensitively", () => {
    expect(isPreviewable("Dockerfile")).toBe(true)
    expect(isPreviewable("README")).toBe(true)
    expect(isPreviewable(".gitignore")).toBe(true)
    expect(isPreviewable("CMakeLists.txt")).toBe(true)
  })

  it("accepts files by extension", () => {
    expect(isPreviewable("main.go")).toBe(true)
    expect(isPreviewable("notes.MD")).toBe(true)
    expect(isPreviewable("schema.prisma")).toBe(true)
    expect(isPreviewable("infra.tf")).toBe(true)
  })

  it("rejects binary-ish extensions and unknown bare names", () => {
    expect(isPreviewable("photo.png")).toBe(false)
    expect(isPreviewable("archive.tar.gz")).toBe(false)
    expect(isPreviewable("crewship.db")).toBe(false)
    expect(isPreviewable("somebinary")).toBe(false)
  })
})
