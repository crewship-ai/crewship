import { beforeEach, describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { AddCredentialWizard } from "../add-credential-wizard"
import { readCredentialFile, CREDENTIAL_FILE_LIMIT } from "../credential-file-input"
import { credentialEntryError } from "@/lib/credentials/entry-validation"
import { extraFieldsFor } from "@/lib/credentials/item-types"

const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({apiFetch: api}))
vi.mock("@/hooks/use-abilities", () => ({useAbilities: () => ({abilities: {can: () => true}})}))
const response = (body: unknown, status = 200) => ({ok: status < 400, status, json: async () => body})
const body = (call: unknown[]) => JSON.parse((call[1] as {body: string}).body)
const creates = () => api.mock.calls.filter(([url, init]) => String(url).startsWith("/api/v1/credentials?") && init?.method === "POST")
beforeEach(() => { api.mockReset().mockImplementation(async (url: string) => url.includes("/agents?") ? response([{id:"agent-one",name:"Alex"}]) : url.includes("/bindings?") ? response({bindings: []}) : response({id:"saved-one"},201)) })
function fill() {
 fireEvent.click(screen.getByRole("button", {name:/^Token/}))
 fireEvent.change(screen.getByLabelText(/^Name$/), {target:{value:"Work account"}})
 fireEvent.change(screen.getByLabelText(/^Token$/), {target:{value:"  fixture-secret  "}})
 fireEvent.click(screen.getByRole("button", {name:/^Continue$/}))
}
describe("credential entry safety", () => {
 it("saves without assignments and preserves the exact secret bytes", async () => {
  const done=vi.fn(); render(<AddCredentialWizard workspaceId="ws" onSuccess={done} onCancel={() => {}} />); fill()
  fireEvent.click(screen.getByRole("button", {name:/^Save secret$/}))
  await waitFor(() => expect(done).toHaveBeenCalledWith("saved-one"))
  expect(body(creates()[0])).toMatchObject({value:"  fixture-secret  ",scope:"WORKSPACE"})
  expect(body(creates()[0]).crew_ids).toBeUndefined()
  expect(api.mock.calls.some(([url]) => url.includes("/bindings"))).toBe(false)
 })
 it("assigns only the selected agent, with a conflict check before creation", async () => {
  const done=vi.fn(); render(<AddCredentialWizard workspaceId="ws" onSuccess={done} onCancel={() => {}} />); fill()
  fireEvent.click(screen.getByRole("button", {name:/^Assign now$/}))
  fireEvent.click(await screen.findByRole("checkbox", {name:"Alex"}))
  fireEvent.change(screen.getByLabelText("Variable name"), {target:{value:"WORK_TOKEN"}})
  fireEvent.click(screen.getByRole("button", {name:"Save & assign"}))
  await waitFor(() => expect(done).toHaveBeenCalled())
  const bindings=api.mock.calls.filter(([url, init]) => url.includes("/bindings") && init?.method === "POST")
  expect(bindings).toHaveLength(1)
  expect(body(bindings[0])).toMatchObject({scope:"AGENT",agent_id:"agent-one",crew_id:"",slot:"WORK_TOKEN"})
 })
 it("retries failed fields without repeating creation or successful fields", async () => {
  let attempts=0
  api.mockImplementation(async (url, init) => {
    if (url.includes("/fields") && JSON.parse(init.body).key === "region" && attempts++ === 0) return response({},500)
    return response({id:"saved-one"},201)
  })
  const done=vi.fn(); render(<AddCredentialWizard workspaceId="ws" onSuccess={done} onCancel={() => {}} />)
  fireEvent.click(screen.getByRole("button", {name:/^Key pair/}))
  fireEvent.change(screen.getByLabelText(/^Name$/), {target:{value:"AWS"}})
  fireEvent.change(screen.getByLabelText(/^Access key ID$/), {target:{value:"fixture-id"}})
  fireEvent.change(screen.getByLabelText(/^Secret access key$/), {target:{value:"fixture-key"}})
  fireEvent.change(screen.getByLabelText(/Region \(optional\)/), {target:{value:"eu-central-1"}})
  fireEvent.click(screen.getByRole("button", {name:"Continue"})); fireEvent.click(screen.getByRole("button", {name:"Save secret"}))
  await screen.findByRole("button", {name:"Retry missing parts"})
  expect(done).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole("button", {name:"Retry missing parts"}))
  await waitFor(() => expect(done).toHaveBeenCalledWith("saved-one"))
  expect(creates()).toHaveLength(1)
  expect(api.mock.calls.filter(([url, init]) => url.includes("/fields") && JSON.parse(init.body).key === "access_key_id")).toHaveLength(1)
 })
 it("refuses a workspace switch rather than saving its old draft in the new workspace", async () => {
  const props={onSuccess:vi.fn(),onCancel:vi.fn()}
  const view=render(<AddCredentialWizard workspaceId="original" {...props}/>);fill()
  view.rerender(<AddCredentialWizard workspaceId="different" {...props}/>)
  fireEvent.click(screen.getByRole("button", {name:"Save secret"}))
  expect(await screen.findByText(/Workspace changed/)).toBeInTheDocument();expect(creates()).toHaveLength(0)
 })
 it("updates an existing provider without offering account creation or device sign-in", async () => {
  const done=vi.fn();render(<AddCredentialWizard workspaceId="ws" onSuccess={done} onCancel={() => {}} initial={{credentialId:"existing",itemType:"PROVIDER_LOGIN",provider:"OPENAI",loginMode:"api_key",step:"values",name:"Existing account"}} />)
  fireEvent.change(screen.getByLabelText(/^API key$/), {target:{value:"fixture-new"}})
  fireEvent.click(screen.getByRole("button", {name:"Update login"}))
  await waitFor(() => expect(done).toHaveBeenCalledWith("existing"))
  expect(creates()).toHaveLength(0)
  const patch=api.mock.calls.find(([,init]) => init?.method === "PATCH")!
  expect(body(patch)).toEqual({value:"fixture-new",mode:"api_key"})
 })
})

describe("local file and format validation", () => {
 const file=(value: string, name="secret.txt") => ({name,size:new TextEncoder().encode(value).length,arrayBuffer:async () => new TextEncoder().encode(value).buffer}) as File
 it("reads text exactly, and rejects empty, oversized, binary and malformed JSON files", async () => {
  await expect(readCredentialFile(file(" \r\nfixture\r\n"))).resolves.toBe(" \r\nfixture\r\n")
  await expect(readCredentialFile(file(""))).rejects.toThrow("empty")
  await expect(readCredentialFile(file("x".repeat(CREDENTIAL_FILE_LIMIT+1)))).rejects.toThrow("64 KiB")
  await expect(readCredentialFile(file("a\0b"))).rejects.toThrow("Binary")
  await expect(readCredentialFile(file("broken", "auth.json"))).rejects.toThrow("JSON")
  await expect(readCredentialFile(file("arbitrary config"))).resolves.toBe("arbitrary config")
 })
 it("checks login structure without exposing pasted secrets in errors", () => {
  expect(credentialEntryError('{"access_token":"fixture-private"}',"PROVIDER_LOGIN","GOOGLE","subscription")).toBe("The login file is missing refresh_token.")
  expect(credentialEntryError('{"tokens":{"access_token":"fixture-private"}}',"PROVIDER_LOGIN","OPENAI","subscription")).toBe("The login file is missing tokens.id_token.")
  expect(credentialEntryError("opaque-token","TOKEN","NONE","")).toBeNull()
 })
 it("preserves optional secret bytes and omits untouched empty parts", () => {
  expect(extraFieldsFor("SSH_KEY",{passphrase:"  fixture  ",public_key:""})).toEqual([{key:"passphrase",value:"  fixture  ",is_secret:true,ordinal:0}])
 })
})
