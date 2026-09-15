// Files this routine runs: the tree, the missing pill, and the read-only
// preview that opens beside it and closes again.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { RoutineFilesCard, crewFileDownloadUrl } from "../routine-files-card"
import type { RoutineFile } from "@/lib/routine-files"

const { fetcher } = vi.hoisted(() => ({ fetcher: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetcher, broadcastSessionExpired: vi.fn() }))
vi.mock("next/link", () => ({ default: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a> }))
vi.mock("@/components/features/chat/files/file-preview", () => ({ FilePreview: ({ name }: { name: string }) => <div>binary preview {name}</div> }))

const files: RoutineFile[] = [
  { path: "scripts/ledger-post.go", language: "go", step_ids: ["post"], size_bytes: 4120, present: true, description: "Posts one invoice" },
  { path: "scripts/normalize_lines.py", language: "py", step_ids: ["extract"], size_bytes: 1800, present: true },
  { path: "checks/invoice-rules.yaml", language: "yaml", step_ids: ["verify"], size_bytes: 614, present: true },
  { path: "scripts/notify-finance.ts", language: "ts", step_ids: ["notify"], present: false, status: "missing" },
]
const names: Record<string, string> = { post: "Post to the ledger", extract: "Read the invoice", verify: "Check the extraction", notify: "Tell #finance" }
const nameOf = (id: string) => names[id] ?? id

beforeEach(() => {
  fetcher.mockReset()
})

describe("<RoutineFilesCard>", () => {
  it("draws the tree with folders, the step that uses each file, sizes and the missing pill", () => {
    render(<RoutineFilesCard files={files} workspaceId="ws" crewId="crew_fin" nameOf={nameOf} />)
    expect(screen.getByText("Files this routine runs · 4")).toBeInTheDocument()
    expect(screen.getByTestId("routine-file-dir-checks")).toHaveTextContent("1 file")
    expect(screen.getByTestId("routine-file-dir-scripts")).toHaveTextContent("3 files")
    expect(screen.getByTestId("routine-file-scripts/ledger-post.go")).toHaveTextContent("used by Post to the ledger")
    expect(screen.getByTestId("routine-file-scripts/ledger-post.go")).toHaveTextContent("4.0 kB")
    expect(screen.getByTestId("routine-file-scripts/notify-finance.ts")).toHaveTextContent("Missing on the share")
    expect(screen.getAllByText("Missing on the share")).toHaveLength(1)
  })

  it("tells an unverified share apart from a missing file", () => {
    // The share could not be listed: nothing is "missing", every row is "Not verified".
    const unverified = files.map((f) => ({ ...f, present: false, status: "unverified" as const }))
    render(<RoutineFilesCard files={unverified} workspaceId="ws" crewId="crew_fin" nameOf={nameOf} />)
    expect(screen.queryByText("Missing on the share")).toBeNull()
    expect(screen.getAllByText("Not verified")).toHaveLength(4)
    expect(screen.getByText("not verified")).toBeInTheDocument()
    // Older servers send only the boolean: false alone never reads as missing,
    // so the preview still tries to fetch the file instead of declaring it gone.
    fetcher.mockResolvedValue({ ok: true, status: 200, text: async () => "package main\n" })
    fireEvent.click(screen.getByTestId("routine-file-scripts/ledger-post.go"))
    expect(screen.queryByText(/is not on the crew share/)).toBeNull()
    expect(fetcher).toHaveBeenCalled()
  })

  it("collapses and expands a folder", () => {
    render(<RoutineFilesCard files={files} workspaceId="ws" crewId="crew_fin" nameOf={nameOf} />)
    fireEvent.click(screen.getByTestId("routine-file-dir-scripts"))
    expect(screen.queryByTestId("routine-file-scripts/ledger-post.go")).toBeNull()
    fireEvent.click(screen.getByTestId("routine-file-dir-scripts"))
    expect(screen.getByTestId("routine-file-scripts/ledger-post.go")).toBeInTheDocument()
  })

  it("opens a read-only preview from the crew share beside the tree and closes it", async () => {
    fetcher.mockResolvedValue({ ok: true, status: 200, text: async () => "package main\n\nfunc main() {}\n" })
    render(<RoutineFilesCard files={files} workspaceId="ws" crewId="crew_fin" nameOf={nameOf} />)
    fireEvent.click(screen.getByTestId("routine-file-scripts/ledger-post.go"))
    const preview = screen.getByTestId("routine-file-preview")
    expect(preview).toHaveTextContent("/crew/shared/scripts/ledger-post.go")
    await waitFor(() => expect(preview).toHaveTextContent("package main"))
    expect(preview).toHaveTextContent("read-only")
    expect(preview).toHaveTextContent("Posts one invoice")
    expect(fetcher).toHaveBeenCalledWith(crewFileDownloadUrl("crew_fin", "ws", "scripts/ledger-post.go"), expect.anything())
    expect(fetcher.mock.calls[0][0]).toContain("path=shared%2Fscripts%2Fledger-post.go")
    expect(screen.getByRole("link", { name: /Open in Files/ })).toHaveAttribute("href", "/crews?crew=crew_fin")
    expect(screen.getByTestId("routine-file-scripts/ledger-post.go")).toHaveAttribute("aria-current", "true")
    fireEvent.click(screen.getByRole("button", { name: "Close preview" }))
    expect(screen.queryByTestId("routine-file-preview")).toBeNull()
  })

  it("explains a missing file instead of fetching it", () => {
    render(<RoutineFilesCard files={files} workspaceId="ws" crewId="crew_fin" nameOf={nameOf} />)
    fireEvent.click(screen.getByTestId("routine-file-scripts/notify-finance.ts"))
    expect(screen.getByTestId("routine-file-preview")).toHaveTextContent("not on the crew share")
    expect(screen.getByTestId("routine-file-preview")).toHaveTextContent("crewship crew files save")
    expect(fetcher).not.toHaveBeenCalled()
  })

  it("renders nothing without files", () => {
    const { container } = render(<RoutineFilesCard files={[]} workspaceId="ws" nameOf={nameOf} />)
    expect(container).toBeEmptyDOMElement()
  })
})
