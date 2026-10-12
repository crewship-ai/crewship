import { afterEach, describe, expect, it, vi } from "vitest"
import { render, cleanup } from "@testing-library/react"
import { initialFocusedCard } from "../settings-layout"
import { useFocusSettingsCard } from "../use-focus-settings-card"
import { SettingsCard, settingsCardId, settingsCardSlug } from "../shared"

afterEach(() => { cleanup(); vi.useRealTimers() })

describe("settings card anchors", () => {
  it("slugs a title the way ⌘K links to it", () => {
    expect(settingsCardSlug("Danger zone")).toBe("danger-zone")
    expect(settingsCardSlug("Sessions & access")).toBe("sessions-and-access")
    expect(settingsCardId("Who may reveal")).toBe("settings-card-who-may-reveal")
  })

  it("reads ?card= next to ?tab=", () => {
    expect(initialFocusedCard("?tab=general&card=danger-zone")).toBe("danger-zone")
    expect(initialFocusedCard("?tab=general")).toBe("")
  })
})

function Probe({ card, tab }: { card: string; tab: string }) {
  useFocusSettingsCard(card, tab)
  return null
}

describe("useFocusSettingsCard", () => {
  it("scrolls to the card once it has rendered, and marks it for a moment", () => {
    vi.useFakeTimers()
    const scroll = vi.fn()
    Element.prototype.scrollIntoView = scroll
    const { rerender } = render(<Probe card="danger-zone" tab="general" />)
    // The pane loads after the tab switches: the card is not there yet.
    vi.advanceTimersByTime(150)
    expect(scroll).not.toHaveBeenCalled()
    rerender(
      <>
        <Probe card="danger-zone" tab="general" />
        <SettingsCard title="Danger zone"><p>x</p></SettingsCard>
      </>,
    )
    vi.advanceTimersByTime(150)
    expect(scroll).toHaveBeenCalledTimes(1)
    const el = document.getElementById("settings-card-danger-zone")!
    expect(el.dataset.focused).toBe("true")
    vi.advanceTimersByTime(3000)
    expect(el.dataset.focused).toBeUndefined()
  })

  it("does nothing without a card", () => {
    vi.useFakeTimers()
    const scroll = vi.fn()
    Element.prototype.scrollIntoView = scroll
    render(<><Probe card="" tab="general" /><SettingsCard title="Identity"><p>x</p></SettingsCard></>)
    vi.advanceTimersByTime(3000)
    expect(scroll).not.toHaveBeenCalled()
  })
})
