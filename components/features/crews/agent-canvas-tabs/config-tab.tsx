"use client"

import { Bot, CalendarClock, ClipboardList, MessageSquareText, Settings2, Sparkles, Webhook, Wrench } from "lucide-react"
import { useEffect, useId, useRef, useState } from "react"
import { toast } from "sonner"

import { AgentLearningToggle } from "@/components/features/agents/agent-learning-toggle"
import { SystemPromptEditor } from "@/components/features/crews/system-prompt-editor"

import { AnthropicIcon, GeminiIcon, OpenAIIcon } from "@/components/icons/provider-icons"

import { Appear, DetailCard } from "@/components/ui/detail"
import { MAX_SUGGESTED_PROMPTS, MAX_SUGGESTED_PROMPT_LENGTH } from "@/lib/agent-suggestions"
import { MAX_FIELDS_PER_FORM, MAX_FORMS, summarizeAskForms } from "@/lib/ask-template"
import { AGENT_EXTERNAL_TRIGGERS, AGENT_SELF_LEARNING } from "@/lib/feature-gates"
import { cn } from "@/lib/utils"

import {
  ConfigCards, ConfigPresets, ConfigReadOnly, ConfigRow, ConfigSelect, ConfigSwitch, ConfigText,
} from "../canvas/config-field"
import { ConfigModel } from "../canvas/config-model"
import type { AgentRecord } from "./types"

// =============================================================================
// Agent configuration.
//
// Every field here exists in the API — verified against internal/api/agents.go
// and agents_create.go. Fields the schema carries but no handler exposes
// (temperature, max_tokens, the delegation limits) are deliberately absent:
// rendering a control that silently fails to save is worse than not offering
// it. The container and network rows are read-only because they belong to the
// crew, and editing them from here would let two screens fight over one value.
// =============================================================================

const PROVIDERS = [
  { value: "ANTHROPIC", label: "Anthropic" },
  { value: "OPENAI", label: "OpenAI" },
  { value: "GOOGLE", label: "Google" },
  { value: "OLLAMA", label: "Ollama" },
] as const

const ADAPTERS = [
  { value: "CLAUDE_CODE", label: "Claude Code" },
  { value: "OPENCODE", label: "OpenCode" },
  { value: "CODEX_CLI", label: "Codex CLI" },
  { value: "GEMINI_CLI", label: "Gemini CLI" },
  { value: "CURSOR_CLI", label: "Cursor CLI" },
  { value: "FACTORY_DROID", label: "Factory Droid" },
] as const

const TOOL_PROFILES = [
  {
    value: "MINIMAL",
    title: "MINIMAL",
    description: "Reads and plans only. Codex runs read-only, Gemini in plan mode, Claude with a restricted tool list.",
  },
  {
    value: "CODING",
    title: "CODING",
    description: "Everyday work — writes to the workspace and runs commands inside the crew container.",
  },
  {
    value: "FULL",
    title: "FULL",
    description: "Highest autonomy. On Factory Droid it also raises the autonomy level.",
  },
] as const

const TIMEOUTS = [
  { value: 300, label: "5 m" },
  { value: 900, label: "15 m" },
  { value: 1800, label: "30 m" },
  { value: 3600, label: "1 h" },
]

function providerMark(provider: string | null | undefined) {
  const p = (provider ?? "").toUpperCase()
  if (p === "OPENAI") return <OpenAIIcon className="h-3.5 w-3.5 shrink-0" />
  if (p === "GOOGLE") return <GeminiIcon className="h-3.5 w-3.5 shrink-0 text-[#4285F4]" />
  if (p === "ANTHROPIC") return <AnthropicIcon className="h-3.5 w-3.5 shrink-0 text-[#D97757]" />
  return <Bot className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
}

// Mirrors `controlBase` in ../canvas/config-field.tsx, which is module-private
// there. Copied rather than re-derived so this textarea sits on the same line
// and reacts to focus the same way as every other control on the screen.
const textareaBase =
  "type-row w-full rounded-lg border border-border bg-background px-2.5 text-foreground outline-none " +
  "transition-[border-color,box-shadow] hover:border-foreground/25 " +
  "focus:border-primary focus:shadow-[0_0_0_3px_color-mix(in_oklch,var(--primary)_20%,transparent)]"

/**
 * Suggested questions — the whole of the per-agent chip list (PRD
 * chat-as-a-primary-surface, Step 7). One question per line.
 *
 * The counter and the over-long marks are a courtesy, not the rule: the server
 * normalises and caps on write (internal/api/agents_suggested_prompts.go) and
 * names the offending prompt when it refuses. Showing it here only means the
 * refusal is rarely how anyone finds out. Nothing is blocked client-side —
 * a field that silently won't submit is worse than a specific error.
 */
function SuggestedPromptsField({ value, onSave }: {
  value: string
  onSave: (next: string) => Promise<void> | void
}) {
  const id = useId()
  const [local, setLocal] = useState(value)
  // The prop is the only trustworthy baseline for a rollback: `local` has
  // moved on with every keystroke. Same reasoning as config-field's
  // useOptimistic, which this deliberately mirrors.
  const server = useRef(value)
  useEffect(() => {
    server.current = value
    setLocal(value)
  }, [value])

  const prompts = parseSuggestedPromptsUncapped(local)
  const overLong = prompts
    .map((p, i) => ({ position: i + 1, length: [...p].length }))
    .filter((p) => p.length > MAX_SUGGESTED_PROMPT_LENGTH)
  const tooMany = prompts.length > MAX_SUGGESTED_PROMPTS

  async function commit() {
    if (local === server.current) return
    try {
      await onSave(local)
      server.current = local
      toast.success("Suggested questions saved")
    } catch (err) {
      setLocal(server.current)
      toast.error(err instanceof Error ? err.message : "Could not save")
    }
  }

  return (
    <ConfigRow
      full
      label="Suggested questions"
      hint="One per line. These appear as buttons under the chat, so the person talking to this agent can start without typing. Leave it empty and the defaults are used."
      htmlFor={id}
    >
      <div className="w-full">
        <textarea
          id={id}
          value={local}
          rows={5}
          placeholder={"What shipped this week?\nWhich invoices are overdue?\nDraft a reply to the last email"}
          onChange={(e) => setLocal(e.target.value)}
          onBlur={() => void commit()}
          onKeyDown={(e) => {
            if (e.key === "Escape") {
              e.preventDefault()
              setLocal(server.current)
              ;(e.target as HTMLElement).blur()
            }
          }}
          className={cn(
            textareaBase,
            "min-h-[92px] resize-y py-1.5 leading-relaxed",
            (tooMany || overLong.length > 0) && "border-destructive",
          )}
        />
        <div className="type-meta mt-1 flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
          <span className={cn("text-muted-foreground-soft", tooMany && "text-destructive")}>
            {prompts.length} / {MAX_SUGGESTED_PROMPTS}
          </span>
          {overLong.length > 0 && (
            <span className="text-destructive">
              {overLong.map((p) => `question ${p.position} is ${p.length} characters`).join(", ")}
              {` — the limit is ${MAX_SUGGESTED_PROMPT_LENGTH}`}
            </span>
          )}
        </div>
      </div>
    </ConfigRow>
  )
}

/**
 * Ask forms — the questions that need answers before they can be asked.
 *
 * Chat suggestions above are one line of text each; a form collects a
 * supplier, an amount, a month and a photo of the receipt, then renders them
 * into an ordinary message. The definition is JSON and is edited as JSON,
 * deliberately: a schema builder is the right surface once forms are shared
 * across agents (the pack library in the companion PRD), and until then a
 * builder would be several hundred lines of UI standing between an author and
 * a document they can already read.
 *
 * The counts and the parse error are a courtesy, not the rule. The server
 * validates on write (internal/askforms) and names the form and the
 * placeholder when it refuses — including the one refusal that matters most,
 * a {{placeholder}} that names no field, which is caught here at SAVE time so
 * the person talking to the agent never meets a broken template.
 */
function AskFormsField({ value, onSave }: {
  value: string
  onSave: (next: string) => Promise<void> | void
}) {
  const id = useId()
  const [local, setLocal] = useState(value)
  const server = useRef(value)
  useEffect(() => {
    server.current = value
    setLocal(value)
  }, [value])

  const summary = summarizeAskForms(local)

  async function commit() {
    if (local === server.current) return
    try {
      await onSave(local)
      server.current = local
      toast.success("Ask forms saved")
    } catch (err) {
      setLocal(server.current)
      toast.error(err instanceof Error ? err.message : "Could not save")
    }
  }

  return (
    <ConfigRow
      full
      label="Forms"
      hint="A JSON array of form definitions. Each one becomes a chip that opens a short questionnaire; submitting it sends an ordinary message built from the template."
      htmlFor={id}
    >
      <div className="w-full">
        <textarea
          id={id}
          value={local}
          rows={10}
          spellCheck={false}
          placeholder={ASK_FORMS_PLACEHOLDER}
          onChange={(e) => setLocal(e.target.value)}
          onBlur={() => void commit()}
          onKeyDown={(e) => {
            if (e.key === "Escape") {
              e.preventDefault()
              setLocal(server.current)
              ;(e.target as HTMLElement).blur()
            }
          }}
          className={cn(
            textareaBase,
            "min-h-[180px] resize-y py-1.5 font-mono text-xs leading-relaxed",
            (summary.error || summary.tooManyForms || summary.overFullForms.length > 0) &&
              "border-destructive",
          )}
        />
        <div className="type-meta mt-1 flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
          <span className={cn("text-muted-foreground-soft", summary.tooManyForms && "text-destructive")}>
            {summary.forms} / {MAX_FORMS} forms · {summary.fields} fields
          </span>
          {summary.error && <span className="text-destructive">{summary.error}</span>}
          {!summary.error && summary.overFullForms.length > 0 && (
            <span className="text-destructive">
              {summary.overFullForms.join(", ")} — the limit is {MAX_FIELDS_PER_FORM} fields per form
            </span>
          )}
        </div>
      </div>
    </ConfigRow>
  )
}

/** Shown in an empty editor: a whole working form, because the fastest way to
 *  write the second one is to edit the first. */
const ASK_FORMS_PLACEHOLDER = `[
  {
    "id": "receipt",
    "label": "Add a receipt",
    "attachment": "required",
    "template": "Please file this receipt.\\n\\nSupplier: {{supplier}}\\nAmount: {{amount}} {{amount_currency}}\\nPeriod: {{month}}",
    "fields": [
      { "name": "supplier", "label": "Supplier", "type": "text", "required": true },
      { "name": "amount", "label": "Amount", "type": "money", "currency": ["CZK", "EUR"] },
      { "name": "month", "label": "Period", "type": "month" }
    ]
  }
]`

/**
 * Like parseSuggestedPrompts but WITHOUT the cap — the editor has to be able
 * to show a ninth line in order to say there is one. parseSuggestedPrompts is
 * the render path and truncates on purpose; this is the counting path.
 */
function parseSuggestedPromptsUncapped(raw: string): string[] {
  return raw.split(/\r\n|\r|\n/).map((l) => l.trim()).filter((l) => l.length > 0)
}

export interface ConfigTabProps {
  agent: AgentRecord
  crews: { id: string; name: string; slug: string }[]
  patch: (body: Record<string, unknown>) => Promise<void>
  onSelectCrew: (slug: string | null) => void
}

export function ConfigTab({ agent, crews, patch, onSelectCrew }: ConfigTabProps) {
  const isLead = agent.agent_role === "LEAD"
  const webhookSet = (agent as AgentRecord & { webhook_secret_set?: boolean }).webhook_secret_set ?? false
  const tools = agent.cli_tools ?? []

  return (
    // `columns: 3 24rem` is the whole rule: at most three columns, each at
    // least 24rem. Narrow gives one, the usual width two, a wide pane three —
    // no breakpoints, and a card can never be dealt into a column too thin to
    // hold a label and its control.
    //
    // This block IS capped, unlike the pane around it, because it is a form.
    // Data fills a monitor happily; a settings row does not — stretch it and
    // the label drifts one way, the control the other, and the pair stops
    // reading as one thing. That was the gap Pavel spotted in Identity.
    <div className="[columns:3_24rem] gap-4 max-w-[105rem] [&>*]:mb-4 [&>*]:break-inside-avoid">
      <Appear order={0}>
        <DetailCard bare icon={Bot} title="Identity">
          <ConfigText label="Name" value={agent.name} onSave={(v) => patch({ name: v })} />
          <ConfigText
            label="Slug" mono hint="Used in the CLI and when delegating between agents."
            value={agent.slug} onSave={(v) => patch({ slug: v })}
          />
          <ConfigText label="Role title" value={agent.role_title ?? ""} onSave={(v) => patch({ role_title: v })} />
          {/* Only offered once the crew list has arrived. Rendering it early
              meant the agent's own crew was not among the options, so the
              select fell back to "(no crew)" and the first stray change
              detached the agent from its crew — silently. */}
          {crews.length > 0 ? (
            <ConfigSelect
              label="Crew"
              hint="Decides the container, the network and the shared memory — a change there hits every agent in the crew."
              value={agent.crew_id ?? ""}
              options={[{ value: "", label: "(no crew)" }, ...crews.map((c) => ({ value: c.id, label: c.name }))]}
              onSave={(v) => patch({ crew_id: v || null })}
              action={agent.crew ? (
                <button
                  type="button"
                  onClick={() => onSelectCrew(agent.crew!.slug)}
                  className="type-meta shrink-0 whitespace-nowrap text-primary hover:underline"
                >
                  Open crew
                </button>
              ) : undefined}
            />
          ) : (
            <ConfigReadOnly label="Crew" value={agent.crew?.name ?? "—"} note="loading" />
          )}
          <ConfigSelect
            label="Role in crew" hint="A lead may assign work to the others and wait for the result."
            value={agent.agent_role}
            options={[{ value: "AGENT", label: "Agent" }, { value: "LEAD", label: "Lead" }]}
            onSave={(v) => patch({ agent_role: v })}
          />
          {isLead && (
            <ConfigSelect
              label="Lead mode" hint="A passive lead only answers; it never drives anyone."
              value={agent.lead_mode || "active"}
              options={[{ value: "active", label: "Active" }, { value: "passive", label: "Passive" }]}
              onSave={(v) => patch({ lead_mode: v })}
            />
          )}
          <ConfigText
            label="Description" multiline value={agent.description ?? ""}
            placeholder="What this agent does…"
            onSave={(v) => patch({ description: v })}
          />
        </DetailCard>
      </Appear>

      <Appear order={1}>
        <DetailCard bare icon={Settings2} title="Model and run">
          <ConfigSelect
            label="Provider" value={(agent.llm_provider ?? "ANTHROPIC").toUpperCase()}
            adornment={providerMark(agent.llm_provider)}
            options={PROVIDERS.map((p) => ({ value: p.value, label: p.label }))}
            onSave={(v) => patch({ llm_provider: v })}
          />
          <ConfigModel
            label="Model" hint="Only what this provider can actually serve."
            workspaceId={agent.workspace_id}
            provider={(agent.llm_provider ?? "ANTHROPIC").toUpperCase()}
            value={agent.llm_model ?? ""}
            onSave={(v) => patch({ llm_model: v })}
          />
          <ConfigSelect
            label="CLI adapter" hint="What launches the agent inside the container."
            value={agent.cli_adapter}
            options={ADAPTERS.map((a) => ({ value: a.value, label: a.label }))}
            onSave={(v) => patch({ cli_adapter: v })}
          />
          <ConfigPresets
            label="Longest run" hint="When it expires the run ends as a timeout."
            value={agent.timeout_seconds} presets={TIMEOUTS}
            onSave={(v) => patch({ timeout_seconds: v })}
          />
          <ConfigSwitch
            label="Memory between sessions" hint="Without it every session starts from nothing."
            checked={agent.memory_enabled}
            onSave={(v) => patch({ memory_enabled: v })}
          />
        </DetailCard>
      </Appear>

      <Appear order={2}>
        <DetailCard
          bare icon={Wrench} title="What it may do" subtitle="tool_profile"
          footer={<>Where the agent reaches <b className="font-medium text-foreground">outward</b> is not decided here — that is the crew network policy.</>}
        >
          <ConfigCards
            value={agent.tool_profile}
            options={TOOL_PROFILES.map((t) => ({ value: t.value, title: t.title, description: t.description }))}
            onSave={(v) => patch({ tool_profile: v })}
          />
          {tools.length > 0 && (
            <div className="border-t border-hairline px-3 py-2.5">
              <div className="type-meta mb-1.5 uppercase tracking-wide text-muted-foreground-soft">
                Tools currently enabled
              </div>
              <div className="flex flex-wrap gap-1">
                {tools.slice(0, 8).map((t) => (
                  <span
                    key={t}
                    className="type-meta rounded border border-border bg-muted px-1.5 py-0.5 font-mono text-foreground/80"
                  >
                    {t}
                  </span>
                ))}
                {tools.length > 8 && (
                  <span className="type-meta text-muted-foreground-soft">+ {tools.length - 8} more</span>
                )}
              </div>
            </div>
          )}
        </DetailCard>
      </Appear>

      {/* Scheduling an agent directly is a second cron alongside routines —
          internal/scheduler/scheduler.go registers one entry per agent with
          schedule_enabled=1 and fires it straight through the orchestrator,
          while routine schedules dedupe at the executor chokepoint. One
          concept, two mechanisms, two idempotency stories. So this screen no
          longer offers it: a recurring job is a routine.

          It is NOT simply deleted, because the cron is real and still running.
          Removing the card outright would leave agents firing on a schedule
          with nothing in the product that admits it exists. The card appears
          only when a schedule is actually set, read-only, and its one action
          is to stop it. */}
      {(agent.schedule_enabled || agent.schedule_cron) && (
        <Appear order={3}>
          <DetailCard
            bare icon={CalendarClock} title="Scheduled run" tone="warn"
            subtitle="legacy"
            footer="Recurring work belongs in Routines, where a run is visible, versioned and replayable. This per-agent schedule predates that and is being retired — move it to a routine and switch it off here."
          >
            <ConfigReadOnly label="Cron" value={agent.schedule_cron || "—"} />
            <ConfigReadOnly
              label="Next run"
              value={agent.schedule_next_run ? new Date(agent.schedule_next_run).toLocaleString() : "—"}
            />
            {agent.schedule_prompt && (
              <ConfigReadOnly label="Prompt" value={agent.schedule_prompt} />
            )}
            <ConfigSwitch
              label="Still firing" hint="Turn this off once the work lives in a routine."
              checked={agent.schedule_enabled ?? false}
              onSave={(v) => patch({ schedule_enabled: v })}
            />
          </DetailCard>
        </Appear>
      )}

      {AGENT_EXTERNAL_TRIGGERS && (
        <Appear order={4}>
          <DetailCard
            bare icon={Webhook} title="Webhook and hooks"
            footer={<>
              An agent has one signing secret, not a list of webhooks — the multi-webhook surface belongs to
              routines. The secret is shown once on rotation and can never be read back. Rotate it with{" "}
              <code className="font-mono text-foreground/80">crewship agent rotate-webhook-secret {agent.slug}</code>;
              hooks are listed and toggled with{" "}
              <code className="font-mono text-foreground/80">crewship hooks list / enable / disable</code>.
            </>}
          >
            <ConfigReadOnly
              label="Signing key"
              value={webhookSet ? "set" : "not set"}
              note={webhookSet ? "rotate in Settings" : undefined}
            />
          </DetailCard>
        </Appear>
      )}

      {/* Per-agent chat suggestions. Without them every agent in the product
          offers the same four generic chips, which is the most-seen and
          least-useful text in the app. One column, one textarea — the pack
          library it could have been is the companion PRD's problem. */}
      <Appear order={5}>
        <DetailCard
          bare icon={MessageSquareText} title="Chat suggestions"
          footer="Shown only on an empty conversation. Write the questions this agent is actually good at — the ones you would otherwise type every morning."
        >
          <SuggestedPromptsField
            value={(agent as AgentRecord & { suggested_prompts?: string | null }).suggested_prompts ?? ""}
            onSave={(v) => patch({ suggested_prompts: v })}
          />
        </DetailCard>
      </Appear>

      {/* Ask forms sit next to the suggestions because they are the same
          feature at two sizes: a question you click, and a question you fill
          in first. The footer states the one rule an author cannot infer from
          the JSON — the line-drop — where they will actually read it. */}
      <Appear order={6}>
        <DetailCard
          bare icon={ClipboardList} title="Ask forms"
          footer={<>
            Every <code className="font-mono text-foreground/80">{"{{field}}"}</code> in a template must
            name a field on the same form, or the save is refused. An <b className="font-medium text-foreground">
            unanswered optional field takes its whole line away</b> — label and all — so
            {" "}<code className="font-mono text-foreground/80">Period: {"{{month}}"}</code> leaves nothing
            behind when no month was given. Preview one without a browser with{" "}
            <code className="font-mono text-foreground/80">crewship agent ask-preview {agent.slug} &lt;form-id&gt; --var k=v</code>.
          </>}
        >
          <AskFormsField
            value={(agent as AgentRecord & { ask_forms?: string | null }).ask_forms ?? ""}
            onSave={(v) => patch({ ask_forms: v })}
          />
        </DetailCard>
      </Appear>

      {/* The system prompt is the longest thing on this screen and the one
          people actually read, so it takes a column of its own instead of
          being squeezed beside a switch. It stays inside the same bounded
          block — 800 characters of mono set 2000px wide is unreadable. */}
      <Appear order={7}>
        <SystemPromptEditor
          value={agent.system_prompt}
          onSave={(v) => patch({ system_prompt: v })}
          updatedHint={`updated ${new Date(agent.updated_at).toLocaleDateString()}`}
        />
      </Appear>

      {AGENT_SELF_LEARNING && (
        <Appear order={8}>
          <div data-testid="learning-card">
            <DetailCard
              bare icon={Sparkles} title="Learning posture"
              footer="Per agent, and separate from the crew's autonomy level. Every flip is recorded with its reason."
            >
              <AgentLearningToggle bare agentId={agent.id} workspaceId={agent.workspace_id} />
            </DetailCard>
          </div>
        </Appear>
      )}

    </div>
  )
}
