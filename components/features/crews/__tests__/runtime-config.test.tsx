import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, within, fireEvent, waitFor } from "@testing-library/react"
import { RuntimeConfig, type RuntimeConfigValue } from "@/components/features/crews/runtime-config"
import { BASE_IMAGES } from "@/components/features/crews/runtime-config-data"

// RuntimeConfig fetches /api/v1/features/catalog and /api/v1/runtimes/catalog
// on mount via useCatalog. Stub them empty so every test starts from a known,
// network-free state — the base-image catalogue itself is static data, not
// fetched (see runtime-config.tsx's filteredBaseImages comment).
function stubCatalogFetch() {
  const fetchMock = vi.fn(async () => ({
    ok: true,
    status: 200,
    json: async () => ({ features: [], runtimes: [] }),
    text: async () => "{}",
  }))
  vi.stubGlobal("fetch", fetchMock)
  return fetchMock
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.clearAllMocks()
})

function emptyValue(): RuntimeConfigValue {
  return { runtimeImage: "", devcontainerConfig: "", miseConfig: "" }
}

describe("<RuntimeConfig> base image catalogue", () => {
  it("opens a fresh crew on the catalogue, not the custom-image field", async () => {
    stubCatalogFetch()
    render(<RuntimeConfig value={emptyValue()} onChange={vi.fn()} />)

    // The catalogue is one radio per BASE_IMAGES entry.
    await waitFor(() => {
      expect(screen.getAllByRole("radio").length).toBe(BASE_IMAGES.length)
    })
    expect(screen.getByRole("button", { name: /use custom image/i })).toBeInTheDocument()
    expect(screen.queryByPlaceholderText(/myregistry\/myimage/i)).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /^preset$/i })).not.toBeInTheDocument()
  })

  it("still opens on the custom field for a crew with a genuinely custom image", async () => {
    stubCatalogFetch()
    const value: RuntimeConfigValue = {
      runtimeImage: "",
      devcontainerConfig: JSON.stringify({ image: "ghcr.io/acme/custom:latest" }),
      miseConfig: "",
    }
    render(<RuntimeConfig value={value} onChange={vi.fn()} />)

    await waitFor(() => {
      expect(screen.getByDisplayValue("ghcr.io/acme/custom:latest")).toBeInTheDocument()
    })
    expect(screen.getByRole("button", { name: /^preset$/i })).toBeInTheDocument()
    expect(screen.queryAllByRole("radio")).toHaveLength(0)
  })

  it("opens on the catalogue for a crew explicitly stored on the debian:bookworm-slim default", async () => {
    stubCatalogFetch()
    const value: RuntimeConfigValue = {
      runtimeImage: "",
      devcontainerConfig: JSON.stringify({ image: "debian:bookworm-slim" }),
      miseConfig: "",
    }
    render(<RuntimeConfig value={value} onChange={vi.fn()} />)

    await waitFor(() => {
      expect(screen.getAllByRole("radio").length).toBe(BASE_IMAGES.length)
    })
  })

  it("selecting a cataloged image propagates it through onChange", async () => {
    stubCatalogFetch()
    const onChange = vi.fn()
    render(<RuntimeConfig value={emptyValue()} onChange={onChange} />)
    await waitFor(() => expect(screen.getAllByRole("radio").length).toBe(BASE_IMAGES.length))

    const debian = BASE_IMAGES.find((b) => b.label.startsWith("Debian"))!
    fireEvent.click(screen.getByRole("radio", { name: new RegExp(`^${debian.label.split(" ")[0]}`) }))

    await waitFor(() => {
      expect(onChange).toHaveBeenCalledWith(
        expect.objectContaining({ runtimeImage: debian.value })
      )
    })
  })
})

describe("<RuntimeConfig> base image search", () => {
  it("filters the catalogue by label", async () => {
    stubCatalogFetch()
    render(<RuntimeConfig value={emptyValue()} onChange={vi.fn()} />)
    await waitFor(() => expect(screen.getAllByRole("radio").length).toBe(BASE_IMAGES.length))

    // "3.12" is unique to the Python entry's label/value — unlike "python"
    // itself, which also matches Universal's "Node + Python + Go..."
    // description (a correct match, not a test bug).
    fireEvent.change(screen.getByPlaceholderText(/search base images/i), {
      target: { value: "3.12" },
    })

    const radios = screen.getAllByRole("radio")
    expect(radios).toHaveLength(1)
    expect(radios[0]).toHaveAccessibleName(/python/i)
  })

  it("shows a not-found message when nothing matches", async () => {
    stubCatalogFetch()
    render(<RuntimeConfig value={emptyValue()} onChange={vi.fn()} />)
    await waitFor(() => expect(screen.getAllByRole("radio").length).toBe(BASE_IMAGES.length))

    fireEvent.change(screen.getByPlaceholderText(/search base images/i), {
      target: { value: "nonexistent-image-xyz" },
    })

    expect(screen.queryAllByRole("radio")).toHaveLength(0)
    expect(screen.getByText(/no images found/i)).toBeInTheDocument()
  })

  it("gives every catalogue row a hoverable info affordance instead of wrapping the description inline", async () => {
    stubCatalogFetch()
    render(<RuntimeConfig value={emptyValue()} onChange={vi.fn()} />)
    await waitFor(() => expect(screen.getAllByRole("radio").length).toBe(BASE_IMAGES.length))

    // One "i" affordance per row, and the long description text is not
    // dumped inline under the label (that was the two-line-wrap complaint).
    expect(screen.getAllByRole("button", { name: /image details/i })).toHaveLength(BASE_IMAGES.length)
    const longDescription = BASE_IMAGES.find((b) => b.description.length > 40)!.description
    expect(screen.queryByText(longDescription)).not.toBeInTheDocument()
  })
})

// ── layout="sections" — the wizard's Container step ──────────────────────
//
// New crew wrapped this component whole inside one section, so its four-tab
// strip appeared inside a create surface: a navigation model no other door
// has, and the reason Container kept reading as a different product from the
// rest of the wizard. The sections layout renders the same controls with the
// chrome /design specifies — base image and tooling on the page, the rest
// folded — and the point of these tests is that folding is all it is.
describe("RuntimeConfig — sections layout", () => {
  function renderSections() {
    return render(
      <RuntimeConfig
        value={{ runtimeImage: "", devcontainerConfig: "", miseConfig: "" }}
        onChange={() => {}}
        layout="sections"
      />,
    )
  }

  it("leads with base image and tooling instead of a tab strip", async () => {
    renderSections()
    expect(await screen.findByText("Base image")).toBeInTheDocument()
    expect(screen.getByText("Preinstalled tooling")).toBeInTheDocument()
    // The tab strip it replaces.
    expect(screen.queryByRole("tab", { name: /Features/ })).toBeNull()
  })

  it("keeps runtimes, security and the generated files reachable", async () => {
    renderSections()
    await screen.findByText("Base image")
    // Folded, not dropped: removing settings the create path has today would
    // be a capability change wearing a redesign's clothes.
    expect(screen.getByRole("button", { name: /Language runtimes/ })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /Security/ })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /Generated files/ })).toBeInTheDocument()
  })

  it("says what is inside a fold without opening it", async () => {
    renderSections()
    await screen.findByText("Base image")
    expect(screen.getByRole("button", { name: /Language runtimes/ })).toHaveTextContent(/none pinned/i)
    expect(screen.getByRole("button", { name: /Security/ })).toHaveTextContent(/standard sandbox/i)
  })

  it("still draws the tab strip in the default layout", async () => {
    render(
      <RuntimeConfig
        value={{ runtimeImage: "", devcontainerConfig: "", miseConfig: "" }}
        onChange={() => {}}
      />,
    )
    expect(await screen.findByRole("tab", { name: /Features/ })).toBeInTheDocument()
    expect(screen.queryByText("Preinstalled tooling")).toBeNull()
  })
})

// ── A row is not a button inside a button ────────────────────────────────
//
// Each base-image row was a <button role="radio"> that CONTAINED the "i"
// tooltip trigger, which is also a <button>. Nested interactive content is
// invalid HTML: React logs a hydration error for it on every render of this
// list, the dev overlay covers the page, and what a screen reader or a
// keyboard user gets from the inner control is undefined.
describe("RuntimeConfig — the base image rows", () => {
  it("does not nest the details trigger inside the radio", async () => {
    render(
      <RuntimeConfig
        value={{ runtimeImage: "", devcontainerConfig: "", miseConfig: "" }}
        onChange={() => {}}
      />,
    )
    await screen.findByRole("radiogroup", { name: /base image/i })

    const nested = Array.from(document.querySelectorAll("button")).filter(
      (b) => b.querySelector("button") !== null,
    )
    expect(nested).toEqual([])
  })

  it("keeps both controls reachable", async () => {
    render(
      <RuntimeConfig
        value={{ runtimeImage: "", devcontainerConfig: "", miseConfig: "" }}
        onChange={() => {}}
      />,
    )
    const group = await screen.findByRole("radiogroup", { name: /base image/i })
    expect(within(group).getAllByRole("radio").length).toBeGreaterThan(1)
    expect(within(group).getAllByRole("button", { name: /image details/i }).length).toBeGreaterThan(1)
  })
})

// ── The tooling browser under sections ───────────────────────────────────
//
// The tabs-era row is a table: name, ref, description, publisher, tier, size
// hint and a Switch. Right for the Settings editor, six columns of metadata
// for a yes/no question on a create step — and the features already chosen
// scroll away above the list.
describe("RuntimeConfig — tooling under sections", () => {
  // The shared stub serves an empty catalogue; these tests need rows.
  const FEATURES = [
    {
      ref: "ghcr.io/devcontainers/features/anaconda:1",
      name: "Anaconda",
      description: "Python distribution",
      category: "languages",
      icon: "python",
      size_hint: "1.2 GB",
      publisher: "devcontainers",
      tier: "official",
    },
    {
      ref: "ghcr.io/some-person/features/oddity:1",
      name: "Oddity",
      description: "A community feature",
      category: "tools",
      icon: "",
      size_hint: "4 MB",
      publisher: "some-person",
      tier: "community",
    },
  ]

  function stubWithFeatures() {
    vi.stubGlobal("fetch", vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({ features: FEATURES, runtimes: [] }),
      text: async () => "{}",
    })))
  }

  function renderSections() {
    stubWithFeatures()
    return render(
      <RuntimeConfig
        value={{ runtimeImage: "", devcontainerConfig: "", miseConfig: "" }}
        onChange={() => {}}
        layout="sections"
      />,
    )
  }

  it("offers categories without a count column", async () => {
    renderSections()
    await screen.findByText("Preinstalled tooling")
    const languages = screen.getByRole("button", { name: "Languages" })
    // "Languages 116" is the tabs layout; the label alone is the specimen.
    expect(languages.textContent).toBe("Languages")
    expect(screen.queryByRole("button", { name: /^All/ })).toBeNull()
  })

  it("toggles a feature by clicking the row, not by hunting a switch", async () => {
    renderSections()
    await screen.findByText("Preinstalled tooling")

    const row = await screen.findByRole("button", { name: /Anaconda/ })
    expect(row).toHaveAttribute("aria-pressed", "false")
    fireEvent.click(row)
    await waitFor(() => expect(row).toHaveAttribute("aria-pressed", "true"))
  })

  it("keeps what you picked on screen once the list scrolls", async () => {
    renderSections()
    await screen.findByText("Preinstalled tooling")
    fireEvent.click(await screen.findByRole("button", { name: /Anaconda/ }))

    // A chip above the list, removable, holding its place while you search on.
    const chip = await screen.findByRole("button", { name: /^Remove / })
    expect(chip).toBeInTheDocument()
    fireEvent.click(chip)
    await waitFor(() => expect(screen.queryByRole("button", { name: /^Remove / })).toBeNull())
  })

  // The runtimes catalogue arrives in the server's order, which puts Agebox
  // and Mkcert above node and python — six rows of generic package glyphs
  // before anything recognisable. Branded entries sort first so the first
  // screen is the tools people came for.
  it("puts entries that have a brand mark first", async () => {
    renderSections()
    await screen.findByText("Preinstalled tooling")

    const rows = await screen.findAllByRole("button", { name: /Anaconda|Oddity/ })
    // Anaconda resolves to a brand mark; Oddity does not.
    expect(rows[0].textContent).toMatch(/Anaconda/)
  })

  it("flags a feature nobody official published, and says nothing when official", async () => {
    renderSections()
    await screen.findByText("Preinstalled tooling")

    // Tier is the one annotation kept from the dense row: dropping it to match
    // a specimen would lose a trust signal rather than lose chrome.
    const odd = await screen.findByRole("button", { name: /Oddity/ })
    expect(odd).toHaveTextContent("community")
    // "official" is the unremarkable case and does not earn a column.
    expect(await screen.findByRole("button", { name: /Anaconda/ })).not.toHaveTextContent(/official/i)
  })
})
