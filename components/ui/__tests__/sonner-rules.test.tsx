import { describe, it, expect, vi } from "vitest"
import { render } from "@testing-library/react"

// Harbor 2 feedback rules (#3366): at most three toasts, 4 s each.
const seen = vi.hoisted(() => ({ props: {} as Record<string, unknown> }))
vi.mock("sonner", () => ({ Toaster: (p: Record<string, unknown>) => { seen.props = p; return null } }))
vi.mock("next-themes", () => ({ useTheme: () => ({ resolvedTheme: "light" }) }))

import { Toaster, TOAST_DURATION_MS } from "../sonner"

describe("Toaster", () => {
  it("shows at most three toasts and keeps each for 4 s", () => {
    render(<Toaster />)
    expect(seen.props.visibleToasts).toBe(3)
    expect(seen.props.duration).toBe(TOAST_DURATION_MS)
    expect(TOAST_DURATION_MS).toBe(4000)
  })
})
