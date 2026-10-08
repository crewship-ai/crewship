"use client"

import { CalendarClock, ClipboardList, MessageSquareText, Sparkles, Webhook } from "lucide-react"
import { useEffect, useId, useRef, useState } from "react"
import { toast } from "sonner"

import { AgentLearningToggle, type LearningDraft } from "@/components/features/agents/agent-learning-toggle"

import { Appear, DetailCard } from "@/components/ui/detail"
import { MAX_SUGGESTED_PROMPTS, MAX_SUGGESTED_PROMPT_LENGTH } from "@/lib/agent-suggestions"
import { AGENT_EXTERNAL_TRIGGERS, AGENT_SELF_LEARNING } from "@/lib/feature-gates"
import { cn } from "@/lib/utils"

import { ConfigReadOnly, ConfigRow, ConfigSwitch } from "../canvas/config-field"
import { AskFormsBuilder } from "../ask-forms-builder"
import type { AgentRecord } from "./types"

// =============================================================================
// Agent configuration.
//
// Every field here exists in the API — verified against internal/api/agents.go
// and agents_create.go. Fields the schema carries but no handler exposes
// (temperature, max_tokens, the delegation limits) are deliberately absent:
// rendering a control that silently fails to save is worse than not offering
// it. The create-agent dialog's "Not editable here" note says the same thing;
// it used to send people to this tab instead (#1781), so if that ever changes
// it changes in both places. The container and network rows are read-only
// because they belong to the crew, and editing them from here would let two
// screens fight over one value.
// =============================================================================

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
export function SuggestedPromptsField({ value, onSave, draftMode = false }: {
  draftMode?: boolean
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
    if (draftMode || local === server.current) return
    try {
      await onSave(local)
      server.current = local
      if (!draftMode) toast.success("Suggested questions saved")
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
          onChange={(e) => { setLocal(e.target.value); if (draftMode) void onSave(e.target.value) }}
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
 * Like parseSuggestedPrompts but WITHOUT the cap — the editor has to be able
 * to show a ninth line in order to say there is one. parseSuggestedPrompts is
 * the render path and truncates on purpose; this is the counting path.
 */
function parseSuggestedPromptsUncapped(raw: string): string[] {
  return raw.split(/\r\n|\r|\n/).map((l) => l.trim()).filter((l) => l.length > 0)
}

/**
 * The agent settings the Edit dialog's own sections do not cover: chat
 * suggestions, ask forms, the legacy per-agent schedule and the learning
 * posture. Every change goes to `patch`, which the dialog keeps as a draft;
 * nothing here writes on its own. (Identity, model, tools and the system
 * prompt used to have a second, save-per-field editor here; the dialog's
 * sections own them now.)
 */
export interface ConfigTabProps {
  agent: AgentRecord
  patch: (body: Record<string, unknown>) => Promise<void>
  /** The learning flip waiting for the dialog's Save. */
  learning?: { draft: LearningDraft | null; onChange: (next: LearningDraft | null) => void }
}

export function ConfigTab({ agent, patch, learning }: ConfigTabProps) {
  const webhookSet = (agent as AgentRecord & { webhook_secret_set?: boolean }).webhook_secret_set ?? false

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
            draftMode
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
          <AskFormsBuilder value={(agent as AgentRecord & { ask_forms?: string | null }).ask_forms ?? ""} onChange={(value) => { void patch({ ask_forms: value }) }} />
        </DetailCard>
      </Appear>

      {AGENT_SELF_LEARNING && (
        <Appear order={8}>
          <div data-testid="learning-card">
            <DetailCard
              bare icon={Sparkles} title="Learning posture"
              footer="Per agent, and separate from the crew's autonomy level. Every flip is recorded with its reason."
            >
              <AgentLearningToggle bare agentId={agent.id} workspaceId={agent.workspace_id} draft={learning?.draft} onDraftChange={learning?.onChange} />
            </DetailCard>
          </div>
        </Appear>
      )}

    </div>
  )
}
