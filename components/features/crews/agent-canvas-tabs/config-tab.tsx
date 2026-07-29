"use client"

import { Bot, CalendarClock, FileText, Settings2, Shield, Webhook, Wrench } from "lucide-react"

import { AnthropicIcon, GeminiIcon, OpenAIIcon } from "@/components/icons/provider-icons"

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
    description: "Jen čte a plánuje. Codex jede read-only, Gemini v plan módu, Claude s omezeným seznamem nástrojů.",
  },
  {
    value: "CODING",
    title: "CODING",
    description: "Běžná práce — zápis do workspace a spouštění příkazů uvnitř kontejneru crew.",
  },
  {
    value: "FULL",
    title: "FULL",
    description: "Nejvyšší autonomie. U Factory Droid zvedá i úroveň samostatnosti.",
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

function Section({ icon: Icon, title, note, children, footer }: {
  icon: typeof Bot
  title: string
  note?: string
  children: React.ReactNode
  footer?: React.ReactNode
}) {
  return (
    <section className="overflow-hidden rounded-[11px] border border-border bg-card">
      <header className="flex items-center gap-2 border-b border-border bg-surface-subtle px-3 py-2.5 text-label font-semibold">
        <Icon className="h-3.5 w-3.5" />
        {title}
        {note && <span className="ml-auto text-micro font-normal text-muted-foreground-soft">{note}</span>}
      </header>
      <div>{children}</div>
      {footer && (
        <p className="border-t border-border bg-surface-subtle/60 px-3 py-2 text-micro leading-relaxed text-muted-foreground-soft">
          {footer}
        </p>
      )}
    </section>
  )
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
    <div className="grid items-start gap-4 lg:grid-cols-2">
      <div className="grid gap-4">
        <Section icon={Bot} title="Identita">
          <ConfigText label="Jméno" value={agent.name} onSave={(v) => patch({ name: v })} />
          <ConfigText
            label="Slug" mono hint="Používá se v CLI a při delegaci mezi agenty."
            value={agent.slug} onSave={(v) => patch({ slug: v })}
          />
          <ConfigText label="Role title" value={agent.role_title ?? ""} onSave={(v) => patch({ role_title: v })} />
          <ConfigSelect
            label="Crew" hint="Určuje kontejner, síť a sdílenou paměť."
            value={agent.crew_id ?? ""}
            options={[{ value: "", label: "(bez crew)" }, ...crews.map((c) => ({ value: c.id, label: c.name }))]}
            onSave={(v) => patch({ crew_id: v || null })}
          />
          <ConfigSelect
            label="Role v crew" hint="Lead smí zadávat práci ostatním a čekat na výsledek."
            value={agent.agent_role}
            options={[{ value: "AGENT", label: "Agent" }, { value: "LEAD", label: "Lead" }]}
            onSave={(v) => patch({ agent_role: v })}
          />
          {isLead && (
            <ConfigSelect
              label="Režim leada" hint="Pasivní lead jen odpovídá, sám nikoho neřídí."
              value={agent.lead_mode || "active"}
              options={[{ value: "active", label: "Aktivní" }, { value: "passive", label: "Pasivní" }]}
              onSave={(v) => patch({ lead_mode: v })}
            />
          )}
          <ConfigText
            label="Popis" multiline value={agent.description ?? ""}
            placeholder="Čím se tenhle agent zabývá…"
            onSave={(v) => patch({ description: v })}
          />
        </Section>

        <Section icon={Settings2} title="Model a běh">
          <ConfigSelect
            label="Provider" value={(agent.llm_provider ?? "ANTHROPIC").toUpperCase()}
            adornment={providerMark(agent.llm_provider)}
            options={PROVIDERS.map((p) => ({ value: p.value, label: p.label }))}
            onSave={(v) => patch({ llm_provider: v })}
          />
          <ConfigText label="Model" mono value={agent.llm_model ?? ""} onSave={(v) => patch({ llm_model: v })} />
          <ConfigSelect
            label="CLI adapter" hint="Čím se agent pouští uvnitř kontejneru."
            value={agent.cli_adapter}
            options={ADAPTERS.map((a) => ({ value: a.value, label: a.label }))}
            onSave={(v) => patch({ cli_adapter: v })}
          />
          <ConfigPresets
            label="Nejdelší běh" hint="Po vypršení se běh ukončí jako timeout."
            value={agent.timeout_seconds} presets={TIMEOUTS}
            onSave={(v) => patch({ timeout_seconds: v })}
          />
          <ConfigSwitch
            label="Paměť mezi sezeními" hint="Bez ní začíná každé sezení od nuly."
            checked={agent.memory_enabled}
            onSave={(v) => patch({ memory_enabled: v })}
          />
        </Section>

        <Section
          icon={Wrench} title="Co smí dělat" note="tool_profile"
          footer={<>Kam se agent dostane <b className="font-medium text-foreground">ven</b>, tohle neřeší — to je síťová politika crew.</>}
        >
          <ConfigCards
            value={agent.tool_profile}
            options={TOOL_PROFILES.map((t) => ({ value: t.value, title: t.title, description: t.description }))}
            onSave={(v) => patch({ tool_profile: v })}
          />
        </Section>
      </div>

      <div className="grid gap-4">
        <Section
          icon={FileText} title="Systémový prompt"
          footer="Uloží se po opuštění pole. Esc vrátí původní znění."
        >
          <ConfigText
            label="Instrukce" multiline value={agent.system_prompt ?? ""}
            placeholder="Bez promptu převezme agent výchozí chování crew."
            onSave={(v) => patch({ system_prompt: v })}
          />
        </Section>

        <Section icon={CalendarClock} title="Plán">
          <ConfigSwitch
            label="Spouštět podle plánu" checked={agent.schedule_enabled ?? false}
            onSave={(v) => patch({ schedule_enabled: v })}
          />
          <ConfigText
            label="Cron" mono value={agent.schedule_cron ?? ""} placeholder="0 3 * * *"
            onSave={(v) => patch({ schedule_cron: v || null })}
          />
          <ConfigText
            label="Zadání pro plánovaný běh" multiline value={agent.schedule_prompt ?? ""}
            placeholder="Co má agent v tom běhu udělat…"
            onSave={(v) => patch({ schedule_prompt: v || null })}
          />
          <ConfigReadOnly
            label="Příští běh"
            value={agent.schedule_next_run ? new Date(agent.schedule_next_run).toLocaleString() : "—"}
          />
        </Section>

        <Section
          icon={Webhook} title="Webhook"
          footer="Tajemství se ukáže jen jednou při rotaci — zpětně se přečíst nedá."
        >
          <ConfigReadOnly
            label="Podpisový klíč"
            value={webhookSet ? "nastaven" : "nenastaven"}
            note={webhookSet ? "rotovat v Nastavení" : undefined}
          />
        </Section>

        <Section
          icon={Shield} title="Prostředí"
          note={agent.crew ? `patří crew ${agent.crew.name}` : "bez crew"}
          footer={agent.crew
            ? <>Kontejner, paměť a síť nastavuje crew — změna by se dotkla všech jejích agentů.</>
            : <>Agent bez crew běží v izolovaném kontejneru workspace.</>}
        >
          <ConfigReadOnly
            label="Crew"
            value={agent.crew?.name ?? "—"}
            note={agent.crew ? "otevřít" : undefined}
          />
          {agent.crew && (
            <div className="px-3 pb-3">
              <button
                type="button"
                onClick={() => onSelectCrew(agent.crew!.slug)}
                className="rounded-lg border border-border bg-surface-raised px-3 py-1.5 text-label transition-colors hover:bg-white/[.09]"
              >
                Otevřít nastavení crew
              </button>
            </div>
          )}
        </Section>
      </div>
    </div>
  )
}
