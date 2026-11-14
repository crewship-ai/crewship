import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(), broadcastSessionExpired: vi.fn() }))
vi.mock("@/lib/toast", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

import { apiFetch } from "@/lib/api-fetch"
import { ConfigModel } from "../config-model"

const listed = [
  { id: "gpt-6-astra", provider: "OPENAI" },
  { id: "gpt-6.1-sol", provider: "OPENAI" },
]

beforeEach(() => {
  vi.mocked(apiFetch).mockReset()
  vi.mocked(apiFetch).mockResolvedValue(new Response(JSON.stringify({ models: listed, source: "live" }), { status: 200 }))
})

function renderPicker(value: string, onSave = vi.fn()) {
  render(<ConfigModel draftMode label="Model" workspaceId="ws1" provider="OPENAI" value={value} onSave={onSave} />)
  return onSave
}

async function openPicker(value: string) {
  fireEvent.click(screen.getByRole("button", { name: new RegExp(`^Model: ${value.replace(/[.[\]]/g, "\\$&")}`) }))
  await waitFor(() => expect(screen.getByText("gpt-6-astra")).toBeInTheDocument())
}

describe("ConfigModel custom model id (#5376)", () => {
  it("saves a model id that is not in the list", async () => {
    const onSave = renderPicker("gpt-6.1-sol")
    await openPicker("gpt-6.1-sol")
    fireEvent.change(screen.getByLabelText("Custom model ID"), { target: { value: "  gpt-7-preview " } })
    fireEvent.click(screen.getByRole("button", { name: "Use model" }))
    await waitFor(() => expect(onSave).toHaveBeenCalledWith("gpt-7-preview"))
  })

  it("uses the typed id on Enter instead of the highlighted row", async () => {
    const onSave = renderPicker("gpt-6.1-sol")
    await openPicker("gpt-6.1-sol")
    const input = screen.getByLabelText("Custom model ID")
    fireEvent.change(input, { target: { value: "claude-opus-5-5[1m]" } })
    fireEvent.keyDown(input, { key: "Enter" })
    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1))
    expect(onSave).toHaveBeenCalledWith("claude-opus-5-5[1m]")
  })

  it("refuses an id the server would reject and says why", async () => {
    const onSave = renderPicker("gpt-6.1-sol")
    await openPicker("gpt-6.1-sol")
    fireEvent.change(screen.getByLabelText("Custom model ID"), { target: { value: "gpt 7; id" } })
    expect(screen.getByRole("button", { name: "Use model" })).toBeDisabled()
    expect(screen.getByText(/no spaces/)).toBeInTheDocument()
    fireEvent.keyDown(screen.getByLabelText("Custom model ID"), { key: "Enter" })
    expect(onSave).not.toHaveBeenCalled()
  })

  it("marks a stored custom id and pre-fills it for editing", async () => {
    renderPicker("gpt-7-preview")
    expect(screen.getByText(/Custom model ID — not in this catalog/)).toBeInTheDocument()
    await openPicker("gpt-7-preview")
    expect(screen.getByLabelText("Custom model ID")).toHaveValue("gpt-7-preview")
  })

  it("does not mark a listed model as custom", async () => {
    renderPicker("gpt-6.1-sol")
    await openPicker("gpt-6.1-sol")
    expect(screen.getByLabelText("Custom model ID")).toHaveValue("")
    expect(screen.queryByText(/not in this catalog/)).not.toBeInTheDocument()
  })
})
