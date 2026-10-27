import { describe, it, expect, vi } from "vitest"
import React from "react"
import { render, screen, cleanup } from "@testing-library/react"
import type { ChatTurn, TurnPart } from "@/hooks/use-chat"

// AssistantTurn leans on heavy ai-elements components (motion/react inside
// Reasoning, portal logic in Tool/CodeBlock). Replace them with thin probes
// so the tests focus on the dispatch logic this file actually owns, rather
// than the primitives those libraries already cover.
vi.mock("@/components/ai-elements/message", () => ({
  Message: ({ children, from }: { children: React.ReactNode; from: string }) => (
    <div data-testid="message" data-from={from}>{children}</div>
  ),
  MessageContent: ({ children, className }: { children: React.ReactNode; className?: string }) => (
    <div data-testid="message-content" className={className}>{children}</div>
  ),
  MessageResponse: ({ children }: { children: React.ReactNode }) => (
    <div data-testid="message-response">{children}</div>
  ),
  MessageActions: ({ children }: { children: React.ReactNode }) => (
    <div data-testid="message-actions">{children}</div>
  ),
  MessageAction: ({ children, tooltip, onClick }: {
    children: React.ReactNode
    tooltip: string
    onClick?: () => void
  }) => (
    <button data-testid={`action-${tooltip.toLowerCase().replace(/ /g, "-")}`} onClick={onClick}>
      {children}
    </button>
  ),
}))

vi.mock("@/components/ai-elements/reasoning", () => ({
  Reasoning: ({ children, isStreaming }: { children: React.ReactNode; isStreaming?: boolean }) => (
    <div data-testid="reasoning" data-streaming={String(!!isStreaming)}>{children}</div>
  ),
  ReasoningTrigger: () => <span data-testid="reasoning-trigger" />,
  ReasoningContent: ({ children }: { children: React.ReactNode }) => (
    <div data-testid="reasoning-content">{children}</div>
  ),
}))

// The smooth-reveal animation is covered by hooks/__tests__/use-smooth-text;
// here it would make streaming parts render progressively across frames,
// which is noise for dispatch assertions. Identity-mock it.
vi.mock("@/hooks/use-smooth-text", () => ({
  useSmoothText: (text: string) => text,
}))

vi.mock("@/components/ai-elements/tool", () => ({
  Tool: ({ children }: { children: React.ReactNode }) => <div data-testid="tool">{children}</div>,
  ToolContent: ({ children }: { children: React.ReactNode }) => <div data-testid="tool-content">{children}</div>,
  ToolHeader: ({ title, state, statusLabel }: { title?: string; state?: string; statusLabel?: string }) => <div data-testid="tool-header" data-state={state} data-label={statusLabel ?? ""}>{title}</div>,
}))

vi.mock("@/components/ai-elements/code-block", () => ({
  CodeBlock: ({ code }: { code: string }) => <pre data-testid="code-block">{code}</pre>,
}))

vi.mock("@/components/features/chat/status-indicator", () => ({
  StatusIndicator: ({ content }: { content: string }) => (
    <div data-testid="status-indicator">{content}</div>
  ),
}))

// TurnFeedbackActions calls useSession() to namespace the feedback
// store by user (privacy fix for the per-user persisted state). The
// real useAuth/useSession require an AuthProvider; tests don't have
// one and shouldn't — the feedback wiring is an implementation detail
// for these dispatch-logic tests. Stub useSession to a fixed identity
// so the component renders without throwing.
vi.mock("@/hooks/use-auth", () => ({
  useSession: () => ({
    data: { user: { id: "test-user", name: "Test", email: "t@e.com" }, expires: "" },
    status: "authenticated" as const,
  }),
}))

vi.mock("../reactions/turn-reactions", () => ({ TurnReactions: () => null }))

vi.mock("../artifact/inline-run-images", () => ({ InlineRunImages: ({ agentId, workspaceId, runId }: { agentId: string; workspaceId: string; runId: string }) => <div data-testid="run-images">{`${agentId}:${workspaceId}:${runId}`}</div> }))
const h = vi.hoisted(() => ({ available: true }))
vi.mock("../artifact-context/projects/use-artifact-project-data", () => ({
  useArtifactProjectsAvailable: () => h.available,
  useArtifactProjectSnapshot: () => ({ data: undefined, error: null, isPending: true, refetch: () => Promise.resolve() }),
  useArtifactProjectMutations: () => ({ restore: {}, share: {}, comment: {} }),
}))

import { AssistantTurn } from "../assistant-turn"
import { ArtifactViewerProvider } from "../artifact-context/projects/artifact-viewer-context"

let n = 0
const part = (type: TurnPart["type"], metadata: Record<string, unknown> = {}, content = ""): TurnPart => ({
  id: `p${++n}`, type, content, metadata, timestamp: new Date("2026-10-19T10:00:00Z"),
})
const turn = (parts: TurnPart[], isStreaming = false): ChatTurn => ({
  id: "t", role: "assistant", parts, isStreaming, timestamp: new Date("2026-10-19T10:00:00Z"),
})
const bash = (id: string) => part("tool_call", { tool_id: id, tool_name: "Bash", input: { command: "ls" } }, "Bash")
const publish = (id: string) => part("tool_call", { tool_id: id, tool_name: "mcp__crewship__artifact_publish", input: { slug: "onboarding", base_version: 1 } }, "mcp__crewship__artifact_publish")
const result = (id: string, content = "") => part("tool_result", { tool_id: id }, content)
const published = (id: string) => result(id, JSON.stringify({ slug: "onboarding", version: 2, page_id: "pg" }))
const el = (t: ChatTurn) => <AssistantTurn turn={t} onCopy={async () => true} onFileClick={() => {}} />
const withProvider = (t: ChatTurn) => render(<ArtifactViewerProvider workspaceId="ws-1">{el(t)}</ArtifactViewerProvider>)

describe("artifact tool cards", () => {
  beforeEach(() => { cleanup(); h.available = true })

  it("renders the card instead of the raw tool row with the provider and availability", () => {
    withProvider(turn([publish("a"), published("a")]))
    expect(screen.getByTestId("artifact-tool-card")).toBeTruthy()
    expect(screen.getByText("v2 ready")).toBeTruthy()
    expect(screen.queryByTestId("tool-header")).toBeNull()
  })

  it("lifts the card out of a grouped activity run in order and drops its raw result", () => {
    const { container } = withProvider(turn([bash("a"), result("a", "x"), publish("b"), bash("c"), result("c", "y"), published("b")]))
    const card = screen.getByTestId("artifact-tool-card")
    expect(card.closest("details")).toBeNull()
    expect(screen.getAllByTestId("tool-header").map(h => h.textContent)).toEqual(["Bash  $ ls", "Result", "Bash  $ ls", "Result"])
    expect(container.textContent).not.toContain("\"version\":2")
    // Bash a, card, Bash c — the run is split at the card.
    const order = Array.from(container.querySelectorAll("[data-testid=artifact-tool-card], [data-testid=tool-header]")).map(e => e.getAttribute("data-testid"))
    expect(order[2]).toBe("artifact-tool-card")
  })

  it("keeps the default tool UI without the provider", () => {
    render(el(turn([publish("a"), published("a")])))
    expect(screen.queryByTestId("artifact-tool-card")).toBeNull()
    expect(screen.getAllByTestId("tool-header")[0].textContent).toBe("mcp__crewship__artifact_publish")
  })

  it("keeps the default tool UI when the server reports projects unavailable", () => {
    h.available = false
    withProvider(turn([publish("a"), published("a")]))
    expect(screen.queryByTestId("artifact-tool-card")).toBeNull()
    expect(screen.getAllByTestId("tool-header").length).toBeGreaterThan(0)
  })
})

describe("artifact tool card composition", () => {
  beforeEach(() => { cleanup(); h.available = true })

  it("shows only the progress card while the call runs", () => {
    withProvider(turn([publish("a")], true))
    expect(screen.getByRole("progressbar")).toBeTruthy()
    expect(screen.queryByLabelText("Loading project")).toBeNull()
  })

  it("shows the done row with the project card below once the call finished", () => {
    withProvider(turn([publish("a"), published("a")]))
    expect(screen.getByText("v2 ready")).toBeTruthy()
    expect(screen.getByLabelText("Loading project")).toBeTruthy()
  })

  // A refused create (dev3: "artifact projects are disabled") never made a project:
  // a card would only fetch a missing project and show "Restricted item".
  const create = (id: string) => part("tool_call", { tool_id: id, tool_name: "mcp__crewship__artifact_create", input: { slug: "pricing-site", name: "Pricing site" } }, "mcp__crewship__artifact_create")
  it("shows no project card for a create the server refused", () => {
    withProvider(turn([create("a"), part("tool_result", { tool_id: "a", is_error: true }, JSON.stringify({ error: "artifact projects are disabled" }))]))
    expect(screen.getByText(/artifact projects are disabled/)).toBeTruthy()
    expect(screen.queryByLabelText("Loading project")).toBeNull()
  })

  it("shows no project card for a create that ended without a result", () => {
    withProvider(turn([create("a")], false))
    expect(screen.queryByLabelText("Loading project")).toBeNull()
  })

  it("keeps the project card when a publish to an existing project failed", () => {
    withProvider(turn([publish("a"), part("tool_result", { tool_id: "a", is_error: true }, JSON.stringify({ error: "conflict" }))]))
    expect(screen.getByLabelText("Loading project")).toBeTruthy()
  })
})
