import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, waitFor } from "@testing-library/react"

// =============================================================================
// "Photo → filed under 45 s on mobile" is a target in the ask-packs PRD, and
// an upload that fails is the single largest way that target is missed. So
// both outcomes are recorded — and the failure carries a CLASSIFICATION, never
// the server's error body, which the composer already refuses to put on screen
// because it can echo paths and SQL fragments.
//
// The filename is the obvious leak and it is the one asserted hardest: a
// document's name is frequently the most sensitive thing about it
// ("Q3-layoffs.xlsx", "smlouva-novak.pdf").
// =============================================================================

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", loading: false }),
}))

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() },
}))

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))

import { apiFetch } from "@/lib/api-fetch"
import { useComposerStore } from "@/stores/composer-store"
import { resetChatTelemetry, setChatTelemetrySink, type ChatEvent } from "@/lib/telemetry"

import { useAttachmentUpload } from "../attachment-zone"

const FILENAME = "Q3-layoffs-smlouva-novak.pdf"

function pdf(name = FILENAME, size = 2048): File {
  return new File([new Uint8Array(size)], name, { type: "application/pdf" })
}

let events: ChatEvent[]
const named = (name: string) => events.filter((e) => e.name === name)

/** Drives the one upload path directly — the drop zone, the paperclip, the
 *  camera and a form field all reach it through this hook. */
function Harness({ files, source }: { files: File[]; source?: "picker" | "drop" | "camera" }) {
  const { upload } = useAttachmentUpload("agent-1", "sess-1")
  return (
    <button type="button" onClick={() => void upload(files, source)}>
      upload
    </button>
  )
}

function go(files: File[], source?: "picker" | "drop" | "camera") {
  render(<Harness files={files} source={source} />)
  fireEvent.click(screen.getByText("upload"))
}

beforeEach(() => {
  useComposerStore.setState({ attachments: {} })
  resetChatTelemetry()
  events = []
  setChatTelemetrySink((e) => events.push(e))
  vi.mocked(apiFetch).mockReset()
})

afterEach(cleanup)

describe("a successful upload", () => {
  beforeEach(() => {
    vi.mocked(apiFetch).mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ path: "attachments/sess-1/f.pdf", agent_path: "/output/x/f.pdf" }),
    } as unknown as Response)
  })

  it("records exactly one event, with the class of file and its size", async () => {
    go([pdf()], "camera")
    await waitFor(() => expect(named("attachment_uploaded")).toHaveLength(1))
    expect(named("attachment_uploaded")[0].payload).toMatchObject({
      session_id: "sess-1",
      mime_kind: "pdf",
      size_bytes: 2048,
      source: "camera",
    })
  })

  it("records one event per file in a batch, and no failures", async () => {
    go([pdf("a.pdf"), pdf("b.pdf")], "drop")
    await waitFor(() => expect(named("attachment_uploaded")).toHaveLength(2))
    expect(named("attachment_upload_failed")).toHaveLength(0)
  })
})

describe("a refused upload", () => {
  it("classifies an HTTP failure rather than repeating the server's words", async () => {
    vi.mocked(apiFetch).mockResolvedValue({
      ok: false,
      status: 500,
      json: async () => ({ error: "create parent dir /srv/data/uploads: permission denied" }),
    } as unknown as Response)

    go([pdf()], "picker")
    await waitFor(() => expect(named("attachment_upload_failed")).toHaveLength(1))
    const payload = named("attachment_upload_failed")[0].payload
    expect(payload).toMatchObject({ reason: "http_error", status: 500, mime_kind: "pdf" })
    expect(JSON.stringify(payload)).not.toContain("permission denied")
    expect(JSON.stringify(payload)).not.toContain("/srv/data")
  })

  it("tells a dropped connection apart from a server refusal", async () => {
    vi.mocked(apiFetch).mockRejectedValue(new TypeError("Failed to fetch"))
    go([pdf()], "picker")
    await waitFor(() => expect(named("attachment_upload_failed")).toHaveLength(1))
    expect(named("attachment_upload_failed")[0].payload.reason).toBe("network")
  })

  it("names the cap when the file was too big to even try", async () => {
    const huge = pdf("huge.pdf", 26 * 1024 * 1024)
    go([huge], "picker")
    await waitFor(() => expect(named("attachment_upload_failed")).toHaveLength(1))
    expect(named("attachment_upload_failed")[0].payload).toMatchObject({ reason: "too_large" })
    // It never reached the network, so there is no success to report either.
    expect(named("attachment_uploaded")).toHaveLength(0)
    expect(apiFetch).not.toHaveBeenCalled()
  })

  it("records exactly one failure per refused file, not one per batch", async () => {
    vi.mocked(apiFetch).mockResolvedValue({
      ok: false,
      status: 500,
      json: async () => ({ error: "nope" }),
    } as unknown as Response)
    go([pdf("a.pdf"), pdf("b.pdf")], "drop")
    await waitFor(() => expect(named("attachment_upload_failed")).toHaveLength(2))
  })
})

describe("no filename ever leaves the composer", () => {
  it("is absent from a success and from every failure shape", async () => {
    vi.mocked(apiFetch).mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: async () => ({ path: "p", agent_path: "a" }),
    } as unknown as Response)
    go([pdf()], "picker")
    await waitFor(() => expect(named("attachment_uploaded")).toHaveLength(1))
    cleanup()

    vi.mocked(apiFetch).mockRejectedValue(new Error(`upload of ${FILENAME} failed`))
    go([pdf()], "picker")
    await waitFor(() => expect(named("attachment_upload_failed")).toHaveLength(1))

    const serialized = JSON.stringify(events)
    expect(serialized).not.toContain("Q3-layoffs")
    expect(serialized).not.toContain("novak")
    expect(serialized).not.toContain(".pdf")
  })
})

describe("telemetry cannot break an upload", () => {
  it("a throwing sink still leaves the attachment ready", async () => {
    setChatTelemetrySink(() => {
      throw new Error("sink exploded")
    })
    vi.mocked(apiFetch).mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ path: "attachments/sess-1/f.pdf", agent_path: "/output/x/f.pdf" }),
    } as unknown as Response)

    go([pdf()], "picker")
    await waitFor(() => {
      const list = useComposerStore.getState().attachments["sess-1"] ?? []
      expect(list).toHaveLength(1)
      expect(list[0].status).toBe("ready")
    })
  })
})
