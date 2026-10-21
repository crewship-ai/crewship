import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import type { Editor } from "@tiptap/core"
import { afterEach, expect, it, vi } from "vitest"
import { PagesLayout } from "../pages-layout"
import { pagesKeys } from "@/hooks/use-pages"
import { documentSchema } from "@/lib/documents/schema"

vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "pages-test-human" } }, status: "authenticated" }) }))

const requests=vi.hoisted(()=>vi.fn())
const htmlHost=vi.hoisted(()=>vi.fn())
vi.mock("@/lib/api-fetch",()=>({apiFetch:requests,broadcastSessionExpired:vi.fn()}))
vi.mock("@/hooks/use-workspace",async(importOriginal)=>({
  ...await importOriginal<typeof import("@/hooks/use-workspace")>(),
  useWorkspace:()=>({workspaceId:"ws",role:"MANAGER",capabilities:[],loading:false,error:false,workspaces:[{id:"ws",currentUserRole:"MANAGER"}]}),
}))
vi.mock("@/hooks/use-mobile",()=>({useIsMobile:()=>false}))
const push=vi.hoisted(()=>vi.fn())
vi.mock("next/navigation",()=>({useRouter:()=>({push,replace:vi.fn(),prefetch:vi.fn()}),usePathname:()=>"/pages/notes",useSearchParams:()=>new URLSearchParams(),useParams:()=>({})}))
vi.mock("../page-application",()=>({PageApplicationView:({fallback}:{fallback:React.ReactNode})=>{htmlHost();return <>{fallback}</>}}))
const ownedFixtures=new Set<()=>void>()
afterEach(()=>{cleanup();for(const dispose of [...ownedFixtures])dispose();requests.mockReset();htmlHost.mockReset();push.mockReset();document.querySelectorAll("a[data-test-link]").forEach(a=>a.remove())})

async function dirtyDocument() {
  window.history.replaceState(null,"","/pages/notes")
  const saved=documentSchema.create({type:"doc",content:[{type:"paragraph",attrs:{id:"saved"},content:[{type:"text",text:"Saved private document"}]}]},3)
  const page={id:"native-id",slug:"notes",name:"Notes",has_document:true,owner:"user/owner",panels:[]}
  requests.mockImplementation(async(input:string)=>{
    const url=new URL(input,"http://localhost")
    if(url.pathname==="/api/v1/documents/status")return Response.json({enabled:true})
    if(url.pathname==="/api/v1/page-folders")return Response.json({folders:[]})
    if(url.pathname==="/api/v1/pages")return Response.json([page])
    if(url.pathname==="/api/v1/pages/notes")return Response.json(page)
    if(url.pathname==="/api/v1/pages/notes/document")return Response.json({page_id:page.id,schema:3,rev:1,epoch:"opaque-epoch",can_write:true,snapshot:documentSchema.snapshot(saved)})
    // The write never lands, so the edit stays unsent.
    if(url.pathname==="/api/v1/pages/notes/document/patch")return Response.json({error:"Recover this rejected draft"},{status:400})
    return Response.json({})
  })
  const client=new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}})
  const dispose=()=>{if(!ownedFixtures.delete(dispose))return;client.clear();saved.destroy()}
  ownedFixtures.add(dispose)
  client.setQueryData(pagesKeys.detail("ws","notes","pages-test-human"),page)
  await act(async()=>{render(<QueryClientProvider client={client}><PagesLayout workspaceId="ws" slug="notes" /></QueryClientProvider>)})
  const node=await screen.findByLabelText("Document editor")
  const editor=(node as HTMLElement&{editor:Editor}).editor
  await waitFor(()=>expect(editor.isEditable).toBe(true))
  act(()=>{editor.commands.insertContent("Unsaved local text ")})
  await screen.findByRole("button",{name:"Download unsaved draft"},{timeout:3000})
}

function appLink(){
  const link=document.createElement("a");link.href="/chat";link.textContent="Chat";link.dataset.testLink="";document.body.append(link)
  return link
}

it("asks before an in-app link leaves a Document with unsent edits, and stays when asked to",async()=>{
  await dirtyDocument()
  const link=appLink()
  act(()=>{link.click()})
  const question=await screen.findByRole("alertdialog")
  expect(question).toHaveTextContent("have not reached the server")
  expect(push).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole("button",{name:"Keep editing"}))
  await waitFor(()=>expect(screen.queryByRole("alertdialog")).toBeNull())
  expect(screen.getByLabelText("Document editor")).toHaveTextContent("Unsaved local text")
  // Asked again on the next attempt, not refused in silence.
  act(()=>{link.click()})
  await screen.findByRole("alertdialog")
  fireEvent.click(screen.getByRole("button",{name:"Leave anyway"}))
  await waitFor(()=>expect(push).toHaveBeenCalledWith("/chat"))
})
