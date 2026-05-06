// Tests for SchemaForm — the declarative form renderer used by the
// connect sheet. One test class per FieldType plus required/default
// behavior plus submit semantics.
//
// TDD STUB — implementation throws.

import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { SchemaForm } from "../schema-form"
import type { ConnectorField } from "../types"

const f = (over: Partial<ConnectorField>): ConnectorField => ({
  key: over.key ?? "k",
  label: over.label ?? "K",
  type: over.type ?? "text",
  required: over.required ?? false,
  default: over.default,
  placeholder: over.placeholder,
  help: over.help,
  choices: over.choices,
})

describe("SchemaForm — field rendering", () => {
  it("renders text input", () => {
    render(<SchemaForm fields={[f({ key: "name", label: "Name", type: "text" })]} onSubmit={vi.fn()} />)
    expect(screen.getByLabelText("Name")).toBeDefined()
  })

  it("renders password input as type=password", () => {
    render(<SchemaForm fields={[f({ key: "pw", label: "Password", type: "password" })]} onSubmit={vi.fn()} />)
    const input = screen.getByLabelText("Password") as HTMLInputElement
    expect(input.type).toBe("password")
  })

  it("renders number input as type=number", () => {
    render(<SchemaForm fields={[f({ key: "port", label: "Port", type: "number" })]} onSubmit={vi.fn()} />)
    const input = screen.getByLabelText("Port") as HTMLInputElement
    expect(input.type).toBe("number")
  })

  it("renders select with choices", () => {
    render(
      <SchemaForm
        fields={[f({ key: "ssl", label: "SSL", type: "select", choices: ["require", "prefer", "disable"] })]}
        onSubmit={vi.fn()}
      />,
    )
    const select = screen.getByLabelText("SSL") as HTMLSelectElement
    expect(select.tagName.toLowerCase()).toBe("select")
    expect(select.options.length).toBe(3)
    expect([...select.options].map((o) => o.value).sort()).toEqual(["disable", "prefer", "require"])
  })

  it("renders bool as checkbox", () => {
    render(<SchemaForm fields={[f({ key: "enabled", label: "Enabled", type: "bool" })]} onSubmit={vi.fn()} />)
    const cb = screen.getByLabelText("Enabled") as HTMLInputElement
    expect(cb.type).toBe("checkbox")
  })

  it("renders placeholder text", () => {
    render(<SchemaForm fields={[f({ key: "k", label: "K", type: "text", placeholder: "type here..." })]} onSubmit={vi.fn()} />)
    expect(screen.getByPlaceholderText("type here...")).toBeDefined()
  })

  it("marks required fields visibly", () => {
    render(<SchemaForm fields={[f({ key: "k", label: "K", type: "text", required: true })]} onSubmit={vi.fn()} />)
    // Most accessible: required attribute on the input. We allow either
    // the HTML required attribute OR an asterisk in the label.
    const input = screen.getByLabelText(/K/) as HTMLInputElement
    const labelText = screen.getByLabelText(/K/).closest("label, div, fieldset")?.textContent ?? ""
    const indicated = input.required || labelText.includes("*")
    expect(indicated).toBe(true)
  })

  it("renders help text below the input", () => {
    render(
      <SchemaForm
        fields={[f({ key: "k", label: "API Key", type: "password", help: "Get one from console." })]}
        onSubmit={vi.fn()}
      />,
    )
    expect(screen.getByText(/Get one from console/i)).toBeDefined()
  })
})

describe("SchemaForm — submit semantics", () => {
  it("calls onSubmit with all field values", async () => {
    const onSubmit = vi.fn()
    render(
      <SchemaForm
        fields={[
          f({ key: "host", label: "Host", type: "text" }),
          f({ key: "port", label: "Port", type: "number" }),
        ]}
        onSubmit={onSubmit}
      />,
    )
    fireEvent.change(screen.getByLabelText("Host"), { target: { value: "db.example.com" } })
    fireEvent.change(screen.getByLabelText("Port"), { target: { value: "5432" } })
    fireEvent.click(screen.getByRole("button", { name: /connect/i }))

    await waitFor(() => {
      expect(onSubmit).toHaveBeenCalledTimes(1)
    })
    expect(onSubmit.mock.calls[0]?.[0]).toMatchObject({ host: "db.example.com", port: "5432" })
  })

  it("blocks submit when a required field is empty", () => {
    const onSubmit = vi.fn()
    render(
      <SchemaForm
        fields={[f({ key: "api_key", label: "API Key", type: "password", required: true })]}
        onSubmit={onSubmit}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: /connect/i }))
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it("applies default values when user leaves a field blank", async () => {
    const onSubmit = vi.fn()
    render(
      <SchemaForm
        fields={[
          f({ key: "host", label: "Host", type: "text", required: true }),
          f({ key: "port", label: "Port", type: "number", default: "5432" }),
        ]}
        onSubmit={onSubmit}
      />,
    )
    fireEvent.change(screen.getByLabelText("Host"), { target: { value: "db.example.com" } })
    fireEvent.click(screen.getByRole("button", { name: /connect/i }))
    await waitFor(() => {
      expect(onSubmit).toHaveBeenCalled()
    })
    expect(onSubmit.mock.calls[0]?.[0]).toMatchObject({ port: "5432" })
  })

  it("respects initialValues", () => {
    render(
      <SchemaForm
        fields={[f({ key: "host", label: "Host", type: "text" })]}
        initialValues={{ host: "preloaded.example.com" }}
        onSubmit={vi.fn()}
      />,
    )
    expect((screen.getByLabelText("Host") as HTMLInputElement).value).toBe("preloaded.example.com")
  })

  it("disables submit button while submitting=true", () => {
    render(
      <SchemaForm
        fields={[f({ key: "k", label: "K", type: "text" })]}
        onSubmit={vi.fn()}
        submitting
      />,
    )
    const btn = screen.getByRole("button", { name: /connect/i }) as HTMLButtonElement
    expect(btn.disabled).toBe(true)
  })

  it("uses custom submitLabel when provided", () => {
    render(
      <SchemaForm
        fields={[f({ key: "k", label: "K", type: "text" })]}
        onSubmit={vi.fn()}
        submitLabel="Verify & save"
      />,
    )
    expect(screen.getByRole("button", { name: /verify & save/i })).toBeDefined()
  })
})
