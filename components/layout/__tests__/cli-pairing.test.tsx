import * as React from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup, fireEvent } from "@testing-library/react"

import * as Pairing from "../cli-pairing"

// COM-09 / CHR-02 (#3481, #3482; chrome-states.md G15, onboarding O04): one
// CLI pairing component for Connect CLI and the first-run setup, with every
// state the code goes through — pending with its countdown, paired, expired,
// failed.
type Status = "idle" | "starting" | "pending" | "consumed" | "expired" | "failed"
type BlockProps = {
  status: Status
  command: string
  remainingSec: number | null
  copied?: boolean
  error?: string | null
  onCopy: () => void
  onNewCode: () => void
  onRetry: () => void
  paired?: React.ReactNode
}
function PairingBlock(props: BlockProps) {
  const C = (Pairing as unknown as { PairingBlock?: React.ComponentType<BlockProps> }).PairingBlock
  if (!C) throw new Error("PairingBlock is not exported")
  return <C {...props} />
}
const formatCountdown = (sec: number) => {
  const f = (Pairing as unknown as { formatCountdown?: (s: number) => string }).formatCountdown
  if (!f) throw new Error("formatCountdown is not exported")
  return f(sec)
}

const CMD = "crewship login --pair --code=K3F9X2NM --server=https://crewship.example.com"
const base = { command: CMD, remainingSec: 581, onCopy: vi.fn(), onNewCode: vi.fn(), onRetry: vi.fn() }

describe("PairingBlock", () => {
  beforeEach(() => cleanup())

  it("pending: the command to paste, Copy, and the countdown", () => {
    const onCopy = vi.fn()
    render(<PairingBlock {...base} status="pending" onCopy={onCopy} />)
    expect(screen.getByTestId("pair-command")).toHaveTextContent(CMD)
    fireEvent.click(screen.getByRole("button", { name: "Copy command" }))
    expect(onCopy).toHaveBeenCalledOnce()
    expect(screen.getByText(/Waiting for the CLI… code expires in/)).toHaveTextContent("9:41")
  })

  it("starting: says a code is on its way, no command yet", () => {
    render(<PairingBlock {...base} command="" status="starting" />)
    expect(screen.getByRole("status")).toHaveTextContent("Generating a code…")
    expect(screen.queryByTestId("pair-command")).toBeNull()
  })

  it("paired: a status naming where the token went", () => {
    render(<PairingBlock {...base} status="consumed" />)
    expect(screen.getByRole("status")).toHaveTextContent(/CLI connected/)
    expect(screen.queryByText(/Waiting for the CLI/)).toBeNull()
  })

  it("paired: the caller can word it (setup says whether the model token came too)", () => {
    render(<PairingBlock {...base} status="consumed" paired={<>CLI paired and model token received.</>} />)
    expect(screen.getByRole("status")).toHaveTextContent("CLI paired and model token received.")
  })

  it("expired: a warning and a new code", () => {
    const onNewCode = vi.fn()
    render(<PairingBlock {...base} status="expired" remainingSec={0} onNewCode={onNewCode} />)
    expect(screen.getByText("The code expired.").closest("[data-slot=note]")).toHaveAttribute("data-tone", "warn")
    fireEvent.click(screen.getByRole("button", { name: "Get a new code" }))
    expect(onNewCode).toHaveBeenCalledOnce()
  })

  it("failed: an alert with the reason and Retry", () => {
    const onRetry = vi.fn()
    render(<PairingBlock {...base} command="" status="failed" error="Too many requests" onRetry={onRetry} />)
    expect(screen.getByRole("alert")).toHaveTextContent("Too many requests")
    fireEvent.click(screen.getByRole("button", { name: "Retry" }))
    expect(onRetry).toHaveBeenCalledOnce()
  })

  it("formats the code's remaining life as m:ss", () => {
    expect(formatCountdown(581)).toBe("9:41")
    expect(formatCountdown(59)).toBe("0:59")
    expect(formatCountdown(0)).toBe("0:00")
  })
})
