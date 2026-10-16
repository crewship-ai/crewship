import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { webcrypto } from "node:crypto"
import { apiFetch } from "@/lib/api-fetch"
import { NativeSlideImage, SlideImagesProvider, useSlideImageCache } from "../slide-images"
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
const bytes = new Uint8Array([1, 2, 3])
const sha = "039058c6f2c0cb492c533b0a4d14ef77cc0f78abccced5287d84a1a2011cfb81"
const image = { asset_id: "asset", expected_sha256: sha, alt: "Private" }
const revoke = vi.fn(), create = vi.fn()
const fetch = vi.mocked(apiFetch)
function Deny() { const cache = useSlideImageCache(); return <button onClick={() => cache?.deny("Access revoked")}>Deny main</button> }
function Fixture({ revision = 2, historic = 1, reload = 0, denied = false }: { revision?: number; historic?: number; reload?: number; denied?: boolean }) {
 return <SlideImagesProvider workspaceId="workspace" slug="deck" pageId="page" revision={revision} reloadKey={reload} denied={denied}><Deny /><svg data-testid="main"><NativeSlideImage image={image} width={10} height={10} /></svg><SlideImagesProvider workspaceId="workspace" slug="deck" pageId="page" revision={historic}><svg data-testid="historic"><NativeSlideImage image={image} width={10} height={10} /></svg></SlideImagesProvider></SlideImagesProvider>
}
beforeEach(() => {
 vi.stubGlobal("crypto", webcrypto); let serial = 0
 create.mockReset().mockImplementation(() => `blob:private-${++serial}`); revoke.mockReset()
 vi.stubGlobal("URL", class extends URL { static createObjectURL = create; static revokeObjectURL = revoke })
 fetch.mockReset().mockImplementation(async path => String(path).includes("/bytes?") ? new Response(bytes, { headers: { "Content-Type": "image/png" } }) : new Response(JSON.stringify({ id: "asset", page_id: "page", sha256: sha, content_type: "image/png", width: 1, height: 1, byte_size: 3, state: "retained", created_at: "now", expires_at: "later" }), { headers: { "Content-Type": "application/json" } }))
})
afterEach(() => { cleanup(); vi.unstubAllGlobals() })
const href = (name: string) => screen.getByTestId(name).querySelector("image")?.getAttribute("href")
it("clears already-loaded history together with main denial, and only explicit reload recovers", async () => {
 const view = render(<Fixture />); await waitFor(() => expect(create).toHaveBeenCalledTimes(2))
 expect(href("main")).toBeTruthy(); expect(href("historic")).toBeTruthy()
 fireEvent.click(screen.getByText("Deny main")); expect(href("main")).toBeUndefined(); expect(href("historic")).toBeUndefined(); expect(revoke).toHaveBeenCalledTimes(2)
 view.rerender(<Fixture historic={3} />); await waitFor(() => expect(screen.getAllByText(/Image unavailable: Access revoked/)).toHaveLength(2)); expect(create).toHaveBeenCalledTimes(2)
 view.rerender(<Fixture historic={3} reload={1} />); await waitFor(() => expect(create).toHaveBeenCalledTimes(4)); expect(href("historic")).toBeTruthy()
})
it("keeps normal historical revision changes isolated from the active revision", async () => {
 const view = render(<Fixture />); await waitFor(() => expect(create).toHaveBeenCalledTimes(2)); const main = href("main"), historic = href("historic")
 view.rerender(<Fixture historic={3} />); await waitFor(() => expect(create).toHaveBeenCalledTimes(3)); expect(href("main")).toBe(main); expect(href("historic")).not.toBe(historic); expect(revoke).toHaveBeenCalledWith(historic); expect(revoke).not.toHaveBeenCalledWith(main)
})
it("propagates outer Page denial to a loaded historical preview", async () => {
 const view = render(<Fixture />); await waitFor(() => expect(create).toHaveBeenCalledTimes(2)); view.rerender(<Fixture denied />)
 await waitFor(() => expect(revoke).toHaveBeenCalledTimes(2)); expect(href("main")).toBeUndefined(); expect(href("historic")).toBeUndefined()
})
it("does not recreate a historical image from a response arriving after main denial", async () => {
 let finish!: (response: Response) => void
 const normal = fetch.getMockImplementation()!
 let reads = 0
 fetch.mockImplementation((path, options) => {
  if (!String(path).includes("/bytes?") && ++reads === 2) return new Promise(resolve => { finish = resolve })
  return normal(path, options)
 })
 render(<Fixture />); await waitFor(() => expect(create).toHaveBeenCalledTimes(1)); expect(finish).toBeTypeOf("function")
 fireEvent.click(screen.getByText("Deny main"))
 finish(new Response(JSON.stringify({ id: "asset", page_id: "page", sha256: sha, content_type: "image/png", width: 1, height: 1, byte_size: 3, state: "retained", created_at: "now", expires_at: "later" })))
 await waitFor(() => expect(screen.getAllByText(/Image unavailable: Access revoked/)).toHaveLength(2)); expect(create).toHaveBeenCalledTimes(1); expect(fetch).toHaveBeenCalledTimes(3)
})
