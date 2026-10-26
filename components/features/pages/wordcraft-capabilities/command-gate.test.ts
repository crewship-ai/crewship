import { describe, expect, it, vi } from "vitest"
import { createWordCraftCommandGate, defaultWordCraftCatalog, DENIED_LABEL, wordCraftShortcutHandler, type CapabilityCatalog, type ParticipantCapabilities } from "./command-gate"

const participant: ParticipantCapabilities = { engineSha: defaultWordCraftCatalog.engine.sha, catalogVersion: defaultWordCraftCatalog.version, allowedCommands: ["format.bold", "text.insert", "file.open"] }
const qualified = (ids: string[]): CapabilityCatalog => ({ ...defaultWordCraftCatalog, commands: defaultWordCraftCatalog.commands.map(command => ids.includes(command.id) ? { ...command, qualification: "qualified" } : command) })
const event = (overrides: object = {}) => ({ key: "b", ctrlKey: true, metaKey: false, altKey: false, shiftKey: false, preventDefault: vi.fn(), stopPropagation: vi.fn(), ...overrides })

describe("WordCraft command gate", () => {
  it("denies the complete initial catalog even to a participant with every command", () => {
    const dispatch = vi.fn()
    const gate = createWordCraftCommandGate({ participant: { ...participant, allowedCommands: defaultWordCraftCatalog.commands.map(command => command.id) }, dispatch })
    for (const command of defaultWordCraftCatalog.commands) {
      expect(gate.availability(command.id)).toEqual({ enabled: false, reason: DENIED_LABEL })
      expect(gate.execute(command.id)).toBe(false)
    }
    expect(dispatch).not.toHaveBeenCalled()
  })
  it("requires both qualification and an effective role grant", () => {
    const dispatch = vi.fn(), catalog = qualified(["format.bold", "format.italic"])
    const gate = createWordCraftCommandGate({ catalog, participant, dispatch })
    expect(gate.execute("format.bold")).toBe(true)
    expect(dispatch).toHaveBeenCalledWith("format.bold", {})
    expect(gate.execute("format.italic")).toBe(false)
    expect(gate.execute("text.insert", { text: "draft" })).toBe(false)
    expect(createWordCraftCommandGate({ catalog, dispatch }).execute("format.bold")).toBe(false)
  })
  it("refuses another engine, catalog version, unknown IDs and unresolved schemas", () => {
    const catalog = qualified(["format.bold", "format.color"]), dispatch = vi.fn()
    for (const badParticipant of [{ ...participant, engineSha: "another" }, { ...participant, catalogVersion: "another" }]) {
      expect(createWordCraftCommandGate({ catalog, participant: badParticipant, dispatch }).execute("format.bold")).toBe(false)
    }
    const gate = createWordCraftCommandGate({ catalog, participant: { ...participant, allowedCommands: [...participant.allowedCommands, "format.color", "unknown"] }, dispatch })
    expect(gate.execute("format.color")).toBe(false)
    expect(gate.execute("unknown")).toBe(false)
    expect(dispatch).not.toHaveBeenCalled()
  })
  it("keeps permanent prohibitions even if a supplied catalog tries to qualify them", () => {
    const catalog = { ...defaultWordCraftCatalog, commands: defaultWordCraftCatalog.commands.map(command => ({ ...command, qualification: "qualified", permanentlyForbidden: false })) }
    const dispatch = vi.fn()
    const gate = createWordCraftCommandGate({ catalog, participant: { ...participant, allowedCommands: catalog.commands.map(command => command.id) }, dispatch })
    for (const command of defaultWordCraftCatalog.commands.filter(command => command.permanentlyForbidden)) expect(gate.execute(command.id)).toBe(false)
    expect(dispatch).not.toHaveBeenCalled()
  })
  it("rejects extra or invalid parameter values at the shared dispatch boundary", () => {
    const dispatch = vi.fn(), gate = createWordCraftCommandGate({ catalog: qualified(["text.insert"]), participant, dispatch })
    for (const parameters of [{}, { text: 3 }, { text: "hello", nestedCommand: "file.open" }]) expect(gate.execute("text.insert", parameters)).toBe(false)
    expect(gate.execute("text.insert", { text: "Žluťoučký kůň" })).toBe(true)
    expect(dispatch).toHaveBeenCalledExactlyOnceWith("text.insert", { text: "Žluťoučký kůň" })
  })
  it("consumes denied shortcuts without sending a command", () => {
    const dispatch = vi.fn(), e = event()
    const handler = wordCraftShortcutHandler({ gate: createWordCraftCommandGate({ participant, dispatch }) })
    expect(handler(e)).toBe(true)
    expect(e.preventDefault).toHaveBeenCalledOnce()
    expect(e.stopPropagation).toHaveBeenCalledOnce()
    expect(dispatch).not.toHaveBeenCalled()
  })
  it("executes qualified shortcuts through the same gate and exact modifiers", () => {
    const catalog = qualified(["format.bold"]), dispatch = vi.fn(), gate = createWordCraftCommandGate({ catalog, participant, dispatch })
    const handler = wordCraftShortcutHandler({ catalog, gate })
    expect(handler(event({ altKey: true }))).toBe(false)
    expect(handler(event())).toBe(true)
    expect(dispatch).toHaveBeenCalledExactlyOnceWith("format.bold", {})
    const mac = wordCraftShortcutHandler({ catalog, gate, platform: "mac" })
    expect(mac(event())).toBe(false)
    expect(mac(event({ ctrlKey: false, metaKey: true }))).toBe(true)
  })
  it("consumes DOM arrow names and shifted glyphs for denied registry shortcuts", () => {
    const dispatch = vi.fn(), handler = wordCraftShortcutHandler({ gate: createWordCraftCommandGate({ participant, dispatch }) })
    for (const overrides of [
      { key: "ArrowLeft", code: "ArrowLeft", ctrlKey: false },
      { key: "ArrowRight", code: "ArrowRight" },
      { key: "ArrowUp", code: "ArrowUp", ctrlKey: false },
      { key: "ArrowDown", code: "ArrowDown", ctrlKey: false },
      { key: "+", code: "Equal", shiftKey: true },
      { key: "+", code: "Equal", ctrlKey: false, altKey: true, shiftKey: true },
      { key: "*", code: "Digit8", shiftKey: true },
    ]) {
      const e = event(overrides)
      expect(handler(e)).toBe(true)
      expect(e.preventDefault).toHaveBeenCalledOnce()
    }
    expect(dispatch).not.toHaveBeenCalled()
  })
  it("still consumes forbidden native bindings omitted by a supplied catalog", () => {
    const dispatch = vi.fn(), catalog = { ...defaultWordCraftCatalog, commands: [] }
    const handler = wordCraftShortcutHandler({ catalog, gate: createWordCraftCommandGate({ catalog, participant, dispatch }) })
    const e = event({ key: "o" })
    expect(handler(e)).toBe(true)
    expect(e.preventDefault).toHaveBeenCalledOnce()
    expect(dispatch).not.toHaveBeenCalled()
  })
  it("preserves IME composition and refuses ambiguous mappings", () => {
    const catalog = qualified(["format.bold"]), dispatch = vi.fn(), gate = createWordCraftCommandGate({ catalog, participant, dispatch })
    const handler = wordCraftShortcutHandler({ catalog, gate })
    expect(handler(event({ isComposing: true }))).toBe(false)
    expect(handler(event({ nativeEvent: { isComposing: true } }))).toBe(false)
    const ambiguous = { ...catalog, commands: [...catalog.commands, { ...catalog.commands.find(command => command.id === "format.bold")!, id: "unknown.duplicate" }] }
    expect(wordCraftShortcutHandler({ catalog: ambiguous, gate })(event())).toBe(true)
    expect(dispatch).not.toHaveBeenCalled()
  })
})
