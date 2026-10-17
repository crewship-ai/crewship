import { afterEach, describe, expect, it, vi } from "vitest"
import { render, cleanup } from "@testing-library/react"
import { initialFocusedCard } from "../settings-layout"
import { useFocusSettingsCard } from "../use-focus-settings-card"
import { SettingsCard, SettingsDangerCard, settingsCardId, settingsCardSlug } from "../shared"

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

  // Harbor 2 (#3487 SET-04, settings.md T09): the mark is a 2 px ring in the
  // brand ring colour that follows the card's corners, for 1.5 s.
  it("keeps the mark for 1.5 seconds", () => {
    vi.useFakeTimers()
    Element.prototype.scrollIntoView = vi.fn()
    render(<><Probe card="identity" tab="general" /><SettingsCard title="Identity"><p>x</p></SettingsCard></>)
    vi.advanceTimersByTime(100)
    const el = document.getElementById("settings-card-identity")!
    expect(el.dataset.focused).toBe("true")
    vi.advanceTimersByTime(1400)
    expect(el.dataset.focused).toBe("true")
    vi.advanceTimersByTime(150)
    expect(el.dataset.focused).toBeUndefined()
  })

  it("draws the mark as a card-shaped 2 px outline in the ring colour", () => {
    render(<><SettingsCard title="Identity"><p>x</p></SettingsCard><SettingsDangerCard title="Danger zone"><p>y</p></SettingsDangerCard></>)
    for (const id of ["settings-card-identity", "settings-card-danger-zone"]) {
      const cls = document.getElementById(id)!.className
      // An outline follows border-radius and is not clipped by the card's own
      // overflow; a 50 % ring read as a grey box at the card's corners.
      expect(cls).toContain("rounded-card")
      expect(cls).toContain("data-[focused=true]:outline-2")
      expect(cls).toContain("data-[focused=true]:outline-offset-2")
      expect(cls).not.toMatch(/ring-(primary|destructive)\/50/)
    }
    expect(document.getElementById("settings-card-identity")!.className).toContain("data-[focused=true]:outline-ring")
    expect(document.getElementById("settings-card-danger-zone")!.className).toContain("data-[focused=true]:outline-destructive")
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
