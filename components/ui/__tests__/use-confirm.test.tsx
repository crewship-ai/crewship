import { describe, it, expect } from "vitest"
import { act, render, screen, fireEvent, waitFor } from "@testing-library/react"
import { useState } from "react"
import { useConfirm } from "@/components/ui/use-confirm"

function Harness() {
  const { ask, dialog } = useConfirm()
  const [answer, setAnswer] = useState("none")
  return (
    <>
      <button
        type="button"
        onClick={async () => {
          const ok = await ask({ title: "Delete schedule Nightly?", description: "Runs stop.", confirmLabel: "Delete schedule", destructive: true })
          setAnswer(String(ok))
        }}
      >
        Ask
      </button>
      <span data-testid="answer">{answer}</span>
      {dialog}
    </>
  )
}

describe("useConfirm", () => {
  it("renders nothing until asked", () => {
    render(<Harness />)
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument()
  })

  it("resolves true on the confirm button and closes", async () => {
    render(<Harness />)
    fireEvent.click(screen.getByText("Ask"))
    expect(await screen.findByRole("alertdialog", { name: "Delete schedule Nightly?" })).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Delete schedule" }))
    await waitFor(() => expect(screen.getByTestId("answer")).toHaveTextContent("true"))
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument())
  })

  it("resolves false on Cancel", async () => {
    render(<Harness />)
    fireEvent.click(screen.getByText("Ask"))
    fireEvent.click(await screen.findByRole("button", { name: "Cancel" }))
    await waitFor(() => expect(screen.getByTestId("answer")).toHaveTextContent("false"))
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument())
  })

  it("resolves false on Escape", async () => {
    render(<Harness />)
    fireEvent.click(screen.getByText("Ask"))
    const dialog = await screen.findByRole("alertdialog")
    await act(async () => { fireEvent.keyDown(dialog, { key: "Escape" }) })
    await waitFor(() => expect(screen.getByTestId("answer")).toHaveTextContent("false"))
  })
})
