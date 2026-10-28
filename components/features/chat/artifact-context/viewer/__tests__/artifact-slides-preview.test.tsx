import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import fixture from "@/testdata/artifact-slides-projection.json"
import { ArtifactSlidesPreview } from "../artifact-slides-preview"

const mock = vi.hoisted(() => ({ send: vi.fn(), user: "reader", font: vi.fn() }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: mock.user } } }) }))
vi.mock("@/lib/api-fetch", () => ({ AUTH_EVENT: "auth:session-expired", apiFetch: (...args: unknown[]) => mock.send(...args) }))
const props = { workspaceId: "workspace", pageId: "page", codeVersion: 2, artifactKind: "slides", path: "slides/intro.html", title: "Slides", selectMode: true, onSelect: vi.fn(), onNavigate: vi.fn() }
function mount(patch: Partial<typeof props> = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  const tree = (next = {}) => <QueryClientProvider client={client}><textarea aria-label="Comment draft" defaultValue="Keep this draft" /><ArtifactSlidesPreview {...props} {...patch} {...next} /></QueryClientProvider>
  return { client, tree, ...render(tree()) }
}
beforeEach(() => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACT_SLIDES", "true")
  mock.user = "reader"; mock.font.mockReset().mockResolvedValue([{ family: "Go Regular" }])
  Object.defineProperty(document, "fonts", { configurable: true, value: { load: mock.font, ready: Promise.resolve() } })
  mock.send.mockReset().mockImplementation(() => Promise.resolve(new Response(JSON.stringify(fixture))))
  props.onSelect.mockReset(); props.onNavigate.mockReset()
})
afterEach(() => { vi.unstubAllEnvs(); vi.restoreAllMocks() })

it("uses the selected code pin and real typed canvas to send the exact Go-authored anchor", async () => {
  mount()
  const region = await screen.findByRole("region", { name: "Slides" })
  fireEvent.pointerDown(within(region).getByRole("button", { name: "text 1" }))
  expect(props.onSelect).toHaveBeenCalledExactlyOnceWith({ selector: "#title", quote: "Revenue" })
  expect(mock.send.mock.calls[0][0]).toBe("/api/v1/pages/%40page/artifact/slides?workspace_id=workspace&version=2")
  expect(mock.font).toHaveBeenCalledWith('16px "Go Regular"')
  expect(region.querySelector("iframe")).toBeNull()
})

it("navigates actual thumbnail canvases and presents existing typed slides without edit controls", async () => {
  mount({ selectMode: false })
  const region = await screen.findByRole("region", { name: "Slides" })
  const thumbnail = within(region).getByRole("button", { name: "Slide 2" })
  expect(thumbnail.querySelector("svg")).not.toBeNull()
  fireEvent.click(thumbnail)
  expect(props.onNavigate).toHaveBeenCalledExactlyOnceWith("slides/details.html")
  expect(within(region).queryByRole("button", { name: "text 1" })).toBeNull()
  fireEvent.click(within(region).getByRole("button", { name: "Present" }))
  const dialog = screen.getByRole("dialog", { name: "Present slides" })
  expect(within(dialog).getByText("Revenue")).toBeInTheDocument()
  fireEvent.keyDown(dialog, { key: "ArrowRight" })
  expect(within(dialog).getByText("North")).toBeInTheDocument()
  fireEvent.keyDown(dialog, { key: "Escape" })
  expect(screen.queryByRole("dialog")).toBeNull()
})

it.each(["", "false", "1", "TRUE"])("makes no projection or PDF request when the build flag is %s", async flag => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACT_SLIDES", flag)
  mount(); await act(async () => {})
  expect(mock.send).not.toHaveBeenCalled()
  expect(screen.queryByRole("region", { name: "Slides" })).toBeNull()
})

it("makes no request without typed selected metadata or a positive code pin", async () => {
  const view = mount({ artifactKind: undefined })
  await act(async () => {})
  view.rerender(view.tree({ codeVersion: 0 }))
  await act(async () => {})
  expect(mock.send).not.toHaveBeenCalled()
})

it("waits for the packaged font and refuses a missing font instead of substituting", async () => {
  mock.font.mockResolvedValue([])
  mount()
  await screen.findByText("Slides font is unavailable.")
  expect(screen.queryByRole("region", { name: "Slides" })).toBeNull()
})

it.each([401, 403, 404, 409])("retires cached rendered data on authoritative status %s while retaining the draft", async status => {
  const view = mount(); await screen.findByRole("region", { name: "Slides" })
  mock.send.mockResolvedValue(new Response('{}', { status }))
  await act(async () => { await view.client.invalidateQueries() })
  await waitFor(() => expect(screen.queryByRole("region", { name: "Slides" })).toBeNull())
  expect(screen.getByLabelText("Comment draft")).toHaveValue("Keep this draft")
  expect(view.client.getQueriesData({ queryKey: ["artifact-slides"] }).every(([, data]) => data === undefined)).toBe(true)
})

it("retires a projection on authentication retirement without requesting it again", async () => {
  mount(); await screen.findByRole("region", { name: "Slides" })
  act(() => window.dispatchEvent(new Event("auth:session-expired")))
  await waitFor(() => expect(screen.queryByRole("region", { name: "Slides" })).toBeNull())
  expect(mock.send).toHaveBeenCalledTimes(1)
})

it("aborts a late old-pin response and never exposes another identity's cached slides", async () => {
  let finish!: (response: Response) => void
  mock.send.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
  const view = mount()
  await waitFor(() => expect(mock.send).toHaveBeenCalledTimes(1))
  const signal = mock.send.mock.calls[0][1].signal as AbortSignal
  mock.user = "another-reader"
  mock.send.mockResolvedValue(new Response('{}', { status: 403 }))
  view.rerender(view.tree({ codeVersion: 3 }))
  await waitFor(() => expect(signal.aborted).toBe(true))
  await act(async () => finish(new Response(JSON.stringify(fixture))))
  expect(screen.queryByText("Revenue")).toBeNull()
  expect(screen.queryByRole("region", { name: "Slides" })).toBeNull()
})

it("downloads only the bounded authorized selected-version PDF", async () => {
  const create = vi.fn().mockReturnValue("blob:slides")
  const revoke = vi.fn()
  Object.defineProperty(URL, "createObjectURL", { configurable: true, value: create })
  Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: revoke })
  const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})
  mount(); await screen.findByRole("region", { name: "Slides" })
  mock.send.mockResolvedValue(new Response('%PDF-1.7\n%%EOF', { headers: { 'content-type': 'application/pdf' } }))
  fireEvent.click(screen.getByRole("button", { name: "Download PDF" }))
  await waitFor(() => expect(click).toHaveBeenCalledTimes(1))
  expect(mock.send.mock.calls.at(-1)![0]).toBe("/api/v1/pages/%40page/artifact/export.pdf?workspace_id=workspace&version=2")
  expect(create).toHaveBeenCalledTimes(1)
  expect(revoke).toHaveBeenCalledWith("blob:slides")
})

it("accepts an own constructor anchor and refuses an inherited constructor as a selection", async () => {
  const view = mount({ path: "slides/details.html" })
  let region = await screen.findByRole("region", { name: "Slides" })
  fireEvent.pointerDown(within(region).getByRole("button", { name: "table 1" }))
  expect(props.onSelect).toHaveBeenCalledExactlyOnceWith({ selector: "#constructor", quote: "North12" })
  props.onSelect.mockReset()
  const changed = { ...structuredClone(fixture), anchors: {} }
  const table = changed.deck.slides[1].elements[0]
  if (!("table" in table) || !table.table) throw new Error("Expected the table fixture")
  table.table.cells = [["Changed", "17"]]
  mock.send.mockResolvedValue(new Response(JSON.stringify(changed)))
  await act(async () => { await view.client.invalidateQueries() })
  await screen.findAllByText("Changed")
  region = screen.getByRole("region", { name: "Slides" })
  fireEvent.pointerDown(within(region).getByRole("button", { name: "table 1" }))
  expect(props.onSelect).not.toHaveBeenCalled()
  expect(screen.getByText("Choose authored text or a table to comment on.")).toBeInTheDocument()
})

it("renders source-looking strings as React text rather than agent markup", async () => {
  const input = structuredClone(fixture)
  const text = input.deck.slides[0].elements[0]
  if (!("text" in text) || !text.text) throw new Error("Expected the text fixture")
  text.text.value = '<img src="https://evil.invalid/secret" onerror="fetch(\'/private\')">'
  mock.send.mockResolvedValue(new Response(JSON.stringify(input)))
  mount()
  const region = await screen.findByRole("region", { name: "Slides" })
  expect(within(region).getAllByText(/<img src=/)).not.toHaveLength(0)
  expect(region.querySelector("img,script,iframe,link")).toBeNull()
})

it("aborts an unmounted projection and removes its cached content", async () => {
  let finish!: (response: Response) => void
  mock.send.mockImplementation(() => new Promise(resolve => { finish = resolve }))
  const view = mount()
  await waitFor(() => expect(mock.send).toHaveBeenCalledTimes(1))
  const signal = mock.send.mock.calls[0][1].signal as AbortSignal
  view.unmount()
  expect(signal.aborted).toBe(true)
  await act(async () => finish(new Response(JSON.stringify(fixture))))
  expect(view.client.getQueriesData({ queryKey: ["artifact-slides"] }).every(([, data]) => data === undefined)).toBe(true)
})

it.each(["oversized", "html"])("never creates a download for an invalid %s PDF response", async kind => {
  const create = vi.fn()
  Object.defineProperty(URL, "createObjectURL", { configurable: true, value: create })
  mount(); await screen.findByRole("region", { name: "Slides" })
  mock.send.mockResolvedValue(kind === "oversized"
    ? new Response(new Uint8Array((8 << 20) + 1), { headers: { 'content-type': 'application/pdf' } })
    : new Response('%PDF-<script>private</script>', { headers: { 'content-type': 'text/html' } }))
  fireEvent.click(screen.getByRole("button", { name: "Download PDF" }))
  await screen.findByText(kind === "oversized" ? "PDF exceeds the download data limit." : "PDF download is unavailable. Reload to check access or slide overflow.")
  expect(create).not.toHaveBeenCalled()
})

it("retires the preview when PDF authority is denied", async () => {
  mount(); await screen.findByRole("region", { name: "Slides" })
  mock.send.mockResolvedValue(new Response('{}', { status: 403 }))
  fireEvent.click(screen.getByRole("button", { name: "Download PDF" }))
  await waitFor(() => expect(screen.queryByRole("region", { name: "Slides" })).toBeNull())
})

it("aborts late PDF bytes on auth retirement before creating an object URL", async () => {
  const create = vi.fn()
  Object.defineProperty(URL, "createObjectURL", { configurable: true, value: create })
  mount(); await screen.findByRole("region", { name: "Slides" })
  let finish!: (response: Response) => void
  mock.send.mockImplementation(() => new Promise(resolve => { finish = resolve }))
  fireEvent.click(screen.getByRole("button", { name: "Download PDF" }))
  await waitFor(() => expect(mock.send).toHaveBeenCalledTimes(2))
  const signal = mock.send.mock.calls[1][1].signal as AbortSignal
  act(() => window.dispatchEvent(new Event("auth:session-expired")))
  expect(signal.aborted).toBe(true)
  await act(async () => finish(new Response('%PDF-1.7', { headers: { 'content-type': 'application/pdf' } })))
  expect(create).not.toHaveBeenCalled()
})

it("refuses a valid JSON response beyond the aggregate 16 MiB wire limit without rendering", async () => {
  mock.send.mockResolvedValue(new Response(" ".repeat(16 << 20) + JSON.stringify(fixture)))
  mount()
  await screen.findByText("Slides exceeds the preview data limit.")
  expect(screen.queryByRole("region", { name: "Slides" })).toBeNull()
})
