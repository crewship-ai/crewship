import { describe, it, expect, vi, beforeEach } from "vitest"
import { readFileSync, readdirSync, statSync } from "node:fs"
import path from "node:path"
import type { ReactElement } from "react"

// Harbor 2 feedback rules (PRD KIT-04). components/ui/sonner.tsx limits the
// stack to three toasts and makes success/info leave after 4 s. An error must
// stay until the person closes it, so `toast.error` from lib/toast defaults
// to `duration: Infinity`. Everything else reaches sonner unchanged.

const sonner = vi.hoisted(() => {
  const fn = vi.fn((..._args: unknown[]) => "plain-id")
  return Object.assign(fn, {
    success: vi.fn((..._args: unknown[]) => "success-id"),
    info: vi.fn((..._args: unknown[]) => "info-id"),
    warning: vi.fn((..._args: unknown[]) => "warning-id"),
    error: vi.fn((..._args: unknown[]) => "error-id"),
    custom: vi.fn((..._args: unknown[]) => "custom-id"),
    message: vi.fn((..._args: unknown[]) => "message-id"),
    promise: vi.fn((..._args: unknown[]) => "promise-id"),
    dismiss: vi.fn((..._args: unknown[]) => "dismiss-id"),
    loading: vi.fn((..._args: unknown[]) => "loading-id"),
    getHistory: vi.fn(() => []),
    getToasts: vi.fn(() => []),
  })
})

vi.mock("sonner", () => ({ toast: sonner }))

import { toast } from "@/lib/toast"

beforeEach(() => vi.clearAllMocks())

describe("toast.error", () => {
  it("stays until the person closes it", () => {
    expect(toast.error("Could not save")).toBe("error-id")
    expect(sonner.error).toHaveBeenCalledWith("Could not save", { duration: Infinity })
  })

  it("keeps the caller's other options", () => {
    toast.error("Could not save", { id: "save", description: "Try again" })
    expect(sonner.error).toHaveBeenCalledWith("Could not save", {
      duration: Infinity,
      id: "save",
      description: "Try again",
    })
  })

  it("lets the caller's own duration win", () => {
    toast.error("Copy failed", { duration: 6000 })
    expect(sonner.error).toHaveBeenCalledWith("Copy failed", { duration: 6000 })
  })
})

describe("every other kind passes through unchanged", () => {
  const data = { id: "x", description: "d" }

  it.each(["success", "info", "warning", "message", "loading"] as const)("toast.%s", (kind) => {
    expect(toast[kind]("Hello", data)).toBe(`${kind}-id`)
    expect(sonner[kind]).toHaveBeenCalledWith("Hello", data)
    expect(sonner[kind].mock.calls[0][1]).toBe(data)
  })

  it("toast()", () => {
    expect(toast("Hello", data)).toBe("plain-id")
    expect(sonner).toHaveBeenCalledWith("Hello", data)
  })

  it("toast.success without options adds none", () => {
    toast.success("Saved")
    expect(sonner.success).toHaveBeenCalledWith("Saved")
  })

  it("toast.custom, toast.promise, toast.dismiss and the history readers", () => {
    const jsx = () => null as unknown as ReactElement
    toast.custom(jsx, data)
    expect(sonner.custom).toHaveBeenCalledWith(jsx, data)

    const p = Promise.resolve(1)
    const opts = { loading: "…", success: "done", error: "failed" }
    toast.promise(p, opts)
    expect(sonner.promise).toHaveBeenCalledWith(p, opts)

    toast.dismiss("x")
    expect(sonner.dismiss).toHaveBeenCalledWith("x")
    toast.getHistory()
    toast.getToasts()
    expect(sonner.getHistory).toHaveBeenCalled()
    expect(sonner.getToasts).toHaveBeenCalled()
  })
})

// Guard: the rule only holds while every toast goes through lib/toast.
const ROOT = path.resolve(__dirname, "../..")
const DIRS = ["app", "components", "hooks", "lib"]
const ALLOWED = new Set(["lib/toast.ts", "components/ui/sonner.tsx"])
// Chat and Pages files whose tests pin the exact `toast.error` arguments.
// Their teams own both the code and the tests; each switches its import to
// "@/lib/toast" together with its assertions. This list may only shrink.
const PENDING = new Set([
  "components/features/chat/chat-panel.tsx",
  "components/features/chat/hooks/use-file-editor.ts",
  "components/features/chat/hooks/use-message-submit.ts",
  "components/features/pages/page-avatar-popover.tsx",
  "components/features/pages/pages-layout.tsx",
])
const SONNER_IMPORT = /(?:\bfrom\s*|\bimport\s*\(\s*|\brequire\s*\(\s*|^\s*import\s+)["']sonner["']/m

function sources(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    if (name === "node_modules" || name === "__tests__" || name === "generated" || name.startsWith(".")) continue
    const p = path.join(dir, name)
    if (statSync(p).isDirectory()) sources(p, out)
    else if (/\.(tsx?|jsx?|mjs)$/.test(name) && !/\.(test|spec)\.[tj]sx?$/.test(name)) out.push(p)
  }
  return out
}

describe("toast import guard", () => {
  it('no app code imports "sonner" directly', () => {
    const offenders = DIRS.flatMap((d) => sources(path.join(ROOT, d)))
      .map((f) => path.relative(ROOT, f).split(path.sep).join("/"))
      .filter((rel) => !ALLOWED.has(rel) && !PENDING.has(rel))
      .filter((rel) => SONNER_IMPORT.test(readFileSync(path.join(ROOT, rel), "utf8")))
    expect(offenders, 'import { toast } from "@/lib/toast" instead of "sonner"').toEqual([])
  })

  it("the pending list names only files that still import sonner", () => {
    const stale = [...PENDING].filter((rel) => {
      try {
        return !SONNER_IMPORT.test(readFileSync(path.join(ROOT, rel), "utf8"))
      } catch {
        return true
      }
    })
    expect(stale, "remove these from PENDING").toEqual([])
  })
})
