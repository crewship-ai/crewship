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

  // #1380 tail — the backend save path (Config.ValidateSecurity) rejects every
  // cap except NET_BIND_SERVICE with a 400. The picker must not offer a fresh
  // selection the server is certain to refuse.
  it("locks capabilities the save path would reject", () => {
    renderCfg()
    expect(screen.getByRole("checkbox", { name: /^NET_BIND_SERVICE$/i })).toBeEnabled()
    expect(screen.getByRole("checkbox", { name: /^SYS_ADMIN$/i })).toBeDisabled()
    expect(screen.getAllByText(/privileged only/i).length).toBeGreaterThan(0)
  })

  it("keeps a legacy stored cap unlockable so it can be removed", () => {
    // Saved before the gate landed: still checked, still interactive, and
    // called out as no-longer-saveable.
    const { onChange } = renderCfg({ capAdd: ["SYS_ADMIN"] })
    const box = screen.getByRole("checkbox", { name: /^SYS_ADMIN$/i })
    expect(box).toBeEnabled()
    expect(screen.getByText(/no longer accepted/i)).toBeInTheDocument()
    fireEvent.click(box)
    expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ capAdd: [] }))
  })

  it("preserves an unmodeled capability across a toggle", () => {
    // BPF isn't in KNOWN_CAPS; a plain filter would silently drop it.
    const { onChange } = renderCfg({ capAdd: ["BPF"] })
    fireEvent.click(screen.getByRole("checkbox", { name: /^NET_BIND_SERVICE$/i }))
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({ capAdd: ["NET_BIND_SERVICE", "BPF"] }),
    )
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
