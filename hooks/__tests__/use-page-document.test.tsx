import { act, renderHook, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import * as Y from "yjs"
import { documentSchema } from "@/lib/documents/schema"
import { documentRequest, encodeUpdate, decodeUpdate, usePageDocument } from "../use-page-document"
const fetchMock = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))
afterEach(() => { vi.restoreAllMocks(); fetchMock.mockReset() })
function model(value = "") { return documentSchema.create({type:"doc",content:[{type:"paragraph",attrs:{id:"text"},...(value?{content:[{type:"text",text:value}]}:{})}]},2) }
function text(doc: Y.Doc) { const value=documentSchema.textNode(doc,"text"); if (!(value instanceof Y.XmlText)) throw new Error("typed text required"); return value }
const initial=model();const emptySnapshot=documentSchema.snapshot(initial);initial.destroy()
function response(body: unknown, status = 200) { return Promise.resolve({ ok: status < 400, status, json: async () => body }) }
describe("document durable synchronization", () => {
  it("round-trips binary updates", () => { expect(decodeUpdate(encodeUpdate(new Uint8Array([0, 255, 128])))).toEqual(new Uint8Array([0, 255, 128])) })
  it("merges remote changes while retrying the same unacknowledged local update", async () => {
    const server = model("base")
    const ids: string[] = []; let fail = true
    fetchMock.mockImplementation((_url: string, init?: RequestInit) => {
      if (init?.method === "POST") { const body = JSON.parse(init.body as string); ids.push(body.id); if (fail) return response({ error: "offline" }, 503); Y.applyUpdate(server, decodeUpdate(body.update)); return response({ rev: 2 }) }
      return response({ schema: 1, rev: 1, epoch: "go:node:1", can_write: true, snapshot: encodeUpdate(Y.encodeStateAsUpdate(server)) })
    })
    const { result, unmount } = renderHook(() => usePageDocument("w", "p"))
    await waitFor(() => expect(result.current.state).toBeDefined())
    act(() => { for (let i = 0; i < 100; i++) text(result.current.doc).insert(4, " local") })
    await waitFor(() => expect(ids.length).toBeGreaterThan(0), { timeout: 4000 })
    text(server).insert(0, "remote "); fail = false
    await waitFor(() => expect(result.current.pending).toBe(0), { timeout: 4000 })
    expect(new Set(ids).size).toBe(1)
    expect(text(result.current.doc).toString()).toContain("remote")
    expect(text(result.current.doc).toString()).toContain("local")
    unmount(); server.destroy()
  })
  it("refreshes durable metadata before Saved so an immediate checkpoint uses the committed revision", async () => {
    let rev = 1
    fetchMock.mockImplementation((url: string, init?: RequestInit) => {
      if (url.includes("/patch")) { rev = 2; return response({ rev }) }
      if (url.includes("/checkpoint")) { const body = JSON.parse(init!.body as string); return response({ rev }, body.ifRev === rev ? 200 : 409) }
      return response({ schema: 2, rev, epoch: "go:node:1", can_write: true, snapshot: emptySnapshot, content: { savedRevision: rev }, outline: [{ id: "heading", text: `Revision ${rev}` }] })
    })
    const { result, unmount } = renderHook(() => usePageDocument("w", "p"))
    await waitFor(() => expect(result.current.status).toBe("Saved"))
    act(() => text(result.current.doc).insert(0, "edit"))
    await waitFor(() => { expect(result.current.status).toBe("Saved"); expect(result.current.state?.rev).toBe(2) })
    expect(result.current.state?.content).toEqual({ savedRevision: 2 })
    expect(result.current.state?.outline?.[0].text).toBe("Revision 2")
    await expect(documentRequest("w", "p", "/checkpoint", { ifRev: result.current.state?.rev })).resolves.toEqual({ rev: 2 })
    unmount()
  })
  it("does not recommit an acknowledged batch when its metadata refresh fails", async () => {
    let rev = 1, readFailed = true, commits = 0
    fetchMock.mockImplementation((url: string) => {
      if (url.includes("/patch")) { commits++; rev = 2; return response({ rev }) }
      if (rev === 2 && readFailed) return response({ error: "read temporarily unavailable" }, 503)
      return response({ schema: 2, rev, epoch: "go:node:1", can_write: true, snapshot: emptySnapshot })
    })
    const { result, unmount } = renderHook(() => usePageDocument("w", "p"))
    await waitFor(() => expect(result.current.status).toBe("Saved"))
    act(() => text(result.current.doc).insert(0, "edit"))
    await waitFor(() => expect(result.current.status).toBe("Disconnected"))
    expect(result.current.pending).toBe(0)
    readFailed = false
    await waitFor(() => expect(result.current.state?.rev).toBe(2))
    expect(commits).toBe(1)
    unmount()
  })
  it.each([{ status: 400 }, { status: 413 }, { status: 422 }, { status: 429, code: "document_quota" }])("retains a rejected draft and stops retries for $status $code", async ({ status, code }) => {
    let commits = 0
    fetchMock.mockImplementation((url: string) => {
      if (url.includes("/patch")) { commits++; return response({ error: "unsupported document content", code }, status) }
      return response({ schema: 2, rev: 1, epoch: "go:node:1", can_write: true, snapshot: emptySnapshot })
    })
    const { result, unmount } = renderHook(() => usePageDocument("w", "p"))
    await waitFor(() => expect(result.current.editable).toBe(true))
    act(() => text(result.current.doc).insert(0, "Recover this draft"))
    await waitFor(() => expect(result.current.recovery).toBe(true))
    expect(result.current.editable).toBe(false)
    expect(result.current.status).toContain("rejected")
    expect(result.current.pending).toBe(1)
    const recovered = new Y.Doc(); Y.applyUpdate(recovered, Y.encodeStateAsUpdate(result.current.doc))
    expect(text(recovered).toString()).toBe("Recover this draft")
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 1100)) })
    expect(commits).toBe(1)
    recovered.destroy(); unmount()
  })
  it("keeps the draft and disables editing after a restore changes the generation", async () => {
    let epoch = "go:node:1"
    fetchMock.mockImplementation(() => response({ schema: 1, rev: 1, epoch, can_write: true, snapshot: emptySnapshot }))
    const { result, unmount } = renderHook(() => usePageDocument("w", "p"))
    await waitFor(() => expect(result.current.editable).toBe(true))
    act(() => text(result.current.doc).insert(0, "draft")); epoch = "go:new-node:1"
    await waitFor(() => expect(result.current.recovery).toBe(true), { timeout: 4000 })
    expect(result.current.editable).toBe(false)
    expect(text(result.current.doc).toString()).toBe("draft")
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false)
    unmount()
  })
})

it('keeps the same Page draft and exact attempted schema3 payload through slug rename',async()=>{
 const server=model('base'),posted:{url:string;body:Record<string,unknown>}[]=[];let fail=true,renamed=false;
 fetchMock.mockImplementation((url:string,init?:RequestInit)=>{if(init?.method==='POST'){const body=JSON.parse(init.body as string);posted.push({url,body});if(renamed && url.includes('/old/'))return response({error:'old slug reused'},404);if(fail)return response({error:'uncertain'},503);Y.applyUpdate(server,decodeUpdate(body.update));return response({id:body.id,rev:2})};return response({page_id:'page',schema:3,rev:1,epoch:'go:node:1',can_write:true,snapshot:documentSchema.snapshot(server)})});
 const {result,rerender,unmount}=renderHook(({slug})=>usePageDocument('w',slug,'page'),{initialProps:{slug:'old'}});await waitFor(()=>expect(result.current.status).toBe('Saved'));const original=result.current.doc;act(()=>text(original).insert(4,' private draft'));await waitFor(()=>expect(posted.length).toBeGreaterThan(0),{timeout:4000});
 renamed=true;rerender({slug:'renamed'});expect(result.current.doc).toBe(original);expect(text(result.current.doc).toString()).toContain('private draft');fail=false;await waitFor(()=>expect(result.current.pending).toBe(0),{timeout:4000});expect(posted.at(-1)?.body).toEqual(posted[0].body);expect(posted[0].body).toMatchObject({schema:3,page_id:'page',epoch:'go:node:1'});expect(posted[0].url).toContain('/old/document/patch');expect(posted.at(-1)?.url).toContain('/renamed/document/patch');unmount();server.destroy()
});
it('rejects a reused slug Page before applying any foreign binary',async()=>{
 const server=model('original'),foreign=model('foreign content');let wrong=false;fetchMock.mockImplementation(()=>response({page_id:wrong?'foreign':'page',schema:2,rev:1,epoch:'go:node:1',can_write:true,snapshot:documentSchema.snapshot(wrong?foreign:server)}));
 const {result,unmount}=renderHook(()=>usePageDocument('w','slug','page'));await waitFor(()=>expect(result.current.status).toBe('Saved'));act(()=>text(result.current.doc).insert(0,'draft '));wrong=true;await waitFor(()=>expect(result.current.recovery).toBe(true));expect(text(result.current.doc).toString()).toBe('draft original');expect(fetchMock.mock.calls.some(([,init])=>init?.method==='POST')).toBe(false);unmount();server.destroy();foreign.destroy()
});

it('blocks an invalid local mark without misreporting a network disconnection or sending it',async()=>{
 const saved=model('base');fetchMock.mockImplementation(()=>response({page_id:'page',schema:3,rev:1,epoch:'go:node:1',can_write:true,snapshot:documentSchema.snapshot(saved)}));const {result,unmount}=renderHook(()=>usePageDocument('w','slug','page'));await waitFor(()=>expect(result.current.status).toBe('Saved'));act(()=>text(result.current.doc).format(0,4,{bold:{}}));await waitFor(()=>expect(result.current.recovery).toBe(true));expect(result.current.editable).toBe(false);expect(result.current.status).toContain('rejected');expect(result.current.error).toContain('Local document content rejected');expect(fetchMock.mock.calls.some(([,init])=>init?.method==='POST')).toBe(false);expect(text(result.current.doc).toDelta()[0].attributes).toEqual({bold:{}});unmount();saved.destroy()
});

it('classifies malformed saved binary as bounded recovery rather than a network retry',async()=>{
 fetchMock.mockImplementation(()=>response({page_id:'page',schema:3,rev:1,epoch:'go:node:1',can_write:true,snapshot:'%%%invalid%%%'}));const {result,unmount}=renderHook(()=>usePageDocument('w','slug','page'));await waitFor(()=>expect(result.current.recovery).toBe(true));expect(result.current.editable).toBe(false);expect(result.current.status).toContain('rejected');expect(result.current.error).toContain('Confirmed document content rejected');await act(async()=>{await new Promise(resolve=>setTimeout(resolve,600))});expect(fetchMock).toHaveBeenCalledTimes(1);unmount()
});

it('retries temporary operation admission with the identical request and retained draft',async()=>{
 const bodies:string[]=[];let limited=true;
 fetchMock.mockImplementation((url:string,init?:RequestInit)=>{
  if(url.includes('/patch')){bodies.push(init!.body as string);return limited?response({error:'wait and retry original request',code:'document_rate_limit'},429):response({rev:2})}
  return response({schema:2,rev:limited?1:2,epoch:'go:node:1',can_write:true,snapshot:emptySnapshot})
 });
 const {result,unmount}=renderHook(()=>usePageDocument('w','p'));
 await waitFor(()=>expect(result.current.status).toBe('Saved'));
 act(()=>text(result.current.doc).insert(0,'unsaved draft'));
 await waitFor(()=>expect(result.current.status).toContain('Temporarily rate limited'));
 expect(result.current.recovery).toBe(false);expect(result.current.pending).toBe(1);expect(text(result.current.doc).toString()).toBe('unsaved draft');
 limited=false;await waitFor(()=>expect(result.current.pending).toBe(0),{timeout:4000});
 expect(bodies.length).toBeGreaterThan(1);expect(new Set(bodies).size).toBe(1);unmount()
});
