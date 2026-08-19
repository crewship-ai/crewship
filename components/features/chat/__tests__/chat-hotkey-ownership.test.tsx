import { useState } from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

// =============================================================================
// One keystroke, one thing.
//
// ⌘K used to have TWO listeners on the chat surface and neither stopped the
// other: a document-level one in AppToolbar (the global command palette) and a
// `mod+k` react-hotkeys-hook binding inside SlashPalette (the chat's own
// command palette). Pressing ⌘K in a conversation opened both dialogs, stacked,
// and which one received the typing depended on mount order.
//
// The resolution: ⌘K is the GLOBAL palette everywhere — that is the binding the
// toolbar prints on screen, on every route, in the search button's key cap —
// and the chat's palette moved to ⌘/ plus a visible button in the chat header
// (it previously had NO door but ⌘K: the composer has no `/` trigger, so on a
// phone the surface was unreachable entirely).
//
// Escape is the same defect in another key: with two surfaces open it used to
// close both. The layering asserted here is "the topmost open surface consumes
// it" — a modal palette is above the right drawer, so Escape closes the palette
// and leaves the drawer exactly where it was. The chat palette enforces that by
// stopping propagation while it is open, the same technique ask-form-sheet.tsx
// already uses deliberately for its own Escape.
//
// `mod` in react-hotkeys-hook is meta on a Mac and ctrl everywhere else, and
// the toolbar listener fires on `metaKey || ctrlKey`, so the collision was
// platform-independent. happy-dom reports a non-Mac userAgent, so ctrl is the
// modifier that exercises both here.
// =============================================================================

vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({
    session: { user: { name: "Demo User", email: "demo@crewship.ai" } },
    signOut: vi.fn().mockResolvedValue(undefined),
  }),
  useSession: () => ({ data: { user: { id: "user-1" } } }),
}))
vi.mock("@/hooks/use-realtime", () => ({ useRealtime: () => ({ status: "connected" }) }))
vi.mock("@/hooks/use-engine-status", () => ({ useEngineStatus: () => ({ status: "connected" }) }))
vi.mock("@/hooks/use-crews-status", () => ({ useCrewsStatus: () => null }))
vi.mock("@/hooks/use-provisioning-status", () => ({ useProvisioningStatus: () => null }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test", loading: false }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "OWNER" }) }))
vi.mock("@/lib/store", () => ({
  useAppStore: (selector: (s: { breadcrumbs: unknown[] }) => unknown) => selector({ breadcrumbs: [] }),
}))
vi.mock("@/components/features/inbox/inbox-bell", () => ({ InboxBell: () => null }))
vi.mock("@/components/features/activity/activity-bell", () => ({ ActivityBell: () => null }))
vi.mock("@/components/layout/app-toolbar-provisioning", () => ({ ProvisioningBadge: () => null }))
vi.mock("@/hooks/use-slash-commands", () => ({ useSlashCommands: () => ({ data: [] }) }))
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() } }))

// The global palette is a prop recorder: this file is about WHICH surface a
// keystroke reaches, not about what either one renders.
vi.mock("@/components/command-palette", () => ({
  CommandPalette: ({ open }: { open: boolean }) =>
    open ? <div data-testid="global-palette" /> : null,
}))

const chatStub = vi.hoisted(() => ({
  turns: [] as unknown[],
  sendMessage: vi.fn(),
  stopGeneration: vi.fn(),
  regenerateLastTurn: vi.fn(),
  editAndResend: vi.fn(),
  loadHistory: vi.fn(),
  markHistoryUnavailable: vi.fn(),
  resubscribeSession: vi.fn(),
  isStreaming: false,
  connectionStatus: "connected",
}))
vi.mock("@/hooks/use-chat", () => ({ useChat: () => chatStub }))

// ChatPanel neighbours that open surfaces of their own and are not what this
// file is about. RightDrawer is deliberately NOT mocked — whether Escape
// reaches it is the assertion.
vi.mock("../right-panel", () => ({ RightPanel: () => null }))
vi.mock("../right-rail", async (importOriginal) => ({
  // DRAWER_TAB_LABELS is a plain string map RightDrawer reads for its
  // accessible name — the RAIL is what this file has no use for.
  ...(await importOriginal<typeof import("../right-rail")>()),
  RightRail: () => null,
}))
vi.mock("../artifact/artifact-pane", () => ({ ArtifactPane: () => null }))
vi.mock("../composer/mention-autocomplete", () => ({ MentionAutocomplete: () => null }))

import { TooltipProvider } from "@/components/ui/tooltip"
import { AppToolbar } from "@/components/layout/app-toolbar"
import { SlashPalette } from "../composer/slash-palette"
import { RightDrawer } from "../right-drawer"
import { ChatPanel } from "../chat-panel"
import { useDrawerStore } from "@/stores/drawer-store"

/** react-hotkeys-hook matches on `event.code`, so both are always supplied. */
function press(key: string, code: string, mods: Record<string, boolean> = {}) {
  fireEvent.keyDown(document, { key, code, ...mods })
}

const REAL_UA = navigator.userAgent
/** Flip the platform react-hotkeys-hook reads `mod` from. Restored per test. */
function asMac() {
  Object.defineProperty(window.navigator, "userAgent", {
    value: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)",
    configurable: true,
  })
}

/** The chat palette renders its rows only while open. */
const chatPaletteOpen = () => !!screen.queryByTestId("slash-item-clear")
const globalPaletteOpen = () => !!screen.queryByTestId("global-palette")

function stubFetch() {
  global.fetch = vi.fn((url: string) => {
    const u = String(url)
    if (u.includes("/messages")) {
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ messages: [] }) }) as unknown as Promise<Response>
    }
    if (u.includes("/participants")) {
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ participants: [] }) }) as unknown as Promise<Response>
    }
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ id: "created-1" }) }) as unknown as Promise<Response>
  }) as unknown as typeof fetch
}

const panelProps = {
  agentId: "agent-1",
  sessionId: "sess-1",
  agentName: "Riley",
  agentSlug: "riley",
  askForms: null,
}

/** A conversation: the toolbar (which every route has) plus the chat's own
 *  palette, which only a conversation mounts. */
function renderInConversation() {
  return render(
    <TooltipProvider>
      <AppToolbar />
      <SlashPalette agentSlug="riley" workspaceId="ws-test" onCommand={vi.fn()} onAction={vi.fn()} />
    </TooltipProvider>,
  )
}

beforeEach(() => {
  Object.defineProperty(window.navigator, "userAgent", { value: REAL_UA, configurable: true })
  useDrawerStore.setState({ open: false, activeTab: "files", mode: "overlay" })
  chatStub.turns = []
  chatStub.isStreaming = false
  stubFetch()
})

describe("⌘K has exactly one owner", () => {
  it("opens the global palette inside a conversation, and NOT the chat palette", () => {
    renderInConversation()

    press("k", "KeyK", { ctrlKey: true })

    expect(globalPaletteOpen()).toBe(true)
    // The other half of the finding: the chat's own palette must not have
    // opened behind it. This is what failed — both dialogs were on screen.
    expect(chatPaletteOpen()).toBe(false)
  })

  it("does the same on a Mac, where `mod` means ⌘ rather than ctrl", () => {
    // react-hotkeys-hook resolves `mod` from navigator.userAgent at match
    // time, so this is the only difference between the two platforms — and
    // the toolbar fires on `metaKey || ctrlKey`, so the collision was on both.
    asMac()
    renderInConversation()

    press("k", "KeyK", { metaKey: true })

    expect(globalPaletteOpen()).toBe(true)
    expect(chatPaletteOpen()).toBe(false)
  })

  it("still opens the global palette outside a conversation", () => {
    render(
      <TooltipProvider>
        <AppToolbar />
      </TooltipProvider>,
    )

    press("k", "KeyK", { ctrlKey: true })

    expect(globalPaletteOpen()).toBe(true)
  })
})

describe("the chat palette keeps a way in", () => {
  it("opens on ⌘/ without disturbing the global palette", () => {
    renderInConversation()

    press("/", "Slash", { ctrlKey: true })

    expect(chatPaletteOpen()).toBe(true)
    expect(globalPaletteOpen()).toBe(false)
  })

  // The key is matched on the PHYSICAL Slash position, so a layout that puts
  // "/" behind a modifier does not get the shortcut at all — and nothing else
  // on this surface ever announced the palette's existence. The button is both
  // the discoverability and the fallback.
  it("has a visible, labelled trigger in the chat header", async () => {
    render(<ChatPanel {...panelProps} />)

    const trigger = await screen.findByRole("button", { name: /chat commands/i })
    // The binding has to be on screen, not just in a keymap nobody can read.
    expect(trigger).toHaveAccessibleName(/⌘\//)

    fireEvent.click(trigger)

    await waitFor(() => expect(chatPaletteOpen()).toBe(true))
  })
})

/** Palette + drawer, both mounted, with the palette's open state driven from
 *  outside so this block tests Escape and nothing else. */
function TwoLayers({ paletteOpen }: { paletteOpen: boolean }) {
  const [open, setOpen] = useState(paletteOpen)
  return (
    <>
      <SlashPalette
        agentSlug="riley"
        workspaceId="ws-test"
        onCommand={vi.fn()}
        onAction={vi.fn()}
        open={open}
        onOpenChange={setOpen}
      />
      <RightDrawer>panel</RightDrawer>
    </>
  )
}

describe("Escape closes exactly one thing", () => {
  it("closes the chat palette and leaves the right drawer open", async () => {
    useDrawerStore.setState({ open: true })
    render(<TwoLayers paletteOpen />)
    expect(chatPaletteOpen()).toBe(true)

    press("Escape", "Escape")

    await waitFor(() => expect(chatPaletteOpen()).toBe(false))
    // The drawer is BELOW the palette in the layering, so it must not have
    // heard the same keystroke. This is what failed — one Escape closed both.
    expect(useDrawerStore.getState().open).toBe(true)
  })

  it("closes the right drawer when the drawer is the topmost thing open", async () => {
    useDrawerStore.setState({ open: true })
    render(<TwoLayers paletteOpen={false} />)

    press("Escape", "Escape")

    await waitFor(() => expect(useDrawerStore.getState().open).toBe(false))
  })
})
