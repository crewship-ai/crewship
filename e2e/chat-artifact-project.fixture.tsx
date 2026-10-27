// Browser component fixture: the real AssistantTurn, artifact cards and viewer,
// with every artifact request answered by the in-memory mock server. No live backend,
// model or credentials. Driven by e2e/chat-artifact-project.browser.mjs.
import React, { useState } from "react"
import { createRoot } from "react-dom/client"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ChatTurn, TurnPart } from "@/hooks/use-chat"
import { Message, MessageContent } from "@/components/ai-elements/message"
import { AssistantTurn } from "../components/features/chat/assistant-turn"
import { ArtifactViewerProvider, type ArtifactViewerTarget } from "../components/features/chat/artifact-context/projects/artifact-viewer-context"
import { artifactCommentMessage, type ArtifactAgentRequest } from "../components/features/chat/artifact-context/viewer/agent-dispatch"
import { ArtifactViewerPanel } from "../components/features/chat/artifact-context/viewer"
import { createArtifactMockServer, type ArtifactMockServer } from "../components/features/chat/artifact-context/projects/mock-server"
import type { ArtifactFile } from "../components/features/chat/artifact-context/projects/types"

const WORKSPACE = "ws-fixture"
const USER = "fixture-human"
const SLUG = "pricing-site"
const TOOL_CREATE = "mcp__crewship-routines__artifact_create"
const TOOL_PUBLISH = "mcp__crewship-routines__artifact_publish"

// ----- the small pricing site --------------------------------------------------------------------

const STYLE = `<style>
body{font-family:system-ui,sans-serif;margin:0;color:#1f2933;background:#f8fafc}
nav{display:flex;gap:16px;padding:14px 24px;background:#0f172a}nav a{color:#cbd5e1;text-decoration:none;font-weight:600}nav a.on{color:#fff}
main{padding:24px}h1{margin:0 0 6px;font-size:26px}.lead{color:#52606d;margin:0 0 20px}
.plans{display:flex;gap:14px;flex-wrap:wrap}.plans section{flex:1 1 160px;background:#fff;border:1px solid #d9e2ec;border-radius:12px;padding:16px}
.plans h2{margin:0 0 4px;font-size:18px}.price{font-size:24px;font-weight:700;margin:6px 0}.hot{border:2px solid #2563eb!important;box-shadow:0 6px 18px rgba(37,99,235,.18)}
.badge{display:inline-block;background:#2563eb;color:#fff;border-radius:99px;padding:2px 8px;font-size:12px;font-weight:700}
table{border-collapse:collapse;width:100%;background:#fff}td,th{border:1px solid #d9e2ec;padding:8px 10px;text-align:left}
dl{background:#fff;border:1px solid #d9e2ec;border-radius:12px;padding:12px 16px}dt{font-weight:700;margin-top:10px}dd{margin:4px 0 0;color:#52606d}
</style>`
const nav = (on: string) => `<nav><a href="plans.html"${on === "plans" ? ' class="on"' : ""}>Plans</a><a href="compare.html"${on === "compare" ? ' class="on"' : ""}>Compare</a><a href="faq.html"${on === "faq" ? ' class="on"' : ""}>FAQ</a></nav>`
const page = (title: string, on: string, body: string) => `<!doctype html><html><head><meta charset="utf-8"><title>${title}</title>${STYLE}</head><body>${nav(on)}<main>${body}</main></body></html>`

// #plan-team is minified on purpose: the anchor quote joins text nodes with nothing between tags
// ("Team$24…"), in the frame, the mock and the backend alike.
const plans = (highlight: boolean) => page("Plans", "plans", `<h1 id="headline">Simple pricing for every team</h1><p class="lead">Start free, upgrade when you grow.</p>
<div class="plans">
<section id="plan-starter">
  <h2>Starter</h2>
  <p class="price">$0</p>
  <p>1 workspace, 3 agents</p>
</section>
<section id="plan-team"${highlight ? ' class="hot"' : ""}>${highlight ? '<span class="badge">Most popular</span>' : ""}<h2>Team</h2><p class="price">$24</p><p>Unlimited agents, shared crews</p></section>
<section id="plan-business">
  <h2>Business</h2>
  <p class="price">$79</p>
  <p>SSO, audit log, priority support</p>
</section>
</div><p><a href="compare.html">Compare all features →</a></p>`)
const compare = page("Compare", "compare", `<h1 id="cmp-title">Compare plans</h1>
<table id="cmp-table"><tr><th>Feature</th><th>Starter</th><th>Team</th><th>Business</th></tr>
<tr><td>Agents</td><td>3</td><td>Unlimited</td><td>Unlimited</td></tr><tr><td>Shared crews</td><td>–</td><td>✓</td><td>✓</td></tr>
<tr><td>SSO</td><td>–</td><td>–</td><td>✓</td></tr></table><p><a href="faq.html">Questions? Read the FAQ →</a></p>`)
const faq = page("FAQ", "faq", `<h1 id="faq-title">Frequently asked questions</h1>
<dl><dt id="faq-seats">How are seats counted?</dt><dd>Every person who signs in during a month.</dd>
<dt id="faq-cancel">Can I cancel any time?</dt><dd>Yes, plans are monthly and stop at the end of the period.</dd></dl>
<p><a href="plans.html">Back to plans</a></p>`)
const html = (path: string, content: string): ArtifactFile => ({ path, content, media_type: "text/html" })
const FILES_V1 = [html("plans.html", plans(false)), html("compare.html", compare), html("faq.html", faq)]
const FILES_V2 = [html("plans.html", plans(true)), html("compare.html", compare), html("faq.html", faq)]

// ----- mock backend behind window.fetch ----------------------------------------------------------

const project = createArtifactMockServer({ slug: SLUG, name: "Pricing site", entrypoint: "plans.html", files: FILES_V1, message: "Plans, Compare and FAQ linked" })
const mocks: Record<string, ArtifactMockServer> = { [SLUG]: project }
// The project does not exist until the agent's create call returns.
let created = false

declare global { interface Window { __unmocked: string[]; __mock: typeof project; __dispatched: Record<string, unknown>[] } }
window.__unmocked = []
// What the chat would send to start the agent on a comment (the real chat sends it over its socket).
window.__dispatched = []
const sendToAgent = async ({ pageId, comment }: ArtifactAgentRequest) => {
  window.__dispatched.push(artifactCommentMessage(pageId, comment).metadata)
  return true
}
window.__mock = project
const realFetch = window.fetch.bind(window)
window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
  const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url
  const path = new URL(url, location.href).pathname
  const slug = /^\/api\/v1\/pages\/([^/]+)\/artifact\b/.exec(path)?.[1]
  const mock = slug ? mocks[decodeURIComponent(slug)] : path === "/api/v1/pages/artifacts/availability" ? project : undefined
  if (mock) {
    if (mock === project && slug && !created) return new Response(JSON.stringify({ error: "No such artifact.", code: "not_found" }), { status: 404, headers: { "content-type": "application/json" } })
    const response = mock.handle(url, init)
    if (response) return response
  }
  if (path === "/bundle.js") return realFetch(input, init)
  window.__unmocked.push(`${(init?.method ?? "GET").toUpperCase()} ${url}`)
  return new Response("not mocked", { status: 404 })
}

// ----- conversation ------------------------------------------------------------------------------

const at = new Date()
const part = (id: string, type: TurnPart["type"], content: string, metadata?: Record<string, unknown>): TurnPart => ({ id, type, content, metadata, timestamp: at })
const userTurn: ChatTurn = { id: "u1", role: "user", isStreaming: false, timestamp: at, parts: [part("u1-t", "text", "Turn the pricing prototype into a small site with three screens: plans, compare and FAQ.")] }
const createCall = part("a1-call", "tool_call", TOOL_CREATE, {
  tool_name: TOOL_CREATE, tool_id: "toolu_create",
  input: { slug: SLUG, name: "Pricing site", entrypoint: "plans.html", files: FILES_V1, message: "Plans, Compare and FAQ linked" },
})
const createResult = part("a1-result", "tool_result", JSON.stringify({ page_id: `page-${SLUG}`, slug: SLUG, version: 1, url: `/p/${SLUG}` }), { tool_id: "toolu_create" })
const publishCall = part("a2-call", "tool_call", TOOL_PUBLISH, { tool_name: TOOL_PUBLISH, tool_id: "toolu_publish", input: { slug: SLUG, base_version: 1, files: FILES_V2, message: "Team plan highlighted as Most popular" } })
const publishResult = part("a2-result", "tool_result", JSON.stringify({ page_id: `page-${SLUG}`, slug: SLUG, version: 2, url: `/p/${SLUG}` }), { tool_id: "toolu_publish" })

const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })

function Fixture() {
  const [turns, setTurns] = useState<ChatTurn[]>([
    userTurn,
    { id: "a1", role: "assistant", isStreaming: true, timestamp: at, parts: [part("a1-t", "text", "I’ll build it as an artifact project with three linked screens."), createCall] },
  ])
  const [target, setTarget] = useState<ArtifactViewerTarget | null>(null)
  const [step, setStep] = useState(0)
  const mobile = innerWidth < 768
  const viewerOpen = Boolean(target)

  const finishCreate = () => {
    created = true
    setTurns(prev => prev.map(t => t.id === "a1" ? { ...t, isStreaming: false, parts: [...t.parts, createResult] } : t))
    setStep(1)
  }
  const publishV2 = () => {
    project.agentPublish(FILES_V2, "Team plan highlighted as Most popular", "Highlighted the Team plan and marked it Most popular.")
    setTurns(prev => [...prev, { id: "a2", role: "assistant", isStreaming: false, timestamp: at, parts: [part("a2-t", "text", "Done: the Team plan now stands out with a Most popular badge."), publishCall, publishResult] }])
    setStep(2)
    void queryClient.invalidateQueries()
  }

  const conversation = <div className="grid content-start gap-4 px-4 py-4">
    <Message from="user"><MessageContent>{userTurn.parts[0].content}</MessageContent></Message>
    {turns.slice(1).map(turn => <AssistantTurn key={turn.id} turn={turn} onCopy={async () => true} onFileClick={() => {}} />)}
  </div>

  const controls = <div role="toolbar" aria-label="Fixture controls" className="flex flex-wrap items-center gap-2 border-b border-dashed bg-muted/60 px-4 py-1.5 text-[11px]">
    <b className="uppercase tracking-wide text-muted-foreground">Fixture controls</b>
    <button type="button" className="rounded border bg-card px-2 py-0.5 disabled:opacity-40" disabled={step !== 0} onClick={finishCreate}>Agent finishes create</button>
    <button type="button" className="rounded border bg-card px-2 py-0.5 disabled:opacity-40" disabled={step !== 1} onClick={publishV2}>Agent publishes v2</button>
  </div>

  const chat = <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-auto" style={mobile && viewerOpen ? { display: "none" } : undefined} inert={mobile && viewerOpen}>
    {controls}
    {conversation}
  </div>

  return <QueryClientProvider client={queryClient}>
    <ArtifactViewerProvider workspaceId={WORKSPACE} target={target} onTargetChange={setTarget} sendToAgent={sendToAgent}>
      {mobile
        ? <div className="relative flex h-dvh flex-col bg-background text-foreground">
          {chat}
          <div className="absolute inset-0 z-20 min-h-0 bg-background" hidden={!viewerOpen}><ArtifactViewerPanel variant="fullscreen" /></div>
        </div>
        : <div className="relative flex h-dvh bg-background text-foreground">
          {chat}
          <aside aria-label={viewerOpen ? "Artifact viewer" : undefined} hidden={!viewerOpen} className="min-h-0 min-w-0 w-[45%] min-w-[20rem] max-w-full shrink-0 border-l">
            <ArtifactViewerPanel variant="pane" />
          </aside>
        </div>}
    </ArtifactViewerProvider>
  </QueryClientProvider>
}

createRoot(document.getElementById("root")!).render(<Fixture />)
