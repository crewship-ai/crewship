import { useState } from "react"
import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import {
  RuntimeSecurityConfig,
  type SecurityConfigValue,
} from "@/components/features/crews/runtime-security-config"

const base: SecurityConfigValue = {
  privileged: false,
  init: false,
  capAdd: [],
  mounts: [],
  containerEnv: {},
  postStartCommand: "",
}

// Stateful harness — the component is fully controlled, so the wrapper feeds
// each onChange back as the new value (like the real parent) and records every
// emitted value so tests can assert the serialized result.
function renderCfg(
  overrides: Partial<SecurityConfigValue> = {},
  props: { canEditPrivileged?: boolean } = {},
) {
  const onChange = vi.fn()
  function Harness() {
    const [value, setValue] = useState<SecurityConfigValue>({ ...base, ...overrides })
    return (
      <RuntimeSecurityConfig
        value={value}
        onChange={(v) => {
          onChange(v)
          setValue(v)
        }}
        canEditPrivileged={props.canEditPrivileged ?? true}
      />
    )
  }
  render(<Harness />)
  return { onChange }
}

describe("<RuntimeSecurityConfig>", () => {
  it("renders the privileged danger warning", () => {
    renderCfg()
    expect(screen.getByText(/no-new-privileges/i)).toBeInTheDocument()
  })

  it("toggling privileged serializes privileged=true", () => {
    const { onChange } = renderCfg()
    fireEvent.click(screen.getByRole("switch", { name: /privileged/i }))
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({ privileged: true }),
    )
  })

  it("shows an isolation-reduced badge only when privileged", () => {
    renderCfg({ privileged: true })
    expect(screen.getByText(/isolation reduced/i)).toBeInTheDocument()
  })

  it("does not show the isolation-reduced badge when not privileged", () => {
    renderCfg()
    expect(screen.queryByText(/isolation reduced/i)).not.toBeInTheDocument()
  })

  it("disables the privileged toggle for non-admins", () => {
    renderCfg({}, { canEditPrivileged: false })
    expect(screen.getByRole("switch", { name: /privileged/i })).toBeDisabled()
    expect(screen.getByText(/requires an admin/i)).toBeInTheDocument()
  })

  it("adding a capability serializes it into capAdd", () => {
    const { onChange } = renderCfg()
    fireEvent.click(screen.getByRole("checkbox", { name: /NET_BIND_SERVICE/i }))
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({ capAdd: ["NET_BIND_SERVICE"] }),
    )
  })

  it("removing a capability serializes it out of capAdd", () => {
    const { onChange } = renderCfg({ capAdd: ["NET_BIND_SERVICE"] })
    fireEvent.click(screen.getByRole("checkbox", { name: /NET_BIND_SERVICE/i }))
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({ capAdd: [] }),
    )
  })

  it("adds a mount row and serializes source/target/readonly", () => {
    const { onChange } = renderCfg()
    fireEvent.click(screen.getByRole("button", { name: /add mount/i }))
    fireEvent.change(screen.getByLabelText(/mount source/i), {
      target: { value: "/dev/fuse" },
    })
    fireEvent.change(screen.getByLabelText(/mount target/i), {
      target: { value: "/dev/fuse" },
    })
    const last = onChange.mock.calls.at(-1)![0] as SecurityConfigValue
    expect(last.mounts[0]).toMatchObject({ source: "/dev/fuse", target: "/dev/fuse" })
  })

  it("flags a disallowed mount source (docker.sock)", () => {
    renderCfg({ mounts: [{ source: "/var/run/docker.sock", target: "/x" }] })
    expect(screen.getByText(/not allowed/i)).toBeInTheDocument()
  })

  it("edits the start hook (init script)", () => {
    const { onChange } = renderCfg()
    fireEvent.change(screen.getByLabelText(/start hook/i), {
      target: { value: "echo boot" },
    })
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({ postStartCommand: "echo boot" }),
    )
  })
})
