import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import JoinPage from "../join/page"
import { refreshWorkspaceSettings } from "@/hooks/use-workspace"
import { serverFetch } from "@/lib/server-base"

const router=vi.hoisted(()=>({replace:vi.fn()}))
vi.mock("next/navigation",()=>({useRouter:()=>router}))
vi.mock("@/hooks/use-workspace",()=>({refreshWorkspaceSettings:vi.fn().mockResolvedValue(undefined)}))
vi.mock("@/lib/server-base",()=>({serverFetch:vi.fn()}))
beforeEach(()=>{vi.mocked(refreshWorkspaceSettings).mockClear();vi.mocked(serverFetch).mockReset();router.replace.mockReset();window.history.replaceState({},"","/join?invitation_token=synthetic-proof")})
afterEach(()=>{cleanup();window.history.replaceState({},"","/")})
it("requires an explicit acceptance and sends only the presented token",async()=>{
 vi.mocked(serverFetch).mockResolvedValue(new Response('{"ok":true}',{status:200}))
 render(<JoinPage/>);expect(serverFetch).not.toHaveBeenCalled()
 await act(async()=>{fireEvent.click(screen.getByRole("button",{name:"Accept invitation"}))})
 expect(serverFetch).toHaveBeenCalledWith("/api/v1/auth/invitation/accept",expect.objectContaining({method:"POST",body:JSON.stringify({token:"synthetic-proof"})}))
 expect(refreshWorkspaceSettings).toHaveBeenCalledTimes(1)
 expect(router.replace).toHaveBeenCalledWith("/")
})
it("preserves the invitation through sign in and signup without retrying",async()=>{
 vi.mocked(serverFetch).mockResolvedValue(new Response('{}',{status:401}))
 render(<JoinPage/>);await act(async()=>{fireEvent.click(screen.getByRole("button",{name:"Accept invitation"}))})
 expect(screen.getByRole("link",{name:"Sign in"})).toHaveAttribute("href","/login?redirect=%2Fjoin%3Finvitation_token%3Dsynthetic-proof")
 expect(screen.getByRole("link",{name:"Create an account"})).toHaveAttribute("href","/signup?invitation_token=synthetic-proof")
 expect(serverFetch).toHaveBeenCalledTimes(1);expect(router.replace).not.toHaveBeenCalled()
})
it("shows an unavailable invitation without claiming membership",async()=>{
 vi.mocked(serverFetch).mockResolvedValue(new Response('{}',{status:404}))
 render(<JoinPage/>);await act(async()=>{fireEvent.click(screen.getByRole("button",{name:"Accept invitation"}))})
 expect(screen.getByRole("alert")).toHaveTextContent("This invitation is unavailable")
 expect(router.replace).not.toHaveBeenCalled()
})
it("does not send a request without a token",()=>{
 window.history.replaceState({},"","/join");render(<JoinPage/>)
 expect(screen.getByRole("button",{name:"Accept invitation"})).toBeDisabled();expect(serverFetch).not.toHaveBeenCalled()
})
