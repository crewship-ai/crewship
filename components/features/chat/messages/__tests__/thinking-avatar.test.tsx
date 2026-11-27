import { readFileSync } from "node:fs"
import { join } from "node:path"
import { describe, expect, it, vi } from "vitest"
import { render } from "@testing-library/react"
import { ThinkingAvatar } from "../thinking-avatar"

vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: ({ className }: { className?: string }) => <span data-testid="face" className={className} /> }))

const agent = { id: "a1", name: "Jamie", avatarSeed: "a1" } as Parameters<typeof ThinkingAvatar>[0]["agent"]
const css = readFileSync(join(process.cwd(), "app/globals.css"), "utf8")
/** The body of the top-level rule for exactly `selector`. */
const rule = (source: string, selector: string) => {
  const match = new RegExp(`^${selector.replace(".", "\\.")} \\{([^}]*)\\}`, "m").exec(source)
  return match?.[1] ?? ""
}

describe("the agent's face is the one working indicator of a turn", () => {
  it("while the agent works the face carries a soft breathing glow as well as its ring", () => {
    const { container } = render(<ThinkingAvatar agent={agent} active />)
    expect(container.querySelector(".thinking-glow")).not.toBeNull()
    expect(container.querySelector(".thinking-ring")).not.toBeNull()
  })

  it("a finished turn's face holds still, with no glow and no ring", () => {
    const { container } = render(<ThinkingAvatar agent={agent} active={false} />)
    expect(container.querySelector(".thinking-glow, .thinking-ring, .thinking-face")).toBeNull()
  })

  it("the glow is drawn from theme tokens and turns calmly", () => {
    const glow = rule(css, ".thinking-glow")
    expect(glow).toMatch(/var\(--primary\)/)
    expect(glow).not.toMatch(/#[0-9a-f]{3,8}\b|rgb\(/i)
    const seconds = Number(/animation:[^;]*?([\d.]+)s/.exec(glow)?.[1])
    expect(seconds).toBeGreaterThanOrEqual(2)
    const ring = Number(/animation:[^;]*?([\d.]+)s/.exec(rule(css, ".thinking-ring"))?.[1])
    expect(ring).toBeGreaterThanOrEqual(2)
  })

  it("with reduced motion the glow, the ring and the face hold still but stay visible", () => {
    const reduced = css.slice(css.indexOf("@media (prefers-reduced-motion: reduce) {", css.indexOf(".thinking-face")))
    const block = reduced.slice(0, reduced.indexOf("}\n}") + 3)
    for (const name of [".thinking-ring", ".thinking-face", ".thinking-glow"]) expect(block).toContain(name)
    expect(block).toMatch(/animation:\s*none/)
    expect(block).not.toMatch(/display:\s*none|opacity:\s*0[;\s]/)
  })
})
