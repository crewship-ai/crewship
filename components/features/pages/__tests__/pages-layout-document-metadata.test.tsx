import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
import type { Editor } from "@tiptap/core"
import { afterEach, expect, it, vi } from "vitest"
import { PagesLayout } from "../pages-layout"
import { pagesKeys } from "@/hooks/use-pages"
import { documentSchema } from "@/lib/documents/schema"

const requests=vi.hoisted(()=>vi.fn())
const htmlHost=vi.hoisted(()=>vi.fn())
vi.mock("@/lib/api-fetch",()=>({apiFetch:requests,broadcastSessionExpired:vi.fn()}))
vi.mock("@/hooks/use-workspace",async(importOriginal)=>({
  ...await importOriginal<typeof import("@/hooks/use-workspace")>(),
  useWorkspace:()=>({workspaceId:"ws",role:"MANAGER",capabilities:[],loading:false,error:false,workspaces:[{id:"ws",currentUserRole:"MANAGER"}]}),
}))
vi.mock("@/hooks/use-mobile",()=>({useIsMobile:()=>false}))
vi.mock("next/navigation",()=>({useRouter:()=>({push:vi.fn(),replace:vi.fn(),prefetch:vi.fn()}),usePathname:()=>"/pages/notes",useSearchParams:()=>new URLSearchParams(),useParams:()=>({})}))
vi.mock("../page-application",()=>({PageApplicationView:({fallback}:{fallback:React.ReactNode})=>{htmlHost();return <>{fallback}</>}}))
afterEach(()=>{cleanup();requests.mockReset();htmlHost.mockReset()})

async function dirtyDocument() {
  window.history.replaceState(null,"","/pages/notes")
  const saved=documentSchema.create({type:"doc",content:[{type:"paragraph",attrs:{id:"saved"},content:[{type:"text",text:"Saved private document"}]}]},3)
  const page={id:"native-id",slug:"notes",name:"Notes",has_document:true,owner:"user/owner",panels:[]}
  let metadataStatus=200
  requests.mockImplementation(async(input:string)=>{
    const url=new URL(input,"http://localhost")
    if(url.pathname==="/api/v1/documents/status")return Response.json({enabled:true})
    if(url.pathname==="/api/v1/page-folders")return Response.json({folders:[]})
    if(url.pathname==="/api/v1/pages")return Response.json([page])
    if(url.pathname==="/api/v1/pages/notes")return metadataStatus===200?Response.json(page):Response.json({error:"Page metadata unavailable"},{status:metadataStatus})
    if(url.pathname==="/api/v1/pages/notes/document")return Response.json({page_id:page.id,schema:3,rev:1,epoch:"opaque-epoch",can_write:true,snapshot:documentSchema.snapshot(saved)})
    if(url.pathname==="/api/v1/pages/notes/document/patch")return Response.json({error:"Recover this rejected draft"},{status:400})
    return Response.json({})
  })
  const client=new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}})
  render(<QueryClientProvider client={client}><PagesLayout workspaceId="ws" slug="notes" /></QueryClientProvider>)
  const node=await screen.findByLabelText("Document editor")
  const editor=(node as HTMLElement&{editor:Editor}).editor
  await waitFor(()=>expect(editor.isEditable).toBe(true))
  act(()=>{editor.commands.insertContent("Unsaved local text ")})
  await screen.findByRole("button",{name:"Download unsaved draft"},{timeout:3000})
  expect(node).toHaveTextContent("Unsaved local text")
  return {node,editor,client,saved,refetch:async(status:number)=>{
    metadataStatus=status
    await act(async()=>{await client.refetchQueries({queryKey:pagesKeys.detail("ws","notes"),exact:true})})
  }}
}

it("retains the real dirty Document session through a metadata-only 503",async()=>{
  const view=await dirtyDocument()
  try {
    await view.refetch(503)
    await waitFor(()=>expect(screen.queryByText("Document access is unavailable.") || screen.queryByText("Page details are temporarily unavailable. Your document remains open.")).not.toBeNull())
    expect(view.editor.isDestroyed).toBe(false)
    expect(screen.getByLabelText("Document editor")).toBe(view.node)
    expect(screen.getByText("Page details are temporarily unavailable. Your document remains open.")).toBeInTheDocument()
    expect(view.node).toHaveTextContent("Unsaved local text")
    expect(screen.getByRole("button",{name:"Download unsaved draft"})).toBeEnabled()
    expect(htmlHost).not.toHaveBeenCalled()
    await view.refetch(200)
    await waitFor(()=>expect(screen.queryByText("Page details are temporarily unavailable. Your document remains open.")).toBeNull())
    expect(screen.getByLabelText("Document editor")).toBe(view.node)
    expect(view.node).toHaveTextContent("Unsaved local text")
  } finally {cleanup();view.client.clear();view.saved.destroy()}
})

it.each([401,403,404])("hides the native session on denied metadata revalidation (%s)",async(status)=>{
  const view=await dirtyDocument()
  try {
    await view.refetch(status)
    await screen.findByText("Document access is unavailable.")
    expect(screen.queryByLabelText("Document editor")).toBeNull()
    expect(screen.queryByText(/Saved private document/)).toBeNull()
    expect(screen.queryByText(/Unsaved local text/)).toBeNull()
    expect(htmlHost).not.toHaveBeenCalled()
  } finally {cleanup();view.client.clear();view.saved.destroy()}
})
