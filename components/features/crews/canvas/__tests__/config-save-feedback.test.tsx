import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

import { toast } from "sonner"

import { ConfigSelect, ConfigSwitch, ConfigText } from "@/components/features/crews/canvas/config-field"

// =============================================================================
// Say it saved.
//
// These rows commit on blur or on change — there is no Save button to click,
// which is the right call for a settings screen but leaves the user with no
// evidence anything happened. The confirmation is a toast, bottom-right, and
// it names the field: "Name saved" answers both "did it work" and "which one",
// which is why the small inline tick that used to sit beside each control is
// gone rather than kept alongside it.
//
// A failed save must not toast success, and must put the server's value back.
// =============================================================================

beforeEach(() => {
  vi.mocked(toast.success).mockClear()
  vi.mocked(toast.error).mockClear()
})

describe("save feedback", () => {
  it("confirms a text commit by name", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined)
    render(<ConfigText label="Name" value="Alex" onSave={onSave} />)

    const input = screen.getByLabelText("Name")
    fireEvent.change(input, { target: { value: "AlexXXXX" } })
    fireEvent.blur(input)

    await waitFor(() => expect(onSave).toHaveBeenCalledWith("AlexXXXX"))
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Name saved"))
    expect(toast.error).not.toHaveBeenCalled()
  })

  it("says nothing when the value did not change", () => {
    const onSave = vi.fn()
    render(<ConfigText label="Name" value="Alex" onSave={onSave} />)
    fireEvent.blur(screen.getByLabelText("Name"))
    expect(onSave).not.toHaveBeenCalled()
    expect(toast.success).not.toHaveBeenCalled()
  })

  it("confirms a dropdown and a switch too", async () => {
    const onSelect = vi.fn().mockResolvedValue(undefined)
    const onToggle = vi.fn().mockResolvedValue(undefined)
    render(
      <>
        <ConfigSelect
          label="Role in crew" value="AGENT"
          options={[{ value: "AGENT", label: "Agent" }, { value: "LEAD", label: "Lead" }]}
          onSave={onSelect}
        />
        <ConfigSwitch label="Memory between sessions" checked={false} onSave={onToggle} />
      </>,
    )

    fireEvent.change(screen.getByLabelText("Role in crew"), { target: { value: "LEAD" } })
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Role in crew saved"))

    fireEvent.click(screen.getByRole("switch", { name: "Memory between sessions" }))
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Memory between sessions saved"))
  })

  it("reports a rejected save and restores the server value", async () => {
    const onSave = vi.fn().mockRejectedValue(new Error("slug already taken"))
    render(<ConfigText label="Slug" value="alex" onSave={onSave} />)

    const input = screen.getByLabelText("Slug") as HTMLInputElement
    fireEvent.change(input, { target: { value: "morgan" } })
    fireEvent.blur(input)

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("slug already taken"))
    expect(toast.success).not.toHaveBeenCalled()
    await waitFor(() => expect(input.value).toBe("alex"))
  })
})
