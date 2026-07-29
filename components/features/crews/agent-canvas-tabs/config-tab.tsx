"use client"

import { Bot, CalendarClock, Settings2, Sparkles, Webhook, Wrench } from "lucide-react"

import { AgentLearningToggle } from "@/components/features/agents/agent-learning-toggle"
import { SystemPromptEditor } from "@/components/features/crews/system-prompt-editor"

import { AnthropicIcon, GeminiIcon, OpenAIIcon } from "@/components/icons/provider-icons"

import { Button } from "@/components/ui/button"
import { Appear, DetailCard } from "@/components/ui/detail"
import { AGENT_EXTERNAL_TRIGGERS, AGENT_SELF_LEARNING } from "@/lib/feature-gates"

import {
  ConfigCards, ConfigPresets, ConfigReadOnly, ConfigSelect, ConfigSwitch, ConfigText,
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

      {/* The system prompt is the longest thing on this screen and the one
          people actually read, so it takes a column of its own instead of
          being squeezed beside a switch. It stays inside the same bounded
          block — 800 characters of mono set 2000px wide is unreadable. */}
      <Appear order={6}>
        <SystemPromptEditor
          value={agent.system_prompt}
          onSave={(v) => patch({ system_prompt: v })}
          updatedHint={`updated ${new Date(agent.updated_at).toLocaleDateString()}`}
        />
      </Appear>

      {AGENT_SELF_LEARNING && (
        <Appear order={7}>
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
