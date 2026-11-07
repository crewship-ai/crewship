import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"

const fetchMock = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))
import { DesignExportButton } from "../design-export-button"

beforeEach(() => {
 vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN_EXPORT", "true")
 fetchMock.mockReset()
 URL.createObjectURL = vi.fn(() => "blob:design-export")
 URL.revokeObjectURL = vi.fn()
})
afterEach(() => { cleanup(); vi.unstubAllEnvs(); vi.restoreAllMocks() })

describe("Design HTML export button", () => {
 it.each(["false", "", undefined])("renders nothing while the export flag is %s", value => {
  if (value === undefined) vi.unstubAllEnvs()
  else vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN_EXPORT", value)
  const { container } = render(<DesignExportButton workspaceId="ws" pageId="page" version={3} />)
  expect(container).toBeEmptyDOMElement()
  expect(screen.queryByRole("button", { name: /Export HTML/ })).not.toBeInTheDocument()
 })

 it("renders nothing without a saved version", () => {
  const { container } = render(<DesignExportButton workspaceId="ws" pageId="page" />)
  expect(container).toBeEmptyDOMElement()
 })

 it("downloads the exact saved version under the server file name", async () => {
  fetchMock.mockResolvedValue(new Response("<!doctype html><title>x</title>", { status: 200, headers: { "Content-Type": "text/html; charset=utf-8", "Content-Disposition": 'attachment; filename="checkout-design-v3.html"' } }))
  const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
   expect(this.download).toBe("checkout-design-v3.html")
   expect(this.href).toBe("blob:design-export")
  })
  render(<DesignExportButton workspaceId="ws 1" pageId="page/1" version={3} />)
  fireEvent.click(screen.getByRole("button", { name: "Export HTML (version 3)" }))
  await waitFor(() => expect(click).toHaveBeenCalledTimes(1))
  expect(fetchMock).toHaveBeenCalledWith("/api/v1/pages/@page%2F1/design/export?format=html&version=3&workspace_id=ws%201", { cache: "no-store" })
 })

 it("falls back to a safe name when the server name is unexpected", async () => {
  fetchMock.mockResolvedValue(new Response("<!doctype html>", { status: 200, headers: { "Content-Disposition": 'attachment; filename="../../evil.html"' } }))
  const names: string[] = []
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) { names.push(this.download) })
  render(<DesignExportButton workspaceId="ws" pageId="page" version={2} />)
  fireEvent.click(screen.getByRole("button", { name: /Export HTML/ }))
  await waitFor(() => expect(names).toEqual(["design-v2.html"]))
 })

 it("shows the server refusal and downloads nothing", async () => {
  fetchMock.mockResolvedValue(new Response(JSON.stringify({ error: "Design export exceeds the 8 MiB file limit.", code: "design_export_limit" }), { status: 413, headers: { "Content-Type": "application/json" } }))
  const click = vi.spyOn(HTMLAnchorElement.prototype, "click")
  render(<DesignExportButton workspaceId="ws" pageId="page" version={4} />)
  fireEvent.click(screen.getByRole("button", { name: /Export HTML/ }))
  expect(await screen.findByRole("alert")).toHaveTextContent("Design export exceeds the 8 MiB file limit.")
  expect(click).not.toHaveBeenCalled()
  expect(URL.createObjectURL).not.toHaveBeenCalled()
 })
})
