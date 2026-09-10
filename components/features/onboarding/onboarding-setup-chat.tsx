"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { MessageSquareText, WifiOff } from "lucide-react"
import { motion } from "motion/react"
import { useSession } from "@/hooks/use-auth"
import { Spinner } from "@/components/ui/spinner"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { useChat, type ChatMessage, type ChatTurn, type HistoryPart } from "@/hooks/use-chat"
import { RealtimeProvider } from "@/hooks/use-realtime"
import { apiFetch } from "@/lib/api-fetch"
import { resolveWsBase } from "@/lib/server-base"
import { TurnRenderer } from "@/components/features/chat/turn-renderer"
import { ChatComposer } from "@/components/features/chat/composer/chat-composer"
import { FollowUps } from "@/components/features/chat/suggestions/follow-ups"
import { ProposalCard } from "./proposal-card"
import {
  applyOnboardingProposal,
  createOnboardingProposal,
  parseProposalSuggestion,
  startSetupAgentSession,
  type ApplyProposalResult,
  type OnboardingProposal,
  type SetupAgentSession,
  type SetupAgentUnavailableReason,
} from "./setup-agent-api"

/** Same construction as chat-panel.tsx's own `getWsUrl` — one `/ws` endpoint,
 *  app-scoped, not agent- or session-scoped. Not shared code because that
 *  component lives outside this feature's ownership; the two definitions are
 *  one line each and have to stay identical only in behaviour, not location. */
function getWsUrl(): string {
  const base = resolveWsBase()
  return base === "" ? "" : `${base}/ws`
}

/** Same shape as chat-panel.tsx's getWsToken: 401/403 is a real auth death
 *  (return null, useWebSocket gives up); anything else transient (throw, the
 *  WS hook retries with backoff). */
async function getWsToken(): Promise<string | null> {
  const res = await apiFetch("/api/v1/ws-token")
  if (res.status === 401 || res.status === 403) return null
  if (!res.ok) throw new Error(`ws-token fetch failed: ${res.status}`)
  const data = await res.json()
  if (typeof data?.token !== "string") {
    throw new Error("ws-token response missing token field")
  }
  return data.token
}

/** Generic follow-up chips for a conversation that has no per-agent
 *  suggested-prompts config (the setup agent is not a user-created agent
 *  row with its own `suggested_prompts`, so there is nothing to read one
 *  from). Reuses the same `FollowUps` chip rail the main chat renders —
 *  only the prompt strings are onboarding-specific. */
// Follow-up chips, in the language the person picked on step 1.
//
// These are sent verbatim as the user's own next message, so an English chip
// in a Czech conversation does not merely look wrong — it puts words in the
// user's mouth in a language they did not choose, and the Guide then has to
// decide which language to answer in. Only the languages the wizard's own
// picker offers need an entry; anything else falls back to English, which is
// the same default the system prompt uses when no LANGUAGE block is present.
const FOLLOW_UP_PROMPTS_BY_LANGUAGE: Record<string, string[]> = {
  English: [
    "Tell me more about what it would do",
    "Give me an example task it could run",
    "Let's try a different crew",
  ],
  Czech: [
    "Řekni mi víc o tom, co by dělala",
    "Dej mi příklad úkolu, který by zvládla",
    "Zkusme jinou crew",
  ],
}

function followUpPrompts(language?: string): string[] {
  return FOLLOW_UP_PROMPTS_BY_LANGUAGE[language ?? "English"] ?? FOLLOW_UP_PROMPTS_BY_LANGUAGE.English
}

/** What the person sees before their first message — the chat used to open
 *  on a single grey line ("What work should this crew take off your hands?")
 *  above an empty box, which is the one screen in the wizard where a
 *  first-time user has to invent something from nothing. These are concrete
 *  openers, sent verbatim as the user's own message, in the language they
 *  picked on step 1 (same rule as the follow-up chips above). */
interface StarterPrompt {
  title: string
  prompt: string
}

const STARTER_PROMPTS_BY_LANGUAGE: Record<string, { greeting: string; lead: string; footer: string; prompts: StarterPrompt[] }> = {
  English: {
    greeting: "Hi, I'm Crewship Guide.",
    lead: "Tell me what you'd like to hand off and I'll propose a crew of agents for it. Nothing is created until you click Create.",
    footer: "Or describe it in your own words below.",
    prompts: [
      { title: "Watch a codebase", prompt: "I want a crew that reviews pull requests in my GitHub repo and flags risky changes." },
      { title: "Scrape and summarise", prompt: "I need a crew that collects listings from a website every morning and sends me a summary." },
      { title: "Write content", prompt: "I need a crew that drafts blog posts and social media updates from my notes." },
      { title: "Keep the books", prompt: "I want a crew that sorts invoices, checks them against orders and prepares a monthly report." },
    ],
  },
  Czech: {
    greeting: "Ahoj, jsem Crewship Guide.",
    lead: "Napište mi, co chcete delegovat, a navrhnu vám pro to tým agentů. Nic se nevytvoří, dokud nekliknete na Create.",
    footer: "Nebo to popište vlastními slovy níže.",
    prompts: [
      { title: "Hlídat kód", prompt: "Chci tým, který kontroluje pull requesty v mém GitHub repozitáři a upozorní na riskantní změny." },
      { title: "Stahovat a shrnovat", prompt: "Potřebuji tým, který každé ráno stáhne inzeráty z webu a pošle mi souhrn." },
      { title: "Psát obsah", prompt: "Potřebuji tým, který z mých poznámek připraví články na blog a příspěvky na sociální sítě." },
      { title: "Vést účetnictví", prompt: "Chci tým, který třídí faktury, kontroluje je proti objednávkám a připraví měsíční report." },
    ],
  },
}

function starterPrompts(language?: string) {
  return STARTER_PROMPTS_BY_LANGUAGE[language ?? "English"] ?? STARTER_PROMPTS_BY_LANGUAGE.English
}

/** A one-shot latch the composer can await instead of being refused.
 *
 *  `settle` is idempotent and there is deliberately no way to close one again:
 *  a gate is re-armed by replacing it, so a promise that was already handed to
 *  a waiting send can never be un-resolved under it. */
interface HistoryGate {
  /** Resolves once the transcript base is in place — loaded, or given up on.
   *  Never rejects: "we could not load the old messages" is a state this
   *  surface continues from, not one it fails on. */
  open: Promise<void>
  settle: () => void
  settled: boolean
}

function newHistoryGate(): HistoryGate {
  let resolve!: () => void
  const open = new Promise<void>((r) => { resolve = r })
  const gate: HistoryGate = {
    open,
    settled: false,
    settle: () => {
      if (gate.settled) return
      gate.settled = true
      resolve()
    },
  }
  return gate
}

interface OnboardingSetupChatProps {
  /** Fires once, the moment starting the setup agent's session turns out not
   *  to be possible, with WHY. The parent step should fall back to the
   *  template grid (PRD §4.3's escape hatch) rather than leave this pane
   *  stuck either way — but "credential_required" is a recoverable, expected
   *  state (the user hasn't reached step 3 yet), not a dead end the way
   *  "unavailable" is, and the parent can treat the two differently (e.g.
   *  keep offering a way back to chat only for the former). */
  onUnavailable: (reason: SetupAgentUnavailableReason) => void
  /** Fires once a proposal has actually been applied. `result` is whatever
   *  the apply endpoint returned (fields optional — its response shape is
   *  still being finalised by the lane building it); `proposal` is the
   *  server-stored object the human approved, so the wizard can show its
   *  name immediately without waiting on the apply response to carry it. */
  onProposalApplied: (result: ApplyProposalResult, proposal: OnboardingProposal) => void
  /** Mirrors the current server-materialised proposal into the left pane.
   *  This is display-only; Create remains exclusively inside ProposalCard. */
  onProposalPrepared?: (proposal: OnboardingProposal | null) => void
  /** The language chosen on step 1, used for the follow-up chips. The chips
   *  are sent as the USER's message, so they have to be in the user's own
   *  language. The agent's own replies are handled separately, by the
   *  LANGUAGE block the server injects into its system prompt. */
  language?: string
}

/**
 * Step 2's conversation surface — a setup agent the user describes their
 * need to, which proposes a crew as a server-stored object rendered by
 * `ProposalCard`.
 *
 * This does NOT reimplement chat. It calls `hooks/use-chat.ts` — the same
 * hook `components/features/chat/chat-panel.tsx` uses — for turn assembly,
 * streaming reassembly and reconnect/resume, and it renders turns through
 * the SAME `TurnRenderer` the main chat surface uses (streaming assistant
 * text, the reasoning disclosure, status lines, the crew-provisioning build
 * card, errors, and the copy/reaction/regenerate action row all come from
 * there) and sends through the SAME `ChatComposer` (including its attachment
 * affordance). What is new here is only the shell around them: this feature
 * doesn't own `components/features/chat/`, so it cannot reuse that module's
 * `ChatPanel` (which also drags in sessions sidebars, ask-forms, a slash
 * palette and a dozen other panes this ephemeral, one-shot onboarding
 * conversation has no use for) — everything else is the real chat UI.
 */
export function OnboardingSetupChat({
  onUnavailable,
  onProposalApplied,
  onProposalPrepared,
  language,
}: OnboardingSetupChatProps) {
  const [session, setSession] = useState<SetupAgentSession | null>(null)
  const [starting, setStarting] = useState(true)
  const startedRef = useRef(false)

  // Start exactly once per mount, guarded by a ref rather than relying on an
  // empty dependency array alone: React 18 Strict Mode runs an effect twice
  // in development, and standing up the setup agent's session is a WRITE
  // (PRD §5.3 — it provisions a crew). A second, discarded session per mount
  // would be a second container nobody uses.
  useEffect(() => {
    if (startedRef.current) return
    startedRef.current = true
    let cancelled = false
    void (async () => {
      const outcome = await startSetupAgentSession()
      if (cancelled) return
      setStarting(false)
      if (!outcome.ok) {
        onUnavailable(outcome.reason)
        return
      }
      setSession(outcome.session)
    })()
    return () => {
      cancelled = true
    }
    // onUnavailable intentionally excluded: this must run exactly once per
    // mount, and the parent's callback identity is not what decides that.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  if (starting) {
    // The same shell the live chat renders into, so the pane does not jump
    // from a dashed placeholder to a full-height card when the Guide answers.
    return (
      <div
        className="flex h-[calc(100dvh-3rem)] min-h-[420px] max-h-[760px] w-full flex-col overflow-hidden rounded-[20px] border border-border bg-card shadow-lg lg:h-full lg:min-h-0 lg:max-h-none"
        role="status"
      >
        <div className="flex items-center gap-2 border-b border-border px-4 py-3 shrink-0">
          <span className="h-7 w-7 shrink-0 animate-pulse rounded-lg bg-muted" />
          <div className="min-w-0 flex-1 space-y-1.5">
            <div className="h-3 w-28 animate-pulse rounded bg-muted" />
            <div className="h-2.5 w-44 animate-pulse rounded bg-muted/70" />
          </div>
        </div>
        <div className="flex flex-1 items-center justify-center gap-2 text-sm text-muted-foreground">
          <Spinner className="h-4 w-4" />
          Waking up Crewship Guide…
        </div>
      </div>
    )
  }

  if (!session) {
    // onUnavailable already fired above; the parent step switches away from
    // this component on its next render. Nothing to show in the meantime.
    return null
  }

  return (
    // The CrewProvisioningCard TurnRenderer renders for a crew_provisioning
    // event calls useProvisioningStatus internally (for its live step/
    // progress display), which calls useRealtime() internally, and
    // useRealtime() throws hard without a provider in its tree. The
    // dashboard route mounts one in app/(dashboard)/layout.tsx;
    // the onboarding route has no such layout, so this feature mounts its
    // own — a self-contained provider (its own WS connection, scoped to
    // whatever's inside it), safe to nest anywhere.
    <RealtimeProvider>
      <ConnectedSetupChat
        agentId={session.agentId}
        sessionId={session.sessionId}
        workspaceId={session.workspaceId}
        onProposalApplied={onProposalApplied}
        onProposalPrepared={onProposalPrepared}
        language={language}
      />
    </RealtimeProvider>
  )
}

/** What a deferred send (server replied `ErrCrewProvisioning`) is waiting on.
 *
 *  The SERVER owns the resume: it attaches the message that triggered the
 *  build to the provisioning job (chatbridge.Bridge.HandleChatMessage →
 *  api.ProvisioningHandler.AttachPendingMessage) and runs — or fails — it
 *  itself on the job's completion, streaming the outcome on this same chat's
 *  session channel exactly like any other turn. That means this client needs
 *  NO polling and NO auto-resend logic of its own: `pendingResume` below is
 *  derived purely from whether the crew_provisioning turn is still the LAST
 *  turn in the list. The moment the server's resumed run posts anything —
 *  an assistant reply, or a real error — that becomes the new last turn and
 *  the banner clears on its own.
 *
 *  What CAN'T resume on its own is the one case where no job exists at all:
 *  enqueueing the build itself failed (bridge's enqErr branch). Nothing will
 *  ever complete in that case, so `failed` surfaces an immediate manual
 *  retry instead of a banner waiting on something that can't arrive.
 *
 *  The manual "Resend now" button is a fallback, not the primary path — kept
 *  because the coalescing in api.ProvisionJob.Pending makes a manual resend
 *  safe even while a build is genuinely still running (it replaces, not
 *  queues, the attached message) and because it gives the user an out if the
 *  automatic resume is ever slow to show up (e.g. this tab's WS connection
 *  dropped and reconnected after the resumed run already finished). */
interface PendingResume {
  turnId: string
  crewId?: string
  crewSlug?: string
  /** Empty when we have nothing to resend (e.g. this turn was already in
   *  history on mount, from a previous tab) — the banner then has nothing
   *  useful to offer and stays hidden. */
  text: string
  /** True only when the build never started at all (no job was ever
   *  created) — see the doc comment above. */
  failed: boolean
}

function ConnectedSetupChat({
  agentId,
  sessionId,
  workspaceId,
  onProposalApplied,
  onProposalPrepared,
  language,
}: {
  agentId: string
  sessionId: string
  workspaceId: string
  onProposalApplied: (result: ApplyProposalResult, proposal: OnboardingProposal) => void
  onProposalPrepared?: (proposal: OnboardingProposal | null) => void
  language?: string
}) {
  const session = useSession()
  const currentUserId = session.data?.user?.id
  const [historyReady, setHistoryReady] = useState(false)
  const [historyWarning, setHistoryWarning] = useState<string | null>(null)
  const [historyReloadNonce, setHistoryReloadNonce] = useState(0)

  // The latch a send typed during the load waits on. Created on the first
  // render rather than in the effect below, because the composer is on screen
  // for that render and the user can press Send in it — there must already be
  // something for that send to wait on. See `ensureSession`.
  const historyGateRef = useRef<HistoryGate | null>(null)
  if (!historyGateRef.current) historyGateRef.current = newHistoryGate()
  const rearmHistoryGate = useCallback(() => {
    if (historyGateRef.current?.settled) historyGateRef.current = newHistoryGate()
  }, [])

  const requestHistoryReload = useCallback(() => {
    // Shut the latch in the SAME tick useChat shut its own frame gate, not
    // when the reload effect gets around to running. This is called straight
    // out of the WS handler that has already set historyLoadedRef to false
    // (hooks/use-chat.ts), and between that call and the passive effect below
    // there is a rendered, interactive composer whose ensureSession would
    // otherwise still be looking at the settled latch and wave a send through
    // into a gate that is shut. The effect re-arms too — harmlessly, since
    // this one already did it and re-arming only replaces a SETTLED latch.
    rearmHistoryGate()
    setHistoryReloadNonce((n) => n + 1)
  }, [rearmHistoryGate])

  const {
    turns,
    sendMessage,
    stopGeneration,
    regenerateLastTurn,
    loadHistory,
    markHistoryUnavailable,
    isStreaming,
    connectionStatus,
  } = useChat({
    wsUrl: getWsUrl(),
    getToken: getWsToken,
    sessionId,
    currentUserId,
    onStreamReset: requestHistoryReload,
  })

  // useChat intentionally keeps every sequenced WS frame behind a history
  // gate until its caller establishes the transcript base. The main ChatPanel
  // does this, but the onboarding shell originally never called loadHistory;
  // every build card, assistant token, error and done frame was therefore
  // buffered forever while optimistic user bubbles remained on screen. Load
  // the real setup-chat history here (including message metadata, so a
  // proposal card survives reload) and only then let the composer send.
  useEffect(() => {
    let cancelled = false
    setHistoryReady(false)
    setHistoryWarning(null)
    // Re-arm for a reload: a send typed now has to wait for the NEW base and
    // must not sail through on the strength of a transcript already being
    // replaced. On the stream-reset path requestHistoryReload has done this
    // already, in the tick the reset happened; this covers every other way
    // the effect can re-run and is a no-op when the latch is still closed.
    rearmHistoryGate()
    const gate = historyGateRef.current

    const settleUnavailable = (message: string) => {
      if (cancelled) return
      // Do not deadlock live chat behind a transient history failure. Keep
      // existing turns, open useChat's stream gate, and tell the user that
      // only older messages may be missing.
      markHistoryUnavailable()
      setHistoryWarning(message)
      setHistoryReady(true)
    }

    void (async () => {
      try {
        const res = await apiFetch(
          `/api/v1/chats/${encodeURIComponent(sessionId)}/messages?workspace_id=${encodeURIComponent(workspaceId)}`,
        )
        if (!res.ok) {
          settleUnavailable("Previous setup messages could not be loaded. You can still continue chatting.")
          return
        }
        const data = await res.json().catch(() => null)
        const rows = data && typeof data === "object" && Array.isArray((data as { messages?: unknown }).messages)
          ? ((data as { messages: Array<Record<string, unknown>> }).messages)
          : []
        const history: ChatMessage[] = rows.map((row, index) => ({
          id: typeof row.id === "string" ? row.id : `setup-history-${sessionId}-${index}`,
          role: row.role === "user" || row.role === "system" || row.role === "tool" ? row.role : "assistant",
          content: typeof row.content === "string" ? row.content : "",
          parts: Array.isArray(row.parts) ? (row.parts as HistoryPart[]) : undefined,
          timestamp: new Date(typeof row.ts === "string" ? row.ts : Date.now()),
          metadata: row.metadata && typeof row.metadata === "object"
            ? (row.metadata as Record<string, unknown>)
            : undefined,
        }))
        if (cancelled) return
        loadHistory(history)
        setHistoryReady(true)
      } catch {
        settleUnavailable("Previous setup messages could not be loaded. You can still continue chatting.")
      } finally {
        // ONE settle site, in a `finally`, so no branch added here later can
        // leave a waiting send hanging: every way out of this function — a
        // transcript, a refused GET, a thrown one — is a way past the gate.
        //
        // Not on `cancelled`, deliberately. That is an unmount or a re-run
        // (Strict Mode double-invokes this in development), and the run that
        // replaces this one inherits the same unsettled gate and settles it.
        // Opening it here would either release a send into a component that
        // is gone, or release it against a transcript we just abandoned.
        if (!cancelled) gate?.settle()
      }
    })()

    return () => { cancelled = true }
  }, [sessionId, workspaceId, historyReloadNonce, loadHistory, markHistoryUnavailable, rearmHistoryGate])

  const [applying, setApplying] = useState(false)
  const [applyError, setApplyError] = useState<string | null>(null)
  const [appliedId, setAppliedId] = useState<string | null>(null)
  const scrollRef = useRef<HTMLDivElement>(null)

  // The materialised proposal (a real, server-computed roster) and its own
  // loading/error state — distinct from `applying`/`applyError`, which are
  // about the human's Create click. This state is about turning the agent's
  // SUGGESTION (a template + a model) into something with real agent rows,
  // which itself requires a network round trip (createOnboardingProposal)
  // before there is anything to put on a card.
  const [proposal, setProposal] = useState<OnboardingProposal | null>(null)
  const [proposalLoading, setProposalLoading] = useState(false)
  const [proposalPrepError, setProposalPrepError] = useState<string | null>(null)
  // Which TURN's marker has already been materialised.
  //
  // Keyed on the turn, not on the suggestion's content, and that is the whole
  // point. Content-keying (either as one slot or as a set of every key ever
  // seen) breaks the case the Guide hits constantly: the user says "give me
  // the approval card then", the Guide re-emits the SAME crew, the key
  // matches something already processed, no proposal is materialised — and
  // since the card only renders for a live `proposal`, no card appears at
  // all. The user is stuck asking for a card the code has decided it already
  // handled.
  //
  // A re-emit arrives on a NEW assistant turn, so a turn id distinguishes
  // "the Guide offered this again" from "React re-rendered" — which is the
  // only thing the guard was ever for. Nothing is created without a Create
  // click, and Apply is idempotent per proposal id server-side, so an extra
  // PENDING row is the entire cost of being wrong here.
  const processedSuggestionTurnRef = useRef<string | null>(null)

  // What the user most recently sent — the only record of it, since a
  // message deferred by provisioning is never persisted before the server
  // resumes it (see PendingResume's doc comment). Composer-cleared text
  // still lives here.
  const lastSentTextRef = useRef<string>("")
  // Pre-fills the composer for the proposal card's "Edit" affordance.

  // Derived, not stateful: the server resumes a deferred message on this
  // same chat channel (see PendingResume's doc comment), so "is a resume
  // still pending" is exactly "is the crew_provisioning turn still the LAST
  // turn". No effect, no polling — the instant the server's resumed run (or
  // its failure) posts anything, that becomes the new last turn and this
  // recomputes to null on its own.
  const pendingResume = useMemo<PendingResume | null>(() => {
    const last = turns[turns.length - 1]
    if (!last || last.role !== "system") return null
    const part = last.parts.find((p) => p.type === "crew_provisioning")
    if (!part) return null
    const text = lastSentTextRef.current
    // Nothing captured to resend (e.g. this turn was already in history
    // when the component mounted, from a previous tab/reload) — no banner,
    // nothing to retry blindly.
    if (!text) return null
    const meta = part.metadata ?? {}
    return {
      turnId: last.id,
      crewId: typeof meta.crew_id === "string" ? meta.crew_id : undefined,
      crewSlug: typeof meta.crew_slug === "string" ? meta.crew_slug : undefined,
      text,
      // enqueueStatus === "failed" (bridge/hub.go's ChatEvent.Metadata): no
      // job was ever created, so nothing will ever post a follow-up turn —
      // surface the manual retry immediately instead of waiting forever.
      failed: meta.status === "failed",
    }
  }, [turns])

  useEffect(() => {
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight })
  }, [turns, pendingResume])

  // Turn the setup agent's latest suggestion into a real proposal.
  //
  // This is NOT the write the "nothing before Create" property is about —
  // that property covers the CREW (crew/agent rows), which only
  // applyOnboardingProposal ever creates, from ProposalCard's onClick alone.
  // Materialising a proposal row is closer to a read: it asks the server to
  // resolve a template + model into what deploying it would actually
  // produce, so the card can show real agent rows instead of the agent's own
  // unverified claim (PRD §5.6 — the card must not be able to lie about what
  // a template resolves to). It runs under the user's own session
  // (createOnboardingProposal → apiFetch), same as picking a template.
  useEffect(() => {
    let suggestion: ReturnType<typeof parseProposalSuggestion> = null
    let suggestionTurnId: string | null = null
    outer: for (let i = turns.length - 1; i >= 0; i--) {
      suggestion = parseProposalSuggestion(turns[i].metadata)
      if (suggestion) {
        suggestionTurnId = turns[i].id
        break
      }
      for (const part of turns[i].parts) {
        suggestion = parseProposalSuggestion(part.metadata)
        if (suggestion) {
          suggestionTurnId = turns[i].id
          break outer
        }
      }
    }
    if (!suggestion || !suggestionTurnId) return
    if (processedSuggestionTurnRef.current === suggestionTurnId) return
    processedSuggestionTurnRef.current = suggestionTurnId
    setProposal(null)
    onProposalPrepared?.(null)
    setProposalPrepError(null)
    setProposalLoading(true)
    void (async () => {
      try {
        const created = await createOnboardingProposal(suggestion!, workspaceId)
        setProposal(created)
        onProposalPrepared?.(created)
      } catch (err) {
        setProposalPrepError(err instanceof Error ? err.message : "Could not prepare the proposal")
      } finally {
        setProposalLoading(false)
      }
    })()
  }, [turns, workspaceId, onProposalPrepared])

  // Manual fallback only — see PendingResume's doc comment for why this is
  // safe even while a build is genuinely still running (coalescing in
  // api.ProvisionJob.Pending) rather than the primary resume mechanism.
  // `pendingResume` is not cleared here: it is derived from `turns`, and it
  // clears itself once whatever this send produces (a fresh crew_provisioning
  // card if still building, a real reply or error once ready) lands as a new
  // last turn.
  const handleManualResume = useCallback(() => {
    if (!pendingResume?.text) return
    lastSentTextRef.current = pendingResume.text
    sendMessage(pendingResume.text)
  }, [pendingResume, sendMessage])

  // Wraps useChat's sendMessage so every outbound message is remembered for
  // a possible provisioning deferral. Mirrors useMessageSubmit's own
  // convention (components/features/chat/hooks/use-message-submit.ts) of
  // calling with exactly one argument when there is no metadata to carry,
  // rather than a trailing `undefined` — observable to a spy either way.
  const sendMessageTracked = useCallback(
    (text: string, metadata?: Record<string, unknown>) => {
      lastSentTextRef.current = text
      if (metadata) sendMessage(text, metadata)
      else sendMessage(text)
    },
    [sendMessage],
  )

  /** The composer's pre-send hook — here, a wait rather than a verdict.
   *
   *  There is nothing on this surface for it to ensure. The setup chat's row
   *  was written server-side by `startSetupAgentSession` before this component
   *  was ever rendered, which is where `sessionId` comes from; unlike the main
   *  chat's draft sessions (chat-panel.tsx's own ensureSession, which POSTs the
   *  row on the first send) there is no create here that can be refused. The
   *  only thing that was ever in question is whether the transcript base has
   *  landed — and until it has, a message must not go out, because useChat
   *  buffers every sequenced frame behind that same gate and the reply would
   *  arrive into a shut one.
   *
   *  So this returns `false` never, and waits instead. It used to return
   *  `historyReady` directly, and that boolean collapsed "not yet" into "no":
   *  the send was dropped in silence by useMessageSubmit (the toast belongs to
   *  chat-panel's `ensureSessionForSend`, whose doc comment is explicit that it
   *  fires only when a create is actually refused — which is exactly what this
   *  caller could not tell it), and the SAME `false` reaching the upload path
   *  through EnsureChatSessionProvider marked the file with an error chip
   *  saying the conversation could not be started. Both statements were false
   *  about a local GET that was still in flight and about to succeed.
   *
   *  The wait is bounded by the load itself and cannot outlive it: the gate is
   *  settled in a `finally`, on every outcome including a failed fetch. The
   *  draft survives the wait (nothing clears it until `onSent`), the transcript
   *  above the composer is already showing "Loading setup chat…" while it
   *  happens, and on any load fast enough to be uninteresting it is not a wait
   *  at all — the gate is open before the user has finished typing. */
  const ensureSession = useCallback(async () => {
    await historyGateRef.current?.open
    return true
  }, [])
  const starters = starterPrompts(language)
  const handleCopy = useCallback((content: string) => {
    navigator.clipboard.writeText(content).catch(() => {})
  }, [])
  const noopFileClick = useCallback(() => {}, [])

  // The ONLY call site of applyOnboardingProposal in this component, and it
  // is reached from exactly one place: ProposalCard's onCreate, which itself
  // fires only from that button's own onClick. See proposal-card.tsx and its
  // pinning test for the property this preserves end to end.
  const handleCreate = useCallback(() => {
    if (!proposal) return
    setApplying(true)
    setApplyError(null)
    void (async () => {
      try {
        const result = await applyOnboardingProposal(proposal.id, workspaceId)
        setAppliedId(proposal.id)
        onProposalApplied(result, proposal)
      } catch (err) {
        setApplyError(err instanceof Error ? err.message : "Could not create the crew")
      } finally {
        setApplying(false)
      }
    })()
  }, [proposal, workspaceId, onProposalApplied])

  return (
    <div className="flex h-[calc(100dvh-3rem)] min-h-[420px] max-h-[760px] w-full flex-col overflow-hidden rounded-[20px] border border-border bg-card shadow-lg lg:h-full lg:min-h-0 lg:max-h-none">
      <div className="flex items-center gap-2 border-b border-border px-4 py-3 shrink-0">
        <span className="flex h-7 w-7 shrink-0 items-center justify-center overflow-hidden rounded-lg border border-primary/30 bg-primary/15">
          {/* workspaceId is passed explicitly: this route never mounts the
              workspace store (nothing in app/(onboarding)/** calls
              useWorkspace()), so without it the backfill for the one real
              agent this page shows would be skipped (#2196). */}
          <AgentAvatar
            seed={agentId}
            agentId={agentId}
            workspaceId={workspaceId}
            alt=""
            className="h-full w-full rounded-none"
          />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5 text-sm font-medium tracking-tight">
            Crewship Guide
            <span
              aria-hidden="true"
              className={`inline-block h-1.5 w-1.5 rounded-full ${
                connectionStatus === "connected" ? "bg-success" : "bg-warn animate-pulse"
              }`}
            />
          </div>
          <div className="text-[11px] text-muted-foreground truncate">
            {connectionStatus === "connected"
              ? "Tell it what you need — it'll propose a crew"
              : "Reconnecting to the Guide…"}
          </div>
        </div>
        {connectionStatus !== "connected" && (
          <span className="flex items-center gap-1 text-[11px] text-muted-foreground shrink-0" data-testid="onboarding-chat-connection">
            <WifiOff className="h-3 w-3" />
            {connectionStatus}
          </span>
        )}
      </div>

      <div ref={scrollRef} className="min-h-0 flex-1 overscroll-contain overflow-y-auto px-4 py-3 space-y-3">
        {!historyReady && (
          <div className="flex items-center justify-center gap-2 py-6 text-xs text-muted-foreground" role="status">
            <Spinner className="h-3 w-3" />
            Loading setup chat…
          </div>
        )}
        {historyReady && turns.length === 0 && (
          <div className="px-1 py-4" data-testid="onboarding-chat-welcome">
            <div className="flex items-start gap-3">
              <span className="flex h-9 w-9 shrink-0 items-center justify-center overflow-hidden rounded-xl border border-primary/30 bg-primary/15">
                <AgentAvatar
                  seed={agentId}
                  agentId={agentId}
                  workspaceId={workspaceId}
                  alt=""
                  className="h-full w-full rounded-none"
                />
              </span>
              <div className="min-w-0 space-y-1">
                <div className="text-sm font-semibold tracking-tight">{starters.greeting}</div>
                <p className="text-sm leading-relaxed text-muted-foreground">{starters.lead}</p>
              </div>
            </div>
            <div className="mt-4 grid grid-cols-1 gap-2 sm:grid-cols-2">
              {starters.prompts.map((p, i) => (
                <motion.button
                  key={p.title}
                  type="button"
                  data-testid="onboarding-starter-prompt"
                  onClick={() => sendMessageTracked(p.prompt)}
                  disabled={connectionStatus !== "connected"}
                  initial={{ opacity: 0, y: 6 }}
                  animate={{ opacity: 1, y: 0 }}
                  transition={{ duration: 0.3, delay: 0.05 * i, ease: [0.16, 1, 0.3, 1] }}
                  whileTap={{ scale: 0.99 }}
                  className="flex items-start gap-2.5 rounded-xl border border-border bg-background/60 p-3 text-left transition-colors hover:border-primary/40 hover:bg-primary/5 disabled:cursor-not-allowed disabled:opacity-60"
                >
                  <MessageSquareText className="mt-0.5 h-3.5 w-3.5 shrink-0 text-primary" />
                  <span className="min-w-0">
                    <span className="block text-xs font-medium tracking-tight">{p.title}</span>
                    <span className="mt-0.5 block text-[11px] leading-relaxed text-muted-foreground">{p.prompt}</span>
                  </span>
                </motion.button>
              ))}
            </div>
            <p className="mt-3 text-center text-[11px] text-muted-foreground">{starters.footer}</p>
          </div>
        )}
        {historyWarning && (
          <div role="status" className="rounded-lg border border-warning/30 bg-warning/10 p-2.5 text-xs text-muted-foreground">
            {historyWarning}
          </div>
        )}
        {turns.map((turn: ChatTurn, idx: number) => {
          const isLastAssistant = turn.role === "assistant" && idx === turns.length - 1
          return (
            <TurnRenderer
              key={turn.id}
              turn={turn}
              onCopy={handleCopy}
              onFileClick={noopFileClick}
              isLastAssistant={isLastAssistant}
              onRegenerate={isLastAssistant && !isStreaming ? regenerateLastTurn : undefined}
              agentId={agentId}
              chatId={sessionId}
            />
          )
        })}

        {/* Deferred-send banner — the same crew_provisioning turn is already
            rendered above via TurnRenderer's CrewProvisioningCard; this line
            is the part that build card cannot say on its own: what happens
            to the message the user already sent. The SERVER resumes it
            automatically once the build finishes (see PendingResume's doc
            comment) — the button here is a manual fallback only, disabled
            while a run is actively streaming so it can't race that resume. */}
        {pendingResume && (
          <div className="flex items-center gap-2 text-xs text-muted-foreground" role="status">
            {pendingResume.failed ? (
              <div role="alert" className="flex flex-1 items-center gap-2 rounded-lg border border-destructive/40 bg-destructive/10 p-2.5 text-destructive">
                <span className="flex-1">Could not start the build for your message.</span>
                <button
                  type="button"
                  onClick={handleManualResume}
                  disabled={isStreaming}
                  className="shrink-0 rounded-md border border-destructive/40 px-2 py-1 font-medium hover:bg-destructive/10 disabled:opacity-50"
                >
                  Resend message
                </button>
              </div>
            ) : (
              <>
                <Spinner className="h-3 w-3" />
                <span className="flex-1">Building your agent&apos;s environment — your message will run automatically once it&apos;s ready.</span>
                <button
                  type="button"
                  onClick={handleManualResume}
                  disabled={isStreaming}
                  className="shrink-0 underline hover:text-foreground disabled:opacity-50 disabled:no-underline"
                >
                  Resend now
                </button>
              </>
            )}
          </div>
        )}

        {proposalLoading && (
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <Spinner className="h-3 w-3" />
            Preparing your proposal…
          </div>
        )}
        {proposalPrepError && !proposal && (
          <div role="alert" className="rounded-lg border border-destructive/40 bg-destructive/10 p-2.5 text-xs text-destructive">
            {proposalPrepError}
          </div>
        )}
        {/* Only while the proposal is still a QUESTION. Once Create has
            succeeded the card has nothing left to ask, and the crew it made is
            already listed in the panel beside the chat — leaving a second,
            identical copy pinned under the transcript just repeated the same
            roster twice on one screen. */}
        {proposal && appliedId !== proposal.id && (
          <ProposalCard
            proposal={proposal}
            onCreate={handleCreate}
            creating={applying}
            created={false}
            error={applyError}
          />
        )}
      </div>

      <FollowUps
        prompts={followUpPrompts(language)}
        onPick={sendMessageTracked}
        show={historyReady && connectionStatus === "connected" && !isStreaming && turns.length > 0 && !proposal && !pendingResume}
      />

      <ChatComposer
        agentId={agentId}
        sessionId={sessionId}
        agentName="Crewship Guide"
        variant="desktop"
        isStreaming={isStreaming}
        connectionStatus={connectionStatus}
        stopGeneration={stopGeneration}
        ensureSession={ensureSession}
        sendMessage={sendMessageTracked}
      />
    </div>
  )
}
