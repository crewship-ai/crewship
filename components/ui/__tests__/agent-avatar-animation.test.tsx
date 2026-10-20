import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, render, screen } from "@testing-library/react"

// #3789 — which AgentAvatar renders move. A voxel avatar animates at 32 px
// and above, and stays still below that, under Reduce motion: On, or with
// the viewer's "Animated avatars" switch off.

vi.mock("@/lib/agent-avatar", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/lib/agent-avatar")>()
  return {
    ...real,
    getAgentAvatarUrl: (seed: string, style?: string | null, opts?: { animated?: boolean }) =>
      `data:image/svg+xml;utf8,${seed}-${style ?? "default"}-${opts?.animated ? "animated" : "still"}`,
  }
})
vi.mock("@/lib/agent-avatar-persist", () => ({
  resolveStoredAvatarSrc: (url: string | null | undefined) => (url ? `stored:${url}` : null),
}))
vi.mock("@/hooks/use-avatar-styles", () => ({ useAvatarStylesVersion: () => 0 }))

import { AgentAvatar } from "../agent-avatar"
import { setAvatarAnimation, setMotion } from "@/lib/theme/appearance"

// happy-dom does no layout, so every element measures 0. The component reads
// the rendered box width; this stands in for the CSS size class.
let width = 0
const original = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetWidth")

let store: Map<string, string>
function stubStorage() {
  store = new Map()
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
  })
}
beforeEach(() => {
  stubStorage()
  setMotion("system")
  setAvatarAnimation("on")
  Object.defineProperty(HTMLElement.prototype, "offsetWidth", { configurable: true, get: () => width })
})

afterEach(() => {
  vi.unstubAllGlobals()
  if (original) Object.defineProperty(HTMLElement.prototype, "offsetWidth", original)
})

function src(): string {
  return screen.getByAltText("A").getAttribute("src") ?? ""
}

describe("AgentAvatar animation", () => {
  it("animates a voxel avatar at 32 px and above", () => {
    width = 32
    render(<AgentAvatar seed="a" style="voxel-bot" alt="A" />)
    expect(src()).toBe("data:image/svg+xml;utf8,a-voxel-bot-animated")
  })

  it("keeps a voxel avatar still below 32 px", () => {
    width = 24
    render(<AgentAvatar seed="a" style="voxel-bot" alt="A" />)
    expect(src()).toBe("data:image/svg+xml;utf8,a-voxel-bot-still")
  })

  it("animates the default (no style) because the default is Voxel Bot", () => {
    width = 56
    render(<AgentAvatar seed="a" alt="A" />)
    expect(src()).toBe("data:image/svg+xml;utf8,a-default-animated")
  })

  it("never animates a style that cannot move", () => {
    width = 96
    render(<AgentAvatar seed="a" style="bottts-neutral" alt="A" />)
    expect(src()).toBe("data:image/svg+xml;utf8,a-bottts-neutral-still")
  })

  it("stays still under the app's Reduce motion: On", () => {
    width = 96
    setMotion("on")
    render(<AgentAvatar seed="a" style="voxel-art" alt="A" />)
    expect(src()).toBe("data:image/svg+xml;utf8,a-voxel-art-still")
  })

  it("stays still with Animated avatars off, and follows the switch live", () => {
    width = 96
    setAvatarAnimation("off")
    render(<AgentAvatar seed="a" style="voxel-art" alt="A" />)
    expect(src()).toBe("data:image/svg+xml;utf8,a-voxel-art-still")
    act(() => setAvatarAnimation("on"))
    expect(src()).toBe("data:image/svg+xml;utf8,a-voxel-art-animated")
  })

  it("stays still when the OS asks for reduced motion and Reduce motion is System", () => {
    width = 96
    const mm = vi.spyOn(window, "matchMedia").mockImplementation((q: string) => ({
      matches: q.includes("reduce"), media: q, onchange: null,
      addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
    }) as MediaQueryList)
    render(<AgentAvatar seed="a" style="voxel-bot" alt="A" />)
    expect(src()).toBe("data:image/svg+xml;utf8,a-voxel-bot-still")
    mm.mockRestore()
  })

  it("an explicit still prop wins", () => {
    width = 96
    render(<AgentAvatar seed="a" style="voxel-bot" still alt="A" />)
    expect(src()).toBe("data:image/svg+xml;utf8,a-voxel-bot-still")
  })

  it("still prefers a stored render over generation", () => {
    width = 96
    render(<AgentAvatar seed="a" style="voxel-bot" avatarUrl="/api/v1/agents/x/avatar?v=1" alt="A" />)
    expect(src()).toBe("stored:/api/v1/agents/x/avatar?v=1")
  })
})
