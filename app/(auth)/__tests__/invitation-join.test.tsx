import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import JoinPage from "../join/page"
import { refreshWorkspaceSettings } from "@/hooks/use-workspace"
import { serverFetch } from "@/lib/server-base"

const router=vi.hoisted(()=>({replace:vi.fn()}))
vi.mock("next/navigation",()=>({useRouter:()=>router}))
vi.mock("@/hooks/use-workspace",()=>({refreshWorkspaceSettings:vi.fn().mockResolvedValue(undefined)}))
vi.mock("@/lib/server-base",()=>({serverFetch:vi.fn()}))
beforeEach(()=>{vi.mocked(refreshWorkspaceSettings).mockClear();vi.mocked(serverFetch).mockReset();router.replace.mockReset();window.sessionStorage.clear();window.history.replaceState({},"","/join#invitation_token=synthetic-proof")})
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
 expect(screen.getByRole("link",{name:"Sign in"})).toHaveAttribute("href","/login?redirect=%2Fjoin")
 expect(screen.getByRole("link",{name:"Create an account"})).toHaveAttribute("href","/signup")
 expect(serverFetch).toHaveBeenCalledTimes(1);expect(router.replace).not.toHaveBeenCalled()
})
it("shows an unavailable invitation without claiming membership",async()=>{
 vi.mocked(serverFetch).mockResolvedValue(new Response('{}',{status:404}))
 render(<JoinPage/>);await act(async()=>{fireEvent.click(screen.getByRole("button",{name:"Accept invitation"}))})
 expect(screen.getByRole("alert")).toHaveTextContent("This invitation is unavailable")
 expect(router.replace).not.toHaveBeenCalled()
})
it("does not send a request without a token and says the link is incomplete",()=>{
 window.history.replaceState({},"","/join");render(<JoinPage/>)
 expect(screen.getByRole("heading",{name:"This invitation link is incomplete"})).toBeVisible()
 expect(screen.queryByRole("button",{name:"Accept invitation"})).not.toBeInTheDocument();expect(serverFetch).not.toHaveBeenCalled()
})
it("401 shows Sign in as primary and keeps the token for return",async()=>{
 vi.mocked(serverFetch).mockResolvedValue(new Response('{}',{status:401}))
 render(<JoinPage/>);await act(async()=>{fireEvent.click(screen.getByRole("button",{name:"Accept invitation"}))})
 expect(screen.getByRole("status")).toHaveTextContent("Sign in first. You'll come back here.")
 const signIn=screen.getByRole("link",{name:"Sign in"})
 expect(signIn).toHaveAttribute("data-variant","default")
 expect(screen.getByRole("link",{name:"Create an account"})).toHaveAttribute("data-variant","outline")
 expect(screen.queryByRole("button",{name:"Accept invitation"})).not.toBeInTheDocument()
 expect(window.sessionStorage.getItem("crewship.pending-invitation")).toBe("synthetic-proof")
})

it("scrubs proof from the address and link URLs before interaction",()=>{
 render(<JoinPage/>);
 expect(window.location.href).not.toContain("synthetic-proof")
 for(const link of screen.getAllByRole("link")){expect(link.getAttribute("href")).not.toContain("synthetic-proof")}
})

it("discloses the Guest account restriction before explicitly accepting it",async()=>{
 vi.mocked(serverFetch).mockResolvedValueOnce(new Response('{"code":"guest_confirmation_required"}',{status:400})).mockResolvedValueOnce(new Response('{"ok":true}',{status:200}))
 render(<JoinPage/>);await act(async()=>{fireEvent.click(screen.getByRole("button",{name:"Accept invitation"}))})
 expect(screen.getByRole("status")).toHaveTextContent("entire account across all workspaces")
 expect(screen.getByRole("status")).toHaveTextContent("own or administer any workspace cannot accept")
 expect(router.replace).not.toHaveBeenCalled()
 // guest confirmation requires the checkbox and sends confirm_account_restriction
 const accept=screen.getByRole("button",{name:"Accept Guest restrictions"})
 expect(accept).toBeDisabled()
 await act(async()=>{fireEvent.click(accept)})
 expect(serverFetch).toHaveBeenCalledTimes(1)
 fireEvent.click(screen.getByRole("checkbox",{name:"I understand this applies to the whole account"}))
 expect(accept).toBeEnabled()
 await act(async()=>{fireEvent.click(accept)})
 expect(JSON.parse(String(vi.mocked(serverFetch).mock.calls[1][1]?.body))).toEqual({token:"synthetic-proof",confirm_account_restriction:true})
 expect(router.replace).toHaveBeenCalledWith("/")
})
