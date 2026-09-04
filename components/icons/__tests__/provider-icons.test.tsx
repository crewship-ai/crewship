import { describe, it, expect } from "vitest"
import { renderToStaticMarkup } from "react-dom/server"

import { ClaudeIcon, CursorIcon, OpenCodeIcon, AnthropicIcon } from "@/components/icons/provider-icons"
import { CLI_ADAPTERS } from "@/lib/cli-adapters"

// The onboarding toolchain picker is the first place a customer sees our
// vendor marks, and until this existed three of the six were approximations:
// a generic <Code/> glyph for OpenCode, a hand-drawn hexagon for Cursor, and
// the Anthropic corporate "A" standing in for Claude Code. Each assertion
// checks path data, because "an <svg> rendered" is what passed before.

const path = (el: React.ReactElement) => renderToStaticMarkup(el).match(/ d="([^"]+)"/)?.[1] ?? ""

describe("provider marks are the official Simple Icons paths", () => {
  it("Claude Code shows the Claude starburst, not the Anthropic wordmark", () => {
    const Icon = CLI_ADAPTERS.CLAUDE_CODE.icon
    expect(Icon).toBe(ClaudeIcon)
    expect(path(<Icon />)).toMatch(/^m4\.7144 15\.9555/)
    expect(path(<Icon />)).not.toBe(path(<AnthropicIcon />))
  })

  it("OpenCode is its own mark rather than a lucide placeholder", () => {
    expect(CLI_ADAPTERS.OPENCODE.icon).toBe(OpenCodeIcon)
    expect(path(<OpenCodeIcon />)).toBe("M22 24H2V0h20zM17 4.8H7v14.4h10z")
  })

  it("Cursor is the official mark", () => {
    expect(CLI_ADAPTERS.CURSOR_CLI.icon).toBe(CursorIcon)
    expect(path(<CursorIcon />)).toMatch(/^M11\.503\.131 1\.891 5\.678/)
  })

  it("every adapter icon accepts className and style like any SVG", () => {
    for (const cfg of Object.values(CLI_ADAPTERS)) {
      const Icon = cfg.icon
      const html = renderToStaticMarkup(<Icon className="h-4 w-4" style={{ color: "#fff" }} />)
      expect(html).toContain('class="h-4 w-4"')
      expect(html).toContain("color:#fff")
    }
  })
})
