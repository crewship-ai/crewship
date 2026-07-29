"use client"

import { Bot, CalendarClock, Settings2, Shield, Webhook, Wrench } from "lucide-react"

import { AnthropicIcon, GeminiIcon, OpenAIIcon } from "@/components/icons/provider-icons"

import { Button } from "@/components/ui/button"
import { DetailCard } from "@/components/ui/detail"

import {
  ConfigCards, ConfigPresets, ConfigReadOnly, ConfigSelect, ConfigSwitch, ConfigText,
} from "../canvas/config-field"
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

  return (
    // Cards flow into as many columns as the viewport affords, instead of being
    // dealt by hand into two fixed piles. At 1180px that is still two columns
    // and reads exactly as before; on a wide monitor it becomes three and the
    // screen stops ending halfway down. break-inside-avoid keeps a card whole.
    <div className="columns-1 gap-4 lg:columns-2 min-[1920px]:columns-3 [&>*]:mb-4 [&>*]:break-inside-avoid">
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
            label="Crew" hint="Decides the container, the network and the shared memory."
            value={agent.crew_id ?? ""}
            options={[{ value: "", label: "(no crew)" }, ...crews.map((c) => ({ value: c.id, label: c.name }))]}
            onSave={(v) => patch({ crew_id: v || null })}
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

      <DetailCard bare icon={Settings2} title="Model and run">
        <ConfigSelect
          label="Provider" value={(agent.llm_provider ?? "ANTHROPIC").toUpperCase()}
          adornment={providerMark(agent.llm_provider)}
          options={PROVIDERS.map((p) => ({ value: p.value, label: p.label }))}
          onSave={(v) => patch({ llm_provider: v })}
        />
        <ConfigText label="Model" mono value={agent.llm_model ?? ""} onSave={(v) => patch({ llm_model: v })} />
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

      <DetailCard
        bare icon={Wrench} title="What it may do" subtitle="tool_profile"
        footer={<>Where the agent reaches <b className="font-medium text-foreground">outward</b> is not decided here — that is the crew network policy.</>}
      >
        <ConfigCards
          value={agent.tool_profile}
          options={TOOL_PROFILES.map((t) => ({ value: t.value, title: t.title, description: t.description }))}
          onSave={(v) => patch({ tool_profile: v })}
        />
      </DetailCard>

      <DetailCard bare icon={CalendarClock} title="Schedule">
        <ConfigSwitch
          label="Run on a schedule" checked={agent.schedule_enabled ?? false}
          onSave={(v) => patch({ schedule_enabled: v })}
        />
        <ConfigText
          label="Cron" mono value={agent.schedule_cron ?? ""} placeholder="0 3 * * *"
          onSave={(v) => patch({ schedule_cron: v || null })}
        />
        <ConfigText
          label="Prompt for the scheduled run" multiline value={agent.schedule_prompt ?? ""}
          placeholder="What the agent should do in that run…"
          onSave={(v) => patch({ schedule_prompt: v || null })}
        />
        <ConfigReadOnly
          label="Next run"
          value={agent.schedule_next_run ? new Date(agent.schedule_next_run).toLocaleString() : "—"}
        />
      </DetailCard>

      <DetailCard
        bare icon={Webhook} title="Webhook"
        footer="The secret is shown once on rotation — it can never be read back."
      >
        <ConfigReadOnly
          label="Signing key"
          value={webhookSet ? "set" : "not set"}
          note={webhookSet ? "rotate in Settings" : undefined}
        />
      </DetailCard>

      <DetailCard
        bare icon={Shield} title="Environment"
        subtitle={agent.crew ? `owned by crew ${agent.crew.name}` : "no crew"}
        footer={agent.crew
          ? <>The container, memory and network belong to the crew — a change would hit all of its agents.</>
          : <>An agent with no crew runs in an isolated workspace container.</>}
      >
        <ConfigReadOnly
          label="Crew"
          value={agent.crew?.name ?? "—"}
          note={agent.crew ? "open" : undefined}
        />
        {agent.crew && (
          <div className="px-3 pb-3">
            <Button variant="outline" size="sm" onClick={() => onSelectCrew(agent.crew!.slug)}>
              Open crew settings
            </Button>
          </div>
      )}
      </DetailCard>
    </div>
  )
}
