import { afterEach, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { BackupsUpload } from "../backups-upload"

vi.mock("@/lib/server-base", () => ({ getBearerToken: () => "test-token", withServerBase: (p: string) => `https://test.invalid${p}` }))
let current: MockRequest
class MockRequest {
  upload = { onprogress: null as ((e: { lengthComputable: boolean; loaded: number; total: number }) => void) | null }
  withCredentials = false
  status = 201
  responseText = JSON.stringify({ path: "/backups/import.tar.zst", scope: "instance", proof_level: 1 })
  onload: (() => void) | null = null
  onerror: (() => void) | null = null
  onabort: (() => void) | null = null
  open = vi.fn()
  setRequestHeader = vi.fn()
  send = vi.fn()
  abort = vi.fn(() => this.onabort?.())
  constructor() { current = this }
}
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

it("streams the selected file with authentication, reports progress and returns only the upload receipt", () => {
  vi.stubGlobal("XMLHttpRequest", MockRequest)
  const uploaded = vi.fn()
  render(<BackupsUpload onUploaded={uploaded} />)
  const file = new File(["encrypted bundle"], "backup.tar.zst")
  fireEvent.change(screen.getByLabelText("Encrypted backup archive"), { target: { files: [file] } })
  expect(current.open).toHaveBeenCalledWith("POST", "https://test.invalid/api/v1/admin/instance/backups/bundles/upload")
  expect(current.setRequestHeader).toHaveBeenCalledWith("Authorization", "Bearer test-token")
  expect(current.withCredentials).toBe(true)
  expect(current.send).toHaveBeenCalledWith(file)
  act(() => current.upload.onprogress?.({ lengthComputable: true, loaded: 5, total: 10 }))
  expect(screen.getByRole("status")).toHaveTextContent("Uploading 50%")
  act(() => current.onload?.())
  expect(uploaded).toHaveBeenCalledWith(expect.objectContaining({ path: "/backups/import.tar.zst", proof_level: 1 }))
})

it("cancels an upload without selecting a backup for restore", () => {
  vi.stubGlobal("XMLHttpRequest", MockRequest)
  const uploaded = vi.fn()
  render(<BackupsUpload onUploaded={uploaded} />)
  fireEvent.change(screen.getByLabelText("Encrypted backup archive"), { target: { files: [new File(["bundle"], "backup.tar.zst")] } })
  fireEvent.click(screen.getByRole("button", { name: "Cancel upload" }))
  expect(current.abort).toHaveBeenCalledOnce()
  expect(uploaded).not.toHaveBeenCalled()
  expect(screen.getByRole("alert")).toHaveTextContent("Upload cancelled")
})
