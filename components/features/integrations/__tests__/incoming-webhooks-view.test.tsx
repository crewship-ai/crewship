import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { IncomingWebhooksView } from "../views/incoming-webhooks-view"

vi.mock("@/hooks/use-pipelines", () => ({ usePipelines: () => ({ pipelines: [{ id: "routine-id", slug: "review", name: "Review PR" }], loading: false, error: null }) }))
vi.mock("@/hooks/use-pages", () => ({ usePages: () => ({ pages: [{ id: "page-id", slug: "report", name: "Review report" }], loading: false, error: null }), usePage: () => ({ loading: false, page: { panels: [{spec:{id:"results"},producer:"webhook/review"},{spec:{id:"private"},producer:"agent/other"}] } }) }))
vi.mock("@/hooks/use-page-grants", () => ({ usePageGrants: () => ({ refusal: null }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(async () => ({ ok: true, json: async () => [{id:"agent-id",slug:"pepa",name:"Pepa"}] })) }))
vi.mock("@/components/features/routines/routine-webhooks-tab", () => ({ RoutineWebhooksTab: ({pipelineId}: {pipelineId:string}) => <div>Configure routine {pipelineId}</div> }))
vi.mock("@/components/features/chat/right-panel-tabs/triggers-tab", () => ({ TriggersTab: ({agentId}: {agentId:string}) => <div>Configure agent {agentId}</div> }))
vi.mock("@/components/features/pages/page-settings", () => ({ WebhooksCard: ({panelIDs}: {panelIDs:string[]}) => <div>Configure panels {panelIDs.join(",")}</div> }))
function mount() { render(<QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false}}})}><IncomingWebhooksView workspaceId="workspace" /></QueryClientProvider>) }

describe("incoming webhook target configuration", () => {
 it("opens the existing routine configuration and links its activity", () => {
  mount();fireEvent.click(screen.getByRole("button",{name:"Review PR"}));expect(screen.getByText("Configure routine routine-id")).toBeInTheDocument();expect(screen.getByRole("link",{name:"Open activity"})).toHaveAttribute("href","/activity?pipeline=review")
 })
 it("describes issue routing honestly without pretending selection creates an issue step", () => {
  mount();fireEvent.click(screen.getByRole("button",{name:"Issues via a routine"}));expect(screen.getByText(/does not add an issue step/)).toBeInTheDocument();fireEvent.click(screen.getByRole("button",{name:"Review PR"}));expect(screen.getByText("Configure routine routine-id")).toBeInTheDocument()
 })
 it("only offers compatible page panels and clears the previous selection", () => {
  mount();fireEvent.click(screen.getByRole("button",{name:"Review PR"}));fireEvent.click(screen.getByRole("button",{name:"Pages"}));expect(screen.queryByText("Configure routine routine-id")).not.toBeInTheDocument();fireEvent.click(screen.getByRole("button",{name:"Review report"}));expect(screen.getByText("Configure panels results")).toBeInTheDocument()
 })
 it("configures the selected agent without claiming an existing chat turn", async () => {
  mount();fireEvent.click(screen.getByRole("button",{name:"Agents / chat"}));fireEvent.click(await screen.findByRole("button",{name:"Pepa"}));expect(screen.getByText("Configure agent agent-id")).toBeInTheDocument();expect(screen.getByText(/does not append a turn/)).toBeInTheDocument()
 })
})
