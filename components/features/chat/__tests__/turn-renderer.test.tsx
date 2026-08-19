import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"
import type { ReactNode } from "react"
import type { ChatTurn, TurnPart } from "@/hooks/use-chat"

// AssistantTurn is a 493-line component with its own deep tree; the renderer
// simply delegates to it for the "assistant" role. Replace it with a probe so
// we can assert delegation without exercising its internals.
vi.mock("../assistant-turn", () => ({
  AssistantTurn: vi.fn(({ turn }: { turn: ChatTurn }) => (
    <div data-testid="assistant-turn-mock" data-turn-id={turn.id} />
  )),
}))

// Radix mounts HoverCardContent only while the card is open, and no pointer in
// this environment opens it. Render both halves inline: the provenance table
// and the per-server lists are the part of this card most likely to meet a
// shape the CLI changed, so they have to be assertable.
vi.mock("@/components/ui/hover-card", () => ({
  HoverCard: ({ children }: { children?: ReactNode }) => <div>{children}</div>,
  HoverCardTrigger: ({ children }: { children?: ReactNode }) => <>{children}</>,
  HoverCardContent: ({ children }: { children?: ReactNode }) => <div>{children}</div>,
}))

import { TurnRenderer } from "../turn-renderer"

function makePart(partial: Partial<TurnPart> = {}): TurnPart {
  return {
    id: partial.id ?? "p1",
    type: partial.type ?? "text",
    content: partial.content ?? "",
    metadata: partial.metadata,
    isStreaming: partial.isStreaming,
    timestamp: partial.timestamp ?? new Date("2026-04-17T12:00:00Z"),
  }
}

function userTurn(text: string): ChatTurn {
  return {
    id: "u1",
    role: "user",
    parts: [makePart({ type: "text", content: text })],
    isStreaming: false,
    timestamp: new Date("2026-04-17T12:34:00Z"),
  }
}

function systemTurn(
  type: TurnPart["type"],
  content = "",
  metadata?: Record<string, unknown>,
): ChatTurn {
  return {
    id: "s1",
    role: "system",
    parts: [makePart({ type, content, metadata })],
    isStreaming: false,
    timestamp: new Date(),
  }
}

function assistantTurn(id = "a1", isStreaming = false): ChatTurn {
  return {
    id,
    role: "assistant",
    parts: [makePart({ content: "hello" })],
    isStreaming,
    timestamp: new Date(),
  }
}

const noop = () => {}

describe("TurnRenderer", () => {
  describe("user role", () => {
    it("renders text content", () => {
      render(<TurnRenderer turn={userTurn("hello world")} onCopy={noop} onFileClick={noop} />)
      expect(screen.getByText("hello world")).toBeTruthy()
    })

    it("falls back to empty string when no text part is present", () => {
      const turn: ChatTurn = {
        ...userTurn("ignored"),
        parts: [makePart({ type: "thinking", content: "internal" })],
      }
      // Render must not crash; the user bubble is still emitted.
      const { container } = render(
        <TurnRenderer turn={turn} onCopy={noop} onFileClick={noop} />,
      )
      expect(container.textContent).not.toContain("internal")
    })

    // A message sent with an attachment carries the file's agent-visible path
    // in its own text (lib/attachment-message.ts). The transcript therefore
    // already shows the user exactly what went — as long as the bubble does
    // not collapse the line breaks the block is built from.
    const withAttachment =
      "have a look\n\n" +
      "I've attached a file to this message. The path is relative to your working directory:\n\n" +
      "- attachments/sess-1/invoice.pdf"

    it("shows the attachment paths of a sent message, line breaks intact", () => {
      const { container } = render(
        <TurnRenderer turn={userTurn(withAttachment)} onCopy={noop} onFileClick={noop} />,
      )
      expect(container.textContent).toContain("attachments/sess-1/invoice.pdf")
      const bubble = screen.getByText(/attachments\/sess-1\/invoice\.pdf/)
      expect(bubble.className).toContain("whitespace-pre-wrap")
    })

    it("keeps the line breaks in the editable bubble too", () => {
      render(
        <TurnRenderer
          turn={userTurn(withAttachment)}
          onCopy={noop}
          onFileClick={noop}
          onEditUserMessage={noop}
        />,
      )
      const bubble = screen.getByText(/attachments\/sess-1\/invoice\.pdf/)
      expect(bubble.className).toContain("whitespace-pre-wrap")
    })
  })

  describe("system role — system_init", () => {
    it("renders the Session started pill", () => {
      render(<TurnRenderer turn={systemTurn("system_init")} onCopy={noop} onFileClick={noop} />)
      expect(screen.getByText(/Session started/)).toBeTruthy()
    })

    it("shows the model chip when metadata.model is present", () => {
      render(
        <TurnRenderer
          turn={systemTurn("system_init", "", { model: "claude-opus-4-7" })}
          onCopy={noop}
          onFileClick={noop}
        />,
      )
      expect(screen.getByText("claude-opus-4-7")).toBeTruthy()
    })

    it("shows tool count when tools list is non-empty", () => {
      render(
        <TurnRenderer
          turn={systemTurn("system_init", "", { tools: ["a", "b", "c"] })}
          onCopy={noop}
          onFileClick={noop}
        />,
      )
      expect(screen.getByText(/3 tools/)).toBeTruthy()
    })

    it("omits tool count when tools list is empty", () => {
      render(
        <TurnRenderer
          turn={systemTurn("system_init", "", { tools: [] })}
          onCopy={noop}
          onFileClick={noop}
        />,
      )
      expect(screen.queryByText(/tools/)).toBeNull()
    })

    it("carries data-turn-id like every other turn branch", () => {
      const { container } = render(
        <TurnRenderer turn={systemTurn("system_init")} onCopy={noop} onFileClick={noop} />,
      )
      expect(container.querySelector('[data-turn-id="s1"]')).toBeTruthy()
    })

    it("shows the CLI version chip when claude_code_version is present", () => {
      render(
        <TurnRenderer
          turn={systemTurn("system_init", "", { claude_code_version: "2.1.204" })}
          onCopy={noop}
          onFileClick={noop}
        />,
      )
      expect(screen.getByText("v2.1.204")).toBeTruthy()
    })

    it("omits the version chip when claude_code_version is absent", () => {
      render(
        <TurnRenderer
          turn={systemTurn("system_init", "", { model: "claude-opus-4-7" })}
          onCopy={noop}
          onFileClick={noop}
        />,
      )
      expect(screen.queryByText(/^v\d/)).toBeNull()
    })

    it("warns on the pill when MCP servers were skipped", () => {
      render(
        <TurnRenderer
          turn={systemTurn("system_init", "", {
            mcp_server_errors: [
              { name: "memory", type: "config", message: "missing command" },
              { name: "github", type: "spawn", message: "ENOENT" },
            ],
          })}
          onCopy={noop}
          onFileClick={noop}
        />,
      )
      expect(screen.getByText(/2 MCP servers skipped/)).toBeTruthy()
      // The names must be reachable without hovering — a skipped server is a
      // silently lost capability, so it can't depend on a pointer.
      expect(screen.getByLabelText(/memory, github/)).toBeTruthy()
    })

    it("uses singular wording for a single skipped MCP server", () => {
      render(
        <TurnRenderer
          turn={systemTurn("system_init", "", {
            mcp_server_errors: [{ name: "memory", type: "config", message: "boom" }],
          })}
          onCopy={noop}
          onFileClick={noop}
        />,
      )
      expect(screen.getByText(/1 MCP server skipped/)).toBeTruthy()
    })

    it("shows no MCP warning when mcp_server_errors is absent", () => {
      render(
        <TurnRenderer
          turn={systemTurn("system_init", "", { model: "claude-opus-4-7" })}
          onCopy={noop}
          onFileClick={noop}
        />,
      )
      expect(screen.queryByText(/skipped/)).toBeNull()
    })

    it("shows no MCP warning when mcp_server_errors is an empty array", () => {
      render(
        <TurnRenderer
          turn={systemTurn("system_init", "", { mcp_server_errors: [] })}
          onCopy={noop}
          onFileClick={noop}
        />,
      )
      expect(screen.queryByText(/skipped/)).toBeNull()
    })

    it("renders the pill for adapters that emit only model and tools", () => {
      render(
        <TurnRenderer
          turn={systemTurn("system_init", "", { model: "gpt-o5", tools: ["a", "b"] })}
          onCopy={noop}
          onFileClick={noop}
        />,
      )
      expect(screen.getByText(/Session started/)).toBeTruthy()
      expect(screen.getByText("gpt-o5")).toBeTruthy()
      expect(screen.getByText(/2 tools/)).toBeTruthy()
      expect(screen.queryByText(/skipped/)).toBeNull()
    })
  })

  describe("system role — error/info", () => {
    it("renders the content for an error part", () => {
      render(
        <TurnRenderer
          turn={systemTurn("error", "something blew up")}
          onCopy={noop}
          onFileClick={noop}
        />,
      )
      expect(screen.getByText("something blew up")).toBeTruthy()
    })
  })

  describe("assistant role", () => {
    it("delegates rendering to AssistantTurn", () => {
      render(
        <TurnRenderer turn={assistantTurn("a-42")} onCopy={noop} onFileClick={noop} />,
      )
      const probe = screen.getByTestId("assistant-turn-mock")
      expect(probe.getAttribute("data-turn-id")).toBe("a-42")
    })

    it("shows Regenerate when isLastAssistant + onRegenerate + not streaming", () => {
      render(
        <TurnRenderer
          turn={assistantTurn("a1", false)}
          onCopy={noop}
          onFileClick={noop}
          isLastAssistant
          onRegenerate={vi.fn()}
        />,
      )
      expect(screen.getByText("Regenerate")).toBeTruthy()
    })

    it("hides Regenerate while streaming", () => {
      render(
        <TurnRenderer
          turn={assistantTurn("a1", true)}
          onCopy={noop}
          onFileClick={noop}
          isLastAssistant
          onRegenerate={vi.fn()}
        />,
      )
      expect(screen.queryByText("Regenerate")).toBeNull()
    })

    it("hides Regenerate when onRegenerate is not provided", () => {
      render(
        <TurnRenderer
          turn={assistantTurn("a1", false)}
          onCopy={noop}
          onFileClick={noop}
          isLastAssistant
        />,
      )
      expect(screen.queryByText("Regenerate")).toBeNull()
    })

    it("hides Regenerate when isLastAssistant is false", () => {
      render(
        <TurnRenderer
          turn={assistantTurn("a1", false)}
          onCopy={noop}
          onFileClick={noop}
          onRegenerate={vi.fn()}
        />,
      )
      expect(screen.queryByText("Regenerate")).toBeNull()
    })
  })
})

// When the CLI reports skipped MCP servers in a shape the backend cannot read,
// it stores a sentinel carrying only a category — no server name. The CLI
// renders that category; the card used to print "unnamed", which is an alarm
// with nothing to act on. Same fallback order as `crewship run get`:
// name (type) → name → type → unnamed.
describe("system_init — MCP skips without a server name", () => {
  const sentinel = { mcp_server_errors: [{ type: "unrecognized_shape" }] }

  it("labels a nameless skip by its category, not 'unnamed'", () => {
    render(
      <TurnRenderer turn={systemTurn("system_init", "", sentinel)} onCopy={noop} onFileClick={noop} />,
    )
    const chip = screen.getByLabelText(/MCP server.*skipped/i)
    expect(chip.getAttribute("aria-label")).toContain("unrecognized_shape")
    expect(chip.getAttribute("aria-label")).not.toContain("unnamed")
  })

  it("still names the server when the CLI gave one", () => {
    render(
      <TurnRenderer
        turn={systemTurn("system_init", "", {
          mcp_server_errors: [{ name: "crewship-memory", type: "invalid_config" }],
        })}
        onCopy={noop}
        onFileClick={noop}
      />,
    )
    expect(screen.getByLabelText(/crewship-memory/)).toBeTruthy()
  })
})

// The adapter forwards this report unparsed, so its shape belongs to whichever
// CLI release answered. Degradation is therefore decided by the PRESENCE of a
// report, exactly as every backend path decides it — a shape we cannot read
// must still raise the alarm, or a CLI release turns a degraded session into a
// silent one.
describe("system_init — MCP skip reports in shapes the card did not expect", () => {
  it("warns when the skips arrive as an object keyed by server name", () => {
    render(
      <TurnRenderer
        turn={systemTurn("system_init", "", {
          mcp_server_errors: {
            "crewship-memory": { type: "invalid_config", message: "missing command" },
            github: { type: "spawn" },
          },
        })}
        onCopy={noop}
        onFileClick={noop}
      />,
    )
    expect(screen.getByText(/2 MCP servers skipped/)).toBeTruthy()
    expect(screen.getByLabelText(/crewship-memory, github/)).toBeTruthy()
    expect(screen.getByText(/Skipped: crewship-memory, github/)).toBeTruthy()
  })

  it("keeps the reason when an object-keyed entry maps the name to a string", () => {
    render(
      <TurnRenderer
        turn={systemTurn("system_init", "", {
          mcp_server_errors: { "crewship-memory": "spawn ENOENT" },
        })}
        onCopy={noop}
        onFileClick={noop}
      />,
    )
    expect(screen.getByLabelText(/crewship-memory/)).toBeTruthy()
    expect(screen.getByText("spawn ENOENT")).toBeTruthy()
  })

  it("warns when the skips arrive as bare name strings", () => {
    render(
      <TurnRenderer
        turn={systemTurn("system_init", "", {
          mcp_server_errors: ["crewship-memory", "github"],
        })}
        onCopy={noop}
        onFileClick={noop}
      />,
    )
    expect(screen.getByText(/2 MCP servers skipped/)).toBeTruthy()
    expect(screen.getByLabelText(/crewship-memory, github/)).toBeTruthy()
  })

  it("raises the alarm and admits it for a shape it cannot enumerate", () => {
    render(
      <TurnRenderer
        turn={systemTurn("system_init", "", {
          mcp_server_errors: "crewship-memory failed to start",
        })}
        onCopy={noop}
        onFileClick={noop}
      />,
    )
    const chip = screen.getByLabelText(/MCP servers skipped/)
    expect(chip.getAttribute("aria-label")).toContain("unrecognized_shape")
    // No count is invented: this shape says nothing about how many servers went.
    expect(screen.queryByText(/1 MCP server skipped/)).toBeNull()
    expect(screen.getByText(/could not be read/)).toBeTruthy()
  })

  it("shows no MCP warning for an empty object report", () => {
    render(
      <TurnRenderer
        turn={systemTurn("system_init", "", { mcp_server_errors: {} })}
        onCopy={noop}
        onFileClick={noop}
      />,
    )
    expect(screen.queryByText(/skipped/)).toBeNull()
  })

  it("shows no MCP warning for a null report", () => {
    render(
      <TurnRenderer
        turn={systemTurn("system_init", "", { mcp_server_errors: null })}
        onCopy={noop}
        onFileClick={noop}
      />,
    )
    expect(screen.queryByText(/skipped/)).toBeNull()
  })
})

// Every field below is a compile-time assertion over JSON the CLI wrote. A
// render that throws here does not cost a row: nothing in the chat tree is an
// error boundary, so the nearest one is the dashboard route segment's, and the
// degraded session's whole page is replaced by "Something went wrong".
describe("system_init — fields that are not the strings they were typed as", () => {
  it("labels a skip whose name is not a string by its category", () => {
    render(
      <TurnRenderer
        turn={systemTurn("system_init", "", {
          mcp_server_errors: [{ name: { server: "crewship-memory" }, type: "invalid_config" }],
        })}
        onCopy={noop}
        onFileClick={noop}
      />,
    )
    expect(screen.getByText(/1 MCP server skipped/)).toBeTruthy()
    expect(screen.getByLabelText(/invalid_config/)).toBeTruthy()
  })

  it("renders the MCP server list when a server's name and status are not strings", () => {
    render(
      <TurnRenderer
        turn={systemTurn("system_init", "", {
          mcp_servers: [{ name: { id: "crewship-memory" }, status: { state: "connected" } }],
        })}
        onCopy={noop}
        onFileClick={noop}
      />,
    )
    expect(screen.getByText(/Session started/)).toBeTruthy()
    expect(screen.getByText("unknown")).toBeTruthy()
  })

  it("omits the model chip when the model is not a string", () => {
    render(
      <TurnRenderer
        turn={systemTurn("system_init", "", { model: { id: "claude-opus-4-7" } })}
        onCopy={noop}
        onFileClick={noop}
      />,
    )
    expect(screen.getByText(/Session started/)).toBeTruthy()
  })

  it("ignores a tool inventory that is not a list", () => {
    render(
      <TurnRenderer
        turn={systemTurn("system_init", "", { tools: "Bash" })}
        onCopy={noop}
        onFileClick={noop}
      />,
    )
    expect(screen.queryByText(/tools/)).toBeNull()
  })
})
