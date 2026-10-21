import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"

vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  usePathname: () => "/settings/hooks",
}))

import SettingsHooksPage from "@/app/(dashboard)/settings/hooks/page"

describe("/settings/hooks while lifecycle hooks are paused", () => {
  it("says the feature is paused and offers no hook controls", () => {
    render(<SettingsHooksPage />)
    expect(screen.getByRole("region", { name: "Lifecycle hooks are paused" })).toHaveTextContent(/do not run/)
    expect(screen.queryByRole("button", { name: /new hook/i })).toBeNull()
  })
})
