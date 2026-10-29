import React from "react"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
const state=vi.hoisted(()=>({search:"artifact=1"}))
vi.mock("next/navigation",()=>({useSearchParams:()=>new URLSearchParams(state.search)}))
vi.mock("@/lib/use-url-segment",()=>({useUrlSegment:()=>"demo"}))
vi.mock("@/hooks/use-page-workspace",()=>({usePageWorkspace:()=>({workspaceId:"w",loading:false})}))
vi.mock("@/hooks/use-access-mode",()=>({useAccessMode:()=>"full"}))
vi.mock("@/components/features/restricted/access-notice",()=>({useAccessContext:()=>({})}))
vi.mock("@/components/features/pages/restricted-pages",()=>({RestrictedPages:()=>null}))
vi.mock("@/components/features/pages/pages-layout",()=>({PagesLayout:()=>null}))
vi.mock("@/hooks/use-auth",()=>({useSessionSafe:()=>({data:{user:{id:"owner"}}})}))
vi.mock("@/hooks/use-artifact-agent-dispatch",()=>({useArtifactAgentDispatch:()=>({dispatch:vi.fn(),ready:false})}))
vi.mock("@/lib/pages/artifact-project-preview",async original=>({...await original<typeof import("@/lib/pages/artifact-project-preview")>(),artifactProjectDocument:()=>"safe preview"}))
vi.mock("@/hooks/use-artifact-project",()=>({useArtifactProject:(_workspace:string,_slug:string,version?:number)=>({query:{data:{page_id:"page",can_write:true,latest_version:2,shared_version:1,manifest:{version:version??2,entrypoint:"index.html",files:[{path:"index.html",content:"<h1>Overview</h1>",media_type:"text/html"}]},versions:[{version:2},{version:1}],quota:{used_bytes:10,limit_bytes:100,warning:false},comments:[]}},restore:{mutate:vi.fn()},share:{mutate:vi.fn()},comment:{mutate:vi.fn()}})}))
import { PageDetailClient } from "../page-client"
afterEach(()=>{cleanup();state.search="artifact=1";vi.clearAllMocks()})
it("opens the exact version when client navigation changes the query on the same Page",()=>{
 const view=render(<PageDetailClient />)
 expect(screen.getByLabelText("Version")).toHaveValue("2")
 state.search="artifact=1&version=1";view.rerender(<PageDetailClient />)
 expect(screen.getByLabelText("Version")).toHaveValue("1")
})
it("opens the shared version even if its URL already matches but the local viewer changed versions",()=>{
 state.search="artifact=1&version=1";render(<PageDetailClient />)
 fireEvent.change(screen.getByLabelText("Version"),{target:{value:"2"}})
 const click=new MouseEvent("click",{bubbles:true,cancelable:true});click.preventDefault()
 fireEvent(screen.getByRole("link",{name:"Open shared version 1"}),click)
 expect(screen.getByLabelText("Version")).toHaveValue("1")
})
