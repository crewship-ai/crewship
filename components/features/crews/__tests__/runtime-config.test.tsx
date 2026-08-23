import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
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
