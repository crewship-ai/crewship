import { beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"

const mocks = vi.hoisted(() => ({
  userId: "viewer", workspaceId: "w", ready: false,
  prefs: new Map<string, { enabled: boolean; dnd: boolean; volume: number; chat: string; inbox: string }>(),
  unlock: vi.fn(), play: vi.fn(), save: vi.fn(),
}))
vi.mock("@/hooks/use-auth", () => ({ useAuth: () => ({ session: mocks.userId ? { user: { id: mocks.userId } } : null }) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: mocks.workspaceId }) }))
vi.mock("@/lib/notification-sounds", () => ({
  SOUND_PRESETS: ["soft-pop", "glass", "chime", "inbox-drop", "attention"].map(id => ({ id, label: id })),
  SOUND_PREFERENCES_EVENT: "sound-preferences",
  readSoundPreferences: (scope: string) => mocks.prefs.get(scope) ?? { enabled: false, dnd: false, volume: .35, chat: "soft-pop", inbox: "chime" },
  saveSoundPreferences: mocks.save,
  unlockNotificationAudio: mocks.unlock,
  playNotificationSound: mocks.play,
  isNotificationAudioReady: () => mocks.ready,
}))
import { NotificationSoundSettings } from "../notification-sound-settings"
const scope = JSON.stringify(["viewer", "w"])
function open() { /* Settings renders the controls inline. */ }
beforeEach(() => {
  const storage = new Map<string, string>()
  vi.mocked(localStorage.getItem).mockImplementation(key => storage.get(key) ?? null)
  vi.mocked(localStorage.setItem).mockImplementation((key, value) => { storage.set(key, value) })
  vi.mocked(localStorage.removeItem).mockImplementation(key => { storage.delete(key) })
  Object.defineProperty(navigator, "locks", { configurable: true, value: { request: vi.fn() } })
  cleanup(); mocks.prefs.clear(); mocks.userId = "viewer"; mocks.workspaceId = "w"; mocks.ready = false
  mocks.unlock.mockReset().mockResolvedValue(true); mocks.play.mockReset().mockResolvedValue(true)
  mocks.save.mockReset().mockImplementation((key, prefs) => { mocks.prefs.set(key, prefs); return prefs })
})
describe("Personal notification sounds", () => {
  it("offers accessible separate sound choices to a viewer without playing on arrival or enable", async () => {
    render(<NotificationSoundSettings />); open()
    expect(screen.getByRole("region", { name: "Notification sounds" })).toBeVisible()
    expect(screen.getByRole("combobox", { name: "Chat sound" })).toHaveValue("soft-pop")
    expect(screen.getByRole("combobox", { name: "Inbox sound" })).toHaveValue("chime")
    expect(screen.getByRole("slider", { name: "Notification volume" })).toHaveValue("35")
    expect(screen.getAllByRole("option")).toHaveLength(12)
    expect(mocks.unlock).not.toHaveBeenCalled(); expect(mocks.play).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Enable sounds", exact: true }))
    expect(mocks.unlock).toHaveBeenCalledTimes(1) // starts in the original click task
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Audio is ready"))
    expect(mocks.prefs.get(scope)?.enabled).toBe(true); expect(mocks.play).not.toHaveBeenCalled()
  })
  it("persists volume, separate choices and DND while explicit Preview remains available", async () => {
    render(<NotificationSoundSettings />); open()
    fireEvent.change(screen.getByRole("slider"), { target: { value: "60" } })
    fireEvent.change(screen.getByRole("combobox", { name: "Chat sound" }), { target: { value: "glass" } })
    fireEvent.change(screen.getByRole("combobox", { name: "Inbox sound" }), { target: { value: "off" } })
    fireEvent.click(screen.getByRole("checkbox", { name: "Do not disturb" }))
    expect(mocks.prefs.get(scope)).toMatchObject({ volume: .6, chat: "glass", inbox: "off", dnd: true })
    expect(screen.getByRole("button", { name: "Preview Inbox sound" })).toBeDisabled()
    fireEvent.click(screen.getByRole("button", { name: "Preview Chat sound" }))
    expect(mocks.unlock).toHaveBeenCalledTimes(1)
    await waitFor(() => expect(mocks.play).toHaveBeenCalledWith("glass", .6))
    expect(mocks.prefs.get(scope)?.enabled).toBe(false) // preview is not an opt-in
  })
  it("reloads stored preferences and responds to storage and same-page events", () => {
    mocks.prefs.set(scope, { enabled: true, dnd: true, volume: .8, chat: "attention", inbox: "glass" })
    const view = render(<NotificationSoundSettings />); open()
    expect(screen.getByRole("checkbox")).toBeChecked()
    expect(screen.getByRole("slider")).toHaveValue("80")
    mocks.prefs.set(scope, { enabled: false, dnd: false, volume: .2, chat: "off", inbox: "chime" })
    act(() => window.dispatchEvent(new Event("storage")))
    expect(screen.getByRole("checkbox")).not.toBeChecked(); expect(screen.getByRole("slider")).toHaveValue("20")
    mocks.prefs.set(scope, { enabled: false, dnd: false, volume: .4, chat: "glass", inbox: "chime" })
    act(() => window.dispatchEvent(new Event("sound-preferences")))
    expect(screen.getByRole("slider")).toHaveValue("40")
    view.unmount(); render(<NotificationSoundSettings />); open()
    expect(screen.getByRole("combobox", { name: "Chat sound" })).toHaveValue("glass")
    expect(mocks.play).not.toHaveBeenCalled()
  })
  it("isolates workspace and account preferences and ignores late unlocks after scope changes", async () => {
    let resolve!: (value: boolean) => void
    mocks.unlock.mockImplementation(() => new Promise<boolean>(r => { resolve = r }))
    const view = render(<NotificationSoundSettings />); open()
    fireEvent.click(screen.getByRole("button", { name: "Preview Chat sound" }))
    mocks.workspaceId = "other"; view.rerender(<NotificationSoundSettings />); open()
    expect(screen.getByRole("slider")).toHaveValue("35")
    await act(async () => { resolve(true) })
    expect(mocks.play).not.toHaveBeenCalled()
    fireEvent.change(screen.getByRole("slider"), { target: { value: "10" } })
    expect(mocks.prefs.get(JSON.stringify(["viewer", "other"]))?.volume).toBe(.1)
    mocks.userId = "another"; view.rerender(<NotificationSoundSettings />); open()
    expect(screen.getByRole("slider")).toHaveValue("35")
  })
  it("reports blocked or failed audio honestly and can retry activation", async () => {
    mocks.unlock.mockResolvedValueOnce(false).mockRejectedValueOnce(new Error("blocked")).mockResolvedValue(true)
    render(<NotificationSoundSettings />); open()
    fireEvent.click(screen.getByRole("button", { name: "Enable sounds", exact: true }))
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("browser blocked audio"))
    expect(mocks.play).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Activate audio", exact: true }))
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("Audio is unavailable"))
    fireEvent.click(screen.getByRole("button", { name: "Activate audio", exact: true }))
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Audio is ready"))
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
  })
  it("reports failed previews and persistence exceptions without breaking the controls", async () => {
    mocks.play.mockResolvedValue(false); mocks.save.mockImplementation(() => { throw new Error("denied") })
    render(<NotificationSoundSettings />); open()
    fireEvent.click(screen.getByRole("checkbox"))
    expect(screen.getByRole("alert")).toHaveTextContent("could not be saved")
    fireEvent.click(screen.getByRole("button", { name: "Preview Chat sound" }))
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("preview could not play"))
  })
  it("treats zero volume as intentional silence and does not try a preview", () => {
    render(<NotificationSoundSettings />); open()
    fireEvent.change(screen.getByRole("slider"), { target: { value: "0" } })
    expect(screen.getByRole("button", { name: "Preview Chat sound" })).toBeDisabled()
    expect(screen.getByRole("button", { name: "Preview Inbox sound" })).toBeDisabled()
    expect(screen.getByText(/Volume is 0%/)).toBeVisible()
    expect(mocks.play).not.toHaveBeenCalled()
  })
  it.each(["locks", "storage"])("does not claim automatic readiness without %s, but keeps previews available", async reason => {
    if (reason === "locks") Object.defineProperty(navigator, "locks", { configurable: true, value: undefined })
    else vi.mocked(localStorage.setItem).mockImplementation(() => { throw new Error("denied") })
    render(<NotificationSoundSettings />); open()
    fireEvent.click(screen.getByRole("button", { name: "Enable sounds", exact: true }))
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Automatic notifications are unavailable"))
    expect(screen.getByRole("status")).not.toHaveTextContent("Audio is ready")
    fireEvent.click(screen.getByRole("button", { name: "Preview Chat sound" }))
    await waitFor(() => expect(mocks.play).toHaveBeenCalledWith("soft-pop", .35))
  })
  it("disables the entry point without an authenticated user or workspace", () => {
    mocks.userId = ""; const view = render(<NotificationSoundSettings />)
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument()
    mocks.userId = "viewer"; mocks.workspaceId = ""; view.rerender(<NotificationSoundSettings />)
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument()
  })
})
