import { afterEach, expect, it, vi } from "vitest"

afterEach(() => { document.documentElement.removeAttribute("style"); vi.resetModules() })
it("applies validated palette tokens before notifying the custom application", async () => {
 const port = { onmessage: null as ((event: { data: unknown }) => void) | null, start: vi.fn(), postMessage: vi.fn() }
 window.__crewshipPagesPort = port as unknown as MessagePort
 const sdk = await import("../../../tools/pages-build/sdk")
 const listener = vi.fn(() => expect(document.documentElement.style.getPropertyValue("--crewship-page-accent")).toBe("#123abc"))
 sdk.subscribe(listener)
 port.onmessage?.({ data: { type: "crewship.pages.snapshot/v1", seq: 1, snapshot: { slug: "ops", name: "Ops", panels: [], theme: { accent: "#123abc", background: "#ffffff", text: "#000000", surface: "#eeeeee", muted: "#333333", border: "url(https://example.com)" } } } })
 expect(listener).toHaveBeenCalledOnce()
 expect(document.documentElement.style.colorScheme).toBe("light")
 expect(document.documentElement.style.getPropertyValue("--crewship-page-on-accent")).toBe("#ffffff")
 expect(document.documentElement.style.getPropertyValue("--crewship-page-border")).toBe("")
 expect(port.postMessage).toHaveBeenCalledWith({ type: "crewship.pages.snapshot-ack/v1", seq: 1 })
})
