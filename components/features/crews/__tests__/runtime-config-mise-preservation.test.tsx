import { afterEach, describe, expect, it, vi } from "vitest"
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { RuntimeConfig, type RuntimeConfigValue } from "../runtime-config"
import { buildMiseJSON, parseMiseConfig } from "../runtime-config-data"

function click(element: HTMLElement) {
  fireEvent.mouseDown(element, { button: 0, ctrlKey: false })
  fireEvent.click(element)
}

const original = '# Keep the reviewed pins\n[tools]\ncodex = "0.159.0"\n[env]\nMODE = "test"\n'
afterEach(() => { vi.unstubAllGlobals(); vi.clearAllMocks() })
function setup(mise = original, layout: "tabs" | "sections" = "tabs") {
  vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, status: 200, json: async () => ({ features: [], runtimes: [{ tool: "node", name: "Node", default_version: "22", versions: ["22"] }] }), text: async () => "{}" })))
  const onChange = vi.fn()
  const value: RuntimeConfigValue = { runtimeImage: "debian:bookworm-slim", devcontainerConfig: '{"image":"debian:bookworm-slim"}', miseConfig: mise }
  render(<RuntimeConfig value={value} onChange={onChange} layout={layout} />)
  return onChange
}

describe("mise configuration preservation", () => {
  it.each([original, '{"tools":{"node":["20","22"]},"env":{"KEEP":"yes"}}', '{"tools":null}', 'null', '[1]', '{invalid'])('preserves unsupported existing input: %s', input => {
    expect(buildMiseJSON({}, input)).toBe(input)
    expect(parseMiseConfig(input)).toEqual({})
  })
  it.each(["tabs", "sections"] as const)("preserves TOML while changing an unrelated image in %s layout", async layout => {
    const onChange = setup(original, layout)
    await act(() => new Promise<void>(resolve => requestAnimationFrame(() => resolve())))
    expect(onChange.mock.calls.every(([value]) => value.miseConfig === original)).toBe(true)
    const radio = screen.getAllByRole("radio").find(el => el.getAttribute("aria-checked") !== "true")!
    fireEvent.click(radio)
    await waitFor(() => {
      expect(onChange).toHaveBeenCalled()
      expect(onChange.mock.calls.at(-1)?.[0].runtimeImage).not.toBe("debian:bookworm-slim")
    })
    expect(onChange.mock.calls.every(([value]) => value.miseConfig === original)).toBe(true)
  })
  it.each(["tabs", "sections"] as const)("does not offer destructive visual tool changes in %s layout", async layout => {
    setup(original, layout)
    if (layout === "tabs") click(screen.getByRole("tab", { name: /Language Runtimes/ }))
    else click(screen.getByRole("button", { name: /Language runtimes/ }))
    expect(await screen.findByText(/^Runtime configuration is preserved\./)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /Node/ })).not.toBeInTheDocument()
    expect(screen.queryByRole("switch")).not.toBeInTheDocument()
  })
  it("keeps existing TOML intact when raw editing only devcontainer JSON", async () => {
    const onChange = setup()
    click(screen.getByRole("tab", { name: "Preview" }))
    click(screen.getByRole("button", { name: /edit/i }))
    fireEvent.change(screen.getByLabelText("devcontainer.json"), { target: { value: '{"image":"alpine:3"}' } })
    click(screen.getByRole("button", { name: "Apply" }))
    await waitFor(() => expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ runtimeImage: "alpine:3", miseConfig: original })))
    expect(screen.queryByText("Edit Raw Configuration")).not.toBeInTheDocument()
  })
  it("does not partially apply devcontainer changes when raw mise validation fails", async () => {
    const onChange = setup('{"tools":{"node":"22"}}')
    await act(() => new Promise<void>(resolve => requestAnimationFrame(() => resolve())))
    click(screen.getByRole("tab", { name: "Preview" }))
    click(screen.getByRole("button", { name: /edit/i }))
    fireEvent.change(screen.getByLabelText("devcontainer.json"), { target: { value: '{"image":"alpine:3"}' } })
    fireEvent.change(screen.getByLabelText(/Language runtimes config/), { target: { value: '{bad' } })
    click(screen.getByRole("button", { name: "Apply" }))
    click(screen.getByRole("button", { name: "Cancel" }))
    expect(onChange.mock.calls.every(([value]) => value.runtimeImage === "debian:bookworm-slim")).toBe(true)
    click(screen.getByRole("tab", { name: "Preview" }))
    click(screen.getByRole("button", { name: /edit/i }))
    expect(JSON.parse((screen.getByLabelText("devcontainer.json") as HTMLTextAreaElement).value).image).toBe("debian:bookworm-slim")
  })
})
