import { beforeEach, describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { useState } from "react"

vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test", loading: false }) }))
const toastError = vi.fn()
vi.mock("sonner", () => ({ toast: { error: (message: string) => toastError(message), success: vi.fn(), info: vi.fn() } }))

import { AskSubmissionError } from "@/lib/ask-submission-error"
import { AskFormSheet } from "../ask-form-sheet"
import { AskFormsBuilder } from "@/components/features/crews/ask-forms-builder"
import { renderAskTemplate, type AskForm } from "@/lib/ask-template"
import { useComposerStore } from "@/stores/composer-store"

// Trap before compilation/execution rather than trying a catastrophic match.
// This also sees native execution through a constructor cached before render.
function trapNativePattern(pattern: string | string[]) {
  const patterns = typeof pattern === "string" ? [pattern] : pattern
  const Native = globalThis.RegExp
  const calls: string[] = []
  const inspect = (source: string) => {
    if (patterns.some((authorSource) => source.includes(authorSource))) {
      calls.push(source)
      throw new Error("author pattern reached native RegExp")
    }
  }
  const originalExec = Native.prototype.exec
  const exec = vi.spyOn(Native.prototype, "exec").mockImplementation(function (this: RegExp, value: string) {
    inspect(this.source)
    return originalExec.call(this, value)
  })
  globalThis.RegExp = new Proxy(Native, {
    construct(target, args) {
      inspect(typeof args[0] === "string" ? args[0] : args[0]?.source ?? "")
      return Reflect.construct(target, args, target)
    },
    apply(target, thisArg, args) {
      inspect(typeof args[0] === "string" ? args[0] : args[0]?.source ?? "")
      return Reflect.apply(target, thisArg, args)
    },
  })
  return { calls, restore: () => { globalThis.RegExp = Native; exec.mockRestore() } }
}

function formOf(pattern: string): AskForm {
  return { id: "safe-pattern", label: "Pattern form", template: "{{answer}}", fields: [
    { name: "answer", label: "Answer", type: "text", pattern },
  ] }
}
const onSubmit = vi.fn(async (_form: AskForm, _text: string, _envelope: unknown) => true)
const onClose = vi.fn()

beforeEach(() => {
  useComposerStore.setState({ attachments: {}, drafts: {} })
  onSubmit.mockReset()
  onSubmit.mockResolvedValue(true)
  onClose.mockClear()
  toastError.mockClear()
})

describe("Ask pattern boundary in the answering UI", () => {
  it.each(["button", "form"] as const)("%s submit validates a long near-match without native regex or HTML pattern", async (mode) => {
    const pattern = "(a+)+$"
    onSubmit.mockRejectedValueOnce(new AskSubmissionError("Answer is not in the expected format.", [{ field: "answer", code: "pattern", message: "Answer is not in the expected format." }]))
    const trap = trapNativePattern(pattern)
    try {
      const { container } = render(<AskFormSheet form={formOf(pattern)} agentId="agent-1" sessionId="sess-1" onSubmit={onSubmit} onClose={onClose} renderTemplate={renderAskTemplate} />)
      expect(container.querySelectorAll("[pattern]")).toHaveLength(0)
      fireEvent.change(screen.getByLabelText("Answer"), { target: { value: "a".repeat(4096) + "!" } })
      if (mode === "button") fireEvent.click(screen.getByTestId("ask-submit"))
      else fireEvent.submit(container.querySelector("form")!)
      // Inspect immediately: main catches constructor failure and otherwise
      // sends the invalid answer, so the failing boundary cannot be hidden.
      expect(trap.calls).toEqual([])
      await waitFor(() => expect(toastError).toHaveBeenCalledWith("Answer is not in the expected format."))
      expect(onSubmit).toHaveBeenCalledTimes(1)
      expect(screen.getByRole("alert")).toHaveTextContent("Answer is not in the expected format.")
      expect((screen.getByLabelText("Answer") as HTMLInputElement).value.length).toBeGreaterThan(0)
      expect(onClose).not.toHaveBeenCalled()
    } finally { trap.restore() }
  })

  it("sends a matching answer while keeping the author source outside native regex", async () => {
    const pattern = "(a|aa)+$"
    const trap = trapNativePattern(pattern)
    try {
      const { container } = render(<AskFormSheet form={formOf(pattern)} agentId="agent-1" sessionId="sess-1" onSubmit={onSubmit} onClose={onClose} renderTemplate={renderAskTemplate} />)
      fireEvent.change(screen.getByLabelText("Answer"), { target: { value: "aaaa" } })
      fireEvent.click(screen.getByTestId("ask-submit"))
      expect(trap.calls).toEqual([])
      await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1))
      expect(onSubmit.mock.calls[0][1]).toBe("aaaa")
      expect(container.querySelectorAll("[pattern]")).toHaveLength(0)
    } finally { trap.restore() }
  })
})

function BuilderHarness({ form }: { form: AskForm }) {
  const [value, setValue] = useState(JSON.stringify([form]))
  return <AskFormsBuilder value={value} onChange={setValue} />
}

describe("Ask authoring does not execute pattern source", () => {
  it("retains a nested pattern through raw JSON and builder edits without native regex or HTML pattern", () => {
    const pattern = "(a+)+$"
    const trap = trapNativePattern([pattern, "(a|aa)+$"])
    try {
      const { container } = render(<BuilderHarness form={formOf(pattern)} />)
      fireEvent.click(screen.getByRole("button", { name: "Advanced JSON" }))
      const raw = screen.getByLabelText("Forms JSON") as HTMLTextAreaElement
      const edited = JSON.parse(raw.value) as AskForm[]
      edited[0].fields[0].pattern = "(a|aa)+$"
      fireEvent.change(raw, { target: { value: JSON.stringify(edited) } })
      fireEvent.click(screen.getByRole("button", { name: "Form builder" }))
      fireEvent.change(screen.getByLabelText("Field label"), { target: { value: "Renamed answer" } })
      fireEvent.click(screen.getByRole("button", { name: "Advanced JSON" }))
      const roundtrip = JSON.parse((screen.getByLabelText("Forms JSON") as HTMLTextAreaElement).value) as AskForm[]
      expect(roundtrip[0].fields[0].pattern).toBe("(a|aa)+$")
      expect(roundtrip[0].fields[0].label).toBe("Renamed answer")
      expect(container.querySelectorAll("[pattern]")).toHaveLength(0)
      expect(trap.calls).toEqual([])
    } finally { trap.restore() }
  })
})

describe("Ask preview keeps author regexes outside native execution", () => {
  it("updates the actual message preview for a long near-match, then refuses submit safely", async () => {
    const pattern = "(a+)+$"
    onSubmit.mockRejectedValueOnce(new AskSubmissionError("Answer is not in the expected format.", [{ field: "answer", code: "pattern", message: "Answer is not in the expected format." }]))
    const trap = trapNativePattern(pattern)
    try {
      const { container } = render(<AskFormSheet form={formOf(pattern)} agentId="agent-1" sessionId="sess-1" onSubmit={onSubmit} onClose={onClose} renderTemplate={renderAskTemplate} />)
      fireEvent.click(screen.getByTestId("ask-preview-toggle"))
      expect(screen.getByTestId("ask-preview-toggle")).toHaveAttribute("aria-expanded", "true")
      const nearMatch = "a".repeat(1536) + "!"
      fireEvent.change(screen.getByLabelText("Answer"), { target: { value: nearMatch } })
      expect(screen.getByTestId("ask-preview").textContent).toBe(nearMatch)
      const updatedNearMatch = "a".repeat(1537) + "!"
      fireEvent.change(screen.getByLabelText("Answer"), { target: { value: updatedNearMatch } })
      expect(screen.getByTestId("ask-preview").textContent).toBe(updatedNearMatch)
      expect(container.querySelectorAll("[pattern]")).toHaveLength(0)
      expect(trap.calls).toEqual([])
      fireEvent.click(screen.getByTestId("ask-submit"))
      expect(trap.calls).toEqual([])
      await waitFor(() => expect(toastError).toHaveBeenCalledWith("Answer is not in the expected format."))
      expect(onSubmit).toHaveBeenCalledTimes(1)
      expect(screen.getByRole("alert")).toHaveTextContent("Answer is not in the expected format.")
      expect((screen.getByLabelText("Answer") as HTMLInputElement).value.length).toBeGreaterThan(0)
    } finally { trap.restore() }
  })

  it.each([
    { name: "matching ordinary pattern", value: "CZ25788001", accepted: true },
    { name: "nonmatching ordinary pattern", value: "xxCZ25788001xx", accepted: false },
  ])("previews and submits $name without native regex or HTML pattern", async ({ value, accepted }) => {
    const pattern = "CZ[0-9]{8}"
    if (!accepted) onSubmit.mockRejectedValueOnce(new AskSubmissionError("Answer is not in the expected format.", [{ field: "answer", code: "pattern", message: "Answer is not in the expected format." }]))
    const trap = trapNativePattern(pattern)
    try {
      const { container } = render(<AskFormSheet form={formOf(pattern)} agentId="agent-1" sessionId="sess-1" onSubmit={onSubmit} onClose={onClose} renderTemplate={renderAskTemplate} />)
      fireEvent.click(screen.getByTestId("ask-preview-toggle"))
      fireEvent.change(screen.getByLabelText("Answer"), { target: { value } })
      expect(screen.getByTestId("ask-preview").textContent).toBe(value)
      expect(container.querySelectorAll("[pattern]")).toHaveLength(0)
      expect(trap.calls).toEqual([])
      fireEvent.submit(container.querySelector("form")!)
      expect(trap.calls).toEqual([])
      if (accepted) {
        await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1))
        expect(onSubmit.mock.calls[0][1]).toBe(value)
        expect(toastError).not.toHaveBeenCalled()
      } else {
        await waitFor(() => expect(toastError).toHaveBeenCalledWith("Answer is not in the expected format."))
        expect(onSubmit).toHaveBeenCalledTimes(1)
      expect(screen.getByRole("alert")).toHaveTextContent("Answer is not in the expected format.")
      expect((screen.getByLabelText("Answer") as HTMLInputElement).value.length).toBeGreaterThan(0)
      }
    } finally { trap.restore() }
  })
})
