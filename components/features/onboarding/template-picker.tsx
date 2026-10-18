"use client"

import { KeyRound, Plus } from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CrewIcon } from "@/components/ui/crew-icon"
import { StatusPill } from "@/components/ui/status-pill"
import { ChoiceTile } from "@/components/ui/choice-tile"
import { TEAM_TEMPLATE_SLUGS, TEMPLATES, TEMPLATE_MODEL_LABEL, type CrewTemplateSlug } from "./crew-templates"

/**
 * The crew step's template path: four tested teams as tiles, Start blank on
 * its own below them, and — beside them from 768 px — what the picked template
 * brings and what it needs. "It needs" is read from the wizard's real state
 * (the model token from step 2, the container runtime), never assumed.
 */
export function TemplatePicker({
  value,
  onChange,
  hasModelToken,
  runtimeInUse,
}: {
  value: CrewTemplateSlug | null
  onChange: (slug: CrewTemplateSlug) => void
  hasModelToken: boolean
  runtimeInUse: boolean | null
}) {
  const picked = value ? TEMPLATES[value] : null
  return (
    <div className="grid gap-4 md:grid-cols-[minmax(0,1fr)_280px]">
      <div className="space-y-2">
        <div className="grid gap-2 sm:grid-cols-2">
          {TEAM_TEMPLATE_SLUGS.map((slug) => {
            const tpl = TEMPLATES[slug]
            return (
              <ChoiceTile
                key={slug}
                active={value === slug}
                onClick={() => onChange(slug)}
                leading={<CrewIcon icon={tpl.icon} color={tpl.color} size="sm" />}
                title={tpl.name}
                description={tpl.description}
              />
            )
          })}
        </div>
        <ChoiceTile
          icon={Plus}
          active={value === "blank"}
          onClick={() => onChange("blank")}
          title={TEMPLATES.blank.name}
          description={TEMPLATES.blank.description}
        />
      </div>

      <aside aria-label="What this crew gets" className="space-y-3 rounded-lg bg-muted p-3">
        {picked ? (
          <>
            <div>
              <div className="text-micro font-semibold uppercase tracking-wider text-muted-foreground-soft">You get</div>
              <ul className="mt-2 space-y-1.5">
                {picked.agents.map((a) => (
                  <li key={a.slug} data-testid="onboarding-template-agent" className="flex items-center gap-2 text-label">
                    <AgentAvatar seed={a.slug} alt="" className="size-6 shrink-0 rounded-md bg-card" />
                    <span className="min-w-0 flex-1 truncate">
                      <span className="text-foreground">{a.name}</span>
                      <span className="text-muted-foreground"> — {a.role}</span>
                    </span>
                    {a.lead && <span className="text-micro font-semibold uppercase text-muted-foreground-soft">Lead</span>}
                  </li>
                ))}
              </ul>
              <p className="mt-2 font-mono text-micro text-muted-foreground">{TEMPLATE_MODEL_LABEL}</p>
            </div>
            <div>
              <div className="flex items-center gap-1.5 text-micro font-semibold uppercase tracking-wider text-muted-foreground-soft">
                <KeyRound className="size-3" aria-hidden /> It needs
              </div>
              <ul className="mt-2 space-y-1.5 text-label">
                <li className="flex items-center justify-between gap-2">
                  <span className="text-muted-foreground">Model token</span>
                  <StatusPill tone={hasModelToken ? "success" : "warn"} label={hasModelToken ? "From step 2" : "Missing"} />
                </li>
                <li className="flex items-center justify-between gap-2">
                  <span className="text-muted-foreground">Container runtime</span>
                  <StatusPill
                    tone={runtimeInUse === true ? "success" : runtimeInUse === null ? "muted" : "warn"}
                    label={runtimeInUse === true ? "Docker" : runtimeInUse === null ? "Checking…" : "Not running"}
                  />
                </li>
              </ul>
            </div>
          </>
        ) : (
          <p className="text-label text-muted-foreground">Pick a template to see its agents and what it needs.</p>
        )}
      </aside>
    </div>
  )
}
