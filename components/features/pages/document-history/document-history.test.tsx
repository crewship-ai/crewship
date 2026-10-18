import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { DocumentHistory, type DocumentHistoryProps } from "./document-history"
afterEach(cleanup)
const row = { id:"pin-1", rev:7, schema:3, actor:"human:writer", created_at:"2026-10-18T20:00:00Z" }
const content = { type:"doc", content:[{type:"paragraph",attrs:{id:"p1"},content:[{type:"text",text:"Confirmed historical text"}]}] }
function props(): DocumentHistoryProps {
  return {pageId:"page-1",scopeKey:"human/workspace/page-1/auth-1",list:{status:"ready",scopeKey:"human/workspace/page-1/auth-1",data:{checkpoints:[row]}},selectedId:"pin-1",snapshot:{status:"ready",scopeKey:"human/workspace/page-1/auth-1",data:{page_id:"page-1",id:"pin-1",rev:7,schema:3,content}},onSelect:vi.fn(),onClose:vi.fn()}
}
it("shows only immutable confirmed content and explicit checkpoint provenance",()=>{
  const p=props();render(<DocumentHistory {...p}/>)
  expect(screen.getByText("Confirmed historical text")).toBeInTheDocument()
  expect(screen.getByText("human:writer")).toBeInTheDocument()
  expect(screen.getByRole("button",{name:/Revision 7/})).toHaveAttribute("aria-pressed","true")
  expect(document.querySelector('[contenteditable="true"]')).toBeNull()
  expect(screen.queryByRole("button",{name:/restore|save|apply/i})).toBeNull()
  fireEvent.click(screen.getByRole("button",{name:/Revision 7/}));expect(p.onSelect).toHaveBeenCalledWith("pin-1")
  fireEvent.click(screen.getByRole("button",{name:"Close history"}));expect(p.onClose).toHaveBeenCalledOnce()
})
it("keeps an adjacent live editor draft unchanged while browsing and closing",()=>{
  const p=props();const {rerender}=render(<><textarea aria-label="Live draft" defaultValue="Unsaved private text"/><DocumentHistory {...p}/></>)
  fireEvent.click(screen.getByRole("button",{name:/Revision 7/}))
  rerender(<><textarea aria-label="Live draft" defaultValue="Unsaved private text"/><DocumentHistory {...p} selectedId={undefined}/></>)
  expect(screen.getByLabelText("Live draft")).toHaveValue("Unsaved private text")
  expect(screen.queryByText("Confirmed historical text")).toBeNull()
})
it("removes old content synchronously when access or identity changes",()=>{
  const p=props();const {rerender}=render(<DocumentHistory {...p}/>)
  rerender(<DocumentHistory {...p} scopeKey="other-human/workspace/page-1/auth-2"/>)
  expect(screen.queryByText("Confirmed historical text")).toBeNull()
  expect(screen.queryByText("human:writer")).toBeNull()
  rerender(<DocumentHistory {...p} list={{status:"denied"}}/>)
  expect(screen.queryByText("Confirmed historical text")).toBeNull()
  expect(screen.getByRole("alert")).toHaveTextContent("History access is unavailable")
})
it("rejects mismatched checkpoint, page, revision, schema and stale scope",()=>{
  for(const change of [{id:"another"},{page_id:"another"},{rev:8},{schema:2}]) {
    const p=props();const snapshot=p.snapshot
    if(snapshot.status!=="ready") throw Error("fixture")
    const {unmount}=render(<DocumentHistory {...p} snapshot={{...snapshot,data:{...snapshot.data,...change}}}/>)
    expect(screen.queryByText("Confirmed historical text")).toBeNull()
    expect(screen.getByRole("status")).toHaveTextContent("Snapshot unavailable")
    unmount()
  }
})
it("shows unknown schemas and malformed content unavailable without normalizing",()=>{
  for(const data of [{schema:99,content},{schema:3,content:{type:"doc",content:[{type:"script",text:"Private unsafe text"}]}}]) {
    const p=props();const {unmount}=render(<DocumentHistory {...p} list={{status:"ready",scopeKey:p.scopeKey,data:{checkpoints:[{...row,schema:data.schema}]}}} snapshot={{status:"ready",scopeKey:p.scopeKey,data:{page_id:p.pageId,id:row.id,rev:row.rev,...data}}}/>)
    expect(screen.queryByText("Confirmed historical text")).toBeNull()
    expect(screen.getByRole("status")).toHaveTextContent("Snapshot unavailable")
    expect(document.querySelector("script")).toBeNull()
    unmount()
  }
})
it("does not confuse loading, failed, unavailable and confirmed empty history",()=>{
  const p=props();const {rerender}=render(<DocumentHistory {...p} list={{status:"loading"}}/>)
  expect(screen.getByRole("status")).toHaveTextContent("Loading history")
  rerender(<DocumentHistory {...p} list={{status:"error"}}/>)
  expect(screen.getByRole("alert")).toHaveTextContent("History could not be loaded")
  expect(screen.queryByText("No checkpoints yet.")).toBeNull()
  rerender(<DocumentHistory {...p} list={{status:"ready",scopeKey:p.scopeKey,data:{checkpoints:[]}}}/>)
  expect(screen.getByText("No checkpoints yet.")).toBeInTheDocument()
})
it("clears list metadata as well as content after a denied snapshot read",()=>{
  const p=props();const {rerender}=render(<DocumentHistory {...p}/>)
  rerender(<DocumentHistory {...p} snapshot={{status:"denied"}}/>)
  expect(screen.queryByText("Confirmed historical text")).toBeNull()
  expect(screen.queryByText("human:writer")).toBeNull()
  expect(screen.getByRole("alert")).toHaveTextContent("History access is unavailable")
})
it("renders schema 2 headings and schema 3 links through the closed schema",()=>{
  for(const schema of [2,3]) {
    const p=props()
    const text={type:"text",text:"Historical heading",...(schema===3?{marks:[{type:"link",attrs:{href:"https://example.com/confirmed"}}]}:{})}
    const doc={type:"doc",content:[{type:"heading",attrs:{id:"h1",level:2},content:[text]}]}
    const {unmount}=render(<DocumentHistory {...p} list={{status:"ready",scopeKey:p.scopeKey,data:{checkpoints:[{...row,schema}]}}} snapshot={{status:"ready",scopeKey:p.scopeKey,data:{page_id:p.pageId,id:row.id,rev:row.rev,schema,content:doc}}}/>)
    expect(screen.getByRole("heading",{name:"Historical heading",level:2})).toBeInTheDocument()
    if(schema===3) expect(screen.getByRole("link",{name:"Historical heading"})).toHaveAttribute("rel","noopener noreferrer")
    unmount()
  }
})
it("rejects executable link targets without rendering links or fallback text",()=>{
  const p=props()
  const unsafe={type:"doc",content:[{type:"paragraph",attrs:{id:"p1"},content:[{type:"text",text:"Unsafe label",marks:[{type:"link",attrs:{href:"javascript:alert(1)"}}]}]}]}
  render(<DocumentHistory {...p} snapshot={{status:"ready",scopeKey:p.scopeKey,data:{page_id:p.pageId,id:row.id,rev:row.rev,schema:3,content:unsafe}}}/>)
  expect(screen.queryByRole("link")).toBeNull()
  expect(screen.queryByText("Unsafe label")).toBeNull()
  expect(screen.getByRole("status")).toHaveTextContent("Snapshot unavailable")
})
