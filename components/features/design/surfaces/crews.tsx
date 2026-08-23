"use client"

/**
 * Crews — New crew, New agent.
 *
 * New agent is the product's largest create form: twenty fields on
 * `AgentDraft` (components/features/crews/create-agent/types.ts), and the one
 * surface people said was already close. It is rebuilt here field-for-field,
 * so the thing being judged is the SHELL, not a reduced version of the form.
 *
 * The one change of substance is where the five advanced fields live. Today
 * they sit behind an "Advanced" that looks like a link and says nothing about
 * what is inside; here the disclosure names its contents and shows the current
 * values while closed, so you can tell at a glance whether you need to open it.
 * Nothing is removed and nothing is promoted.
 */

import * as React from "react"
import {
  Boxes,
  Check,
  CircleHelp,
  Brain,
  Cpu,
  Globe,
  Puzzle,
  MessageSquare,
  GitBranch,
  RefreshCw,
  HardDrive,
  Image as ImageIcon,
  Layers,
  Network,
  Search,
  ShieldCheck,
  Sparkles,
  TriangleAlert,
  Terminal,
  Wand2,
  Wrench,
  X,
} from "lucide-react"

import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { CrewIcon } from "@/components/ui/crew-icon"
import { CREW_ICONS, GRADIENT_PALETTES } from "@/lib/entities"
import { BASE_IMAGES, CATEGORY_FILTERS, CATEGORY_LABELS } from "@/components/features/crews/runtime-config-data"
import { featureRefToTool, getBrandColor, getBrandIcon } from "@/components/features/crews/runtime-config-brands"
import { useCatalog } from "@/hooks/use-catalog"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { AVATAR_STYLES, getAgentAvatarUrl } from "@/lib/agent-avatar"
import { useAvatarStylesVersion } from "@/hooks/use-avatar-styles"
import {
  CreateSurfaceBody,
  CreateSurfaceChoice,
  CreateSurfaceDescriptionInput,
  CreateSurfaceDisclosure,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceGrid,
  CreateSurfaceHeader,
  CreateSurfaceLoading,
  CreateSurfaceNotice,
  CreateSurfacePicker,
  CreateSurfaceSecondaryAction,
  CreateSurfaceSection,
  CreateSurfaceSteps,
  CreateSurfaceTile,
  CreateSurfaceTitleInput,
  CreateSurfaceToggleRow,
} from "@/components/layout/create-surface"

/* ══ Crews → New crew ═══════════════════════════════════════════════════ */

/**
 * Four questions, not five.
 *
 * Runtime and Container were two steps asking one thing — "what does this crew
 * run on" — and between them they put CPU, memory, network policy, base image,
 * devcontainer features and MCP servers in front of somebody whose actual
 * answer to five of those six is "whatever you recommend". Merging them is the
 * cheapest honest simplification available: nothing is removed, the defaults
 * stop being questions, and the flow reads as
 *
 *     who is it  →  who is in it  →  what does it run on  →  review
 */
const CREW_STEPS = [
  { id: "identity", label: "Identity" },
  { id: "lineup", label: "Lineup" },
  { id: "container", label: "Container" },
  { id: "review", label: "Review" },
]

const LINEUPS = [
  { id: "solo", icon: Sparkles, accent: "green" as const, title: "Solo", description: "One generalist. Add more when you know what you need.", meta: "1 agent" },
  { id: "pair", icon: Layers, accent: "blue" as const, title: "Reviewer pair", description: "One writes, one reviews. The default for code work.", meta: "2 agents" },
  { id: "squad", icon: Boxes, accent: "purple" as const, title: "Squad", description: "Lead, two builders, a reviewer. For work that splits.", meta: "4 agents" },
  { id: "empty", icon: Network, accent: "slate" as const, title: "Empty", description: "No agents. Hire into it later.", meta: "0 agents" },
]

/**
 * The explanation, folded into an "i".
 *
 * Prose that is true and long is still prose in the way. Three sentences about
 * why the shipped default is `restricted` belong one hover away, not between
 * the control and the next one — but they must not be DELETED, because the
 * whole argument for flipping that default depends on knowing what it is.
 */
function Info({ children }: { children: React.ReactNode }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          aria-label="More information"
          className="inline-flex h-4 w-4 shrink-0 items-center justify-center rounded-full text-muted-foreground transition-colors hover:text-foreground"
        >
          <CircleHelp className="h-3.5 w-3.5" />
        </button>
      </TooltipTrigger>
      <TooltipContent side="top" className="max-w-[320px] text-left text-[11px] leading-relaxed">
        {children}
      </TooltipContent>
    </Tooltip>
  )
}

/**
 * Module scope, and it MUST stay there.
 *
 * `useCatalog` lists `extract` in its effect dependencies (hooks/use-catalog.ts:59).
 * An inline arrow is a new identity on every render, so the effect re-runs, aborts
 * its own in-flight request, and `finally` skips `setLoading(false)` because the
 * controller is aborted — the spinner runs forever while the endpoint happily
 * returns 1241 features. That is exactly what this specimen did on first try, and
 * it is why runtime-config.tsx declares its extractor at module scope too.
 */
function extractFeatures(json: unknown): CatalogFeature[] {
  const f = (json as { features?: unknown })?.features
  return Array.isArray(f) ? (f as CatalogFeature[]) : []
}

/** What /api/v1/features/catalog returns per entry, narrowed to what is used. */
interface CatalogFeature {
  id?: string
  name?: string
  description?: string
  category?: string
}

export function NewCrewContent({ onClose }: { onClose: () => void }) {
  const [step, setStep] = React.useState(0)
  // The icon picker is a PANEL, not a second dialog over this one. See the
  // CreateSurfacePicker note in create-surface.tsx for why.
  const [panel, setPanel] = React.useState<null | "icon" | "image">(null)
  const [icon, setIcon] = React.useState("code")
  const [image, setImage] = React.useState(BASE_IMAGES[0].value)
  const [imageSearch, setImageSearch] = React.useState("")
  const [featureCategory, setFeatureCategory] = React.useState<string | null>(null)
  const [featureSearch, setFeatureSearch] = React.useState("")
  const [features, setFeatures] = React.useState<string[]>([])
  const [color, setColor] = React.useState("blue")
  const [iconSearch, setIconSearch] = React.useState("")
  const [name, setName] = React.useState("")
  const [description, setDescription] = React.useState("")
  const [lineup, setLineup] = React.useState<string | null>(null)
  const [cpu, setCpu] = React.useState<"1" | "2" | "4">("2")
  const [memory, setMemory] = React.useState<"2" | "4" | "8">("4")
  // Open by default, which DIVERGES from the server: database/crew_defaults.go
  // writes "restricted" on every create path, deliberately, so a new path
  // cannot reintroduce allow-all by omitting the column. Adopting this
  // proposal means changing that constant, and that is a security decision
  // rather than a UI one — the row below says so on screen rather than
  // burying it here.
  const [network, setNetwork] = React.useState<"open" | "allowlist">("open")
  const [domains, setDomains] = React.useState("")

  const last = step === CREW_STEPS.length - 1
  const valid = step === 0 ? name.trim().length > 0 : step === 1 ? lineup !== null : true
  const advance = () => (last ? onClose() : setStep((s) => s + 1))

  // The one specimen on this page that fetches. Everything else is local
  // state on purpose, but "the search does nothing" was the complaint, and a
  // fake catalogue would have answered it with a different lie: this is the
  // real /api/v1/features/catalog the shipped wizard already reads.
  const { data: catalog, loading: catalogLoading, error: catalogError } = useCatalog<CatalogFeature>(
    "/api/v1/features/catalog",
    extractFeatures,
  )

  const featureResults = React.useMemo(() => {
    const all = catalog ?? []
    const q = featureSearch.trim().toLowerCase()
    return all
      .filter((f) => (featureCategory ? f.category === featureCategory : true))
      .filter((f) =>
        q ? `${f.name ?? ""} ${f.id ?? ""} ${f.description ?? ""}`.toLowerCase().includes(q) : true,
      )
      .slice(0, 40)
  }, [catalog, featureCategory, featureSearch])

  const imageDef = BASE_IMAGES.find((b) => b.value === image)
  // Brand colour, not a grey glyph. Every BASE_IMAGES entry already carries a
  // `colorKey` for exactly this — it is stored explicitly because the value is
  // a full registry path and parsing a brand out of it is brittle.
  const imageOptions = BASE_IMAGES.filter((b) =>
    imageSearch.trim() ? b.label.toLowerCase().includes(imageSearch.trim().toLowerCase()) : true,
  ).map((b) => ({
    id: b.value,
    label: b.label,
    render: <b.icon className="h-5 w-5" style={{ color: getBrandColor(b.colorKey ?? "") ?? undefined }} />,
  }))

  const iconOptions = CREW_ICONS.filter((i) =>
    iconSearch.trim() ? i.label.toLowerCase().includes(iconSearch.trim().toLowerCase()) : true,
  ).map((i) => ({
    id: i.name,
    label: i.label,
    render: <i.icon className="h-4 w-4 text-foreground/70" />,
  }))

  return (
    <>
      <CreateSurfaceHeader
        concept="crews"
        title={panel === "icon" ? "Icon — new crew" : panel === "image" ? "Base image — new crew" : "New crew"}
        description={
          panel === "icon"
            ? "The same icon can be reused across crews with different colours as a quick visual differentiator."
            : panel === "image"
              ? "What the container starts from. Node 22 is the recommendation for most agent work; the rest are there for a crew that needs a toolchain preinstalled."
              : undefined
        }
        onBack={panel ? () => setPanel(null) : undefined}
        onClose={onClose}
        meta={
          panel ? undefined : (
            <span className="max-sm:hidden">
              Step {step + 1} of {CREW_STEPS.length}
            </span>
          )
        }
      />

      {!panel && <CreateSurfaceSteps steps={CREW_STEPS} current={step} onJump={setStep} />}

      <CreateSurfaceBody className="space-y-5">
        {panel === "icon" && (
          <CreateSurfacePicker
            preview={<CrewIcon icon={icon} color={color} size="xl" />}
            previewHint={`${CREW_ICONS.find((i) => i.name === icon)?.label ?? icon} · ${color}`}
            palette={{
              value: color,
              onChange: setColor,
              options: GRADIENT_PALETTES.map((g) => ({ id: g.id, dot: g.dot })),
            }}
            search={{ value: iconSearch, onChange: setIconSearch, placeholder: "Search icons…" }}
            options={iconOptions}
            value={icon}
            onChange={setIcon}
          />
        )}

        {panel === "image" && (
          <CreateSurfacePicker
            columns={5}
            captions
            preview={
              <div className="flex flex-col items-center gap-2">
                {imageDef && (
                  <imageDef.icon
                    className="h-10 w-10"
                    style={{ color: getBrandColor(imageDef.colorKey ?? "") ?? undefined }}
                  />
                )}
                <span className="font-mono text-[11px] text-muted-foreground">{image}</span>
              </div>
            }
            previewHint={imageDef?.description}
            search={{ value: imageSearch, onChange: setImageSearch, placeholder: "Search images…" }}
            options={imageOptions}
            value={image}
            onChange={setImage}
            extra={
              <CreateSurfaceField
                label="Or a registry reference"
                htmlFor="crew-custom-image"
                hint="Anything the registry can pull. A custom image is rebuilt on first run and can take several minutes."
              >
                <Input
                  id="crew-custom-image"
                  value={BASE_IMAGES.some((b) => b.value === image) ? "" : image}
                  onChange={(e) => setImage(e.target.value)}
                  placeholder="ghcr.io/your-org/your-image:tag"
                  className="h-8 font-mono text-xs max-sm:h-12 max-sm:text-sm"
                />
              </CreateSurfaceField>
            }
          />
        )}

        {!panel && step === 0 && (
          <>
            <CreateSurfaceSection title="Identity" concept="crews">
              <div className="flex items-start gap-3">
                <button
                  type="button"
                  aria-label="Change crew icon"
                  onClick={() => setPanel("icon")}
                  className="shrink-0 rounded-xl transition-opacity hover:opacity-80"
                >
                  <CrewIcon icon={icon} color={color} size="lg" />
                </button>
                <div className="min-w-0 flex-1">
              <CreateSurfaceTitleInput
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="Crew name"
              />
                  <span className="mt-1 block text-[11px] text-muted-foreground-soft">
                    Tap the icon to change it
                  </span>
                </div>
              </div>
              <CreateSurfaceField label="Slug" htmlFor="crew-slug" hint="Lowercase, no spaces — how agents address this crew.">
                <Input
                  id="crew-slug"
                  placeholder={name.trim().toLowerCase().replace(/\s+/g, "-") || "platform"}
                  className="h-8 font-mono text-xs max-sm:h-12 max-sm:text-sm"
                />
              </CreateSurfaceField>
            </CreateSurfaceSection>

            <CreateSurfaceSection title="What this crew is for">
              <CreateSurfaceDescriptionInput
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="One sentence. It shows up wherever the crew is listed."
                rows={3}
                className="rounded-lg border border-hairline bg-foreground/[0.02] p-2.5 text-xs"
              />
            </CreateSurfaceSection>
          </>
        )}

        {!panel && step === 1 && (
          <div className="grid gap-2 sm:grid-cols-2 group-data-[mobile=true]/surface:grid-cols-1">
            {LINEUPS.map((l) => (
              <CreateSurfaceTile
                key={l.id}
                icon={l.icon}
                accent={l.accent}
                title={l.title}
                description={l.description}
                meta={l.meta}
                selected={lineup === l.id}
                onClick={() => setLineup(l.id)}
              />
            ))}
          </div>
        )}

        {!panel && step === 2 && (
          <>
            <CreateSurfaceSection title="Base image" icon={HardDrive} accent="teal">
              <button
                type="button"
                onClick={() => setPanel("image")}
                className="flex w-full items-center gap-3 rounded-lg border border-hairline bg-foreground/[0.02] p-3 text-left transition-colors hover:border-primary/30 hover:bg-primary/[0.04]"
              >
                {imageDef ? (
                  <imageDef.icon
                    className="h-6 w-6 shrink-0"
                    style={{ color: getBrandColor(imageDef.colorKey ?? "") ?? undefined }}
                  />
                ) : (
                  <Boxes className="h-6 w-6 shrink-0 text-muted-foreground" />
                )}
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-[13px] font-medium text-foreground">
                    {imageDef?.label ?? "Custom image"}
                  </span>
                  <span className="mt-0.5 block truncate font-mono text-[11px] text-muted-foreground">
                    {image}
                  </span>
                </span>
                <span className="shrink-0 text-[11px] text-muted-foreground">Change</span>
              </button>
            </CreateSurfaceSection>

            <CreateSurfaceSection
              title="Preinstalled tooling"
              icon={Wrench}
              accent="amber"
              hint="devcontainer features"
            >
              <div className="flex flex-wrap gap-1.5">
                {CATEGORY_FILTERS.filter((c) => c !== "all").map((c) => {
                  const active = featureCategory === c
                  return (
                    <button
                      key={c}
                      type="button"
                      aria-pressed={active}
                      onClick={() => setFeatureCategory(active ? null : c)}
                      className={`h-8 rounded-full border px-3 text-xs transition-colors max-sm:h-12 group-data-[mobile=true]/surface:h-12 ${
                        active
                          ? "border-primary/40 bg-primary/15 text-primary-hover"
                          : "border-hairline bg-foreground/[0.03] text-muted-foreground hover:text-foreground"
                      }`}
                    >
                      {CATEGORY_LABELS[c] ?? c}
                    </button>
                  )
                })}
              </div>

              <div className="relative">
                <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground-soft" />
                <Input
                  value={featureSearch}
                  onChange={(e) => setFeatureSearch(e.target.value)}
                  placeholder="Search features — ansible, terraform, docker, aws-cli…"
                  aria-label="Search devcontainer features"
                  className="h-8 pl-8 text-xs max-sm:h-12 max-sm:text-sm"
                />
              </div>

              {/* Picked features first, so what you chose never scrolls away. */}
              {features.length > 0 && (
                <div className="flex flex-wrap gap-1.5">
                  {features.map((ref) => {
                    const tool = featureRefToTool(ref)
                    const Icon = getBrandIcon(tool)
                    return (
                      <button
                        key={ref}
                        type="button"
                        onClick={() => setFeatures((f) => f.filter((x) => x !== ref))}
                        aria-label={`Remove ${tool}`}
                        className="flex h-7 items-center gap-1.5 rounded-md border border-primary/40 bg-primary/[0.12] pl-2 pr-1.5 text-xs text-primary-hover transition-colors hover:bg-primary/20 max-sm:h-10 group-data-[mobile=true]/surface:h-10"
                      >
                        {Icon && <Icon className="h-3.5 w-3.5" style={{ color: getBrandColor(tool) ?? undefined }} />}
                        {tool}
                        <X className="h-3 w-3 opacity-60" />
                      </button>
                    )
                  })}
                </div>
              )}

              {catalogLoading ? (
                <CreateSurfaceLoading rows={2} />
              ) : catalogError ? (
                <CreateSurfaceNotice tone="error" icon={TriangleAlert}>
                  The feature catalogue did not load. The list is served by{" "}
                  <code className="font-mono">/api/v1/features/catalog</code>.
                </CreateSurfaceNotice>
              ) : featureResults.length === 0 ? (
                <p className="rounded-lg border border-dashed border-border/60 px-3 py-5 text-center text-xs text-muted-foreground">
                  {featureSearch.trim() ? `Nothing matches “${featureSearch}”.` : "No features in this category."}
                </p>
              ) : (
                <div className="max-h-56 space-y-1 overflow-y-auto overscroll-contain rounded-lg border border-hairline bg-foreground/[0.02] p-1.5">
                  {featureResults.map((f) => {
                    const ref = f.id ?? ""
                    const tool = featureRefToTool(ref)
                    const Icon = getBrandIcon(tool)
                    const picked = features.includes(ref)
                    return (
                      <button
                        key={ref}
                        type="button"
                        aria-pressed={picked}
                        onClick={() =>
                          setFeatures((prev) => (picked ? prev.filter((x) => x !== ref) : [...prev, ref]))
                        }
                        className={`flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-left transition-colors ${
                          picked ? "bg-primary/[0.12]" : "hover:bg-foreground/[0.06]"
                        }`}
                      >
                        {Icon ? (
                          <Icon className="h-4 w-4 shrink-0" style={{ color: getBrandColor(tool) ?? undefined }} />
                        ) : (
                          <Boxes className="h-4 w-4 shrink-0 text-muted-foreground-soft" />
                        )}
                        <span className="min-w-0 flex-1">
                          <span className="block truncate text-xs text-foreground">{f.name ?? tool}</span>
                          {f.description && (
                            <span className="block truncate text-[11px] text-muted-foreground">
                              {f.description}
                            </span>
                          )}
                        </span>
                        {picked && <Check className="h-3.5 w-3.5 shrink-0 text-primary-hover" />}
                      </button>
                    )
                  })}
                </div>
              )}
            </CreateSurfaceSection>

            {/* ── Egress ─────────────────────────────────────────────────
                Open is the proposed default and the allowlist stays built,
                one switch away, because "we will throttle later" only works
                if the throttle already exists. What it must NOT be is the
                default while it is still maturing: a half-working allowlist
                fails as a silent timeout somewhere deep in a run, which is
                the worst failure shape a platform can have. ── */}
            <CreateSurfaceSection
              title={
                <span className="inline-flex items-center gap-1.5">
                  Network
                  <Info>
                    <strong className="text-foreground">Open is a proposal, not the shipped default.</strong>{" "}
                    The server writes <code className="font-mono">restricted</code> on every create path today
                    (<code className="font-mono">database/crew_defaults.go:13</code>) so a new code path cannot
                    reintroduce allow-all by leaving the column out. Adopting open-by-default means changing
                    that constant — a security decision, not a UI one.
                  </Info>
                </span>
              }
              icon={Network}
              accent="purple"
            >
              <CreateSurfaceToggleRow
                icon={network === "open" ? Globe : ShieldCheck}
                accent={network === "open" ? "amber" : "green"}
                label={network === "open" ? "Open egress" : "Allowlist"}
                hint={
                  network === "open"
                    ? "The container reaches any host."
                    : "Only the listed hosts, plus the provider APIs the sidecar always permits."
                }
                control={
                  <Switch
                    checked={network === "allowlist"}
                    onCheckedChange={(on) => setNetwork(on ? "allowlist" : "open")}
                    aria-label="Restrict egress to an allowlist"
                  />
                }
              />

              {network === "allowlist" && (
                <CreateSurfaceField
                  label="Allowed hosts"
                  hint="One per line. api.anthropic.com, api.openai.com and generativelanguage.googleapis.com are always reachable — the sidecar permits them whatever this says."
                >
                  <CreateSurfaceDescriptionInput
                    value={domains}
                    onChange={(e) => setDomains(e.target.value)}
                    rows={3}
                    placeholder={"registry.npmjs.org\ngithub.com\nyour-internal-api.example.com"}
                    className="rounded-lg border border-hairline bg-foreground/[0.02] p-2.5 font-mono text-xs"
                  />
                </CreateSurfaceField>
              )}

            </CreateSurfaceSection>

            {/* ── Sizing: an administrator's question, so it is folded away ── */}
            <CreateSurfaceDisclosure
              icon={Cpu}
              accent="slate"
              label={
                <span className="inline-flex items-center gap-1.5">
                  Size
                  <Info>
                    An administrator&rsquo;s question. The defaults hold two agents, and the server already
                    returns a warning when a size will bite —{" "}
                    <code className="font-mono">crewSizingAdvisories</code>, on every create and update. No
                    screen has ever displayed it; it appears here when there is one.
                  </Info>
                </span>
              }
              summary={`${cpu} cores · ${memory} GiB — the shipped default`}
            >
              <CreateSurfaceGrid>
                <CreateSurfaceField label="CPU limit">
                  <CreateSurfaceChoice
                    ariaLabel="CPU limit"
                    value={cpu}
                    onChange={setCpu}
                    options={[
                      { value: "1", label: "1 core" },
                      { value: "2", label: "2 cores" },
                      { value: "4", label: "4 cores" },
                    ]}
                  />
                </CreateSurfaceField>
                <CreateSurfaceField label="Memory limit">
                  <CreateSurfaceChoice
                    ariaLabel="Memory limit"
                    value={memory}
                    onChange={setMemory}
                    options={[
                      { value: "2", label: "2 GiB" },
                      { value: "4", label: "4 GiB" },
                      { value: "8", label: "8 GiB" },
                    ]}
                  />
                </CreateSurfaceField>
              </CreateSurfaceGrid>

              {Number(memory) < 4 && (
                <CreateSurfaceNotice tone="warn" icon={TriangleAlert}>
                  At {memory} GiB this crew cannot hold a second agent.
                </CreateSurfaceNotice>
              )}
            </CreateSurfaceDisclosure>

            {/* No "Integrations" control here, and the audit says that is
                correct rather than merely unfinished.
                
                A crew CAN own a raw MCP server (crew_mcp_servers), and that is
                the only integration concept with a crew layer at all:
                
                  · Composio grants are PER-AGENT (agent_mcp_bindings); its
                    settings are per-workspace. There is no crew layer.
                  · Notification channels attach to a workspace or to an AGENT.
                    A crew_id column, a join table and a route are all
                    not-found — crew-level attachment does not exist.
                
                So a crew-level "Integrations" control could only offer the one
                thing the product is moving away from — hand-configured MCP —
                and would imply a crew scope the other two do not have. It
                belongs on the agent, where the grant actually lives. */}
          </>
        )}

        {!panel && step === 3 && (
          <div className="space-y-2">
            <ReviewRow label="Name" value={name || "—"} onEdit={() => setStep(0)} />
            <ReviewRow label="Lineup" value={LINEUPS.find((l) => l.id === lineup)?.title ?? "—"} onEdit={() => setStep(1)} />
            <ReviewRow label="Runs on" value={imageDef?.label ?? image} onEdit={() => setStep(2)} />
            <ReviewRow
              label="Network"
              value={network === "open" ? "Open egress" : "Allowlist"}
              onEdit={() => setStep(2)}
            />
            <ReviewRow label="Size" value={`${cpu} cores · ${memory} GiB`} onEdit={() => setStep(2)} />
          </div>
        )}
      </CreateSurfaceBody>

      <CreateSurfaceFooter
        hint={
          panel ? undefined : (
            <>
              <kbd className="font-mono">⌘↵</kbd> to {last ? "create" : "continue"}
            </>
          )
        }
        onCancel={panel ? () => setPanel(null) : onClose}
        cancelLabel={panel ? "Back" : "Cancel"}
        secondary={
          panel ? undefined : step > 0 ? (
            <CreateSurfaceSecondaryAction onClick={() => setStep((s) => s - 1)}>Back</CreateSurfaceSecondaryAction>
          ) : step === 2 ? (
            <CreateSurfaceSecondaryAction icon={Wand2} onClick={() => setStep(3)}>
              Skip to defaults
            </CreateSurfaceSecondaryAction>
          ) : undefined
        }
        primaryLabel={
          panel === "icon" ? "Use this icon" : panel === "image" ? "Use this image" : last ? "Create crew" : "Continue"
        }
        primaryDisabled={panel ? false : !valid}
        onPrimary={panel ? () => setPanel(null) : advance}
      />
    </>
  )
}

function ReviewRow({ label, value, onEdit }: { label: string; value: string; onEdit: () => void }) {
  return (
    <button
      type="button"
      onClick={onEdit}
      className="flex w-full items-center gap-3 rounded-lg border border-hairline bg-foreground/[0.02] px-3 py-2.5 text-left transition-colors hover:bg-foreground/[0.05]"
    >
      <span className="w-20 shrink-0 text-[11px] uppercase tracking-wider text-muted-foreground">{label}</span>
      <span className="min-w-0 flex-1 truncate text-xs text-foreground/85">{value}</span>
      <span className="shrink-0 text-[11px] text-muted-foreground-soft">Edit</span>
    </button>
  )
}

/* ══ Crews → New agent ══════════════════════════════════════════════════ */

const PERSONAS = [
  { id: "b_filip", name: "Filip", role: "Backend lead", accent: "blue" as const },
  { id: "b_tomas", name: "Tomáš", role: "Reviewer", accent: "purple" as const },
  { id: "b_viktor", name: "Viktor", role: "Infra", accent: "amber" as const },
  { id: "b_eva", name: "Eva", role: "Frontend", accent: "green" as const },
  { id: "b_lucie", name: "Lucie", role: "QA", accent: "teal" as const },
  { id: "b_radek", name: "Radek", role: "Docs", accent: "gold" as const },
]

/** Representative Composio toolkits. The real list is /integrations/composio/toolkits. */
const COMPOSIO_TOOLKITS = [
  { id: "github", label: "GitHub", icon: GitBranch },
  { id: "slack", label: "Slack", icon: MessageSquare },
  { id: "linear", label: "Linear", icon: Network },
  { id: "gmail", label: "Gmail", icon: Globe },
  { id: "notion", label: "Notion", icon: Boxes },
]

/** Workspace channels an agent can be allowed onto. */
const NOTIFY_CHANNELS = [
  { id: "eng", label: "#eng (Slack)", icon: MessageSquare },
  { id: "oncall", label: "On-call", icon: TriangleAlert },
  { id: "digest", label: "Daily digest", icon: Globe },
]

const MODELS: Record<string, string[]> = {
  ANTHROPIC: ["claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5"],
  OPENAI: ["gpt-5.5", "o3", "gpt-5.5-pro"],
  GOOGLE: ["gemini-3-pro", "gemini-3-flash"],
  CURSOR: ["cursor-default"],
  FACTORY: ["droid-default"],
  OLLAMA: ["llama3.3", "qwen3"],
}

export function NewAgentContent({ onClose }: { onClose: () => void }) {
  // DiceBear styles load lazily; without this the grid renders placeholders
  // and never upgrades to the real faces.
  useAvatarStylesVersion()

  const [panel, setPanel] = React.useState<null | "avatar">(null)

  // Identity
  const [persona, setPersona] = React.useState<string | null>(null)
  const [name, setName] = React.useState("")
  const [slug, setSlug] = React.useState("")
  const [slugTouched, setSlugTouched] = React.useState(false)
  const [crew, setCrew] = React.useState("platform")
  const [role, setRole] = React.useState<"AGENT" | "LEAD">("AGENT")
  const [roleTitle, setRoleTitle] = React.useState("")
  const [description, setDescription] = React.useState("")
  const [seed, setSeed] = React.useState("agent")
  // `null` means "follow the crew" — a real answer a grid with no selected
  // cell cannot express, which is why the picker has an inherit row.
  const [avatarStyle, setAvatarStyle] = React.useState<string | null>(null)
  const [styleSearch, setStyleSearch] = React.useState("")
  const [quickSeeds, setQuickSeeds] = React.useState<string[]>([])

  React.useEffect(() => {
    if (panel === "avatar" && quickSeeds.length === 0) {
      setQuickSeeds(Array.from({ length: 8 }, () => Math.random().toString(36).slice(2, 12)))
    }
  }, [panel, quickSeeds.length])

  // Persona body
  const [prompt, setPrompt] = React.useState("")

  // Runtime
  const [provider, setProvider] = React.useState<keyof typeof MODELS>("ANTHROPIC")
  const [model, setModel] = React.useState("claude-sonnet-5")
  const [memory, setMemory] = React.useState(true)

  // Advanced
  const [toolProfile, setToolProfile] = React.useState<"MINIMAL" | "CODING" | "FULL">("CODING")
  const [cli, setCli] = React.useState("CLAUDE_CODE")
  const [timeout, setTimeoutSeconds] = React.useState(1800)
  const [leadMode, setLeadMode] = React.useState<"active" | "passive">("active")

  // Integrations — the agent is the unit of access, so they live here.
  const [toolkits, setToolkits] = React.useState<string[]>([])
  const [channels, setChannels] = React.useState<string[]>([])

  const derivedSlug = slugTouched ? slug : name.trim().toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "")
  const valid = name.trim().length >= 2 && derivedSlug.length >= 2 && crew !== ""

  const effectiveSeed = seed.trim() || name.trim() || "agent"
  const styleOptions = Object.entries(AVATAR_STYLES)
    .filter(([, meta]) =>
      styleSearch.trim() ? meta.label.toLowerCase().includes(styleSearch.trim().toLowerCase()) : true,
    )
    .map(([value, meta]) => ({
      id: value,
      label: meta.label,
      render: <img src={getAgentAvatarUrl(effectiveSeed, value)} alt="" className="h-7 w-7 rounded-md" />,
    }))

  return (
    <>
      <CreateSurfaceHeader
        concept="crews"
        accent="purple"
        context={crew}
        title={panel === "avatar" ? "Avatar — new agent" : "New agent"}
        description={
          panel === "avatar"
            ? "Pick a style and a seed. The same seed always produces the same face."
            : "Pick a template to start fast, or fill in the basics. Everything here maps 1:1 onto POST /api/v1/agents."
        }
        onBack={panel ? () => setPanel(null) : undefined}
        onClose={onClose}
      />

      <CreateSurfaceBody className="space-y-5">
        {panel === "avatar" && (
          <CreateSurfacePicker
            columns={6}
            captions
            preview={
              <img
                src={getAgentAvatarUrl(effectiveSeed, avatarStyle)}
                alt=""
                className="h-20 w-20 rounded-2xl"
              />
            }
            previewHint={`${avatarStyle ? AVATAR_STYLES[avatarStyle]?.label : "Bottts Neutral"} · seed “${effectiveSeed}”`}
            inherit={{
              label: "Follow the crew",
              hint: `${crew} has none set, so: Bottts Neutral (the default)`,
              preview: (
                <img src={getAgentAvatarUrl(effectiveSeed, null)} alt="" className="h-9 w-9 rounded-lg" />
              ),
              active: avatarStyle === null,
              onSelect: () => setAvatarStyle(null),
            }}
            search={{ value: styleSearch, onChange: setStyleSearch, placeholder: "Search styles…" }}
            options={styleOptions}
            value={avatarStyle}
            onChange={setAvatarStyle}
            extra={
              <div className="flex flex-col gap-3">
                <CreateSurfaceField label="Quick pick" hint="Eight random seeds in the current style.">
                  <div className="flex flex-wrap gap-1.5">
                    {quickSeeds.map((q) => (
                      <button
                        key={q}
                        type="button"
                        aria-label={`Use seed ${q}`}
                        onClick={() => setSeed(q)}
                        className="rounded-lg transition-transform hover:scale-105"
                      >
                        <img
                          src={getAgentAvatarUrl(q, avatarStyle)}
                          alt=""
                          className="h-10 w-10 rounded-lg"
                        />
                      </button>
                    ))}
                  </div>
                </CreateSurfaceField>

                <CreateSurfaceField
                  label="Seed"
                  htmlFor="agent-seed"
                  hint="Identical seeds across agents produce identical faces. Leave the agent name as the seed for a deterministic default."
                >
                  <div className="flex gap-2">
                    <Input
                      id="agent-seed"
                      value={seed}
                      onChange={(e) => setSeed(e.target.value)}
                      placeholder={name.trim() || "agent"}
                      className="h-8 font-mono text-xs max-sm:h-12 max-sm:text-sm"
                    />
                    <CreateSurfaceSecondaryAction
                      icon={RefreshCw}
                      onClick={() => setSeed(Math.random().toString(36).slice(2, 12))}
                    >
                      Regenerate
                    </CreateSurfaceSecondaryAction>
                  </div>
                </CreateSurfaceField>
              </div>
            }
          />
        )}

        {!panel && (
          <>
        {/* ── Templates ───────────────────────────────────────────────── */}
        <CreateSurfaceSection
          title="Start from"
          icon={Sparkles}
          accent="gold"
          hint="a template fills the persona, model and tool profile"
        >
          <div className="flex flex-wrap gap-1.5">
            {PERSONAS.map((p) => {
              const active = persona === p.id
              return (
                <button
                  key={p.id}
                  type="button"
                  aria-pressed={active}
                  onClick={() => {
                    setPersona(active ? null : p.id)
                    if (!active && !roleTitle) setRoleTitle(p.role)
                  }}
                  className={`flex h-9 items-center gap-2 rounded-full border px-2.5 text-xs transition-colors max-sm:h-12 group-data-[mobile=true]/surface:h-11 ${
                    active
                      ? "border-primary/40 bg-primary/15 text-primary-hover"
                      : "border-hairline bg-foreground/[0.03] text-muted-foreground hover:bg-foreground/[0.07]"
                  }`}
                >
                  <span className="flex h-5 w-5 items-center justify-center rounded-full bg-foreground/[0.08] text-[10px] font-semibold">
                    {p.name.slice(0, 1)}
                  </span>
                  <span className="font-medium text-foreground/90">{p.name}</span>
                  <span className="text-muted-foreground-soft">{p.role}</span>
                </button>
              )
            })}
            <button
              type="button"
              className="flex h-9 items-center gap-1.5 rounded-full border border-dashed border-border px-3 text-xs text-muted-foreground transition-colors hover:border-primary/40 hover:text-foreground max-sm:h-12 group-data-[mobile=true]/surface:h-11"
            >
              <Search className="h-3 w-3" />
              All templates
            </button>
            <button
              type="button"
              onClick={() => setPersona(null)}
              className="flex h-9 items-center rounded-full border border-hairline px-3 text-xs text-muted-foreground transition-colors hover:text-foreground max-sm:h-12 group-data-[mobile=true]/surface:h-11"
            >
              Blank
            </button>
          </div>
        </CreateSurfaceSection>

        {/* ── Identity ────────────────────────────────────────────────── */}
        <CreateSurfaceSection title="Identity" icon={ImageIcon} accent="purple">
          <div className="flex items-start gap-3">
            <button
              type="button"
              aria-label="Change avatar"
              onClick={() => setPanel("avatar")}
              className="shrink-0 rounded-xl transition-opacity hover:opacity-80"
            >
              <img src={getAgentAvatarUrl(effectiveSeed, avatarStyle)} alt="" className="h-12 w-12 rounded-xl" />
            </button>
            <div className="flex min-w-0 flex-1 flex-col gap-2">
              <CreateSurfaceTitleInput
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="Agent name"
              />
              <span className="font-mono text-[11px] text-muted-foreground-soft">
                {avatarStyle ? AVATAR_STYLES[avatarStyle]?.label : "follows the crew"} · seed “{effectiveSeed}”
              </span>
            </div>
          </div>

          <CreateSurfaceGrid>
            <CreateSurfaceField label="Slug" htmlFor="agent-slug" required hint="2–50 chars, lowercase, digits and hyphens.">
              <Input
                id="agent-slug"
                value={derivedSlug}
                onChange={(e) => {
                  setSlugTouched(true)
                  setSlug(e.target.value)
                }}
                placeholder="backend-lead"
                className="h-8 font-mono text-xs max-sm:h-12 max-sm:text-sm"
              />
            </CreateSurfaceField>
            <CreateSurfaceField label="Crew" htmlFor="agent-crew" required>
              <select
                id="agent-crew"
                value={crew}
                onChange={(e) => setCrew(e.target.value)}
                className="h-8 w-full rounded-md border border-hairline bg-background px-2 text-xs text-foreground outline-none transition-colors focus:border-primary max-sm:h-12 max-sm:text-sm"
              >
                <option value="platform">platform</option>
                <option value="growth">growth</option>
                <option value="infra">infra</option>
              </select>
            </CreateSurfaceField>
            <CreateSurfaceField label="Role">
              <CreateSurfaceChoice
                ariaLabel="Agent role"
                value={role}
                onChange={setRole}
                options={[
                  { value: "AGENT", label: "Agent", hint: "Works on what it is given" },
                  { value: "LEAD", label: "Lead", hint: "Can plan and delegate to the crew" },
                ]}
              />
            </CreateSurfaceField>
            <CreateSurfaceField label="Role title" htmlFor="agent-role-title" hint="Free text, shown under the name.">
              <Input
                id="agent-role-title"
                value={roleTitle}
                onChange={(e) => setRoleTitle(e.target.value)}
                placeholder="Backend lead"
                className="h-8 text-xs max-sm:h-12 max-sm:text-sm"
              />
            </CreateSurfaceField>
          </CreateSurfaceGrid>

          <CreateSurfaceField label="Description" hint="One line. Shown in the crew canvas and the agent picker.">
            <Input
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Owns the API surface and its tests."
              className="h-8 text-xs max-sm:h-12 max-sm:text-sm"
            />
          </CreateSurfaceField>
        </CreateSurfaceSection>

        {/* ── Persona ─────────────────────────────────────────────────── */}
        <CreateSurfaceSection
          title="System prompt"
          icon={Brain}
          accent="purple"
          hint={persona ? "from the template — edit freely" : "optional"}
        >
          <CreateSurfaceDescriptionInput
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            rows={5}
            placeholder={
              persona
                ? "You are a backend lead. You own the API surface…"
                : "Leave empty for the built-in default, or write the agent's standing instructions."
            }
            className="rounded-lg border border-hairline bg-foreground/[0.02] p-2.5 text-xs leading-relaxed"
          />
        </CreateSurfaceSection>

        {/* ── Runtime: what 90% of people change ──────────────────────── */}
        <CreateSurfaceSection title="Runtime" icon={Cpu} accent="teal">
          <CreateSurfaceGrid>
            <CreateSurfaceField label="Provider" htmlFor="agent-provider">
              <select
                id="agent-provider"
                value={provider}
                onChange={(e) => {
                  const p = e.target.value as keyof typeof MODELS
                  setProvider(p)
                  setModel(MODELS[p][0])
                }}
                className="h-8 w-full rounded-md border border-hairline bg-background px-2 text-xs text-foreground outline-none transition-colors focus:border-primary max-sm:h-12 max-sm:text-sm"
              >
                {Object.keys(MODELS).map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </select>
            </CreateSurfaceField>
            <CreateSurfaceField label="Model" htmlFor="agent-model">
              <select
                id="agent-model"
                value={model}
                onChange={(e) => setModel(e.target.value)}
                className="h-8 w-full rounded-md border border-hairline bg-background px-2 font-mono text-xs text-foreground outline-none transition-colors focus:border-primary max-sm:h-12 max-sm:text-sm"
              >
                {MODELS[provider].map((m) => (
                  <option key={m} value={m}>
                    {m}
                  </option>
                ))}
              </select>
            </CreateSurfaceField>
          </CreateSurfaceGrid>

          <CreateSurfaceToggleRow
            concept="memory"
            label="Memory"
            hint="The agent keeps what it learned between runs. Off means every run starts cold."
            control={<Switch checked={memory} onCheckedChange={setMemory} />}
          />
        </CreateSurfaceSection>

        {/* ── Advanced: named, with its current values on the lid ─────── */}
        <CreateSurfaceDisclosure
          icon={Wrench}
          accent="amber"
          label="Advanced"
          summary={`${toolProfile.toLowerCase()} tools · ${cli.toLowerCase().replace(/_/g, " ")} · ${Math.round(timeout / 60)} min${role === "LEAD" ? ` · ${leadMode}` : ""}`}
        >
          <CreateSurfaceField
            label="Tool profile"
            hint="What the agent may reach for. MINIMAL is read-only; FULL includes shell and network."
          >
            <CreateSurfaceChoice
              ariaLabel="Tool profile"
              value={toolProfile}
              onChange={setToolProfile}
              options={[
                { value: "MINIMAL", label: "Minimal" },
                { value: "CODING", label: "Coding" },
                { value: "FULL", label: "Full" },
              ]}
            />
          </CreateSurfaceField>

          <CreateSurfaceGrid>
            <CreateSurfaceField label="CLI adapter" htmlFor="agent-cli">
              <select
                id="agent-cli"
                value={cli}
                onChange={(e) => setCli(e.target.value)}
                className="h-8 w-full rounded-md border border-hairline bg-background px-2 font-mono text-xs text-foreground outline-none transition-colors focus:border-primary max-sm:h-12 max-sm:text-sm"
              >
                {["CLAUDE_CODE", "OPENCODE", "CODEX_CLI", "GEMINI_CLI", "CURSOR_CLI", "FACTORY_DROID"].map((a) => (
                  <option key={a} value={a}>
                    {a}
                  </option>
                ))}
              </select>
            </CreateSurfaceField>
            <CreateSurfaceField label="Run timeout" htmlFor="agent-timeout" hint="Seconds before a run is killed.">
              <Input
                id="agent-timeout"
                type="number"
                value={timeout}
                onChange={(e) => setTimeoutSeconds(Number(e.target.value) || 0)}
                className="h-8 font-mono text-xs max-sm:h-12 max-sm:text-sm"
              />
            </CreateSurfaceField>
          </CreateSurfaceGrid>

          {/* Only meaningful for a LEAD — the backend ignores it otherwise, so
              showing it for an AGENT would be a control that does nothing. */}
          {role === "LEAD" && (
            <CreateSurfaceField
              label="Lead mode"
              hint="Active leads plan and delegate on their own; passive leads wait to be asked."
            >
              <CreateSurfaceChoice
                ariaLabel="Lead mode"
                value={leadMode}
                onChange={setLeadMode}
                options={[
                  { value: "active", label: "Active" },
                  { value: "passive", label: "Passive" },
                ]}
              />
            </CreateSurfaceField>
          )}

          <CreateSurfaceNotice tone="info" icon={ShieldCheck}>
            Credentials are not set here. An agent inherits its crew&apos;s vault; grant extras from the
            credential&apos;s own <strong className="text-foreground">Used by</strong> tab.
          </CreateSurfaceNotice>
        </CreateSurfaceDisclosure>

        {/* ── Integrations ──────────────────────────────────────────────
            This is the level that owns them, and the audit is unambiguous
            about why. An agent cannot own an MCP SERVER — mcp_server_scope is
            CHECK-constrained to workspace|crew, and even an agent-level raw
            blob is normalised down into crew_mcp_servers plus a binding. What
            the agent owns is ACCESS: on/off, which credential, which env var.
            
            Composio grants are per-agent (agent_mcp_bindings) and its settings
            per-workspace — no crew layer exists. Notification channels attach
            to a workspace or to an agent; crew-level attachment does not exist
            at all. So both of the things the crew step could not honestly
            offer land here. ── */}
        <CreateSurfaceDisclosure
          icon={Puzzle}
          accent="teal"
          label={
            <span className="inline-flex items-center gap-1.5">
              Tools &amp; notifications
              <Info>
                The agent is the unit of ACCESS, not of definition. A crew or the workspace owns the server;
                this grants this agent a scoped account and says where it may reach a person.
              </Info>
            </span>
          }
          summary={
            toolkits.length === 0 && channels.length === 0
              ? "none — the crew's defaults apply"
              : `${toolkits.length} toolkit${toolkits.length === 1 ? "" : "s"} · ${channels.length} channel${channels.length === 1 ? "" : "s"}`
          }
        >
          <CreateSurfaceField
            label="Composio toolkits"
            hint="OAuth'd app accounts this agent may act through. Scoped per app — full, read-only, or a chosen subset."
          >
            <div className="flex flex-wrap gap-1.5">
              {COMPOSIO_TOOLKITS.map((t) => {
                const on = toolkits.includes(t.id)
                return (
                  <button
                    key={t.id}
                    type="button"
                    aria-pressed={on}
                    onClick={() =>
                      setToolkits((prev) => (on ? prev.filter((x) => x !== t.id) : [...prev, t.id]))
                    }
                    className={`flex h-8 items-center gap-1.5 rounded-full border px-2.5 text-xs transition-colors max-sm:h-12 group-data-[mobile=true]/surface:h-12 ${
                      on
                        ? "border-primary/40 bg-primary/15 text-primary-hover"
                        : "border-hairline bg-foreground/[0.03] text-muted-foreground hover:text-foreground"
                    }`}
                  >
                    <t.icon className="h-3.5 w-3.5" />
                    {t.label}
                  </button>
                )
              })}
            </div>
          </CreateSurfaceField>

          {toolkits.length > 0 && (
            <CreateSurfaceNotice tone="warn" icon={TriangleAlert}>
              <strong className="text-foreground">Granting one agent changes the others.</strong> A server with
              no bindings is handed to every agent; the first binding flips it to opt-in, and every agent
              without its own binding loses it. The product does this today and warns nobody.
            </CreateSurfaceNotice>
          )}

          <CreateSurfaceField
            label="Notification channels"
            hint="Where this agent may reach a person. Workspace channels exist already; this allows or denies them per agent."
          >
            <div className="flex flex-wrap gap-1.5">
              {NOTIFY_CHANNELS.map((c) => {
                const on = channels.includes(c.id)
                return (
                  <button
                    key={c.id}
                    type="button"
                    aria-pressed={on}
                    onClick={() =>
                      setChannels((prev) => (on ? prev.filter((x) => x !== c.id) : [...prev, c.id]))
                    }
                    className={`flex h-8 items-center gap-1.5 rounded-full border px-2.5 text-xs transition-colors max-sm:h-12 group-data-[mobile=true]/surface:h-12 ${
                      on
                        ? "border-primary/40 bg-primary/15 text-primary-hover"
                        : "border-hairline bg-foreground/[0.03] text-muted-foreground hover:text-foreground"
                    }`}
                  >
                    <c.icon className="h-3.5 w-3.5" />
                    {c.label}
                  </button>
                )
              })}
            </div>
          </CreateSurfaceField>
        </CreateSurfaceDisclosure>
          </>
        )}
      </CreateSurfaceBody>

      <CreateSurfaceFooter
        hint={
          panel ? undefined : (
            <span className="flex items-center gap-1.5">
              <Terminal className="h-3 w-3" />
              <span className="font-mono">crewship agent create</span> does the same thing
            </span>
          )
        }
        onCancel={panel ? () => setPanel(null) : onClose}
        cancelLabel={panel ? "Back" : "Cancel"}
        primaryLabel={panel ? "Use this avatar" : "Create agent"}
        primaryIcon={panel ? undefined : HardDrive}
        primaryDisabled={panel ? false : !valid}
        onPrimary={panel ? () => setPanel(null) : onClose}
      />
    </>
  )
}
