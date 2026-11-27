import type { ReactNode } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"

// Letting the agent change the open Sheet, through the real ChatPanel and its own send.
vi.mock("../virtual-conversation", () => ({ virtualChatEnabled: () => false, VirtualConversation: ({ footer }: { footer?: ReactNode }) => <>{footer}</> }))
const chat = vi.hoisted(() => ({
  turns: [{ id: "saved-turn", role: "assistant", parts: [], timestamp: new Date() }],
  sendMessage: vi.fn(), stopGeneration: vi.fn(), regenerateLastTurn: vi.fn(), editAndResend: vi.fn(),
  loadHistory: vi.fn(), markHistoryUnavailable: vi.fn(), resubscribeSession: vi.fn(),
  isStreaming: false, connectionStatus: "connected",
}))
const toast = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn(), info: vi.fn() }))
const refs = vi.hoisted(() => ({ value: { refs: [] } as Record<string, unknown> }))
const access = vi.hoisted(() => ({ value: { allowed: true, max_sheets: 1 } as Record<string, unknown> }))
const extra = vi.hoisted(() => ({ metadata: undefined as Record<string, unknown> | undefined }))
const phone = vi.hoisted(() => ({ value: false }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => phone.value }))
vi.mock("@/hooks/use-chat", () => ({ useChat: () => chat }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "user-1" } } }), useSession: () => ({ data: { user: { id: "user-1" } } }) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test" }) }))
vi.mock("@/hooks/use-pages", async () => ({ ...await vi.importActual("@/hooks/use-pages"), usePage: () => ({ page: { id: "pg_1", name: "Quarterly plan", artifactKind: "sheet" }, loading: false, notFound: false }) }))
vi.mock("../right-rail", () => ({ RightRail: () => null }))
vi.mock("../right-drawer", () => ({ RightDrawer: ({ children }: { children: ReactNode }) => children }))
vi.mock("../right-panel", () => ({ RightPanel: () => null }))
vi.mock("../artifact/artifact-pane", () => ({ ArtifactPane: () => null }))
vi.mock("../composer/slash-palette", () => ({ SlashPalette: () => null }))
const intake = vi.hoisted(() => ({ take: null as null | ((files: File[]) => File[]) }))
vi.mock("../artifact-context/sheet/sheet-from-file", () => ({ SheetFromFileRow: ({ file }: { file: File }) => <p data-testid="sheet-offer">{file.name}</p> }))
vi.mock("../composer/chat-composer", async () => {
  const { useSheetIntake } = await import("../artifact-context/sheet/sheet-intake")
  return { ChatComposer: ({ sendMessage }: { sendMessage: (text: string, metadata?: Record<string, unknown>) => unknown }) => {
    intake.take = useSheetIntake()
    return <button type="button" onClick={() => void (extra.metadata ? sendMessage("Update it", extra.metadata) : sendMessage("Update it"))}>Send message</button>
  } }
})
const editor = vi.hoisted(() => ({ handBack: null as null | (() => Promise<{ state: string; reason?: string }>), status: undefined as undefined | ((s: unknown) => void), /** Still opening: says no version yet. */ silent: false }))
// What the editor reports once it opened: version 3, shown to a reader.
const opened = { ready: true, dirty: false, canEdit: true, editing: false, holder: null, holderIsMe: false, openProposals: 0, recoveredDrafts: 0, version: "3" }
vi.mock("../artifact-context/sheet/sheet-side-panel", async () => {
  const { useEffect } = await import("react")
  return { SheetSidePanel: ({ handBackRef, onStatus }: { handBackRef?: { current: unknown }; onStatus?: (s: unknown) => void }) => {
    if (handBackRef) handBackRef.current = editor.handBack
    editor.status = onStatus
    useEffect(() => { if (!editor.silent) onStatus?.({ ready: true, dirty: false, canEdit: true, editing: false, holder: null, holderIsMe: false, openProposals: 0, recoveredDrafts: 0, version: "3" }) }, []) // eslint-disable-line react-hooks/exhaustive-deps -- once, as the editor opens
    return <div data-testid="sheet-editor" />
  } }
})
vi.mock("../turn-renderer", async () => {
  const { useSheetPanel } = await import("../artifact-context/sheet/sheet-panel-context")
  return { TurnRenderer: () => { const panel = useSheetPanel(); return panel ? <button type="button" onClick={() => panel.open("pg_1")}>Open Sheet</button> : null } }
})
vi.mock("sonner", () => ({ toast }))

import { ChatPanel } from "../chat-panel"
import { installMemoryStorage } from "../artifact-context/sheet/__tests__/memory-storage"

installMemoryStorage()

const json = (value: unknown) => new Response(JSON.stringify(value), { status: 200, headers: { "content-type": "application/json" } })
beforeEach(() => {
  sessionStorage.clear()
  extra.metadata = undefined
  access.value = { allowed: true, max_sheets: 1 }
  refs.value = { refs: [] }
  phone.value = false
  editor.handBack = null
  editor.silent = false
  chat.sendMessage.mockReset()
  chat.sendMessage.mockReturnValue(true)
  toast.error.mockReset()
  toast.info.mockReset()
  global.fetch = vi.fn((input: RequestInfo | URL) => {
    const path = String(input)
    if (path.includes("/craft/agent-access")) return Promise.resolve(json(access.value))
    if (path.includes("/artifact-refs")) return Promise.resolve(json(refs.value))
    return Promise.resolve(json(path.includes("/execution-profile") ? { mode: "trusted" } : path.includes("/messages") ? { messages: [] } : path.includes("/participants") ? { participants: [] } : {}))
  }) as typeof fetch
})
afterEach(() => { cleanup() })

const openSheet = async () => {
  render(<ChatPanel agentId="agent-1" agentSlug="riley" agentName="Riley" sessionId="sess-1" askForms={null} sessionKind="direct" />)
  fireEvent.click(await screen.findByRole("button", { name: "Open Sheet" }))
  const toggle = await screen.findByRole("switch", { name: /Allow agent edits/ })
  // Usable once the server answered and the editor said which version it shows.
  await waitFor(() => expect(toggle).toBeEnabled())
  return toggle
}
const send = () => fireEvent.click(screen.getByRole("button", { name: "Send message" }))

it("defaults to the visible Sheet version and lets the person turn agent edits off", async () => {
  const toggle = await openSheet()
  expect(toggle).toBeChecked()
  send()
  expect(chat.sendMessage).toHaveBeenLastCalledWith("Update it", { artifact_refs: [{ page_id: "pg_1", version: "3" }] })
  fireEvent.click(toggle)
  send()
  expect(chat.sendMessage).toHaveBeenLastCalledWith("Update it")
  expect(toggle).not.toBeChecked()
})

it("keeps the choice when the message did not go", async () => {
  chat.sendMessage.mockReturnValue(false)
  await openSheet()
  send()
  expect(screen.getByRole("switch")).toBeChecked()
})

it("never drops the choice silently: a message that carries something else is held and says why", async () => {
  await openSheet()
  extra.metadata = { document_create_intent: { name: "x" } }
  send()
  expect(chat.sendMessage).not.toHaveBeenCalled()
  expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("cannot also let the agent change the Sheet"))
})

it("removed from the message, the Sheet stays open and is not sent", async () => {
  await openSheet()
  fireEvent.click(screen.getByRole("button", { name: "Remove Quarterly plan from this message" }))
  expect(screen.queryByRole("switch")).toBeNull()
  expect(screen.getByTestId("sheet-editor")).toBeInTheDocument()
  send()
  expect(chat.sendMessage).toHaveBeenCalledWith("Update it")
})

it("offers an added workbook as a Sheet and still attaches it; an ordinary file is left alone", async () => {
  render(<ChatPanel agentId="agent-1" agentSlug="riley" agentName="Riley" sessionId="sess-1" askForms={null} sessionKind="direct" />)
  await screen.findByRole("button", { name: "Open Sheet" })
  const book = new File(["x"], "Budget.xlsx"), text = new File(["x"], "notes.txt")
  let attach: File[] | undefined
  act(() => { attach = intake.take?.([book, text]) })
  expect(attach).toEqual([book, text])
  expect(await screen.findByTestId("sheet-offer")).toHaveTextContent("Budget.xlsx")
})

it("binds consent to the visible version without handing editing back while a local draft is pending", async () => {
  editor.handBack = vi.fn(async () => ({ state: "kept", reason: "unsaved" }))
  expect(await openSheet()).toBeChecked()
  act(() => editor.status?.({ ...opened, dirty: true, editing: true, holderIsMe: true }))
  send()
  expect(chat.sendMessage).toHaveBeenCalledWith("Update it", { artifact_refs: [{ page_id: "pg_1", version: "3" }] })
  expect(editor.handBack).not.toHaveBeenCalled()
})

it("finds a Sheet opened here again after a reload", async () => {
  const first = render(<ChatPanel agentId="agent-1" agentSlug="riley" agentName="Riley" sessionId="sess-1" askForms={null} sessionKind="direct" />)
  fireEvent.click(await screen.findByRole("button", { name: "Open Sheet" }))
  await screen.findByTestId("sheet-editor")
  first.unmount()
  render(<ChatPanel agentId="agent-1" agentSlug="riley" agentName="Riley" sessionId="sess-1" askForms={null} sessionKind="direct" />)
  const links = await screen.findByRole("region", { name: "Sheets in this conversation" })
  fireEvent.click(within(links).getByRole("button", { name: "Open Quarterly plan" }))
  expect(await screen.findByTestId("sheet-editor")).toBeInTheDocument()
})

it("asks the server about this Sheet in this chat, and offers no choice it would refuse", async () => {
  access.value = { allowed: false, reason: "not_attached", detail: "not_in_personal_space", message: "Only a Sheet in your own private space.", max_sheets: 1 }
  render(<ChatPanel agentId="agent-1" agentSlug="riley" agentName="Riley" sessionId="sess-1" askForms={null} sessionKind="direct" />)
  fireEvent.click(await screen.findByRole("button", { name: "Open Sheet" }))
  expect(await screen.findByText("Only a Sheet in your own private space.")).toBeInTheDocument()
  expect(screen.queryByRole("switch")).toBeNull()
  expect(vi.mocked(global.fetch).mock.calls.some(([url]) => String(url).includes("/api/v1/pages/%40pg_1/craft/agent-access?workspace_id=ws-test&chat_id=sess-1"))).toBe(true)
  send()
  expect(chat.sendMessage).toHaveBeenCalledWith("Update it")
})

it("binds the next message to the latest visible committed version", async () => {
  expect(await openSheet()).toBeChecked()
  act(() => editor.status?.({ ...opened, version: "4" }))
  send()
  expect(chat.sendMessage).toHaveBeenCalledWith("Update it", { artifact_refs: [{ page_id: "pg_1", version: "4" }] })
})

it("lists the Sheets the server says messages in this conversation carried, in any browser", async () => {
  refs.value = { refs: [
    { message_id: "m1", page_id: "pg_1", slug: "plan", name: "Quarterly plan", application: "gridcraft", created_at: "2026-11-25T10:00:00Z" },
    { message_id: "m2", page_id: "pg_doc", slug: "memo", name: "Memo", application: "wordcraft", created_at: "2026-11-25T10:01:00Z" },
  ] }
  render(<ChatPanel agentId="agent-1" agentSlug="riley" agentName="Riley" sessionId="sess-1" askForms={null} sessionKind="direct" />)
  const links = await screen.findByRole("region", { name: "Sheets in this conversation" })
  expect(within(links).getAllByRole("listitem")).toHaveLength(1)
  expect(vi.mocked(global.fetch).mock.calls.some(([url]) => String(url).includes("/api/v1/chats/sess-1/artifact-refs?"))).toBe(true)
  fireEvent.click(within(links).getByRole("button", { name: "Open Quarterly plan" }))
  expect(await screen.findByTestId("sheet-editor")).toBeInTheDocument()
})

describe("on a phone, where no Sheet is open beside the chat", () => {
  const listed = { refs: [{ message_id: "m1", page_id: "pg_1", slug: "plan", name: "Quarterly plan", application: "gridcraft", created_at: "2026-11-25T10:00:00Z" }] }
  const seen = { allowed: true, max_sheets: 1, head_version: "12", saved_at: "2026-11-25T09:30:00Z", saved_by_name: "Dana" }
  const choose = async () => {
    phone.value = true; refs.value = listed; access.value = seen
    render(<ChatPanel agentId="agent-1" agentSlug="riley" agentName="Riley" sessionId="sess-1" askForms={null} sessionKind="direct" />)
    const links = await screen.findByRole("region", { name: "Sheets in this conversation" })
    fireEvent.click(within(links).getByRole("button", { name: "Use Quarterly plan with the next message" }))
    const toggle = await screen.findByRole("switch", { name: /Allow agent edits/ })
    await waitFor(() => expect(toggle).toBeEnabled())
    return toggle
  }

  it("names the version the leave is for, who saved it and when, and offers to view it first", async () => {
    const toggle = await choose()
    expect(toggle).toBeChecked()
    const chip = screen.getByRole("group", { name: "Sheet for this message" })
    expect(chip).toHaveTextContent(/Version 12, saved .* by Dana/)
    expect(within(chip).getByRole("link", { name: "View it first" }).getAttribute("href")).toContain("pg_1")
  })

  it("sends the version shown", async () => {
    await choose()
    send()
    await waitFor(() => expect(chat.sendMessage).toHaveBeenCalledWith("Update it", { artifact_refs: [{ page_id: "pg_1", version: "12" }] }))
  })

  it("offers no choice before the server named a version", async () => {
    phone.value = true; refs.value = listed; access.value = { allowed: true, max_sheets: 1 }
    render(<ChatPanel agentId="agent-1" agentSlug="riley" agentName="Riley" sessionId="sess-1" askForms={null} sessionKind="direct" />)
    fireEvent.click(within(await screen.findByRole("region", { name: "Sheets in this conversation" })).getByRole("button", { name: "Use Quarterly plan with the next message" }))
    const toggle = await screen.findByRole("switch", { name: /Allow agent edits/ })
    expect(toggle).toBeDisabled()
    send()
    expect(chat.sendMessage).not.toHaveBeenCalled()
  })
})

// A client starts like this: a new chat, then the workbook. The draft has no row until something creates it.
describe("in a new chat that has not sent anything yet", () => {
  const draft = () => render(<ChatPanel agentId="agent-1" agentSlug="riley" agentName="Riley" sessionId="sess-1" draft askForms={null} sessionKind="direct" />)
  const answer = (create: number, mode = "trusted") => {
    const calls: string[] = []
    global.fetch = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      calls.push(`${init?.method ?? "GET"} ${path}`)
      if (path.includes("/agents/agent-1/chats") && init?.method === "POST") return Promise.resolve(new Response("{}", { status: create, headers: { "content-type": "application/json" } }))
      if (path.includes("/execution-profile")) return Promise.resolve(json({ mode }))
      if (path.includes("/craft/agent-access")) return Promise.resolve(json(access.value))
      if (path.includes("/artifact-refs")) return Promise.resolve(json(refs.value))
      return Promise.resolve(json(path.includes("/messages") ? { messages: [] } : path.includes("/participants") ? { participants: [] } : {}))
    }) as typeof fetch
    return calls
  }
  const book = () => new File(["x"], "Budget.xlsx")
  beforeEach(() => { intake.take = null })

  it("offers an added workbook as a Sheet once the server created the conversation and confirmed it, and still attaches it", async () => {
    const calls = answer(201)
    draft()
    await waitFor(() => expect(intake.take).not.toBeNull())
    const file = book()
    let attach: File[] | undefined
    act(() => { attach = intake.take?.([file]) })
    expect(attach).toEqual([file])
    expect(await screen.findByTestId("sheet-offer")).toHaveTextContent("Budget.xlsx")
    const created = calls.findIndex(c => c.startsWith("POST") && c.includes("/agents/agent-1/chats")), profile = calls.findIndex(c => c.includes("/execution-profile"))
    expect(created).toBeGreaterThanOrEqual(0)
    expect(profile).toBeGreaterThan(created)
  })

  it("then lets the person allow the agent to change the Sheet opened from it", async () => {
    answer(201)
    draft()
    await waitFor(() => expect(intake.take).not.toBeNull())
    expect(screen.queryByRole("button", { name: "Open Sheet" })).toBeNull()
    act(() => { intake.take?.([book()]) })
    fireEvent.click(await screen.findByRole("button", { name: "Open Sheet" }))
    const toggle = await screen.findByRole("switch", { name: /Allow agent edits/ })
    await waitFor(() => expect(toggle).toBeEnabled())
    expect(toggle).toBeChecked()
  })

  it.each([["refused", 403, "trusted"], ["not found", 404, "trusted"], ["restricted", 201, "restricted"]])("offers nothing when the server %s the conversation", async (_name, create, mode) => {
    answer(create, mode)
    draft()
    await waitFor(() => expect(intake.take).not.toBeNull())
    act(() => { intake.take?.([book()]) })
    await new Promise(resolve => setTimeout(resolve, 50))
    expect(screen.queryByTestId("sheet-offer")).toBeNull()
    expect(screen.queryByRole("switch")).toBeNull()
  })
})

// The person presses Send while the Sheet exists but its editor is still
// opening. The message waits for the version the editor actually displays.
describe("sent while the Sheet's editor is still opening", () => {
  const openingSheet = async () => {
    editor.silent = true
    render(<ChatPanel agentId="agent-1" agentSlug="riley" agentName="Riley" sessionId="sess-1" askForms={null} sessionKind="direct" />)
    fireEvent.click(await screen.findByRole("button", { name: "Open Sheet" }))
    await screen.findByRole("switch", { name: /Allow agent edits/ })
  }
  it("waits for the displayed version even if the server already named a different head", async () => {
    access.value = { allowed: true, default_allowed: true, max_sheets: 1, head_version: "1" }
    await openingSheet()
    send()
    expect(chat.sendMessage).not.toHaveBeenCalled()
    act(() => editor.status?.(opened))
    await waitFor(() => expect(chat.sendMessage).toHaveBeenCalledWith("Update it", { artifact_refs: [{ page_id: "pg_1", version: "3" }] }))
  })
  it("waits while no version is known yet, then goes with the one the editor shows", async () => {
    access.value = { allowed: true, default_allowed: true, max_sheets: 1 }
    await openingSheet()
    send()
    await new Promise(r => setTimeout(r, 1500))
    expect(chat.sendMessage, "nothing goes without the Sheet").not.toHaveBeenCalled()
    act(() => { editor.status?.(opened) })
    await waitFor(() => expect(chat.sendMessage).toHaveBeenCalledWith("Update it", { artifact_refs: [{ page_id: "pg_1", version: "3" }] }))
  })
})
