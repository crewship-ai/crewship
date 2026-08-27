import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { Chip, CustomNumberChip, DomainChips, prettyMemory } from "../runtime-controls"

/**
 * These used to be tested through `<StepRuntime>`, a wizard step that no
 * longer exists — its resource cells are folded into Container now. The
 * controls survived the step, so the tests come with them, driven directly
 * rather than through whatever screen happens to host them next.
 */

describe("DomainChips", () => {
  function harness(value: string[] = []) {
    const onChange = vi.fn()
    render(<DomainChips value={value} onChange={onChange} />)
    const input = document.querySelector("input") as HTMLInputElement
    return { onChange, input }
  }

  it("commits a domain on Enter", () => {
    const { onChange, input } = harness()
    fireEvent.change(input, { target: { value: "github.com" } })
    fireEvent.keyDown(input, { key: "Enter" })
    expect(onChange).toHaveBeenCalledWith(["github.com"])
  })

  it("commits on a comma too", () => {
    const { onChange, input } = harness()
    fireEvent.change(input, { target: { value: "api.npmjs.org" } })
    fireEvent.keyDown(input, { key: "," })
    expect(onChange).toHaveBeenCalledWith(["api.npmjs.org"])
  })

  it("lowercases what it stores, because host matching is case-insensitive", () => {
    const { onChange, input } = harness()
    fireEvent.change(input, { target: { value: "GitHub.COM" } })
    fireEvent.keyDown(input, { key: "Enter" })
    expect(onChange).toHaveBeenCalledWith(["github.com"])
  })

  it("ignores a duplicate rather than listing it twice", () => {
    const { onChange, input } = harness(["github.com"])
    fireEvent.change(input, { target: { value: "github.com" } })
    fireEvent.keyDown(input, { key: "Enter" })
    expect(onChange).not.toHaveBeenCalled()
  })

  it("Backspace on an empty draft removes the last one", () => {
    const { onChange, input } = harness(["a.com", "b.com"])
    fireEvent.keyDown(input, { key: "Backspace" })
    expect(onChange).toHaveBeenCalledWith(["a.com"])
  })

  it("removes the one whose × was clicked", () => {
    const onChange = vi.fn()
    render(<DomainChips value={["github.com", "npmjs.org"]} onChange={onChange} />)
    fireEvent.click(screen.getByLabelText("Remove github.com"))
    expect(onChange).toHaveBeenCalledWith(["npmjs.org"])
  })

  it("renders what it already holds, wildcards included", () => {
    render(<DomainChips value={["github.com", "*.npmjs.org"]} onChange={vi.fn()} />)
    expect(screen.getByText("github.com")).toBeInTheDocument()
    expect(screen.getByText("*.npmjs.org")).toBeInTheDocument()
  })

  it("commits what is still in the field when focus leaves", () => {
    const { onChange, input } = harness()
    fireEvent.change(input, { target: { value: "example.com" } })
    fireEvent.blur(input)
    expect(onChange).toHaveBeenCalledWith(["example.com"])
  })
})

describe("CustomNumberChip", () => {
  function open(props: Partial<React.ComponentProps<typeof CustomNumberChip>> = {}) {
    const onChange = vi.fn()
    render(
      <CustomNumberChip
        active={false}
        value={4096}
        onChange={onChange}
        min={512}
        max={16384}
        suffix="MB"
        {...props}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: "Custom…" }))
    return { onChange, input: screen.getByLabelText(/Custom MB value/i) }
  }

  it("commits a value inside the range", () => {
    const { onChange, input } = open()
    fireEvent.change(input, { target: { value: "8192" } })
    fireEvent.blur(input)
    expect(onChange).toHaveBeenCalledWith(8192)
  })

  it("keeps the field mounted after a refusal so the error can be read", () => {
    const { onChange, input } = open()
    fireEvent.change(input, { target: { value: "999999" } })
    fireEvent.blur(input)
    expect(onChange).not.toHaveBeenCalled()
    expect(screen.getByRole("alert")).toHaveTextContent("Enter 512-16384 MB")
    expect(screen.getByLabelText(/Custom MB value/i)).toBeInTheDocument()
  })

  it("Escape abandons the draft", () => {
    const { onChange, input } = open()
    fireEvent.change(input, { target: { value: "8192" } })
    fireEvent.keyDown(input, { key: "Escape" })
    expect(onChange).not.toHaveBeenCalled()
  })
})

describe("Chip", () => {
  it("says which one is chosen through more than colour", () => {
    const onClick = vi.fn()
    render(<Chip active onClick={onClick}>4 GB</Chip>)
    fireEvent.click(screen.getByRole("button", { name: "4 GB" }))
    expect(onClick).toHaveBeenCalled()
  })
})

describe("prettyMemory", () => {
  it("uses MB below a gigabyte", () => {
    expect(prettyMemory(512)).toBe("512 MB")
  })

  it("uses whole GB when it divides", () => {
    expect(prettyMemory(4096)).toBe("4 GB")
  })

  it("keeps one decimal when it does not", () => {
    expect(prettyMemory(1536)).toBe("1.5 GB")
  })
})
