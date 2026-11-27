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
const bus = vi.hoisted(() => ({ publish: vi.fn() }))
vi.mock("@/lib/mooring/native-agent-version", async () => ({ ...await vi.importActual("@/lib/mooring/native-agent-version"), publishAgentVersion: bus.publish }))
vi.mock("@/hooks/use-pages", () => ({ usePage: (workspaceId?: string) => workspaceId ? { page: { id: "pg_1", slug: "plan", name: "Quarterly plan", artifactKind: "sheet" }, loading: false, notFound: false } : { page: null, loading: false, notFound: false } }))

import { AssistantTurn } from "../assistant-turn"
import { SheetPanelProvider } from "../artifact-context/sheet/sheet-panel-context"

let n = 0
const part = (type: TurnPart["type"], metadata: Record<string, unknown> = {}, content = ""): TurnPart => ({ id: `p${++n}`, type, content, metadata, timestamp: new Date("2026-11-25T10:00:00Z") })
const turn = (parts: TurnPart[], isStreaming = false): ChatTurn => ({ id: "t", role: "assistant", parts, isStreaming, timestamp: new Date("2026-11-25T10:00:00Z") })
const tool = "mcp__crewship__craft_sheet_apply"
const apply = (id: string) => part("tool_call", { tool_id: id, tool_name: tool, input: {} }, tool)
const applied = (id: string) => part("tool_result", { tool_id: id }, JSON.stringify({ page_id: "pg_1", slug: "plan", version: 2, base_version: 1, content_hash: "sha256:" + "a".repeat(64), changed_cells: 1 }))
const el = (t: ChatTurn) => <AssistantTurn turn={t} onCopy={async () => true} onFileClick={() => {}} />

// The release build keeps NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS off, so the project
// provider is never mounted there. A Sheet the agent changed must still show.
describe("Sheet card without artifact projects", () => {
  beforeEach(() => { cleanup(); bus.publish.mockReset() })

  it("shows the card, opens the panel and hands the version to the open editor", () => {
    const panel = { open: vi.fn(), noteChanged: vi.fn(), activePageId: null }
    render(<SheetPanelProvider workspaceId="ws-1" value={panel}>{el(turn([apply("a"), applied("a")], true))}</SheetPanelProvider>)
    expect(screen.getByTestId("sheet-chat-card").textContent).toContain("Quarterly plan")
    expect(panel.noteChanged).toHaveBeenCalledWith("pg_1")
    expect(bus.publish).toHaveBeenCalledWith(expect.objectContaining({ pageId: "pg_1", version: "2", baseVersion: "1" }))
  })

  it("shows the card without a side pane too (a phone), linking to the Sheet", () => {
    render(<SheetPanelProvider workspaceId="ws-1" value={null}>{el(turn([apply("a"), applied("a")]))}</SheetPanelProvider>)
    expect(screen.getByRole("link", { name: "Open Quarterly plan" }).getAttribute("href")).toContain("pg_1")
  })

  it("leaves project tools as ordinary steps", () => {
    const name = "mcp__crewship__artifact_publish"
    render(<SheetPanelProvider workspaceId="ws-1" value={null}>{el(turn([part("tool_call", { tool_id: "b", tool_name: name, input: { slug: "x" } }, name)]))}</SheetPanelProvider>)
    expect(screen.queryByTestId("artifact-tool-card")).toBeNull()
    expect(screen.getByTestId("turn-activity").textContent).toContain("1 step")
  })

  it("keeps the ordinary step where a chat shows no Sheets", () => {
    render(el(turn([apply("a"), applied("a")])))
    expect(screen.queryByTestId("artifact-tool-card")).toBeNull()
    expect(screen.getByTestId("turn-activity").textContent).toContain("1 step")
  })
})
