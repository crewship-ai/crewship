import {afterEach,describe,expect,it,vi} from "vitest"
import {cleanup,render,screen,within,fireEvent} from "@testing-library/react"
import {CrewMembers} from "../crew-members"
import type {CrewMember} from "@/lib/types/crew"
vi.mock("../add-member-dialog",()=>({AddMemberDialog:({open}:{open:boolean})=>open?<div role="dialog">Open membership draft</div>:null}))
const members:CrewMember[]=[null,"VIEWER" as const].map((role,i)=>({id:`member-${i}`,user_id:`user-${i}`,role,created_at:"2026-10-26",user:{id:`user-${i}`,full_name:i?"Explicit role":"Ordinary member",email:`person${i}@example.invalid`,avatar_url:null}}))
afterEach(cleanup)
describe("explicit crew Manage membership controls",()=>{
 it("permits ordinary add/remove but never role edits or explicit-role removal",()=>{
  render(<CrewMembers members={members} crewId="crew" workspaceId="ws" canEdit={false} canManage={true} onMembersChange={vi.fn()}/>);
  expect(screen.getByRole("button",{name:"Add Member"})).toBeTruthy();
  expect(within(screen.getByText("Ordinary member").closest("tr")!).getByRole("button",{name:"Remove"})).toBeTruthy();
  expect(within(screen.getByText("Explicit role").closest("tr")!).queryByRole("button",{name:"Remove"})).toBeNull();
  expect(screen.queryByRole("button",{name:"Edit per-crew role"})).toBeNull();
 });
 it("keeps full administrator role controls and no ungranted management controls",()=>{
  const {rerender}=render(<CrewMembers members={members} crewId="crew" workspaceId="ws" canEdit={true} onMembersChange={vi.fn()}/>);
  expect(screen.getAllByRole("button",{name:"Remove"})).toHaveLength(2);
  expect(screen.getAllByRole("button",{name:"Edit per-crew role"})).toHaveLength(2);
  rerender(<CrewMembers members={members} crewId="crew" workspaceId="ws" canEdit={false} canManage={false} onMembersChange={vi.fn()}/>);
  expect(screen.queryByRole("button",{name:"Add Member"})).toBeNull();
  expect(screen.queryByRole("button",{name:"Remove"})).toBeNull();
 });
});

it("withdraws an already open add dialog on authority loss",()=>{
 const props={members,crewId:"crew",workspaceId:"ws",canEdit:false,onMembersChange:vi.fn()};
 const {rerender}=render(<CrewMembers {...props} canManage={true}/>);
 fireEvent.click(screen.getByRole("button",{name:"Add Member"}));
 expect(screen.getByRole("dialog")).toBeTruthy();
 rerender(<CrewMembers {...props} canManage={false}/>);
 expect(screen.queryByRole("dialog")).toBeNull();
 rerender(<CrewMembers {...props} canManage={true}/>);expect(screen.queryByRole("dialog")).toBeNull();
});
it("withdraws an already open role editor on admin loss",()=>{
 const props={members,crewId:"crew",workspaceId:"ws",onMembersChange:vi.fn()};
 const {rerender}=render(<CrewMembers {...props} canEdit={true} canManage={true}/>);
 fireEvent.click(screen.getAllByRole("button",{name:"Edit per-crew role"})[0]);
 expect(screen.getByTitle("Save")).toBeTruthy();
 rerender(<CrewMembers {...props} canEdit={false} canManage={true}/>);
 expect(screen.getByRole("button",{name:"Add Member"})).toBeTruthy();
 expect(screen.queryByTitle("Save")).toBeNull();
 rerender(<CrewMembers {...props} canEdit={true} canManage={true}/>);expect(screen.queryByTitle("Save")).toBeNull();
});
