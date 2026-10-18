import { useRef, useState } from "react"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { DocumentViewer, type DocumentViewerProps } from "./document-viewer"
afterEach(cleanup)
const target={workspaceId:"w1",pageId:"page1",type:"document",mode:"latest"} as const
function props(): DocumentViewerProps<string> {
  return {target,scopeKey:"human/w1/chat1/g1",read:{status:"ready",scopeKey:"human/w1/chat1/g1",target,title:"Private saved title",content:"Authorized document body"},renderBody:vi.fn(value=><p>{value.content}</p>),layout:"side",expanded:false,onClose:vi.fn(),onExpandedChange:vi.fn()}
}
it("renders only the verified latest target and keeps expansion controlled",()=>{
  const p=props();render(<DocumentViewer {...p}/>)
  expect(screen.getByRole("heading",{name:"Private saved title"})).toBeInTheDocument()
  expect(screen.getByText("Current saved document")).toBeInTheDocument()
  expect(screen.getByText("Authorized document body")).toBeInTheDocument()
  expect(p.renderBody).toHaveBeenCalledOnce()
  fireEvent.click(screen.getByRole("button",{name:"Expand document preview"}))
  expect(p.onExpandedChange).toHaveBeenCalledWith(true)
})
it("never invokes body or leaks title when scope, workspace, Page or mode differs",()=>{
  for(const change of [{scopeKey:"other-human"},{target:{...target,workspaceId:"other"}},{target:{...target,pageId:"other"}},{target:{...target,mode:"pinned"}}]) {
    const p=props()
    // Exercise a malformed runtime response as well as well-typed identity changes.
    const read={...p.read,...change} as DocumentViewerProps<string>["read"]
    const {unmount}=render(<DocumentViewer {...p} read={read}/>)
    expect(screen.queryByText("Private saved title")).toBeNull()
    expect(screen.queryByText("Authorized document body")).toBeNull()
    expect(p.renderBody).not.toHaveBeenCalled()
    expect(screen.getByRole("status")).toHaveTextContent("Preview unavailable")
    unmount()
  }
})
it("clears protected content immediately on denial and distinguishes temporary errors",()=>{
  const p=props();const {rerender}=render(<DocumentViewer {...p}/>)
  rerender(<DocumentViewer {...p} read={{status:"denied"}}/>)
  expect(screen.queryByText("Private saved title")).toBeNull()
  expect(screen.queryByText("Authorized document body")).toBeNull()
  expect(screen.getByRole("alert")).toHaveTextContent("Document access is unavailable")
  rerender(<DocumentViewer {...p} read={{status:"error"}}/>)
  expect(screen.getByRole("alert")).toHaveTextContent("Document could not be loaded")
  rerender(<DocumentViewer {...p} read={{status:"loading"}}/>)
  expect(screen.getByRole("status")).toHaveTextContent("Loading document")
})
it("does not remount an adjacent composer draft when expanding or switching layout",()=>{
  const p=props();const {rerender}=render(<><textarea aria-label="Composer" defaultValue="Unsent draft"/><DocumentViewer {...p}/></>)
  fireEvent.change(screen.getByLabelText("Composer"),{target:{value:"Unsent edited draft"}})
  rerender(<><textarea aria-label="Composer" defaultValue="Unsent draft"/><DocumentViewer {...p} expanded layout="mobile"/></>)
  expect(screen.getByLabelText("Composer")).toHaveValue("Unsent edited draft")
  expect(screen.getByRole("button",{name:"Back to chat"})).toBeInTheDocument()
  expect(screen.queryByRole("button",{name:"Show chat alongside"})).toBeNull()
})
it("focuses close on opening and returns focus to the connected opener on Escape",async()=>{
  const opener=document.createElement("button");document.body.append(opener)
  try {
    const p={...props(),openerRef:{current:opener}}
    render(<DocumentViewer {...p}/>)
    const close=screen.getByRole("button",{name:"Close document preview"})
    expect(close).toHaveFocus()
    fireEvent.keyDown(close,{key:"Escape"})
    expect(p.onClose).toHaveBeenCalledOnce()
    await waitFor(()=>expect(opener).toHaveFocus())
  } finally {opener.remove()}
})
it("never renders a body when closed or an empty identity is supplied",()=>{
  const p=props();const {rerender}=render(<DocumentViewer {...p} target={null}/>)
  expect(screen.queryByRole("region",{name:"Document preview"})).toBeNull()
  expect(p.renderBody).not.toHaveBeenCalled()
  rerender(<DocumentViewer {...p} scopeKey=""/>)
  expect(screen.queryByText("Authorized document body")).toBeNull()
  expect(p.renderBody).not.toHaveBeenCalled()
})
it("leaves Escape consumed by a nested control to that control",()=>{
  const p=props()
  render(<DocumentViewer {...p} renderBody={()=><button onKeyDown={event=>event.preventDefault()}>Nested control</button>}/>)
  fireEvent.keyDown(screen.getByRole("button",{name:"Nested control"}),{key:"Escape"})
  expect(p.onClose).not.toHaveBeenCalled()
})

it("returns focus after the controlled mobile host reveals its previously hidden opener",async()=>{
  function Host() {
    const [open,setOpen]=useState(true)
    const openerRef=useRef<HTMLButtonElement>(null)
    return <><button ref={openerRef} hidden={open}>Saved Document card</button><DocumentViewer {...props()} target={open?target:null} layout="mobile" openerRef={openerRef} onClose={()=>setOpen(false)}/></>
  }
  render(<Host/>)
  fireEvent.click(screen.getByRole("button",{name:"Back to chat"}))
  const opener=screen.getByRole("button",{name:"Saved Document card"})
  await waitFor(()=>expect(opener).toHaveFocus())
  expect(screen.queryByRole("region",{name:"Document preview"})).toBeNull()
})
