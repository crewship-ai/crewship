import { describe, it, expect, vi } from "vitest"
import { render, screen, within } from "@testing-library/react"

import { RuntimeTab } from "../runtime-tab"
import type { RuntimeEntry } from "../runtime-tab"

// The two workspace-scoped cards the tab also hosts each fetch on mount. They
// are not what is under test here, and their in-flight requests are aborted at
// teardown, which floods the run with unhandled AbortErrors.
vi.mock("@/components/features/admin/security-posture-card", () => ({
  SecurityPostureCard: () => null,
}))
vi.mock("@/components/features/admin/memory-config-card", () => ({
  MemoryConfigCard: () => null,
}))

// Before #1690 this panel could only ever show one runtime — the endpoint
// behind it returned on the first socket that answered — and it labelled the
// entry after the candidate path rather than the daemon, so a machine running
// OrbStack was told it was running Docker. It also badged the first entry
// "Active" and every other one "Available", which reads as a list you can pick
// from. There is no such setting: `container.provider` accepts docker, apple
// or auto and nothing else.

// Three runtimes present, the one in use NOT first — the case the old
// "runtimes[0] is the active one" rendering got wrong.
const THREE: RuntimeEntry[] = [
  { runtime: "podman", version: "6.0.2", socket: "/run/user/501/podman/podman.sock", in_use: false },
  { runtime: "orbstack", version: "29.4.0", socket: "/var/run/docker.sock", in_use: true },
  { runtime: "apple", version: "1.2.0", socket: "", in_use: false },
]

const LINKS = {
  docker: "https://docs.docker.com/get-docker/",
  podman: "https://podman.io/docs/installation",
  colima: "https://github.com/abiosoft/colima",
  orbstack: "https://orbstack.dev/",
  rancher: "https://rancherdesktop.io/",
  apple: "https://github.com/apple/container",
}

function renderTab(over: Partial<React.ComponentProps<typeof RuntimeTab>> = {}) {
  return render(
    <RuntimeTab
      runtimeChecking={false}
      runtimeAvailable
      allRuntimes={THREE}
      runtimeInstallLinks={LINKS}
      onCheckRuntime={vi.fn()}
      workspaceId="ws1"
      {...over}
    />,
  )
}

function rowFor(name: string): HTMLElement {
  return screen.getByTestId(`runtime-row-${name}`)
}

describe("RuntimeTab — the runtime inventory", () => {
  it("lists every runtime present, by its real product name", () => {
    renderTab()
    expect(within(rowFor("orbstack")).getByText("OrbStack")).toBeInTheDocument()
    expect(within(rowFor("podman")).getByText("Podman")).toBeInTheDocument()
    expect(within(rowFor("apple")).getByText("Apple Containers")).toBeInTheDocument()
    expect(screen.getAllByTestId(/^runtime-row-/)).toHaveLength(3)
  })

  it("gives each runtime its own mark — Podman's is not Docker's, and neither is a tick", () => {
    renderTab()
    const podman = within(rowFor("podman")).getByTestId("runtime-icon").querySelector("svg")!
    const orb = within(rowFor("orbstack")).getByTestId("runtime-icon").querySelector("svg")!
    const apple = within(rowFor("apple")).getByTestId("runtime-icon").querySelector("svg")!

    expect(podman.getAttribute("data-runtime-icon")).toBe("podman")
    expect(podman.getAttribute("data-brand-mark")).toBe("official")
    expect(apple.getAttribute("data-runtime-icon")).toBe("apple")
    expect(apple.getAttribute("data-brand-mark")).toBe("official")
    // OrbStack has no Simple Icons entry — a neutral glyph, not a wrong mark.
    expect(orb.getAttribute("data-runtime-icon")).toBe("orbstack")
    expect(orb.getAttribute("data-brand-mark")).toBe("none")

    const d = (el: SVGElement) => el.querySelector("path")?.getAttribute("d")
    expect(d(podman)).not.toBe(d(apple))
  })

  it("marks exactly the runtime in use, wherever it sits in the list", () => {
    renderTab()
    expect(within(rowFor("orbstack")).getByText("In use")).toBeInTheDocument()
    expect(within(rowFor("podman")).queryByText("In use")).toBeNull()
    expect(within(rowFor("apple")).queryByText("In use")).toBeNull()
    expect(screen.getAllByText("In use")).toHaveLength(1)
    // The others are present, not selectable.
    expect(within(rowFor("podman")).getByText("Detected")).toBeInTheDocument()
  })

  it("puts the runtime in use first, however the server ordered them", () => {
    renderTab()
    const order = screen.getAllByTestId(/^runtime-row-/).map((el) => el.dataset.testid)
    expect(order[0]).toBe("runtime-row-orbstack")
  })

  it("shows each daemon's own version and socket, not the winner's", () => {
    renderTab()
    expect(within(rowFor("orbstack")).getByText("29.4.0")).toBeInTheDocument()
    expect(within(rowFor("podman")).getByText("6.0.2")).toBeInTheDocument()
    expect(within(rowFor("orbstack")).getByText("/var/run/docker.sock")).toBeInTheDocument()
    expect(
      within(rowFor("podman")).getByText("/run/user/501/podman/podman.sock"),
    ).toBeInTheDocument()
  })

  // The honesty requirement. `container.provider` has no value for
  // orbstack/colima/rancher/podman, so nothing here may offer to switch.
  it("offers no way to switch runtime, and says what the real levers are", () => {
    renderTab()
    expect(screen.queryByRole("radio")).toBeNull()
    expect(screen.queryByRole("combobox")).toBeNull()
    expect(screen.queryByRole("switch")).toBeNull()
    for (const rt of ["podman", "apple"]) {
      expect(within(rowFor(rt)).queryByRole("button")).toBeNull()
    }
    const note = screen.getByTestId("runtime-switch-note")
    expect(note).toHaveTextContent(/DOCKER_HOST/)
    expect(note).toHaveTextContent(/container\.provider/)
    expect(note.textContent ?? "").not.toMatch(/switch to|you could switch/i)
  })

  it("says plainly when runtimes are installed but none is driving anything", () => {
    renderTab({ allRuntimes: THREE.map((r) => ({ ...r, in_use: false })) })
    expect(screen.queryByText("In use")).toBeNull()
    expect(screen.getByTestId("runtime-none-in-use")).toHaveTextContent(/no container runtime in use/i)
    expect(screen.getAllByTestId(/^runtime-row-/)).toHaveLength(3)
  })

  it("offers every install link, branded, when no runtime is detected at all", () => {
    renderTab({ runtimeAvailable: false, allRuntimes: [] })
    expect(screen.queryAllByTestId(/^runtime-row-/)).toHaveLength(0)
    for (const [key, url] of Object.entries(LINKS)) {
      const link = screen.getByTestId(`runtime-install-${key}`)
      expect(link).toHaveAttribute("href", url)
      expect(link.querySelector("svg[data-runtime-icon]")?.getAttribute("data-runtime-icon")).toBe(key)
    }
  })

  it("also offers the runtimes not installed when one already is", () => {
    renderTab()
    // OrbStack, Podman and Apple are present; the rest are still worth naming.
    expect(screen.getByTestId("runtime-install-colima")).toBeInTheDocument()
    expect(screen.getByTestId("runtime-install-rancher")).toBeInTheDocument()
    expect(screen.queryByTestId("runtime-install-podman")).toBeNull()
  })
})

// The runtimes this panel lists are not equivalent, and until #1672 the panel
// said nothing about it: the endpoint knew podman below 5 silently drops the
// supplementary GID that grants access to crew-shared memory, and the only
// place it said so was a WARN in the server log at boot. An operator watching
// their agents "forget things" has no route from that symptom to this cause.
const PODMAN_WITH_GAP: RuntimeEntry[] = [
  {
    runtime: "podman",
    version: "4.9.3",
    socket: "/run/user/501/podman/podman.sock",
    in_use: true,
    gaps: [
      {
        control: "GroupAdd",
        detail:
          "podman 4.9.3 drops supplementary GIDs that have no /etc/group entry; agents will not hold gid 1002 and crew-shared memory reads will fail with EACCES.",
      },
    ],
  },
]

describe("RuntimeTab — known runtime gaps", () => {
  it("names the dropped control and what it costs, on the runtime in use", () => {
    renderTab({ allRuntimes: PODMAN_WITH_GAP })
    const gaps = screen.getByTestId("runtime-gaps-podman")
    expect(gaps).toHaveTextContent("GroupAdd")
    // The consequence, not just the control: "GroupAdd is not honoured"
    // connects to nothing an operator can observe.
    expect(gaps).toHaveTextContent(/crew-shared memory/i)
  })

  it("shows nothing at all for a runtime that honours everything", () => {
    renderTab()
    expect(screen.queryByTestId(/^runtime-gaps-/)).toBeNull()
  })

  it("does not warn about an idle runtime — the server only reports gaps for the one in use", () => {
    renderTab({
      allRuntimes: [
        { runtime: "docker", version: "28.0.4", socket: "/var/run/docker.sock", in_use: true },
        { runtime: "podman", version: "4.9.3", socket: "/run/user/501/podman/podman.sock", in_use: false },
      ],
    })
    expect(screen.queryByTestId(/^runtime-gaps-/)).toBeNull()
  })

  // An older server has no `gaps` key at all. The panel must render exactly as
  // it did before rather than crash on an absent array.
  it("tolerates a server that does not send the field", () => {
    renderTab({ allRuntimes: THREE.map(({ gaps: _gaps, ...rt }) => rt) })
    expect(screen.getAllByTestId(/^runtime-row-/)).toHaveLength(3)
    expect(screen.queryByTestId(/^runtime-gaps-/)).toBeNull()
  })
})
