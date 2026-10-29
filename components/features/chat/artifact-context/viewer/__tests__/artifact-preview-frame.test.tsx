import { act, cleanup, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { ArtifactPreviewFrame } from "../artifact-preview-frame"

afterEach(cleanup)

function setup(selectMode = true) {
  const onSelect = vi.fn()
  const onNavigate = vi.fn()
  render(<ArtifactPreviewFrame html="<body><h1 id='hero'>Hi</h1></body>" screens={["index.html", "pricing.html"]} path="index.html" selectMode={selectMode} title="Preview of Site" onSelect={onSelect} onNavigate={onNavigate} />)
  const frame = screen.getByTitle("Preview of Site") as HTMLIFrameElement
  const send = (data: unknown, source: MessageEventSource | null) => act(() => { window.dispatchEvent(new MessageEvent("message", { data, source })) })
  return { frame, onSelect, onNavigate, send }
}

it("is sandboxed to scripts only, without referrer", () => {
  const { frame } = setup()
  expect(frame.getAttribute("sandbox")).toBe("allow-scripts")
  expect(frame.getAttribute("referrerpolicy")).toBe("no-referrer")
  expect(frame.getAttribute("srcdoc")).toContain("crewship-artifact-select")
})

it("ignores messages that do not come from its own frame", () => {
  const { onSelect, onNavigate, send } = setup()
  send({ type: "crewship-artifact-select", selector: "#hero", quote: "Hi" }, window)
  send({ type: "crewship-artifact-select", selector: "#hero", quote: "Hi" }, null)
  send({ type: "crewship-artifact-navigate", path: "pricing.html" }, window)
  expect(onSelect).not.toHaveBeenCalled()
  expect(onNavigate).not.toHaveBeenCalled()
})

it("accepts selections and screen links from its frame", () => {
  const { frame, onSelect, onNavigate, send } = setup()
  send({ type: "crewship-artifact-select", selector: "#hero", quote: "  Hi \n there " }, frame.contentWindow)
  expect(onSelect).toHaveBeenCalledWith({ selector: "#hero", quote: "Hi there" })
  send({ type: "crewship-artifact-navigate", path: "pricing.html" }, frame.contentWindow)
  expect(onNavigate).toHaveBeenCalledWith("pricing.html")
})

it("rejects invalid selectors, unknown screens and selections outside select mode", () => {
  const { frame, onSelect, onNavigate, send } = setup()
  send({ type: "crewship-artifact-select", selector: "#a .b", quote: "x" }, frame.contentWindow)
  send({ type: "crewship-artifact-navigate", path: "https://evil.example/" }, frame.contentWindow)
  expect(onSelect).not.toHaveBeenCalled()
  expect(onNavigate).not.toHaveBeenCalled()
  cleanup()
  const off = setup(false)
  off.send({ type: "crewship-artifact-select", selector: "#hero", quote: "x" }, off.frame.contentWindow)
  expect(off.onSelect).not.toHaveBeenCalled()
})
