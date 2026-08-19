import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, within } from "@testing-library/react"
import { readFileSync, existsSync } from "node:fs"
import path from "node:path"

// =============================================================================
// The palette's honesty contract (audit P0.8).
//
// Every action this palette can put on screen — the client-side rows AND the
// server-driven catalogue — must be in exactly ONE of three states:
//
//   · enabled  — selecting it performs the effect its label advertises;
//   · disabled — it is rendered, visibly disabled, with a reason on screen;
//   · hidden   — it is not rendered at all.
//
// "Closes the palette and does nothing" is not one of them. That was the
// defect: branch / search / export / run-task were rendered, called
// `onCommand`, and the panel's handler covered only regenerate and clear.
//
// The classification lives next to the palette (CLIENT_ACTION_CONTRACT /
// SERVER_ACTION_CONTRACT). This file is the gate: an action that exists in
// either source but carries no classification fails here, so a new row —
// client-side or added to the Go catalogue by a backend change — cannot ship
// as a silent no-op.
// =============================================================================

const push = vi.fn()
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push, replace: vi.fn(), prefetch: vi.fn(), back: vi.fn(), forward: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  usePathname: () => "/",
}))

const toggleDrawer = vi.fn()
vi.mock("@/stores/drawer-store", () => ({
  useDrawerStore: (selector: (s: unknown) => unknown) => selector({ toggle: toggleDrawer }),
}))

const serverCatalog = vi.hoisted(() => ({ list: [] as Array<Record<string, unknown>> }))
vi.mock("@/hooks/use-slash-commands", () => ({
  useSlashCommands: () => ({ data: serverCatalog.list }),
}))

import {
  SlashPalette,
  CLIENT_ACTION_CONTRACT,
  SERVER_ACTION_CONTRACT,
  CLIENT_COMMAND_IDS,
  PANEL_HANDLED_COMMAND_IDS,
  type SlashActionClassification,
} from "../slash-palette"

/** Pull the ids out of the Go catalogue's `slashCommandCatalog` literal. The
 *  server is the other half of this surface: an entry added there renders in
 *  the Actions group with no frontend change at all, which is precisely how an
 *  unclassified action would sneak back in. */
function catalogIdsFrom(source: string): string[] {
  const start = source.indexOf("var slashCommandCatalog")
  if (start < 0) throw new Error("slashCommandCatalog literal not found in the handler source")
  return [...source.slice(start).matchAll(/^\s*ID:\s*"([^"]+)"/gm)].map((m) => m[1])
}

// The override exists so this guard can be PROVEN to bite against a real
// catalogue file without editing the shipped one:
//   SLASH_CATALOGUE_PATH=/tmp/doctored.go pnpm vitest run <this file>
// Unset in CI and everywhere else, where it reads the real handler.
const HANDLER_GO = path.resolve(
  process.cwd(),
  process.env.SLASH_CATALOGUE_PATH ?? "internal/api/slash_commands_handler.go",
)

function serverCatalogIds(): string[] {
  if (!existsSync(HANDLER_GO)) {
    throw new Error(`slash command catalogue not found at ${HANDLER_GO}`)
  }
  return catalogIdsFrom(readFileSync(HANDLER_GO, "utf8"))
}

/** The guard, as one function, so the assertion against the real catalogue and
 *  the self-check below run the SAME code. */
function unclassifiedInCatalogue(source: string): string[] {
  return catalogIdsFrom(source).filter((id) => !SERVER_ACTION_CONTRACT[id])
}

function unclassifiedClientRows(ids: string[]): string[] {
  return ids.filter((id) => !CLIENT_ACTION_CONTRACT[id])
}

function renderPalette(props: Record<string, unknown> = {}) {
  return render(
    <SlashPalette
      open
      agentSlug="riley"
      workspaceId="ws-1"
      onCommand={props.onCommand as never}
      onAction={props.onAction as never}
      {...props}
    />,
  )
}

/** The effect an enabled client row must produce when selected. Rows handled
 *  by the panel assert the delegation only; that the panel then DOES something
 *  is pinned by chat-panel-slash-actions.test.tsx, which walks the same
 *  PANEL_HANDLED_COMMAND_IDS list. */
const CLIENT_EFFECT: Record<string, (onCommand: ReturnType<typeof vi.fn>) => void> = {
  clear: (onCommand) => expect(onCommand).toHaveBeenCalledWith("clear"),
  regenerate: (onCommand) => expect(onCommand).toHaveBeenCalledWith("regenerate"),
  search: (onCommand) => expect(onCommand).toHaveBeenCalledWith("search"),
  export: (onCommand) => expect(onCommand).toHaveBeenCalledWith("export"),
  "open-files": () => expect(toggleDrawer).toHaveBeenCalledWith("files"),
  "toggle-drawer": () => expect(toggleDrawer).toHaveBeenCalled(),
}

beforeEach(() => {
  push.mockClear()
  toggleDrawer.mockClear()
  serverCatalog.list = []
})

describe("slash palette — every action is classified", () => {
  it("classifies every client-side row it renders", () => {
    for (const id of CLIENT_COMMAND_IDS) {
      const entry = CLIENT_ACTION_CONTRACT[id]
      expect(entry, `client command "${id}" has no entry in CLIENT_ACTION_CONTRACT`).toBeDefined()
      expect(entry.state, `client command "${id}" is rendered but classified hidden`).not.toBe("hidden")
    }
  })

  it("renders nothing it classified as hidden, and renders everything it did not", () => {
    for (const [id, entry] of Object.entries(CLIENT_ACTION_CONTRACT)) {
      const rendered = CLIENT_COMMAND_IDS.includes(id)
      expect(rendered, `"${id}" is classified ${entry.state} but ${rendered ? "IS" : "is NOT"} rendered`)
        .toBe(entry.state !== "hidden")
    }
  })

  it("classifies every entry in the server catalogue (internal/api/slash_commands_handler.go)", () => {
    const ids = serverCatalogIds()
    expect(ids.length, "parsed no ids out of the catalogue — the parser is broken").toBeGreaterThan(0)
    expect(unclassifiedInCatalogue(readFileSync(HANDLER_GO, "utf8"))).toEqual([])
  })

  // The guards have to bite, or they are decoration. Same functions, one
  // fabricated catalogue and one fabricated row.
  it("the catalogue guard itself fails on an unclassified entry", () => {
    const fabricated = `
var slashCommandCatalog = []slashCommand{
	{
		ID:         "routine",
		Capability: CapabilityRoutineCreate,
	},
	{
		ID:         "summon-kraken",
		Capability: CapabilityKraken,
	},
}`
    expect(catalogIdsFrom(fabricated)).toContain("summon-kraken")
    expect(unclassifiedInCatalogue(fabricated)).toEqual(["summon-kraken"])
  })

  it("the client-row guard itself fails on an unclassified row", () => {
    expect(unclassifiedClientRows(CLIENT_COMMAND_IDS)).toEqual([])
    expect(unclassifiedClientRows([...CLIENT_COMMAND_IDS, "make-coffee"])).toEqual(["make-coffee"])
  })
})

describe("slash palette — nothing on screen is a no-op", () => {
  // Enumerated from the DOM rather than from the contract, so a row that
  // reaches the screen by any route still has to be one of the three states.
  it("every rendered row either runs or says why it cannot", () => {
    serverCatalog.list = serverCatalogIds().map((id) => ({ id, label: `Server ${id}`, capability: `${id}.create` }))
    const onCommand = vi.fn()
    const onAction = vi.fn()
    renderPalette({ onCommand, onAction })

    const rows = [...document.body.querySelectorAll<HTMLElement>(
      '[data-testid^="slash-item-"], [data-testid^="slash-action-"]',
    )]
    expect(rows.length).toBeGreaterThan(0)

    for (const row of rows) {
      const testid = row.dataset.testid!
      const id = testid.replace(/^slash-(item|action)-/, "")
      if (row.getAttribute("aria-disabled") === "true") {
        const reason = row.querySelector("[data-slash-reason]")
        expect(reason, `"${testid}" is disabled with no reason on screen`).not.toBeNull()
        expect(reason!.textContent?.trim().length, `"${testid}" has an empty reason`).toBeGreaterThan(0)
        continue
      }
      const runnable = testid.startsWith("slash-item-")
        ? Boolean(CLIENT_EFFECT[id])
        : SERVER_ACTION_CONTRACT[id]?.state === "enabled"
      expect(runnable, `"${testid}" is offered as runnable but nothing runs it`).toBe(true)
    }
  })
})

describe("slash palette — the three states, as rendered", () => {
  it.each(Object.entries(CLIENT_ACTION_CONTRACT))(
    "client row %s is in exactly its declared state",
    (id, entry: SlashActionClassification) => {
      const onCommand = vi.fn()
      renderPalette({ onCommand })
      const item = screen.queryByTestId(`slash-item-${id}`)

      if (entry.state === "hidden") {
        expect(item).toBeNull()
        return
      }
      expect(item, `"${id}" is classified ${entry.state} but is not on screen`).not.toBeNull()

      if (entry.state === "disabled") {
        expect(item).toHaveAttribute("aria-disabled", "true")
        // The reason must be readable, not a title attribute nobody sees.
        expect(within(item!).getByText(entry.reason)).toBeInTheDocument()
        fireEvent.click(item!)
        expect(onCommand).not.toHaveBeenCalled()
        expect(toggleDrawer).not.toHaveBeenCalled()
        expect(push).not.toHaveBeenCalled()
        return
      }

      expect(item).not.toHaveAttribute("aria-disabled", "true")
      const assertEffect = CLIENT_EFFECT[id]
      expect(assertEffect, `"${id}" is classified enabled but this test declares no effect for it`).toBeDefined()
      fireEvent.click(item!)
      assertEffect(onCommand)
    },
  )

  it.each(Object.entries(SERVER_ACTION_CONTRACT))(
    "server action %s is in exactly its declared state",
    (id, entry: SlashActionClassification) => {
      serverCatalog.list = [{ id, label: `Server ${id}`, capability: `${id}.create` }]
      const onAction = vi.fn()
      renderPalette({ onAction })
      const item = screen.queryByTestId(`slash-action-${id}`)

      if (entry.state === "hidden") {
        expect(item).toBeNull()
        return
      }
      expect(item, `"${id}" is classified ${entry.state} but is not on screen`).not.toBeNull()

      if (entry.state === "disabled") {
        expect(item).toHaveAttribute("aria-disabled", "true")
        expect(within(item!).getByText(entry.reason)).toBeInTheDocument()
        fireEvent.click(item!)
        expect(onAction).not.toHaveBeenCalled()
        return
      }

      expect(item).not.toHaveAttribute("aria-disabled", "true")
      fireEvent.click(item!)
      expect(onAction).toHaveBeenCalledWith(expect.objectContaining({ id }))
    },
  )

  // A server newer than this build. It cannot be trusted to a modal that has
  // no endpoint mapping for it, and it must not be a silent no-op either.
  it("renders an action this build has never heard of as disabled, with a reason", () => {
    serverCatalog.list = [{ id: "summon-kraken", label: "Summon the kraken", capability: "kraken.summon" }]
    const onAction = vi.fn()
    renderPalette({ onAction })

    const item = screen.getByTestId("slash-action-summon-kraken")
    expect(item).toHaveAttribute("aria-disabled", "true")
    expect(within(item).getByText(/this build/i)).toBeInTheDocument()
    fireEvent.click(item)
    expect(onAction).not.toHaveBeenCalled()
  })

  it("hands the picked action to the parent, which owns the modal", () => {
    serverCatalog.list = [
      { id: "skill", label: "Create skill from this conversation", capability: "skill.create", form_schema: [{ name: "slug", type: "slug" }] },
    ]
    const onAction = vi.fn()
    renderPalette({ onAction })

    fireEvent.click(screen.getByTestId("slash-action-skill"))
    expect(onAction).toHaveBeenCalledWith(expect.objectContaining({ id: "skill", form_schema: expect.any(Array) }))
  })

  it("says nothing about server actions when there is no workspace to scope them to", () => {
    serverCatalog.list = []
    renderPalette({ workspaceId: undefined })
    expect(screen.queryByTestId("slash-action-skill")).toBeNull()
    // The client rows are unaffected — a palette without a workspace still works.
    expect(screen.getByTestId("slash-item-clear")).toBeInTheDocument()
  })

  it("disables a row the host says is unavailable right now, with the host's reason", () => {
    const onCommand = vi.fn()
    renderPalette({ onCommand, disabledCommands: { regenerate: "No response to regenerate yet" } })

    const item = screen.getByTestId("slash-item-regenerate")
    expect(item).toHaveAttribute("aria-disabled", "true")
    expect(within(item).getByText("No response to regenerate yet")).toBeInTheDocument()
    fireEvent.click(item)
    expect(onCommand).not.toHaveBeenCalled()
  })

  // ⌘/, not ⌘K. ⌘K is the toolbar's global palette — the one whose key is
  // printed in the search button's <kbd> on every route — and this palette
  // used to answer to it as well, so a conversation opened both dialogs at
  // once. Which surface a keystroke reaches is pinned in
  // components/features/chat/__tests__/chat-hotkey-ownership.test.tsx; this
  // case only holds the palette's own end of it.
  it("opens on ⌘/ / ctrl+/", () => {
    render(<SlashPalette agentSlug="riley" workspaceId="ws-1" onCommand={vi.fn()} onAction={vi.fn()} />)
    expect(screen.queryByTestId("slash-item-clear")).toBeNull()

    // react-hotkeys-hook matches on `code`, not `key`, unless useKey is set —
    // an event without one never matches anything. `mod` is meta on a Mac and
    // ctrl everywhere else, so both are fired: one of them is this platform's.
    fireEvent.keyDown(document, { key: "/", code: "Slash", metaKey: true })
    fireEvent.keyDown(document, { key: "/", code: "Slash", ctrlKey: true })

    expect(screen.getByTestId("slash-item-clear")).toBeInTheDocument()
  })

  it("no longer answers to ⌘K, which belongs to the global palette", () => {
    render(<SlashPalette agentSlug="riley" workspaceId="ws-1" onCommand={vi.fn()} onAction={vi.fn()} />)

    fireEvent.keyDown(document, { key: "k", code: "KeyK", metaKey: true })
    fireEvent.keyDown(document, { key: "k", code: "KeyK", ctrlKey: true })

    expect(screen.queryByTestId("slash-item-clear")).toBeNull()
  })
})

describe("slash palette — the delegated rows are named for the host", () => {
  it("exports exactly the enabled rows whose effect the host must implement", () => {
    // chat-panel-slash-actions.test.tsx walks this list and asserts each one
    // produces an observable effect in the panel. A new panel-handled row is
    // therefore red in that file until someone implements it.
    expect(PANEL_HANDLED_COMMAND_IDS.length).toBeGreaterThan(0)
    for (const id of PANEL_HANDLED_COMMAND_IDS) {
      expect(CLIENT_ACTION_CONTRACT[id].state).toBe("enabled")
      expect(CLIENT_COMMAND_IDS).toContain(id)
    }
  })
})
