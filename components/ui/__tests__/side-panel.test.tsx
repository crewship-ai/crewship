import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { SidePanel, SidePanelHeader, SidePanelBody, SidePanelFooter } from "../side-panel"
afterEach(cleanup)
it("exposes a named detail region with close controls, body and footer", () => {
  const close = vi.fn()
  render(<SidePanel open onClose={close} ariaLabel="Run details">
    <SidePanelHeader title="Deployment" subtitle="Run 123" onClose={close}><span>Header action</span></SidePanelHeader>
    <SidePanelBody className="extra-body">Execution log</SidePanelBody>
    <SidePanelFooter className="extra-footer">Total cost</SidePanelFooter>
  </SidePanel>)
  expect(screen.getByRole("complementary", { name: "Run details" })).toHaveStyle({ width: "420px" })
  for (const text of ["Deployment", "Run 123", "Header action", "Execution log", "Total cost"]) expect(screen.getByText(text)).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Close detail" }))
  expect(close).toHaveBeenCalledTimes(1)
  fireEvent.keyDown(window, { key: "Escape" })
  expect(close).toHaveBeenCalledTimes(2)
  fireEvent.keyDown(window, { key: "Enter" })
  expect(close).toHaveBeenCalledTimes(2)
})
it("subscribes only while open and replaces an abandoned close callback", async () => {
  const oldClose = vi.fn(), newClose = vi.fn()
  const { rerender, unmount } = render(<SidePanel open={false} onClose={oldClose} ariaLabel="Details">Content</SidePanel>)
  expect(screen.queryByRole("complementary")).not.toBeInTheDocument()
  fireEvent.keyDown(window, { key: "Escape" })
  expect(oldClose).not.toHaveBeenCalled()
  rerender(<SidePanel open onClose={oldClose} ariaLabel="Details">Content</SidePanel>)
  rerender(<SidePanel open onClose={newClose} ariaLabel="Details" side="left" width={320} className="extra-panel">Content</SidePanel>)
  const region = screen.getByRole("complementary")
  expect(region).toHaveClass("border-r", "extra-panel")
  expect(region).toHaveStyle({ width: "320px" })
  fireEvent.keyDown(window, { key: "Escape" })
  expect(oldClose).not.toHaveBeenCalled()
  expect(newClose).toHaveBeenCalledTimes(1)
  rerender(<SidePanel open={false} onClose={newClose} ariaLabel="Details">Content</SidePanel>)
  await waitFor(() => expect(screen.queryByRole("complementary")).not.toBeInTheDocument())
  fireEvent.keyDown(window, { key: "Escape" })
  expect(newClose).toHaveBeenCalledTimes(1)
  unmount()
  fireEvent.keyDown(window, { key: "Escape" })
  expect(newClose).toHaveBeenCalledTimes(1)
})
it.each([{ title: "Title only" }, { subtitle: "Subtitle only" }, {}])("supports optional header text and no dismissal action", props => {
  const { container } = render(<SidePanelHeader {...props} className="extra-header" />)
  expect(container.firstChild).toHaveClass("extra-header")
  expect(screen.queryByRole("button")).not.toBeInTheDocument()
  if (props.title) expect(screen.getByText(props.title)).toBeInTheDocument()
  if (props.subtitle) expect(screen.getByText(props.subtitle)).toBeInTheDocument()
})
